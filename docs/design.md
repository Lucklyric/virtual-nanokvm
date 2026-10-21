# virtual-nanokvm design

A software version of the [Sipeed NanoKVM-Go](https://sipeed.com/nanokvm-go). It runs on the Mac it controls, with no cloud relay. With the driver input, macOS sees its input as a physical USB keyboard and mouse.

## Goals

- Input can arrive as a real USB HID device (driver input), so apps and the OS treat it like hardware. Synthetic system events (sys input) are the no-install alternative.
- Mouse motion looks human: curved paths, natural speed, overshoot and correction. It never jumps.
- Agents control the machine over MCP. People watch and control it over a LAN stream (v2).
- Everything is local. No cloud relay.

## Architecture

```
 MCP client (agent)          browser viewer (v2)
        │ stdio                     │ LAN, token auth
        ▼                           ▼
 ┌──────────────────── vnkvm (user, Go) ────────────────────┐
 │ mcpserver: tools, screenshot-pixel ⇄ display-point maps   │
 │ human:     trajectories, click/scroll/typing rhythm       │
 │ screen:    cursor position (CoreGraphics), screencapture  │
 │ hid:       report client                                  │
 └──────────────────────────┬───────────────────────────────┘
                            │ unix socket, mode 0600, owned by the user
                            ▼
              vnkvm-hid-bridge (root, C++)
                            │ Karabiner client library (protocol v7)
                            ▼
        Karabiner-VirtualHIDDevice-Daemon (root)
                            ▼
     DriverKit virtual keyboard + pointing device → macOS HID stack
```

### Input paths

Only input differs. Capture, MCP and the human-like engine are shared.

| `--input` | How input reaches macOS | Needs |
|---|---|---|
| `driver` (default) | Virtual USB HID device. macOS treats it as hardware; the mouse is relative and accelerated. | Karabiner driver, root bridge |
| `sys` | Synthetic system events (`CGEventPost`), the way remote-desktop tools do it. Apps that check the event source can tell. | Accessibility permission only |

The `sys` input takes the same relative reports and turns them into absolute moves and drags from the current cursor position. It compares each keyboard report with the last one to find which keys went down or up, and maps HID usages to macOS virtual key codes. It sets the click count itself, because macOS does not infer double clicks from the timing of synthetic events.

### Why two processes (driver input)

The Karabiner daemon accepts commands only from root. Screen capture needs the Screen Recording permission of a user session, and MCP clients launch servers as the user. So a small root bridge holds the HID connection and nothing else. All policy lives in the user-side service.

The bridge keeps no state about intent. It forwards one report per line (`K`, `P`, `R`, `S`), accepts one client at a time, and releases every key and button when that client disconnects. If the service crashes, nothing stays held down.

### Virtual HID: Karabiner-DriverKit-VirtualHIDDevice

- It is already signed by its author, so we need no DriverKit entitlement from Apple.
- The pointing device is **relative only**: each report carries signed 8-bit `x`, `y` and wheel deltas. There is no absolute positioning.
- macOS applies pointer acceleration to these reports, so N report units do not equal N points of movement.
- Client headers are pinned with `KARABINER_TAG` in the `Makefile` (v8.6.0, client protocol 7). They must match the installed driver package. The daemon rejects other protocol versions.

## Human-like mouse (`internal/human`)

A `MoveTo(target, size)` call:

1. **Duration** comes from Fitts's law, `T = 0.08 + 0.13·log2(D/W + 1)` seconds, with ±15% variation. A 1400-point move to a 20-point target takes about 0.8 s.
2. **Path** is a cubic Bézier curve bent to one side by 4–20% of the distance, like a wrist or elbow arc.
3. **Speed** follows the minimum-jerk profile `s(τ) = 10τ³ − 15τ⁴ + 6τ⁵`. It starts slow, peaks mid-way, and ends slow.
4. **Tremor** adds a small smoothed random jitter that fades out near the target.
5. **Overshoot**: on 70% of moves longer than 150 points, the first stroke aims 2–6% past the target, pauses 30–90 ms, then makes a short corrective stroke.
6. **Closed loop**: reports go out every 8 ms (a 125 Hz mouse). Each step combines feed-forward along the path with a gentle pull toward where the cursor should be. Because of pointer acceleration, the engine keeps a running estimate of points moved per report unit. A final settle phase makes half-error corrections until the cursor is within 1 point. If it is pinned at a screen edge, it reports an error.

Clicks hold the button for 55–110 ms. A double click waits 70–130 ms between clicks. Drags press, pause, move, pause, and release. The button is released even when the move fails. Scrolling sends one notch at a time in short bursts with occasional longer breaks.

Every move is random, never a replayed trajectory. The engine is seeded randomly per process, and each move draws its own duration, bend, control points, overshoot and tremor. A test checks that 20 moves between the same two points differ.

Typing presses shift before a shifted key. Each key is held 40–95 ms. Gaps between keys are log-normal around the target words-per-minute, with extra pauses after spaces and punctuation. Text is validated before any key is pressed. Key combinations press modifiers one by one and release them in reverse order.

**Take-over.** The engine shares the one cursor with the person at the Mac. If the cursor strays from the planned path in a way the path does not explain, or moves while the engine holds it still (typing, clicking, scrolling), the engine stops, releases every key and button, and returns `user took control`. A cursor pinned at a screen edge does not count. Keyboard input from the person is not detected.

`human_test.go` checks these properties against a simulated screen with acceleration (linear to aggressive), a one-tick position lag, and edge clamping. It checks that moves land within 1 point, that speed is bell-shaped, that paths curve, that moves differ, that moves to off-screen targets fail, and that a hand on the mouse stops moves, drags and typing.

## MCP tools (`vnkvm mcp`)

| Tool | Does |
|---|---|
| `list_windows` | Lists on-screen app windows, frontmost first, with ids and global bounds. |
| `screenshot` | Captures the main display, or one app window (`window_id` or `app`) even when other windows cover it. Returns a JPEG at most 1568 px wide, plus its origin, scale and the cursor position in both coordinate spaces. |
| `cursor_position` | Returns the cursor position, global and relative to the main display or a window. |
| `mouse_move` | Moves along a human-like path to `x, y`. `size` is the target size. |
| `click` | Optionally moves first, then clicks: `button` left/right/middle, `count` 1 or 2. |
| `drag` | Holds the left button from the current position to `x, y`. |
| `scroll` | Scrolls `dy` and `dx` wheel notches. Positive values scroll down and right. |
| `type_text` | Types `text` at `wpm`, US keyboard characters only. |
| `key` | Presses a key or combination, such as `enter` or `cmd+shift+4`. |

### Coordinates

- **Global:** display points, shared by every screen and window. The mouse moves in this space.
- **Relative:** pixels of a frame's screenshot. A frame is the main display (the default) or one window (`window_id` or `app`).
- **Conversion:** `global = origin + pixel × scale`. Every screenshot returns its `origin` and `scale`. Locations come back with both `global` and `relative` (relative is left out when the point is outside the frame).
- **Mouse tools take relative coordinates** for the frame they name, so an agent can act straight from a window screenshot. A window's bounds are read again at action time, so a window that has moved still works. A point that another window covers is refused, since the click would land on the other window.

## Setup (manual, once per machine)

1. Install the driver package `Karabiner-DriverKit-VirtualHIDDevice-8.6.0.pkg` from the upstream repo's `dist/` folder.
2. Run `/Applications/.Karabiner-VirtualHIDDevice-Manager.app/Contents/MacOS/Karabiner-VirtualHIDDevice-Manager activate` and approve the system extension in System Settings.
3. Run the daemon as root:
   `sudo '/Library/Application Support/org.pqrs/Karabiner-DriverKit-VirtualHIDDevice/Applications/Karabiner-VirtualHIDDevice-Daemon.app/Contents/MacOS/Karabiner-VirtualHIDDevice-Daemon'`
4. Run `make` to build, then start the bridge: `sudo bin/vnkvm-hid-bridge --socket /var/run/vnkvm-hid.sock --owner $(id -u)`.
5. Register the MCP server with your client, for example `claude mcp add vnkvm -- <repo>/bin/vnkvm mcp --input driver`.
6. On the first screenshot, grant Screen Recording to the app that launched the server (the terminal or Claude app).
7. On first connect, macOS may open the Keyboard Setup Assistant for the new keyboard. Choose ANSI.

### To verify on hardware

- **Scroll direction:** macOS "Natural scrolling" also applies to mice, so `dy > 0` may scroll up on a machine where it is on.
- **Settle timing:** the 2-tick wait and the 0.3 feedback gain are tuned on the simulator, not real WindowServer latency. Retune them after the first test on the real cursor.

`vnkvm mcp --dry-run` records reports without a driver, which is useful for protocol testing. Moves report "cursor stopped" because the real cursor does not move.

## Roadmap

- **v1 (this)**: both input paths, human-like engine with take-over detection, MCP over stdio, screen and window screenshots.
- **v2: streaming and remote control**. ScreenCaptureKit with VideoToolbox H.264, sent over WebRTC (MJPEG as a first step) to a LAN web viewer. Viewers get view-only or control roles behind token auth, and control goes through the same `human` engine.
- **v3**: launchd units for the bridge and daemon, full-display capture of secondary screens (windows on any screen already work), and Linux (USB gadget on `dummy_hcd` or USB/IP) and Windows targets.
