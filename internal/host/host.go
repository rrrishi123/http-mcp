// Package host reports the resources of the machine the wire runs on — the
// "gets host resources" PMF basic. Read-only, cheap, stdlib only. loadavg is read
// from /proc where present (Linux) and simply omitted elsewhere: probe, don't
// assume — platform-agnostic by degrading, never by a per-OS hardcode.
package host

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Snapshot is a cheap read of the host's resources at a moment.
type Snapshot struct {
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	NumCPU     int       `json:"num_cpu"`
	Goroutines int       `json:"goroutines"`
	AllocMB    uint64    `json:"alloc_mb"`
	SysMB      uint64    `json:"sys_mb"`
	Loadavg    []float64 `json:"loadavg,omitempty"`
}

// Read samples the host now.
func Read() Snapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return Snapshot{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		NumCPU:     runtime.NumCPU(),
		Goroutines: runtime.NumGoroutine(),
		AllocMB:    m.Alloc / (1 << 20),
		SysMB:      m.Sys / (1 << 20),
		Loadavg:    loadavg(),
	}
}

func loadavg() []float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil // no /proc (macOS/Windows) — omit rather than assume
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return nil
	}
	out := make([]float64, 0, 3)
	for _, x := range f[:3] {
		v, _ := strconv.ParseFloat(x, 64)
		out = append(out, v)
	}
	return out
}
