// Package launch turns a game profile into a concrete wine invocation
// (Build) and runs it while recording a log and a JSON run summary (Run).
// The summaries are the input for the later crash-diagnosis assistant.
package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cave/internal/config"
	"cave/internal/engine"
	"cave/internal/prefix"
)

type Backend string

const (
	D3DMetal Backend = "d3dmetal" // Apple's DirectX 11/12 -> Metal (GPTK engines only)
	DXVK     Backend = "dxvk"     // DirectX -> Vulkan -> MoltenVK; DXVK DLLs must be in the prefix
	WineD3D  Backend = "wined3d"  // Wine's builtin DirectX -> OpenGL, for old games
)

func ParseBackend(s string) (Backend, error) {
	switch b := Backend(strings.ToLower(s)); b {
	case D3DMetal, DXVK, WineD3D:
		return b, nil
	}
	return "", fmt.Errorf("unknown backend %q (want d3dmetal, dxvk or wined3d)", s)
}

// DefaultBackend picks D3DMetal when the engine has it.
func DefaultBackend(e config.Engine) Backend {
	if e.D3DMetal {
		return D3DMetal
	}
	return WineD3D
}

type Spec struct {
	Game   config.Game
	Prefix config.Prefix
	Engine config.Engine
	Debug  string // WINEDEBUG override, e.g. "+seh,+loaddll"
}

type Plan struct {
	Game      string
	Engine    string
	Backend   Backend
	Wine      string
	Args      []string
	Dir       string
	Env       []string          // full environment for the child
	Overrides map[string]string // only the variables cave set (logged)
}

// Build computes the command without running anything, so it is unit-testable
// and backs `cave run --dry-run`.
func Build(base []string, s Spec) (Plan, error) {
	b, err := ParseBackend(s.Game.Backend)
	if err != nil {
		return Plan{}, err
	}
	if b == D3DMetal && !s.Engine.D3DMetal {
		return Plan{}, fmt.Errorf("engine %q has no D3DMetal: register a GPTK engine or use --backend dxvk|wined3d", s.Engine.Name)
	}

	set := map[string]string{
		"WINEPREFIX": s.Prefix.Path,
		"WINEDEBUG":  "fixme-all",
		"WINEESYNC":  "1",
		"WINEMSYNC":  "1",
	}
	// winemenubuilder would add Windows shortcuts to the Mac's menus; it is
	// useless here and fails noisily under GPTK.
	overrides := "winemenubuilder.exe=d"
	switch b {
	case DXVK:
		overrides = "dxgi,d3d11,d3d10core=n,b;" + overrides
	case WineD3D:
		overrides = "dxgi,d3d9,d3d10core,d3d11,d3d12=b;" + overrides
	}
	for k, v := range engine.Env(s.Engine) {
		set[k] = v
	}
	if s.Engine.CX && b == D3DMetal {
		// CrossOver's Wine finds D3DMetal through this variable; its builtin
		// d3d11/dxgi then translate to Metal.
		set["CX_APPLEGPTK_LIBD3DSHARED_PATH"] = engine.LibD3DShared(s.Engine)
		overrides = "d3d11,dxgi=b;" + overrides
	}
	set["WINEDLLOVERRIDES"] = overrides
	if s.Game.HUD {
		set["MTL_HUD_ENABLED"] = "1" // Apple's Metal performance HUD (FPS, GPU time)
	}
	if s.Game.AVX {
		set["ROSETTA_ADVERTISE_AVX"] = "1" // needed by games that refuse to start without AVX
	}
	if s.Debug != "" {
		set["WINEDEBUG"] = s.Debug
	}
	for k, v := range s.Game.Env { // per-game overrides win
		set[k] = v
	}

	exe := prefix.ToUnix(s.Prefix.Path, s.Game.Exe)
	dir := filepath.Dir(exe)
	if s.Game.WorkDir != "" {
		dir = prefix.ToUnix(s.Prefix.Path, s.Game.WorkDir)
	}
	return Plan{
		Game:      s.Game.Name,
		Engine:    s.Engine.Name,
		Backend:   b,
		Wine:      s.Engine.Wine,
		Args:      append([]string{exe}, s.Game.Args...),
		Dir:       dir,
		Env:       mergeEnv(base, set),
		Overrides: set,
	}, nil
}

func mergeEnv(base []string, set map[string]string) []string {
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; !ok {
			out = append(out, kv)
		}
	}
	for _, k := range sortedKeys(set) {
		out = append(out, k+"="+set[k])
	}
	return out
}

// Shell renders the plan as a copy-pasteable command.
func (p Plan) Shell() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "cd %s && \\\n", quote(p.Dir))
	for _, k := range sortedKeys(p.Overrides) {
		fmt.Fprintf(&sb, "  %s=%s \\\n", k, quote(p.Overrides[k]))
	}
	sb.WriteString("  " + quote(p.Wine))
	for _, a := range p.Args {
		sb.WriteString(" " + quote(a))
	}
	return sb.String()
}

// Result is written next to the log as <timestamp>.json.
type Result struct {
	Game        string            `json:"game"`
	Engine      string            `json:"engine"`
	Backend     Backend           `json:"backend"`
	Command     []string          `json:"command"`
	Started     time.Time         `json:"started"`
	DurationSec float64           `json:"duration_sec"`
	ExitCode    int               `json:"exit_code"`            // of the process cave started
	WaitedSec   float64           `json:"waited_sec,omitempty"` // time spent waiting for its children
	Log         string            `json:"log"`
	Env         map[string]string `json:"env"` // cave's overrides only, never the full environment
}

// Run executes the plan. wine writes straight into logDir/<timestamp>.log
// (no pipe), so processes that outlive the one cave started, such as Steam
// relaunching itself after an update, keep logging and are never cut off.
// If echo is non-nil the log is followed and copied to it live.
// With waitAll, Run returns only once every process of the prefix has
// exited (`wineserver -w`). A non-zero exit is reported in Result, not as an
// error; an error means wine could not be started at all.
func Run(ctx context.Context, p Plan, logDir string, echo io.Writer, waitAll bool) (Result, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return Result{}, err
	}
	start := time.Now()
	stem := filepath.Join(logDir, start.Format("20060102-150405"))
	logPath := stem + ".log"
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_TRUNC, 0o644)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	fmt.Fprintf(f, "# cave run %s (engine %s, backend %s) %s\n", p.Game, p.Engine, p.Backend, start.Format(time.RFC3339))
	fmt.Fprintf(f, "# %s\n\n", strings.ReplaceAll(p.Shell(), "\n", "\n# "))
	offset, _ := f.Seek(0, io.SeekCurrent)

	stopFollow := make(chan struct{})
	followDone := make(chan struct{})
	if echo != nil {
		go func() { defer close(followDone); follow(logPath, offset, echo, stopFollow) }()
	} else {
		close(followDone)
	}

	cmd := exec.CommandContext(ctx, p.Wine, p.Args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = p.Dir, p.Env, f, f
	// On Ctrl-C, ask wine to stop instead of SIGKILL, then give it time.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	runErr := cmd.Run()
	exited := time.Now()

	res := Result{
		Game: p.Game, Engine: p.Engine, Backend: p.Backend,
		Command:  append([]string{p.Wine}, p.Args...),
		Started:  start,
		ExitCode: -1,
		Log:      logPath,
		Env:      p.Overrides,
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) && cmd.ProcessState == nil {
		close(stopFollow)
		<-followDone
		res.DurationSec = time.Since(start).Seconds()
		fmt.Fprintf(f, "\n# cave: failed to start: %v\n", runErr)
		_ = writeJSON(stem+".json", res)
		return res, fmt.Errorf("starting wine: %w", runErr)
	}

	if waitAll && ctx.Err() == nil {
		if err := waitPrefix(ctx, p); err != nil {
			fmt.Fprintf(f, "\n# cave: could not wait for the prefix: %v\n", err)
		}
		res.WaitedSec = time.Since(exited).Seconds()
	}
	close(stopFollow)
	<-followDone

	res.DurationSec = time.Since(start).Seconds()
	fmt.Fprintf(f, "\n# cave: exit code %d after %.1fs (waited %.1fs for the prefix)\n", res.ExitCode, res.DurationSec, res.WaitedSec)
	return res, writeJSON(stem+".json", res)
}

// waitPrefix blocks until every process in the plan's prefix has exited.
func waitPrefix(ctx context.Context, p Plan) error {
	ws := filepath.Join(filepath.Dir(p.Wine), "wineserver")
	if _, err := os.Stat(ws); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, ws, "-w")
	cmd.Env = p.Env // includes WINEPREFIX and the engine's library path
	return cmd.Run()
}

// follow copies what is appended to path from offset until stop is closed.
func follow(path string, offset int64, w io.Writer, stop <-chan struct{}) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return
	}
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		_, _ = io.Copy(w, f) // reads up to the current end of file
		select {
		case <-stop:
			_, _ = io.Copy(w, f)
			return
		case <-tick.C:
		}
	}
}

// History returns past run summaries for a game, newest first.
func History(logDir string) ([]Result, error) {
	paths, err := filepath.Glob(filepath.Join(logDir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	var out []Result
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r Result
		if json.Unmarshal(data, &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"\\$`!*?()[]{};&|<>") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
