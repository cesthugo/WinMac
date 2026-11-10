// Package steam handles the Windows Steam client inside a prefix.
// The macOS Steam client refuses Windows-only depots, so the Windows
// client has to run under Wine.
package steam

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
)

const (
	DefaultInstallerURL = "https://cdn.akamai.steamstatic.com/client/installer/SteamSetup.exe"
	ExeWinPath          = `C:\Program Files (x86)\Steam\steam.exe`

	// RestartExitCode is what steam.exe returns when it hands over to a newly
	// updated copy of itself. The new copy keeps running: this is not a failure.
	RestartExitCode = 42
)

// Download fetches the installer to dest unless it is already there.
func Download(ctx context.Context, url, dest string, force bool) error {
	if !force {
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// LaunchArgs returns steam.exe arguments that start a game by app ID.
func LaunchArgs(appID string) ([]string, error) {
	if _, err := strconv.ParseUint(appID, 10, 32); err != nil {
		return nil, fmt.Errorf("invalid Steam app ID %q", appID)
	}
	return []string{"-applaunch", appID}, nil
}
