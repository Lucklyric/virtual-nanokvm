// Package doctor checks what vnkvm needs on this Mac and says how to fix it.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Lucklyric/virtual-nanokvm/internal/hid"
	"github.com/Lucklyric/virtual-nanokvm/internal/screen"
	"github.com/Lucklyric/virtual-nanokvm/internal/tailnet"
)

// Check is one line of the report.
type Check struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Info string `json:"info"`
	Fix  string `json:"fix,omitempty"`
	// Optional checks only matter for some features (driver input, tailnet).
	Optional bool `json:"optional,omitempty"`
}

// Run checks everything. With fix, it asks macOS for the missing permissions,
// which opens the system dialogs.
func Run(ctx context.Context, bridgeSocket string, fix bool) []Check {
	var out []Check
	add := func(c Check) { out = append(out, c) }

	ver, _ := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output()
	v := strings.TrimSpace(string(ver))
	add(Check{Name: "macOS", OK: !strings.HasPrefix(v, "13.") && v != "", Info: v,
		Fix: "macOS 14 or later is needed for screen capture."})

	ax := hid.AccessibilityTrusted(fix)
	add(Check{Name: "Accessibility (sys input)", OK: ax, Info: onOff(ax),
		Fix: "System Settings > Privacy & Security > Accessibility: allow the app that runs vnkvm (your terminal, Claude, or Codex)."})

	sr := screen.ScreenAccess(fix)
	add(Check{Name: "Screen Recording", OK: sr, Info: onOff(sr),
		Fix: "System Settings > Privacy & Security > Screen & System Audio Recording: allow the app that runs vnkvm, then restart that app."})

	if sr {
		ready := screen.DisplayReady()
		add(Check{Name: "Display", OK: ready, Info: map[bool]string{true: "capturable", false: "not capturable now"}[ready],
			Fix: "Unlock the Mac and wake the display; macOS hides the screen from capture while locked."})
	}

	if _, err := os.Stat(bridgeSocket); err == nil {
		b, err := hid.DialBridge(bridgeSocket)
		if err == nil {
			b.Close()
		}
		add(Check{Name: "Driver input", OK: err == nil, Optional: true, Info: errInfo(err, "bridge ready"),
			Fix: "Start the Karabiner daemon, then: sudo bin/vnkvm-hid-bridge --socket " + bridgeSocket + " --owner $(id -u)"})
	} else {
		add(Check{Name: "Driver input", OK: false, Optional: true, Info: "bridge not running (only needed for --input driver)",
			Fix: "See the driver setup in the README."})
	}

	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, st, err := tailnet.Read(tctx)
	switch {
	case err != nil:
		add(Check{Name: "Tailscale", OK: false, Optional: true, Info: err.Error(), Fix: "Only needed for serve --tailscale."})
	case len(st.CertDomains) == 0:
		add(Check{Name: "Tailscale", OK: false, Optional: true, Info: "connected, HTTPS certificates off (viewers get MJPEG)",
			Fix: "Tailscale admin console > DNS > enable HTTPS Certificates, for H.264 in the browser."})
	default:
		add(Check{Name: "Tailscale", OK: true, Optional: true, Info: "connected, HTTPS on"})
	}
	return out
}

// Format renders the report for a terminal or an agent.
func Format(checks []Check) string {
	var b strings.Builder
	for _, c := range checks {
		mark := "ok  "
		switch {
		case !c.OK && c.Optional:
			mark = "--  "
		case !c.OK:
			mark = "FIX "
		}
		fmt.Fprintf(&b, "%s%-26s %s\n", mark, c.Name, c.Info)
		if !c.OK && c.Fix != "" {
			fmt.Fprintf(&b, "    %s\n", c.Fix)
		}
	}
	return b.String()
}

func onOff(ok bool) string {
	if ok {
		return "allowed"
	}
	return "not allowed"
}

func errInfo(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}
