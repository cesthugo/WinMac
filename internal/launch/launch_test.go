package launch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"cave/internal/config"
)

func spec(backend string, d3dmetal bool) Spec {
	return Spec{
		Game:   config.Game{Name: "eldenring", Prefix: "er", Exe: `C:\Games\ELDEN RING\eldenring.exe`, Backend: backend},
		Prefix: config.Prefix{Name: "er", Path: "/p/er", Engine: "gptk"},
		Engine: config.Engine{Name: "gptk", Wine: "/e/bin/wine64", D3DMetal: d3dmetal},
	}
}

func env(p Plan, key string) (string, bool) {
	for _, kv := range p.Env {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v, true
		}
	}
	return "", false
}

func TestBuildD3DMetal(t *testing.T) {
	p, err := Build([]string{"HOME=/u", "WINEDEBUG=+all"}, spec("d3dmetal", true))
	if err != nil {
		t.Fatal(err)
	}
	if p.Args[0] != "/p/er/drive_c/Games/ELDEN RING/eldenring.exe" {
		t.Errorf("exe = %q", p.Args[0])
	}
	if p.Dir != "/p/er/drive_c/Games/ELDEN RING" {
		t.Errorf("dir = %q", p.Dir)
	}
	if v, _ := env(p, "WINEPREFIX"); v != "/p/er" {
		t.Errorf("WINEPREFIX = %q", v)
	}
	if v, _ := env(p, "WINEDEBUG"); v != "fixme-all" {
		t.Errorf("cave's WINEDEBUG should replace the inherited one, got %q", v)
	}
	if v, _ := env(p, "WINEDLLOVERRIDES"); v != "winemenubuilder.exe=d" {
		t.Errorf("d3dmetal must only disable winemenubuilder, got %q", v)
	}
	if v, _ := env(p, "HOME"); v != "/u" {
		t.Error("unrelated variables must be inherited")
	}
	count := 0
	for _, kv := range p.Env {
		if strings.HasPrefix(kv, "WINEDEBUG=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("WINEDEBUG appears %d times", count)
	}
}

func TestBuildRejectsD3DMetalWithoutGPTK(t *testing.T) {
	if _, err := Build(nil, spec("d3dmetal", false)); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBuildDXVKAndFlags(t *testing.T) {
	s := spec("dxvk", false)
	s.Game.HUD, s.Game.AVX = true, true
	s.Game.Env = map[string]string{"WINEMSYNC": "0", "DXVK_HUD": "fps"}
	s.Debug = "+seh"
	p, err := Build(nil, s)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"WINEDLLOVERRIDES":      "dxgi,d3d11,d3d10core=n,b;winemenubuilder.exe=d",
		"MTL_HUD_ENABLED":       "1",
		"ROSETTA_ADVERTISE_AVX": "1",
		"WINEMSYNC":             "0", // per-game env wins over cave's default
		"DXVK_HUD":              "fps",
		"WINEDEBUG":             "+seh",
	}
	for k, v := range want {
		if got, _ := env(p, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestBuildWorkDirAndUnknownBackend(t *testing.T) {
	s := spec("wined3d", false)
	s.Game.WorkDir = `C:\Games`
	p, err := Build(nil, s)
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != "/p/er/drive_c/Games" {
		t.Errorf("dir = %q", p.Dir)
	}
	if _, err := Build(nil, spec("vulkan", true)); err == nil {
		t.Error("unknown backend should fail")
	}
}

func TestShellQuoting(t *testing.T) {
	p, _ := Build(nil, spec("d3dmetal", true))
	sh := p.Shell()
	if !strings.Contains(sh, "'/p/er/drive_c/Games/ELDEN RING/eldenring.exe'") {
		t.Errorf("path with spaces not quoted:\n%s", sh)
	}
}

// TestRunRecordsLogAndSummary uses a shell script in place of wine.
func TestRunRecordsLogAndSummary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "wine64")
	script := "#!/bin/sh\necho \"args: $*\"\necho \"prefix: $WINEPREFIX\" >&2\nexit 3\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s := spec("d3dmetal", true)
	s.Engine.Wine = fake
	s.Game.Exe = "/tmp/game.exe"
	s.Game.Args = []string{"-windowed"}
	p, err := Build(os.Environ(), s)
	if err != nil {
		t.Fatal(err)
	}
	p.Dir = dir

	logs := filepath.Join(dir, "logs")
	res, err := Run(context.Background(), p, logs, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", res.ExitCode)
	}
	data, err := os.ReadFile(res.Log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"args: /tmp/game.exe -windowed", "prefix: /p/er", "exit code 3"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log misses %q:\n%s", want, data)
		}
	}
	hist, err := History(logs)
	if err != nil || len(hist) != 1 || hist[0].ExitCode != 3 || hist[0].Backend != D3DMetal {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	if !slices.Contains(hist[0].Command, "-windowed") {
		t.Errorf("command not recorded: %v", hist[0].Command)
	}
	if _, leaked := hist[0].Env["HOME"]; leaked {
		t.Error("summary must only contain cave's overrides")
	}
}

func TestRunMissingWine(t *testing.T) {
	s := spec("d3dmetal", true)
	s.Engine.Wine = "/nonexistent/wine64"
	p, _ := Build(nil, s)
	p.Dir = t.TempDir()
	res, err := Run(context.Background(), p, t.TempDir(), nil, false)
	if err == nil || res.ExitCode != -1 {
		t.Fatalf("want start failure, got %+v, %v", res, err)
	}
}

// Regression: Steam's updater exits with 42 and hands over to a new process
// that keeps writing. That process must neither be cut off nor lose its
// output, and with waitAll cave must wait for it (via wineserver -w).
func TestRunSurvivesHandOverAndWaits(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "wine64")
	marker := filepath.Join(dir, "child.pid")
	script := "#!/bin/sh\necho parent\n( sleep 0.6; echo child-finished ) &\necho $! > " + marker + "\nexit 42\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Fake `wineserver -w`: wait until the child is gone.
	ws := "#!/bin/sh\nwhile kill -0 $(cat " + marker + ") 2>/dev/null; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(dir, "wineserver"), []byte(ws), 0o755); err != nil {
		t.Fatal(err)
	}
	s := spec("d3dmetal", true)
	s.Engine.Wine = fake
	s.Game.Exe = "/tmp/steam.exe"
	p, err := Build(os.Environ(), s)
	if err != nil {
		t.Fatal(err)
	}
	p.Dir = dir

	var echoed strings.Builder
	res, err := Run(context.Background(), p, filepath.Join(dir, "logs"), &echoed, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 42 {
		t.Errorf("exit = %d, want 42", res.ExitCode)
	}
	if res.WaitedSec < 0.3 {
		t.Errorf("did not wait for the child (waited %.2fs)", res.WaitedSec)
	}
	data, _ := os.ReadFile(res.Log)
	for _, want := range []string{"parent", "child-finished"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log misses %q:\n%s", want, data)
		}
		if !strings.Contains(echoed.String(), want) {
			t.Errorf("echo misses %q: %q", want, echoed.String())
		}
	}
}

func TestBuildCXEngine(t *testing.T) {
	s := spec("d3dmetal", true)
	s.Engine.CX, s.Engine.Dir = true, "/e"
	p, err := Build(nil, s)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"DYLD_FALLBACK_LIBRARY_PATH":     "/e/lib:/usr/lib",
		"CX_APPLEGPTK_LIBD3DSHARED_PATH": "/e/lib/external/libd3dshared.dylib",
		"WINEDLLOVERRIDES":               "d3d11,dxgi=b;winemenubuilder.exe=d",
	}
	for k, v := range want {
		if got, _ := env(p, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// DXVK on a CX engine still gets the bundled libraries, but no D3DMetal.
	s.Game.Backend = "dxvk"
	p, _ = Build(nil, s)
	if _, ok := env(p, "CX_APPLEGPTK_LIBD3DSHARED_PATH"); ok {
		t.Error("D3DMetal path set for dxvk")
	}
	if v, _ := env(p, "DYLD_FALLBACK_LIBRARY_PATH"); v != "/e/lib:/usr/lib" {
		t.Errorf("library path missing for dxvk: %q", v)
	}
}
