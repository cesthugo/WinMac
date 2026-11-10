// Package engine finds and inspects Wine builds (Apple's Game Porting
// Toolkit, Wine Stable/Staging, ...). cave never ships an engine: the user
// installs one, cave only registers it.
package engine

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"cave/internal/config"
)

// Candidates are common install locations, checked by `cave engine detect`.
// Layouts change between releases: when detection misses, use `cave engine add`.
var Candidates = []string{
	"/Applications/Game Porting Toolkit.app/Contents/Resources/wine",
	"/usr/local/opt/game-porting-toolkit",
	"/Applications/Wine Stable.app/Contents/Resources/wine",
	"/Applications/Wine Staging.app/Contents/Resources/wine",
	"/Applications/Wine Devel.app/Contents/Resources/wine",
}

// FindWine returns the wine loader inside dir/bin. wine64 is preferred:
// GPTK (Wine 7.7) ships it, newer WoW64 builds only ship `wine`.
func FindWine(dir string) (string, error) {
	for _, name := range []string{"wine64", "wine"} {
		p := filepath.Join(dir, "bin", name)
		if isExecutable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("no wine64 or wine binary in %s", filepath.Join(dir, "bin"))
}

// Wineserver returns the wineserver next to the wine loader.
func Wineserver(wine string) string {
	return filepath.Join(filepath.Dir(wine), "wineserver")
}

// HasD3DMetal reports whether the build bundles Apple's D3DMetal
// (DirectX 11/12 -> Metal), searching a few levels deep.
func HasD3DMetal(dir string) bool {
	root := filepath.Clean(dir)
	depth0 := strings.Count(root, string(os.PathSeparator))
	found := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if n := d.Name(); n == "D3DMetal.framework" || n == "libd3dshared.dylib" {
			found = true
			return fs.SkipAll
		}
		if d.IsDir() && strings.Count(p, string(os.PathSeparator))-depth0 >= 5 {
			return fs.SkipDir
		}
		return nil
	})
	return found
}

// Inspect builds an Engine record for dir.
func Inspect(name, dir string) (config.Engine, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return config.Engine{}, err
	}
	wine, err := FindWine(abs)
	if err != nil {
		return config.Engine{}, err
	}
	return config.Engine{Name: name, Dir: abs, Wine: wine, D3DMetal: HasD3DMetal(abs), CX: IsCX(abs)}, nil
}

// IsCX reports whether dir is a Wine built from CrossOver sources by
// engine/build.sh (or by conversio-wine, which uses the same layout).
func IsCX(dir string) bool {
	for _, marker := range []string{"CAVE-ENGINE.txt", "CONVERSIO-BUILD.txt"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// LibD3DShared is where a CX engine expects Apple's D3DMetal loader.
func LibD3DShared(e config.Engine) string {
	return filepath.Join(e.Dir, "lib", "external", "libd3dshared.dylib")
}

// Env returns the variables every wine process of this engine needs.
// CX engines bundle FreeType, GnuTLS, SDL and MoltenVK in lib/.
func Env(e config.Engine) map[string]string {
	if !e.CX {
		return nil
	}
	return map[string]string{"DYLD_FALLBACK_LIBRARY_PATH": filepath.Join(e.Dir, "lib") + ":/usr/lib"}
}

// EnvList is Env appended to base, as exec.Cmd.Env wants it.
func EnvList(base []string, e config.Engine) []string {
	out := append([]string{}, base...)
	for k, v := range Env(e) {
		out = append(out, k+"="+v)
	}
	return out
}

// ImportD3DMetal copies Apple's D3DMetal (lib/external: D3DMetal.framework,
// libd3dshared.dylib) from a GPTK install into a CX engine. Symlinks inside
// the framework are preserved so its code signature stays valid.
func ImportD3DMetal(fromDir, toDir string) error {
	src := filepath.Join(fromDir, "lib", "external")
	if _, err := os.Stat(filepath.Join(src, "libd3dshared.dylib")); err != nil {
		return fmt.Errorf("no D3DMetal in %s (expected lib/external/libd3dshared.dylib)", fromDir)
	}
	return copyTree(src, filepath.Join(toDir, "lib", "external"))
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		default:
			return copyFile(p, target, info.Mode().Perm())
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".cave-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Detect inspects the known candidates plus every folder in extraRoot.
func Detect(extraRoot string) []config.Engine {
	dirs := append([]string{}, Candidates...)
	if entries, err := os.ReadDir(extraRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(extraRoot, e.Name()))
			}
		}
	}
	var out []config.Engine
	for _, d := range dirs {
		if e, err := Inspect(NameFor(d), d); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// NameFor derives a short name: ".../Wine Stable.app/Contents/Resources/wine" -> "wine-stable".
func NameFor(dir string) string {
	base := filepath.Base(dir)
	for _, part := range strings.Split(filepath.ToSlash(dir), "/") {
		if strings.HasSuffix(part, ".app") {
			base = strings.TrimSuffix(part, ".app")
			break
		}
	}
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(base), " ", "-"))
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}
