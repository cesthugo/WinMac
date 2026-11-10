// Package prefix creates Wine prefixes and maps Windows paths into them.
package prefix

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"cave/internal/engine"
)

var winPath = regexp.MustCompile(`^([A-Za-z]):[\\/](.*)$`)

// ToUnix maps `C:\Games\x.exe` to `<prefix>/drive_c/Games/x.exe`.
// Other drives go through dosdevices; Unix paths are returned unchanged.
func ToUnix(prefixDir, p string) string {
	m := winPath.FindStringSubmatch(p)
	if m == nil {
		return p
	}
	drive := strings.ToLower(m[1])
	rest := strings.ReplaceAll(m[2], `\`, "/")
	if drive == "c" {
		return filepath.Join(prefixDir, "drive_c", rest)
	}
	return filepath.Join(prefixDir, "dosdevices", drive+":", rest)
}

// Initialised reports whether wineboot has already run in dir.
func Initialised(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "system.reg"))
	return err == nil
}

// Create runs `wineboot --init` in a new prefix and waits for wineserver
// to flush the registry before returning.
// extraEnv carries the engine's own variables (see engine.Env).
func Create(ctx context.Context, wine, dir string, extraEnv []string, out io.Writer) error {
	if Initialised(dir) {
		return fmt.Errorf("prefix already initialised: %s", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	env := append(append(os.Environ(), extraEnv...), "WINEPREFIX="+dir, "WINEDEBUG=-all",
		"WINEDLLOVERRIDES=winemenubuilder.exe=d")

	boot := exec.CommandContext(ctx, wine, "wineboot", "--init")
	boot.Env, boot.Stdout, boot.Stderr = env, out, out
	if err := boot.Run(); err != nil {
		return fmt.Errorf("wineboot: %w", err)
	}
	wait := exec.CommandContext(ctx, engine.Wineserver(wine), "-w")
	wait.Env, wait.Stdout, wait.Stderr = env, out, out
	if err := wait.Run(); err != nil {
		return fmt.Errorf("wineserver -w: %w", err)
	}
	if !Initialised(dir) {
		return fmt.Errorf("wineboot finished but %s has no system.reg", dir)
	}
	return nil
}

// Kill stops every process of the prefix (`wineserver -k`).
func Kill(ctx context.Context, wine, dir string, extraEnv []string) error {
	cmd := exec.CommandContext(ctx, engine.Wineserver(wine), "-k")
	cmd.Env = append(append(os.Environ(), extraEnv...), "WINEPREFIX="+dir)
	return cmd.Run()
}
