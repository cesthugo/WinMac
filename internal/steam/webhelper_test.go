package steam

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallWebHelperWrapper(t *testing.T) {
	prefix := t.TempDir()
	cef := filepath.Join(prefix, "drive_c", "Program Files (x86)", "Steam", "bin", "cef", "cef.win64")
	if err := os.MkdirAll(cef, 0o755); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(cef, "steamwebhelper.exe")
	real := filepath.Join(cef, "steamwebhelper_real.exe")
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	if n, err := InstallWebHelperWrapper(t.TempDir(), []byte("x")); n != 0 || err != nil {
		t.Fatalf("no Steam yet: n=%d err=%v", n, err)
	}

	write(helper, "valve-v1")
	wrapperV1 := []byte("wrapper " + wrapperMarker + "v1")
	if n, err := InstallWebHelperWrapper(prefix, wrapperV1); n != 1 || err != nil {
		t.Fatalf("first install: n=%d err=%v", n, err)
	}
	if read(real) != "valve-v1" || read(helper) != string(wrapperV1) {
		t.Fatalf("after install: real=%q helper=%q", read(real), read(helper))
	}

	// Running again changes nothing.
	if _, err := InstallWebHelperWrapper(prefix, wrapperV1); err != nil || read(real) != "valve-v1" {
		t.Fatalf("second run: real=%q err=%v", read(real), err)
	}

	// A newer wrapper replaces the old one without touching Valve's binary.
	wrapperV2 := []byte("wrapper " + wrapperMarker + "v2")
	if _, err := InstallWebHelperWrapper(prefix, wrapperV2); err != nil || read(real) != "valve-v1" || read(helper) != string(wrapperV2) {
		t.Fatalf("wrapper upgrade: real=%q helper=%q err=%v", read(real), read(helper), err)
	}

	// Steam updated itself: its new binary becomes the real one.
	write(helper, "valve-v2")
	if _, err := InstallWebHelperWrapper(prefix, wrapperV2); err != nil || read(real) != "valve-v2" || read(helper) != string(wrapperV2) {
		t.Fatalf("after Steam update: real=%q helper=%q err=%v", read(real), read(helper), err)
	}
}
