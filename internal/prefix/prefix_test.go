package prefix

import "testing"

func TestToUnix(t *testing.T) {
	cases := map[string]string{
		`C:\Program Files (x86)\Steam\steam.exe`: "/p/drive_c/Program Files (x86)/Steam/steam.exe",
		`c:/Games/x.exe`:                         "/p/drive_c/Games/x.exe",
		`D:\Setup.exe`:                           "/p/dosdevices/d:/Setup.exe",
		"/Users/hugo/Downloads/setup.exe":        "/Users/hugo/Downloads/setup.exe",
		"relative/game.exe":                      "relative/game.exe",
	}
	for in, want := range cases {
		if got := ToUnix("/p", in); got != want {
			t.Errorf("ToUnix(%q) = %q, want %q", in, got, want)
		}
	}
}
