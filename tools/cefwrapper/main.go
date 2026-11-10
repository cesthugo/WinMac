//go:build windows

// Command cefwrapper takes the place of a Chromium (CEF) helper such as
// Steam's steamwebhelper.exe. Under Wine on Apple silicon the helper's GPU
// process crashes or paints into a window macOS cannot show, so Steam stays
// black. This wrapper starts the vendor's binary, renamed <name>_real.exe,
// with switches that keep rendering on the CPU and in one process.
//
// The approach comes from conversio-wine's tools/steamwebhelper (MIT,
// (c) 2026 Theoron), itself based on notpop/steam-on-m1-wine and
// ByMedion/macos-wine-steam. See engine/NOTICE.
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Switches by helper name (case-insensitive, without .exe).
var helpers = map[string]string{
	// Steam: everything in one process; the separate network process trips over Wine's TLS.
	"steamwebhelper": "--disable-gpu --single-process",
	// Rockstar's Social Club needs a separate renderer, so only the GPU work moves to the CPU.
	"socialclubhelper": "--in-process-gpu --disable-gpu --disable-gpu-compositing --disable-software-rasterizer",
}

const defaultFlags = "--disable-gpu --disable-gpu-compositing"

// Marker lets cave tell this wrapper apart from Valve's binary, whatever
// version of the wrapper is installed. Keep the prefix stable.
const Marker = "CAVE-CEFWRAPPER-MARKER/v1"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--cave-wrapper-version" {
		os.Stdout.WriteString(Marker + "\n")
		os.Exit(0)
	}
	os.Exit(run())
}

func run() int {
	self, err := os.Executable()
	if err != nil {
		return 1
	}
	dir, file := filepath.Split(self)
	base := strings.TrimSuffix(file, filepath.Ext(file))
	flags, ok := helpers[strings.ToLower(base)]
	if !ok {
		flags = defaultFlags
	}
	real := filepath.Join(dir, base+"_real.exe")

	cmd := exec.Command(real)
	// Pass the original arguments through untouched: re-quoting them could
	// change what Chromium receives.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: `"` + real + `" ` + flags + " " + Arguments(commandLine()),
	}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}

// commandLine returns the raw command line Windows gave this process.
func commandLine() string {
	p := syscall.GetCommandLine()
	if p == nil {
		return ""
	}
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; n++ {
		ptr = unsafe.Add(ptr, 2)
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}
