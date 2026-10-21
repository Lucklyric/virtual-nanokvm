// Command vnkvm is the virtual-nanokvm service.
//
//	vnkvm mcp   [flags]          MCP server on stdio
//	vnkvm move  [flags] X Y      move the cursor to a display point
//	vnkvm click [flags] X Y      move there and left-click
//	vnkvm type  [flags] TEXT     type text
//	vnkvm serve [flags]          browser viewer: live stream and remote control
//
// Flags:
//
//	--input driver|sys    input path (default driver)
//	--bridge-socket PATH  vnkvm-hid-bridge socket (driver input)
//	--dry-run             record reports instead of sending them
//
// serve flags:
//
//	--listen ADDR         address to listen on (default :8090, all interfaces)
//	--token T             control token (random when empty)
//	--view-token T        view-only token (random when empty)
//	--fps N               stream frames per second (default 10)
//	--width PX            stream width in pixels (default 1280)
//
// Only input differs between the two paths: driver is a virtual USB HID
// device, sys posts synthetic system events the way remote-desktop tools do.
// Both run the same human-like motion and typing engine.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Lucklyric/virtual-nanokvm/internal/hid"
	"github.com/Lucklyric/virtual-nanokvm/internal/human"
	"github.com/Lucklyric/virtual-nanokvm/internal/mcpserver"
	"github.com/Lucklyric/virtual-nanokvm/internal/screen"
	"github.com/Lucklyric/virtual-nanokvm/internal/web"
)

const defaultSocket = "/var/run/vnkvm-hid.sock"

type mainScreen struct{}

func (mainScreen) Screenshot(ctx context.Context, width int) ([]byte, error) {
	return screen.Screenshot(ctx, width)
}
func (mainScreen) Bounds() (float64, float64) { return screen.MainBounds() }
func (mainScreen) Windows() []screen.Window   { return screen.Windows() }
func (mainScreen) FindWindow(id uint32, app string) (screen.Window, error) {
	return screen.FindWindow(id, app)
}
func (mainScreen) WindowAt(x, y float64) (screen.Window, bool) { return screen.WindowAt(x, y) }
func (mainScreen) WindowShot(ctx context.Context, id uint32, width int) ([]byte, error) {
	return screen.WindowShot(ctx, id, width)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: vnkvm mcp|move X Y|click X Y|type TEXT|serve [--input driver|sys] [--bridge-socket PATH] [--dry-run]")
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	input := fs.String("input", "driver", "input path: driver (virtual USB HID) or sys (synthetic system events)")
	socket := fs.String("bridge-socket", defaultSocket, "unix socket of vnkvm-hid-bridge")
	dryRun := fs.Bool("dry-run", false, "record HID reports instead of sending them")
	listen := fs.String("listen", ":8090", "serve: address to listen on")
	token := fs.String("token", "", "serve: control token (random when empty)")
	viewToken := fs.String("view-token", "", "serve: view-only token (random when empty)")
	fps := fs.Int("fps", 10, "serve: stream frames per second")
	width := fs.Int("width", 1280, "serve: stream width in pixels")
	fs.Parse(os.Args[2:])
	args := fs.Args()

	// stdout carries the MCP protocol; logs go to stderr.
	log.SetOutput(os.Stderr)

	backend, err := openBackend(*input, *socket, *dryRun)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		backend.ReleaseAll()
		backend.Close()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine := human.New(backend, screen.Cursor{})

	switch cmd {
	case "mcp":
		server := mcpserver.New(engine, mainScreen{})
		if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
			log.Print(err)
		}
	case "move", "click":
		if len(args) != 2 {
			usage()
		}
		x, errX := strconv.ParseFloat(args[0], 64)
		y, errY := strconv.ParseFloat(args[1], 64)
		if errX != nil || errY != nil {
			usage()
		}
		p, err := engine.MoveTo(ctx, human.Point{X: x, Y: y}, 0)
		if err != nil {
			log.Fatal(err)
		}
		if cmd == "click" {
			if err := engine.Click(ctx, human.Left, 1); err != nil {
				log.Fatal(err)
			}
		}
		fmt.Printf("cursor at (%.0f, %.0f)\n", p.X, p.Y)
	case "serve":
		cfg := web.Config{ControlToken: orRandom(*token), ViewToken: orRandom(*viewToken), FPS: *fps, Width: *width}
		srv := web.New(cfg, engine, streamSource{})
		go srv.Run(ctx)
		hs := &http.Server{Addr: *listen, Handler: srv.Handler()}
		go func() {
			<-ctx.Done()
			hs.Close()
		}()
		host := lanAddr(*listen)
		fmt.Printf("vnkvm viewer (keep these links private):\n  control: http://%s/#token=%s\n  view:    http://%s/#token=%s\n", host, cfg.ControlToken, host, cfg.ViewToken)
		if err := hs.ListenAndServe(); err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
	case "type":
		if len(args) != 1 {
			usage()
		}
		if err := engine.Type(ctx, args[0], 0); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
	}
}

type streamSource struct{}

func (streamSource) Frame(width int, quality float64) ([]byte, error) {
	return screen.Frame(width, quality)
}
func (streamSource) Bounds() (float64, float64) { return screen.MainBounds() }

func orRandom(t string) string {
	if t != "" {
		return t
	}
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// lanAddr turns a listen address into host:port a phone or laptop on the same
// network can open.
func lanAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || (host != "" && host != "0.0.0.0" && host != "::") {
		return listen
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ip, ok := a.(*net.IPNet); ok && ip.IP.To4() != nil && !ip.IP.IsLoopback() && ip.IP.IsPrivate() {
			return net.JoinHostPort(ip.IP.String(), port)
		}
	}
	return net.JoinHostPort("localhost", port)
}

func openBackend(name, socket string, dryRun bool) (hid.Backend, error) {
	if dryRun {
		log.Print("dry run: HID reports are recorded, not sent")
		return &hid.Recorder{}, nil
	}
	switch name {
	case "driver":
		return hid.DialBridge(socket)
	case "sys":
		return hid.NewCGEvent()
	}
	return nil, fmt.Errorf("unknown input %q (want driver or sys)", name)
}
