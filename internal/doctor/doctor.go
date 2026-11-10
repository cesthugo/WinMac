// Package doctor checks that the Mac can run Windows games through Wine.
package doctor

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"cave/internal/config"
	"cave/internal/engine"
)

type Check struct {
	Name   string
	OK     bool
	Detail string
	Fix    string
}

func Run(db *config.DB) []Check {
	var cs []Check
	if runtime.GOOS != "darwin" {
		return []Check{{Name: "macOS", Detail: "running on " + runtime.GOOS, Fix: "cave targets macOS on Apple Silicon"}}
	}

	cs = append(cs, Check{Name: "macOS", OK: true, Detail: out("sw_vers", "-productVersion")})

	chip := out("sysctl", "-n", "machdep.cpu.brand_string")
	arm := out("sysctl", "-n", "hw.optional.arm64") == "1"
	cs = append(cs, Check{Name: "Apple Silicon", OK: arm, Detail: chip})

	if mem, err := strconv.ParseUint(out("sysctl", "-n", "hw.memsize"), 10, 64); err == nil {
		gb := mem >> 30
		cs = append(cs, Check{Name: "Memory", OK: gb >= 16, Detail: strconv.FormatUint(gb, 10) + " GB",
			Fix: "16 GB+ recommended for recent AAA games"})
	}

	rosetta := exec.Command("/usr/bin/arch", "-x86_64", "/usr/bin/true").Run() == nil
	cs = append(cs, Check{Name: "Rosetta 2", OK: rosetta,
		Detail: map[bool]string{true: "x86_64 binaries run", false: "not installed"}[rosetta],
		Fix:    "softwareupdate --install-rosetta"})

	if len(db.Engines) == 0 {
		cs = append(cs, Check{Name: "Engine", Detail: "none registered",
			Fix: "install Apple's Game Porting Toolkit, then `cave engine detect --register`"})
	}
	for _, name := range config.SortedKeys(db.Engines) {
		e := db.Engines[name]
		cmd := exec.Command(e.Wine, "--version")
		cmd.Env = engine.EnvList(os.Environ(), e)
		v, err := cmd.Output()
		detail := e.Dir
		if err == nil {
			detail = strings.TrimSpace(string(v)) + " — " + e.Dir
		}
		kind := "GPTK"
		if e.CX {
			kind = "CrossOver sources"
		}
		cs = append(cs, Check{Name: "Engine " + name, OK: err == nil, Detail: kind + ", " + detail,
			Fix: "re-install it or `cave engine add` again"})
		fix := "DX12 games need a GPTK-based engine"
		if e.CX {
			fix = "cave engine import-d3dmetal " + name
		}
		cs = append(cs, Check{Name: "  D3DMetal", OK: e.D3DMetal,
			Detail: map[bool]string{true: "DirectX 11/12 -> Metal available", false: "absent"}[e.D3DMetal],
			Fix:    fix})
	}
	return cs
}

func out(name string, args ...string) string {
	b, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
