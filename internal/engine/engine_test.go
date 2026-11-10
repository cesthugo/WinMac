package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, mode); err != nil {
		t.Fatal(err)
	}
}

func TestInspectGPTKLayout(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "bin", "wine"), 0o755)
	touch(t, filepath.Join(dir, "bin", "wine64"), 0o755)
	if err := os.MkdirAll(filepath.Join(dir, "lib", "external", "D3DMetal.framework"), 0o755); err != nil {
		t.Fatal(err)
	}
	e, err := Inspect("gptk", dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(e.Wine) != "wine64" {
		t.Errorf("wine64 should be preferred, got %s", e.Wine)
	}
	if !e.D3DMetal {
		t.Error("D3DMetal not detected")
	}
}

func TestInspectPlainWine(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "bin", "wine"), 0o755)
	e, err := Inspect("wine", dir)
	if err != nil {
		t.Fatal(err)
	}
	if e.D3DMetal {
		t.Error("plain wine reported as having D3DMetal")
	}
}

func TestInspectRejectsNonExecutable(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "bin", "wine64"), 0o644)
	if _, err := Inspect("x", dir); err == nil {
		t.Error("expected an error for a non-executable wine")
	}
}

func TestNameFor(t *testing.T) {
	cases := map[string]string{
		"/Applications/Wine Stable.app/Contents/Resources/wine":          "wine-stable",
		"/Applications/Game Porting Toolkit.app/Contents/Resources/wine": "game-porting-toolkit",
		"/Users/h/Library/Application Support/Cave/engines/GPTK-3.0":     "gptk-3.0",
	}
	for in, want := range cases {
		if got := NameFor(in); got != want {
			t.Errorf("NameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImportD3DMetalIntoCX(t *testing.T) {
	gptk, cx := t.TempDir(), t.TempDir()
	touch(t, filepath.Join(gptk, "bin", "wine64"), 0o755)
	ext := filepath.Join(gptk, "lib", "external")
	touch(t, filepath.Join(ext, "libd3dshared.dylib"), 0o755)
	fw := filepath.Join(ext, "D3DMetal.framework")
	touch(t, filepath.Join(fw, "Versions", "A", "D3DMetal"), 0o755)
	if err := os.Symlink("A", filepath.Join(fw, "Versions", "Current")); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(cx, "bin", "wine"), 0o755)
	touch(t, filepath.Join(cx, "CAVE-ENGINE.txt"), 0o644)

	before, err := Inspect("cx", cx)
	if err != nil || !before.CX || before.D3DMetal {
		t.Fatalf("before import: %+v %v", before, err)
	}
	if err := ImportD3DMetal(gptk, cx); err != nil {
		t.Fatal(err)
	}
	after, _ := Inspect("cx", cx)
	if !after.D3DMetal {
		t.Error("D3DMetal not detected after import")
	}
	link, err := os.Readlink(filepath.Join(cx, "lib", "external", "D3DMetal.framework", "Versions", "Current"))
	if err != nil || link != "A" {
		t.Errorf("framework symlink not preserved: %q %v", link, err)
	}
	if st, _ := os.Stat(LibD3DShared(after)); st == nil || st.Mode()&0o111 == 0 {
		t.Error("libd3dshared.dylib missing or not executable")
	}
	if err := ImportD3DMetal(t.TempDir(), cx); err == nil {
		t.Error("import from a folder without D3DMetal should fail")
	}
}
