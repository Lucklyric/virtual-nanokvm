// Command vnkvm is the virtual-nanokvm service.
//
//	vnkvm mcp   [flags]          MCP server on stdio
//	vnkvm move  [flags] X Y      move the cursor to a display point
//	vnkvm click [flags] X Y      move there and left-click
//	vnkvm type  [flags] TEXT     type text
//
// Flags:
//
//	--input driver|sys    input path (default driver)
//	--bridge-socket PATH  vnkvm-hid-bridge socket (driver input)
//	--dry-run             record reports instead of sending them
//
// Only input differs between the two paths: driver is a virtual USB HID
// device, sys posts synthetic system events the way remote-desktop tools do.
// Both run the same human-like motion and typing engine.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Lucklyric/virtual-nanokvm/internal/hid"
	"github.com/Lucklyric/virtual-nanokvm/internal/human"
	"github.com/Lucklyric/virtual-nanokvm/internal/mcpserver"
	"github.com/Lucklyric/virtual-nanokvm/internal/screen"
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
	fmt.Fprintln(os.Stderr, "usage: vnkvm mcp|move X Y|click X Y|type TEXT [--input driver|sys] [--bridge-socket PATH] [--dry-run]")
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
