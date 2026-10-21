// Package tailnet shares the viewer on a Tailscale network.
package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// Exposure is how serve is reachable on the tailnet.
type Exposure struct {
	Listen string // address the HTTP server binds
	URL    string // base URL to share
	Close  func()
}

// CLI finds the tailscale command: on PATH, or inside the Mac app.
func CLI() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	const app = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	if _, err := exec.LookPath(app); err == nil {
		return app, nil
	}
	return "", errors.New("tailscale not found: install it from https://tailscale.com/download and log in")
}

// Status is the part of `tailscale status --json` vnkvm reads.
type Status struct {
	BackendState string
	CertDomains  []string
	Self         struct {
		DNSName      string
		TailscaleIPs []string
	}
}

// Expose shares serve on the tailnet. With HTTPS certificates
// enabled for the tailnet, the server binds localhost and `tailscale serve`
// proxies HTTPS to it; HTTPS makes the page a secure context, so browsers can
// decode H.264. Without certificates, it binds the tailnet address over HTTP
// (still encrypted by WireGuard) and viewers fall back to MJPEG.
// Read returns the tailnet status of this Mac.
func Read(ctx context.Context) (string, Status, error) {
	cli, err := CLI()
	if err != nil {
		return "", Status{}, err
	}
	out, err := exec.CommandContext(ctx, cli, "status", "--json").Output()
	if err != nil {
		return cli, Status{}, fmt.Errorf("tailscale status: %w (is Tailscale running and logged in?)", err)
	}
	var st Status
	if err := json.Unmarshal(out, &st); err != nil {
		return cli, Status{}, fmt.Errorf("tailscale status: %w", err)
	}
	if st.BackendState != "Running" {
		return cli, st, fmt.Errorf("tailscale is %s: connect it first", st.BackendState)
	}
	return cli, st, nil
}

func Expose(ctx context.Context, listen string, httpsPort int) (*Exposure, error) {
	cli, st, err := Read(ctx)
	if err != nil {
		return nil, err
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")

	if len(st.CertDomains) > 0 {
		local := net.JoinHostPort("127.0.0.1", port)
		https := fmt.Sprint(httpsPort)
		if b, err := exec.CommandContext(ctx, cli, "serve", "--bg", "--https="+https, "http://"+local).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("tailscale serve: %v: %s", err, b)
		}
		return &Exposure{
			Listen: local,
			URL:    "https://" + st.CertDomains[0] + ":" + https,
			Close: func() {
				exec.Command(cli, "serve", "--https="+https, "off").Run()
			},
		}, nil
	}

	var ip string
	for _, a := range st.Self.TailscaleIPs {
		if net.ParseIP(a).To4() != nil {
			ip = a
		}
	}
	if ip == "" {
		return nil, errors.New("tailscale has no IPv4 address for this Mac")
	}
	host := ip
	if name != "" {
		host = name
	}
	fmt.Println("Tailscale HTTPS certificates are off, so this runs over HTTP (WireGuard still encrypts it).")
	fmt.Println("For H.264 in the browser, enable HTTPS Certificates on the DNS page of the Tailscale admin console.")
	return &Exposure{
		Listen: net.JoinHostPort(ip, port),
		URL:    "http://" + net.JoinHostPort(host, port),
		Close:  func() {},
	}, nil
}
