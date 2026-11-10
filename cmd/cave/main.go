// Command cave runs Windows games on Apple Silicon Macs through Wine and
// Apple's Game Porting Toolkit: one prefix per game, per-game profiles,
// and a log + JSON summary for every run.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"cave/internal/config"
	"cave/internal/doctor"
	"cave/internal/engine"
	"cave/internal/launch"
	"cave/internal/prefix"
	"cave/internal/steam"
)

const usage = `cave — run Windows games on Apple Silicon

Setup
  cave doctor                              check Rosetta, memory, engines
  cave engine detect [--register]          find installed Wine/GPTK builds
  cave engine add <name> <dir>             register a build manually
  cave engine install <archive|URL>        install an engine built by engine/build.sh
  cave engine import-d3dmetal <engine>     copy D3DMetal from your GPTK into it
  cave engine list | default <name>
  cave prefix create <name> [--engine E]   new isolated Windows environment
  cave prefix list

Steam
  cave steam install <prefix>              install the Windows Steam client
  cave steam run <prefix>                  open Steam (log in, install games)
  cave steam add <appid> --name N --prefix P [profile flags]

Games
  cave add <name> --prefix P --exe PATH [profile flags] [-- game args]
  cave list
  cave run <name> [--dry-run] [--debug CHANNELS] [--quiet] [--no-wait]
  cave exec <prefix> <exe> [-- args]       run an installer or any .exe
  cave logs <name> [--prefix P] [-n N] [--tail LINES]
                                           (Steam itself: cave logs steam --prefix <prefix>)
  cave kill <prefix>                       stop every process of a prefix

Profile flags
  --backend d3dmetal|dxvk|wined3d   --hud   --avx   --workdir DIR   --env KEY=VALUE

Data lives in ~/Library/Application Support/Cave (override with CAVE_HOME).
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cave:", err)
		os.Exit(1)
	}
}

type app struct {
	paths config.Paths
	db    *config.DB
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	if err := paths.Ensure(); err != nil {
		return err
	}
	db, err := config.Load(paths.DB)
	if err != nil {
		return err
	}
	a := &app{paths: paths, db: db}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "doctor":
		return a.doctor()
	case "engine":
		return a.engineCmd(rest)
	case "prefix":
		return a.prefixCmd(ctx, rest)
	case "steam":
		return a.steamCmd(ctx, rest)
	case "add":
		return a.add(rest)
	case "list", "ls":
		return a.list()
	case "run":
		return a.runGame(ctx, rest)
	case "exec":
		return a.exec(ctx, rest)
	case "logs":
		return a.logs(rest)
	case "kill":
		return a.kill(ctx, rest)
	}
	return fmt.Errorf("unknown command %q (see `cave help`)", cmd)
}

func (a *app) save() error { return a.db.Save(a.paths.DB) }

// ---------- doctor ----------

func (a *app) doctor() error {
	failed := 0
	for _, c := range doctor.Run(a.db) {
		mark := "✓"
		if !c.OK {
			mark, failed = "✗", failed+1
		}
		fmt.Printf("%s %-16s %s\n", mark, c.Name, c.Detail)
		if !c.OK && c.Fix != "" {
			fmt.Printf("  → %s\n", c.Fix)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

// ---------- engines ----------

func (a *app) engineCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cave engine detect|add|install|import-d3dmetal|list|default")
	}
	switch args[0] {
	case "list":
		w := table()
		fmt.Fprintln(w, "NAME\tD3DMETAL\tWINE")
		for _, n := range config.SortedKeys(a.db.Engines) {
			e := a.db.Engines[n]
			if n == a.db.DefaultEngine {
				n += " (default)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", n, yesNo(e.D3DMetal), e.Wine)
		}
		return w.Flush()

	case "add":
		if len(args) != 3 {
			return errors.New("usage: cave engine add <name> <dir>")
		}
		e, err := engine.Inspect(args[1], args[2])
		if err != nil {
			return err
		}
		a.registerEngine(e)
		fmt.Printf("registered %s (D3DMetal: %s)\n", e.Name, yesNo(e.D3DMetal))
		return a.save()

	case "detect":
		fs := newFlags("engine detect")
		register := fs.Bool("register", false, "register every engine found")
		if _, _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		found := engine.Detect(a.paths.Engines)
		if len(found) == 0 {
			fmt.Println("no engine found; install Apple's Game Porting Toolkit, or drop a Wine build in", a.paths.Engines)
			return nil
		}
		for _, e := range found {
			fmt.Printf("%-24s D3DMetal: %-3s %s\n", e.Name, yesNo(e.D3DMetal), e.Dir)
			if *register {
				a.registerEngine(e)
			}
		}
		if *register {
			return a.save()
		}
		return nil

	case "install":
		return a.engineInstall(args[1:])

	case "import-d3dmetal":
		return a.importD3DMetal(args[1:])

	case "default":
		if len(args) != 2 {
			return errors.New("usage: cave engine default <name>")
		}
		if _, err := a.db.Engine(args[1]); err != nil {
			return err
		}
		a.db.DefaultEngine = args[1]
		return a.save()
	}
	return fmt.Errorf("unknown engine subcommand %q", args[0])
}

// engineInstall unpacks a release archive from engine/build.sh (a local
// .tar.xz or an https URL) into the engines folder and registers it.
func (a *app) engineInstall(args []string) error {
	fs := newFlags("engine install")
	name := fs.String("name", "", "engine name (default: from the archive name)")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: cave engine install <archive.tar.xz | https URL> [--name N]")
	}
	archive := pos[0]
	if strings.HasPrefix(archive, "https://") {
		dest := filepath.Join(a.paths.Cache, filepath.Base(archive))
		fmt.Println("downloading", archive)
		if err := steam.Download(context.Background(), archive, dest, false); err != nil {
			return err
		}
		archive = dest
	}
	if *name == "" {
		*name = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(archive), "cave-wine-"), "-x86_64.tar.xz")
		*name = strings.TrimSuffix(*name, ".tar.xz")
	}
	if err := checkName(*name); err != nil {
		return err
	}
	dir := filepath.Join(a.paths.Engines, *name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s already exists; remove it or pick another --name", dir)
	}
	tmp := dir + ".partial"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	fmt.Println("extracting", filepath.Base(archive))
	if out, err := exec.Command("tar", "-xJf", archive, "-C", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("tar: %v: %s", err, out)
	}
	root := filepath.Join(tmp, "wine") // archives contain a single wine/ folder
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("%s has no wine/ folder at its root", archive)
	}
	if err := os.Rename(root, dir); err != nil {
		return err
	}
	_ = os.RemoveAll(tmp)
	// Archives downloaded by a browser are quarantined; Wine's dylibs would be blocked.
	_ = exec.Command("xattr", "-dr", "com.apple.quarantine", dir).Run()

	e, err := engine.Inspect(*name, dir)
	if err != nil {
		return err
	}
	a.db.Engines[e.Name] = e
	fmt.Printf("installed %s (CrossOver sources: %s, D3DMetal: %s)\n", e.Name, yesNo(e.CX), yesNo(e.D3DMetal))
	if !e.D3DMetal {
		fmt.Printf("next: cave engine import-d3dmetal %s\n", e.Name)
	}
	return a.save()
}

// importD3DMetal copies D3DMetal from a GPTK engine into a CX engine.
func (a *app) importD3DMetal(args []string) error {
	fs := newFlags("engine import-d3dmetal")
	from := fs.String("from", "", "folder of a GPTK install (default: a registered GPTK engine)")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: cave engine import-d3dmetal <engine> [--from DIR]")
	}
	target, err := a.db.Engine(pos[0])
	if err != nil {
		return err
	}
	if !target.CX {
		return fmt.Errorf("%s is not built from CrossOver sources; it cannot load an imported D3DMetal", target.Name)
	}
	src := *from
	if src == "" {
		for _, n := range config.SortedKeys(a.db.Engines) {
			if e := a.db.Engines[n]; e.D3DMetal && !e.CX {
				src = e.Dir
				break
			}
		}
	}
	if src == "" {
		return errors.New("no GPTK engine registered: install the Game Porting Toolkit or pass --from")
	}
	fmt.Printf("copying D3DMetal from %s\n", src)
	if err := engine.ImportD3DMetal(src, target.Dir); err != nil {
		return err
	}
	e, err := engine.Inspect(target.Name, target.Dir)
	if err != nil {
		return err
	}
	a.registerEngine(e)
	fmt.Printf("%s now has D3DMetal: %s (default engine: %s)\n", e.Name, yesNo(e.D3DMetal), a.db.DefaultEngine)
	return a.save()
}

// registerEngine stores e; the first engine with D3DMetal becomes the default.
func (a *app) registerEngine(e config.Engine) {
	a.db.Engines[e.Name] = e
	cur, ok := a.db.Engines[a.db.DefaultEngine]
	if !ok || score(e) > score(cur) {
		a.db.DefaultEngine = e.Name
	}
}

// score ranks engines for the default: D3DMetal first, then a recent CX Wine.
func score(e config.Engine) int {
	s := 0
	if e.D3DMetal {
		s += 2
	}
	if e.CX {
		s++
	}
	return s
}

// ---------- prefixes ----------

func (a *app) prefixCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cave prefix create|list")
	}
	switch args[0] {
	case "list":
		w := table()
		fmt.Fprintln(w, "NAME\tENGINE\tPATH")
		for _, n := range config.SortedKeys(a.db.Prefixes) {
			p := a.db.Prefixes[n]
			fmt.Fprintf(w, "%s\t%s\t%s\n", n, p.Engine, p.Path)
		}
		return w.Flush()

	case "create":
		fs := newFlags("prefix create")
		eng := fs.String("engine", "", "engine to use (default engine if empty)")
		pos, _, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: cave prefix create <name> [--engine E]")
		}
		name := pos[0]
		if err := checkName(name); err != nil {
			return err
		}
		if _, exists := a.db.Prefixes[name]; exists {
			return fmt.Errorf("prefix %q already exists", name)
		}
		e, err := a.db.Engine(*eng)
		if err != nil {
			return err
		}
		dir := filepath.Join(a.paths.Prefixes, name)
		fmt.Printf("creating prefix %s with %s (first run takes a minute)...\n", name, e.Name)
		if err := prefix.Create(ctx, e.Wine, dir, engine.EnvList(nil, e), os.Stdout); err != nil {
			return err
		}
		a.db.Prefixes[name] = config.Prefix{Name: name, Path: dir, Engine: e.Name}
		fmt.Println("done:", dir)
		return a.save()
	}
	return fmt.Errorf("unknown prefix subcommand %q", args[0])
}

// ---------- games ----------

type profileFlags struct {
	backend, workdir string
	hud, avx         bool
	env              kvFlag
}

func addProfileFlags(fs *flag.FlagSet) *profileFlags {
	pf := &profileFlags{env: kvFlag{}}
	fs.StringVar(&pf.backend, "backend", "", "d3dmetal | dxvk | wined3d (default: d3dmetal if the engine has it)")
	fs.StringVar(&pf.workdir, "workdir", "", "working directory (default: the exe's folder)")
	fs.BoolVar(&pf.hud, "hud", false, "show Apple's Metal performance HUD")
	fs.BoolVar(&pf.avx, "avx", false, "advertise AVX through Rosetta (ROSETTA_ADVERTISE_AVX=1)")
	fs.Var(pf.env, "env", "extra KEY=VALUE, repeatable")
	return pf
}

// newGame validates and assembles a game profile.
func (a *app) newGame(name, prefixName, exe string, args []string, pf *profileFlags) (config.Game, error) {
	if err := checkName(name); err != nil {
		return config.Game{}, err
	}
	p, err := a.db.Prefix(prefixName)
	if err != nil {
		return config.Game{}, err
	}
	e, err := a.db.Engine(p.Engine)
	if err != nil {
		return config.Game{}, err
	}
	backend := launch.DefaultBackend(e)
	if pf.backend != "" {
		if backend, err = launch.ParseBackend(pf.backend); err != nil {
			return config.Game{}, err
		}
	}
	g := config.Game{
		Name: name, Prefix: prefixName, Exe: exe, Args: args, WorkDir: pf.workdir,
		Backend: string(backend), HUD: pf.hud, AVX: pf.avx, Env: pf.env,
	}
	// Fail early on an impossible profile (e.g. D3DMetal on a plain Wine engine).
	if _, err := launch.Build(nil, launch.Spec{Game: g, Prefix: p, Engine: e}); err != nil {
		return config.Game{}, err
	}
	if _, err := os.Stat(prefix.ToUnix(p.Path, exe)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s not found yet in prefix %s\n", exe, prefixName)
	}
	return g, nil
}

func (a *app) add(args []string) error {
	fs := newFlags("add")
	pre := fs.String("prefix", "", "prefix the game is installed in")
	exe := fs.String("exe", "", `executable, Windows path (C:\...) or Unix path`)
	pf := addProfileFlags(fs)
	pos, passthrough, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *pre == "" || *exe == "" {
		return errors.New("usage: cave add <name> --prefix P --exe PATH [profile flags] [-- game args]")
	}
	g, err := a.newGame(pos[0], *pre, *exe, passthrough, pf)
	if err != nil {
		return err
	}
	a.db.Games[g.Name] = g
	fmt.Printf("added %s (backend %s) — launch with `cave run %s`\n", g.Name, g.Backend, g.Name)
	return a.save()
}

func (a *app) list() error {
	w := table()
	fmt.Fprintln(w, "NAME\tPREFIX\tBACKEND\tLAST RUN")
	for _, n := range config.SortedKeys(a.db.Games) {
		g := a.db.Games[n]
		last := "never"
		if h, _ := launch.History(filepath.Join(a.paths.Logs, n)); len(h) > 0 {
			last = fmt.Sprintf("%s (exit %d)", h[0].Started.Format("2006-01-02 15:04"), h[0].ExitCode)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", n, g.Prefix, g.Backend, last)
	}
	return w.Flush()
}

type runOpts struct {
	dryRun, quiet, noWait bool
	debug                 string
}

func addRunFlags(fs *flag.FlagSet) *runOpts {
	o := &runOpts{}
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the command and environment, run nothing")
	fs.BoolVar(&o.quiet, "quiet", false, "write output to the log only")
	fs.BoolVar(&o.noWait, "no-wait", false, "return when the started process exits, even if it left others running")
	fs.StringVar(&o.debug, "debug", "", `WINEDEBUG channels, e.g. "+seh,+loaddll"`)
	return o
}

func (a *app) runGame(ctx context.Context, args []string) error {
	fs := newFlags("run")
	o := addRunFlags(fs)
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: cave run <name> [--dry-run] [--debug CHANNELS] [--quiet]")
	}
	g, err := a.db.Game(pos[0])
	if err != nil {
		return err
	}
	return a.launch(ctx, g, filepath.Join(a.paths.Logs, g.Name), o)
}

func (a *app) exec(ctx context.Context, args []string) error {
	fs := newFlags("exec")
	pf := addProfileFlags(fs)
	o := addRunFlags(fs)
	pos, passthrough, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("usage: cave exec <prefix> <exe> [profile flags] [-- args]")
	}
	exe, err := filepath.Abs(pos[1])
	if strings.Contains(pos[1], `:\`) || err != nil {
		exe = pos[1] // Windows path inside the prefix
	}
	name := "exec-" + strings.TrimSuffix(strings.ToLower(filepath.Base(filepath.ToSlash(strings.ReplaceAll(exe, `\`, "/")))), ".exe")
	g, err := a.newGame(name, pos[0], exe, passthrough, pf)
	if err != nil {
		return err
	}
	return a.launch(ctx, g, filepath.Join(a.paths.Logs, "_"+pos[0], name), o)
}

func (a *app) launch(ctx context.Context, g config.Game, logDir string, o *runOpts) error {
	p, err := a.db.Prefix(g.Prefix)
	if err != nil {
		return err
	}
	e, err := a.db.Engine(p.Engine)
	if err != nil {
		return err
	}
	plan, err := launch.Build(os.Environ(), launch.Spec{Game: g, Prefix: p, Engine: e, Debug: o.debug})
	if err != nil {
		return err
	}
	if o.dryRun {
		fmt.Println(plan.Shell())
		return nil
	}
	if _, err := os.Stat(plan.Args[0]); err != nil {
		return fmt.Errorf("executable not found: %s (is it installed in prefix %s?)", plan.Args[0], g.Prefix)
	}
	var echo io.Writer
	if !o.quiet {
		echo = os.Stdout
	}
	fmt.Fprintf(os.Stderr, "▶ %s  [engine %s · backend %s]\n", g.Name, e.Name, plan.Backend)
	res, err := launch.Run(ctx, plan, logDir, echo, !o.noWait)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "■ exit %d after %s — log: %s\n", res.ExitCode,
		(time.Duration(res.DurationSec * float64(time.Second))).Round(time.Second), res.Log)
	switch {
	case ctx.Err() != nil:
		fmt.Fprintf(os.Stderr, "interrupted; if anything is still running: cave kill %s\n", g.Prefix)
		return nil
	case res.ExitCode == 0:
		return nil
	case res.ExitCode == steam.RestartExitCode && strings.EqualFold(filepath.Base(strings.ReplaceAll(g.Exe, `\`, "/")), "steam.exe"):
		fmt.Fprintln(os.Stderr, "(code 42: Steam restarted itself after an update — not an error)")
		return nil
	}
	return fmt.Errorf("%s exited with code %d", g.Name, res.ExitCode)
}

func (a *app) logs(args []string) error {
	fs := newFlags("logs")
	n := fs.Int("n", 5, "number of runs to list")
	tail := fs.Int("tail", 0, "print the last N lines of the latest log")
	pre := fs.String("prefix", "", "logs of a prefix-level run (steam, steam-setup, exec-*) in that prefix")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: cave logs <name> [--prefix P] [-n N] [--tail LINES]")
	}
	dir := filepath.Join(a.paths.Logs, pos[0])
	if *pre != "" {
		dir = filepath.Join(a.paths.Logs, "_"+*pre, pos[0])
	}
	hist, err := launch.History(dir)
	if err != nil {
		return err
	}
	if len(hist) == 0 {
		fmt.Println("no runs recorded for", pos[0])
		return nil
	}
	if *tail > 0 {
		return printTail(hist[0].Log, *tail)
	}
	w := table()
	fmt.Fprintln(w, "STARTED\tEXIT\tDURATION\tBACKEND\tLOG")
	for i, r := range hist {
		if i == *n {
			break
		}
		fmt.Fprintf(w, "%s\t%d\t%.0fs\t%s\t%s\n", r.Started.Format("2006-01-02 15:04:05"), r.ExitCode, r.DurationSec, r.Backend, r.Log)
	}
	return w.Flush()
}

func (a *app) kill(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: cave kill <prefix>")
	}
	p, err := a.db.Prefix(args[0])
	if err != nil {
		return err
	}
	e, err := a.db.Engine(p.Engine)
	if err != nil {
		return err
	}
	return prefix.Kill(ctx, e.Wine, p.Path, engine.EnvList(nil, e))
}

// ---------- steam ----------

func (a *app) steamCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cave steam install|run|add")
	}
	switch args[0] {
	case "install":
		fs := newFlags("steam install")
		url := fs.String("url", steam.DefaultInstallerURL, "installer URL")
		force := fs.Bool("force", false, "download the installer again")
		pos, _, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: cave steam install <prefix>")
		}
		installer := filepath.Join(a.paths.Cache, "SteamSetup.exe")
		fmt.Println("downloading", *url)
		if err := steam.Download(ctx, *url, installer, *force); err != nil {
			return err
		}
		g, err := a.newGame("steam-setup", pos[0], installer, []string{"/S"}, &profileFlags{env: kvFlag{}})
		if err != nil {
			return err
		}
		if err := a.launch(ctx, g, filepath.Join(a.paths.Logs, "_"+pos[0], "steam-setup"), &runOpts{}); err != nil {
			return err
		}
		fmt.Printf("Steam installed. Next: `cave steam run %s` to log in and install games.\n", pos[0])
		return nil

	case "run":
		fs := newFlags("steam run")
		o := addRunFlags(fs)
		pos, passthrough, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: cave steam run <prefix> [-- steam args]")
		}
		g, err := a.newGame("steam", pos[0], steam.ExeWinPath, passthrough, &profileFlags{env: kvFlag{}})
		if err != nil {
			return err
		}
		return a.launch(ctx, g, filepath.Join(a.paths.Logs, "_"+pos[0], "steam"), o)

	case "add":
		fs := newFlags("steam add")
		name := fs.String("name", "", "name to use with `cave run`")
		pre := fs.String("prefix", "", "prefix where Steam is installed")
		pf := addProfileFlags(fs)
		pos, _, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || *name == "" || *pre == "" {
			return errors.New("usage: cave steam add <appid> --name N --prefix P [profile flags]")
		}
		launchArgs, err := steam.LaunchArgs(pos[0])
		if err != nil {
			return err
		}
		g, err := a.newGame(*name, *pre, steam.ExeWinPath, launchArgs, pf)
		if err != nil {
			return err
		}
		a.db.Games[g.Name] = g
		fmt.Printf("added %s (Steam app %s, backend %s)\n", g.Name, pos[0], g.Backend)
		return a.save()
	}
	return fmt.Errorf("unknown steam subcommand %q", args[0])
}

// ---------- helpers ----------

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("cave "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// parse lets flags and positionals mix (`cave add x --prefix p`) and keeps
// everything after `--` untouched for the game.
func parse(fs *flag.FlagSet, args []string) (pos, passthrough []string, err error) {
	for i, s := range args {
		if s == "--" {
			args, passthrough = args[:i], args[i+1:]
			break
		}
	}
	for {
		if err := fs.Parse(args); err != nil {
			return nil, nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, passthrough, nil
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

type kvFlag map[string]string

func (k kvFlag) String() string { return "" }
func (k kvFlag) Set(s string) error {
	key, val, ok := strings.Cut(s, "=")
	if !ok || key == "" {
		return fmt.Errorf("expected KEY=VALUE, got %q", s)
	}
	k[key] = val
	return nil
}

func checkName(n string) error {
	if n == "" || strings.ContainsAny(n, `/\ `) || strings.HasPrefix(n, "_") || strings.HasPrefix(n, ".") {
		return fmt.Errorf("invalid name %q: use letters, digits, - or _ (not first)", n)
	}
	return nil
}

func table() *tabwriter.Writer { return tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0) }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func printTail(path string, n int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	ring := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, sc.Text())
	}
	for _, l := range ring {
		fmt.Println(l)
	}
	return sc.Err()
}
