# virtual-nanokvm design

A software version of the [Sipeed NanoKVM-Go](https://sipeed.com/nanokvm-go). It runs on the Mac it controls, with no cloud relay. With the driver input, macOS sees its input as a physical USB keyboard and mouse.

## Goals

- Input can arrive as a real USB HID device (driver input), so apps and the OS treat it like hardware. Synthetic system events (sys input) are the no-install alternative.
- Mouse motion looks human: curved paths, natural speed, overshoot and correction. It never jumps.
- Agents control the machine over MCP. People watch and control it from a browser on the local network.
- Everything runs on the Mac itself.

## Architecture

```
 MCP client (agent)          browser viewer
        │ stdio                     │ LAN: MJPEG stream + WebSocket, token auth
        ▼                           ▼
 ┌──────────────────── vnkvm (user, Go) ────────────────────┐
 │ mcpserver: tools, screenshot-pixel ⇄ display-point maps   │
 │ human:     trajectories, click/scroll/typing rhythm       │
 │ web:       viewer page, stream, control relay             │
 │ screen:    cursor, windows, screencapture, ScreenCaptureKit│
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
5. **Approach** is drawn per move. **Overshoot** (about 35% of moves over 150 points): the first stroke aims 2–6% past, pauses 30–90 ms, then corrects. **Undershoot** (about 28% of moves over 60 points): it stops 5–12% short, then a second stroke finishes. **Detour** (about 12% of moves over 400 points): it bends through a point off the line. Otherwise the move is **direct**. Every move starts after a log-normal reaction delay of about 140 ms.
6. **Closed loop**: reports go out every 8 ms (a 125 Hz mouse). Each step combines feed-forward along the path with a gentle pull toward where the cursor should be. Because of pointer acceleration, the engine keeps a running estimate of points moved per report unit. A final settle phase makes half-error corrections until the cursor is within 1 point. If it is pinned at a screen edge, it reports an error.

Before a click the hand **hovers**: a log-normal dwell of about 130 ms, sometimes an extra look of about 350 ms, and in about 25% of clicks a 1–2.5 point wobble off the spot and back. The press always lands on the spot. After a click the hand often **relaxes**, drifting 3–12 points away in about 45% of clicks. Clicks hold the button for 55–110 ms. A double click waits 70–130 ms between clicks. Drags press, pause, move, pause, and release. The button is released even when the move fails. Scrolling sends one notch at a time in short bursts with occasional longer breaks.

Every move is random, never a replayed trajectory. The engine is seeded randomly per process, and each move draws its own duration, bend, control points, overshoot and tremor. A test checks that 20 moves between the same two points differ.

Typing presses shift before a shifted key. Each key is held 40–95 ms. Gaps between keys are log-normal around the target words-per-minute, with extra pauses after spaces and punctuation. Text is validated before any key is pressed. Key combinations press modifiers one by one and release them in reverse order.

**Take-over.** The engine shares the one cursor with the person at the Mac. If the cursor strays from the planned path in a way the path does not explain, or moves while the engine holds it still (typing, clicking, scrolling), the engine stops, releases every key and button, and returns `user took control`. A cursor pinned at a screen edge does not count. Keyboard input from the person is not detected.

`human_test.go` checks these properties against a simulated screen with acceleration (linear to aggressive), a one-tick position lag, and edge clamping. It checks that moves land within 1 point, that speed is bell-shaped, that paths curve, that moves differ, that moves to off-screen targets fail, and that a hand on the mouse stops moves, drags and typing.

## MCP tools (`vnkvm mcp`)

| Tool | Does |
|---|---|
| `list_windows` | Lists on-screen app windows, frontmost first, with ids and global bounds. |
| `screenshot` | A new shot of the main display or one window (`window_id` or `app`, even when covered), 1024 px wide by default. With `shot` and `region` it zooms into an earlier shot at up to 1568 px, never upscaled. With `shot` and `mark` it draws a crosshair where that pixel lands (and zooms around it when no region is given). |
| `act` | Runs up to 20 steps in order: `move`, `click`, `double_click`, `right_click`, `drag`, `scroll`, `type`, `key`, `wait`, `wait_for` (`change` or `stable`). Validates every step before anything moves, stops at the first failure, then returns a new low shot (`observe`, default on). |
| `cursor_position` | Returns the cursor in global points and in a shot's pixels. |
| `mouse_move`, `click`, `drag`, `scroll`, `type_text`, `key` | One step each, with `observe` off by default. |
| `lessons` | Reads or records one-line lessons for an app, and returns the session's counters. |

### Background mode

`mode: "background"` on `act` and the action tools acts on the shot's app without the shared cursor or keyboard focus:

- **Click:** the pixel maps to a global point, and `AXUIElementCopyElementAtPosition` on the app's element finds the control there (other apps' windows on top do not matter). The server walks up to 5 parents to the first element that takes `AXPress`. Text fields get `AXFocused` instead. Web controls in Chromium list no press action, so buttons, links, checkboxes and similar roles are focused and then triggered with Space (Enter for links).
- **Chromium and Electron** build their tree only for assistive apps: on a miss, the server sets `AXManualAccessibility` on the app (Chrome answers "unsupported" and builds it anyway) and retries for up to 1.5 s.
- **Keys and wheel:** a second engine over `hid.AppEvents` posts keyboard and wheel events with `CGEventPostToPid`. Its cursor is a fixed point, so the person's mouse never counts as a take-over. `hold` keeps keys down for a set time.
- **Menus:** a `menu` step presses a menu bar item through Accessibility (`File > New`). Commands on the current document do nothing while the app is in the background, since it has no key window.
- **Refused:** right-click (`AXShowMenu` brings the app forward), double-click, drag. `move` is skipped.
- **Settle and change** compare small captures of the app's own window, since other windows may cover it.
- **Not used:** private SkyLight calls (`SLEventPostToPid` plus focus records), which reach more apps but can break with any macOS update.

### Privacy: one app at a time

- `screenshot` needs `app` or `window_id`. Whole-display capture is off unless the person starts the server with `--allow-display` or `VNKVM_ALLOW_DISPLAY=1`. A window capture (`screencapture -l`) contains that window only, even when others cover it.
- The look after `act` stays in the same window. If that window is gone, there is no look, rather than a fall back to the whole display.
- `list_windows` returns ids, apps and bounds for every window, but titles only for the app the agent names, since titles can hold mail subjects or document names.
- Settle and change detection compare in-process previews of the app's window; those pixels never leave the server.

### Lessons

The server counts shots, zooms, single-action calls, `act` calls, steps and failures by kind. When a loop looks slow (three looks without an action, three single actions in a row, the same failure twice), it adds one `Efficiency:` line to the tool result. The skill then records a lesson with `lessons`: one line per app, newest 40 kept, in `~/.config/vnkvm/lessons/<app>.md` (`VNKVM_LESSONS_DIR` overrides), outside the plugin and the source checkout so updates keep them. The skill reads an app's lessons before planning there, and never records personal text or window contents.

Lessons are versioned so they can be judged. Every `set` (a revision) bumps the version in the file header, and each line carries the version it was added under; `add` tags new lines with the version in force (`v3 2026-10-21: ...`). At the end of a task the agent calls `lessons` with `done`. The server then appends a record to `<app>.tasks.jsonl` (newest 50) with the counter delta since the previous task: tool calls (counted by middleware on every `tools/call`), shots, `act` calls, steps, failures, `expect` hits and misses, recipe runs. The same record goes to the `all` log with the version of the cross-app rules. A `lessons` read returns the recent tasks, the average cost per version and the `all` rules; without an app it lists every app with lessons, tasks or recipes and whether a review is due. `/vnkvm:review` runs a review across them. The server asks for a revision every fifth task on one version, and when the current version averages over 25% more tool calls per task than the previous version with at least three tasks. The agent revises; the server only supplies the numbers.

### Predictions

`expect` is a `wait_for` that stops the sequence when it times out. It watches the whole frame or a `region` of the shot (mapped to the preview the same way as the frame; in a small region an eighth of its preview pixels counts as a change). An agent can chain past a change it predicts, and a wrong prediction costs one observation instead of clicks on the wrong screen.

### Recipes

`recipes` saves a sequence the agent sent to `act`, converted from shot pixels to window points, with the window size. `act` with `recipe` converts it back to the pixels of a fresh whole-window shot and runs it through the usual validation, so covered points and background limits apply as before. A window of another size is refused, since its content has reflowed. Typed text becomes `{{textN}}` placeholders unless the agent marks it as fixed, so recipes hold no personal text. Recipes live in `<app>.recipes.json` next to the lessons (newest 30), with run and failure counts. A new whole-window screenshot lists the app's recipes, so the agent sees them when it plans.

### Shots and coordinates

The model's turns, not vnkvm, set the pace of an agent: each turn takes seconds, each action about one. So the tools aim at fewer turns: a cheap first look, zoom only where needed, several actions per turn, and the next look returned with the actions.

- **Shot:** every image gets an id. The server keeps the last 32: the frame (main display or window id), the region it covers in frame points, the image size and the scale (points per pixel).
- **Coordinates are pixels of a named shot.** The pixel's center maps to `global = frame origin now + region origin + (pixel + 0.5) × scale`. A coordinate never travels without its shot, so a low look and a zoom cannot be confused.
- **Frames move, pixels do not.** A window is looked up again at action time: a moved window is followed; a resized or closed one is refused, since its content has reflowed. A point another window covers is refused, since the click would land there.
- **Capture:** `screencapture` writes the frame at full resolution (2 pixels per point on Retina), the server crops the region and scales it down with a box filter so small text stays readable, then encodes JPEG. A zoom captures again, so it shows the screen as it is now.
- **Settle and change:** after actions, the server compares 640 px previews of the frame the shots come from: the window itself (captured by id, so it works on any display and when covered), or the main display for a display shot. Fewer than 20 changed preview pixels between frames 100 ms apart counts as still, so a blinking caret does not count; more than 40 against the frame before an action counts as a change (an eighth of a small region, at least 6). A window that is gone or cannot be captured is reported as such, never as "no change".
- **Freshness and landing:** each shot records when it was captured, and each step reports its shot's age. `max_age` on `act` refuses coordinates from older shots, for targets that move while the window stays put. In the foreground each step reports where the cursor landed when it was 0.5 pt or more off; when the corrections run out more than 2 pt short, the step fails and nothing is clicked.

## Browser viewer (`vnkvm serve`)

- **Video:** H.264 with an MJPEG fallback, as on the NanoKVM.
  - **H.264 (`/video`, WebSocket):** ScreenCaptureKit captures a frame (`captureSampleBufferWithFilter`, BGRA, cursor included) and VideoToolbox encodes it in real time: main profile, no B-frames (decode order must equal display order), low-latency rate control where available, a keyframe every 4 s, `--kbps` bitrate. The encoder output callback queues AVCC frames in C, and Go pulls them, so no Go function is exported to C. The first message is `{"t":"config"}` with the codec string (`avc1.PPCCLL` from the SPS) and the avcC record built from SPS and PPS. Then come binary frames: `[1 byte keyframe flag][8 byte timestamp in µs][AVCC data]`. Each viewer skips frames until a keyframe. A new viewer, or one that falls behind, forces a keyframe. The browser decodes with WebCodecs `VideoDecoder` onto a canvas.
  - **MJPEG (`/stream`):** ScreenCaptureKit captures a JPEG at `--width` (about 40 ms per frame at 1280 px), sent as `multipart/x-mixed-replace` at `--fps`.
  - **Choice:** WebCodecs exists only in a secure context (HTTPS or localhost). The viewer uses H.264 when it can, and falls back to MJPEG when it cannot, or when no frame arrives within 5 s. Capture runs only while someone watches, and a slow viewer gets the newest frame, never a backlog.
  - **Locked screen:** macOS offers no display to capture while the Mac is locked or the display sleeps; a hardware KVM would still see it.
- **Tailscale (`--tailscale`):** reads `tailscale status --json`, finding the CLI on PATH or inside `/Applications/Tailscale.app`. If the tailnet has HTTPS certificates, the server binds `127.0.0.1` and `tailscale serve --bg --https=8443` proxies HTTPS to it, removed again on exit (`--https=8443 off`). Viewers get an HTTPS MagicDNS link and H.264. Without certificates it binds the Mac's tailnet IPv4 over HTTP (encrypted by WireGuard), and viewers get MJPEG.
- **Roles:** two tokens. The view token can watch. The control token can also drive input. The token rides in the link's URL fragment, so it never shows up in server logs, and the page passes it to `/stream` and `/control`. Plain HTTP: use it on a trusted network, or bind `127.0.0.1`.
- **Control:** a WebSocket (`/control`) carries pointer position (as a fraction of the image), buttons, wheel notches and physical key codes (`KeyboardEvent.code`, mapped to HID usages). Only one controller at a time; a second gets `409`. When the controller leaves, or the tab loses focus, every key and button is released.
- **Relay:** a person's hand already moves like a person, so the engine passes it through instead of generating a path (`RelayMove`, `RelayButtons`, `RelayWheel`, `RelayKeys`). Moves feed forward from the last relayed point with a gentle pull toward the real cursor. The engine learns the acceleration gain as it goes, and closes the gap in half steps once the hand holds still. After the hand stops, the viewer repeats the final spot a few times so the cursor settles on it.
- **Shared engine:** relayed input shares the engine's lock and button state with agent actions, so reports never interleave. An agent action running in another process sees the remote hand as a person taking over, and stops.

## Install and setup

- **`install.sh`** (`curl … | bash`): checks macOS and the Xcode command line tools, installs Go with Homebrew if missing, clones or updates `~/.vnkvm`, builds, links `vnkvm` into `~/.local/bin`, then runs `vnkvm doctor --fix`. `--claude` and `--codex` also add the agent plugins.
- **`vnkvm doctor`** (also an MCP tool, and `/vnkvm:setup` in Claude Code): checks the macOS version, Accessibility (`AXIsProcessTrustedWithOptions`), Screen Recording (`CGPreflightScreenCaptureAccess`), whether a display can be captured now, the driver bridge, and Tailscale (connected, HTTPS certificates). `--fix` raises the macOS permission dialogs. Lines read `ok`, `FIX` (blocks vnkvm) or `--` (optional feature).

## Plugin packaging

The repo is a plugin marketplace for both Claude Code and Codex. Both install straight from git.

| Harness | Manifests | MCP config |
|---|---|---|
| Claude Code | `.claude-plugin/plugin.json`, `.claude-plugin/marketplace.json` | `.mcp.json` (`${CLAUDE_PLUGIN_ROOT}/scripts/vnkvm-mcp`) |
| Codex | `.codex-plugin/plugin.json`, `.agents/plugins/marketplace.json` | `.codex-mcp.json` (`cwd` = plugin root) |

Both share `skills/vnkvm/SKILL.md` and `scripts/vnkvm-mcp`. The launcher builds `bin/vnkvm` on first start and rebuilds when the source is newer, then runs `vnkvm mcp --input ${VNKVM_INPUT:-sys}`.

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

- **v1**: both input paths, human-like engine with take-over detection, MCP over stdio, screen and window screenshots.
- **v2**: browser viewer with an MJPEG stream and remote control, token roles, one controller at a time.
- **v2.1**: H.264 (VideoToolbox → WebCodecs) with MJPEG fallback, Tailscale sharing with HTTPS via `tailscale serve`, one-line installer, `doctor`.
- **v2.2**: shot ids, low-detail first looks with zoom and aim marks, `act` sequences with settle and change detection, and a skill that teaches the agent to plan sequences.
- **v2.3**: app-only screenshots by default, titles hidden for other apps, per-app lessons with efficiency hints.
- **v2.4**: background mode through Accessibility and per-app events, `hold` and `menu` steps, foreground speed and game guidance in the skill.
- **v2.5**: `expect` steps to chain past predicted changes, saved recipes, versioned lessons measured per task with server-prompted reviews.
- **v2.6 (this)**: waits watch the window itself on any display, shot age and `max_age`, cursor landing reports and refusal when short, lessons versions per revision, `/vnkvm:review`, PATH and plugin updates in the launcher and installer.
- **v3**: launchd units for the bridge and daemon, full-display capture of secondary screens (windows on any screen already work), and Linux (USB gadget on `dummy_hcd` or USB/IP) and Windows targets.
