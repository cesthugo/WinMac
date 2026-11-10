package steam

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

//go:generate env GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui -s -w" -o assets/cefwrapper.exe ../../tools/cefwrapper

//go:embed assets
var assets embed.FS

// wrapperMarker must match tools/cefwrapper.Marker (prefix only, so any
// wrapper version is recognised).
const wrapperMarker = "CAVE-CEFWRAPPER-MARKER/"

// NoVerifyFiles stops Steam from swapping Valve's steamwebhelper.exe back in
// at start-up.
const NoVerifyFiles = "-noverifyfiles"

// Wrapper returns the embedded cefwrapper.exe.
func Wrapper() ([]byte, error) {
	b, err := assets.ReadFile("assets/cefwrapper.exe")
	if err != nil {
		return nil, errors.New("this cave binary was built without the Steam web helper fix; rebuild it after `go generate ./internal/steam`")
	}
	return b, nil
}

// InstallWebHelperWrapper puts the wrapper in place of every
// steamwebhelper.exe of the Steam install in prefixDir (one per CEF build
// under Steam/bin/cef). Valve's binary is kept as steamwebhelper_real.exe.
// It is safe to call before every launch: an installed wrapper is refreshed,
// and a binary Steam updated in the meantime becomes the new _real.exe.
// It returns how many helpers are wrapped; 0 means Steam has not downloaded
// its interface yet.
func InstallWebHelperWrapper(prefixDir string, wrapper []byte) (int, error) {
	pattern := filepath.Join(prefixDir, "drive_c", "Program Files (x86)", "Steam", "bin", "cef", "*", "steamwebhelper.exe")
	helpers, err := filepath.Glob(pattern)
	if err != nil {
		return 0, err
	}
	for _, helper := range helpers {
		current, err := os.ReadFile(helper)
		if err != nil {
			return 0, err
		}
		real := filepath.Join(filepath.Dir(helper), "steamwebhelper_real.exe")
		if !bytes.Contains(current, []byte(wrapperMarker)) {
			// Valve's binary (first install or after a Steam update).
			if err := os.Rename(helper, real); err != nil {
				return 0, fmt.Errorf("keeping Valve's helper: %w", err)
			}
		} else if _, err := os.Stat(real); err != nil {
			return 0, fmt.Errorf("%s is the wrapper but %s is missing; reinstall Steam", helper, real)
		}
		if !bytes.Equal(current, wrapper) {
			tmp := helper + ".cave-tmp"
			if err := os.WriteFile(tmp, wrapper, 0o755); err != nil {
				return 0, err
			}
			if err := os.Rename(tmp, helper); err != nil {
				return 0, err
			}
		}
	}
	return len(helpers), nil
}
