# virtual-nanokvm

A software KVM for macOS. It lets an AI agent, or a script, use your Mac's mouse and keyboard the way a person does, and lets you watch and control the Mac from a browser.

Inspired by the [Sipeed NanoKVM](https://sipeed.com/nanokvm-go), but with no extra hardware: it runs locally on the Mac it controls.

![A cursor moving along a human-like path](docs/img/cursor.svg)

## How it works

![Architecture](docs/img/architecture.svg)

One human-like input engine, two input paths:

| Input | How it reaches macOS | Setup |
|---|---|---|
| `sys` | Synthetic system events (`CGEventPost`) | Accessibility permission only |
| `driver` | Virtual USB keyboard and mouse ([Karabiner-DriverKit-VirtualHIDDevice](https://github.com/pqrs-org/Karabiner-DriverKit-VirtualHIDDevice)); macOS sees real hardware | Driver install plus a small root bridge |

## Human-like movement

A move never jumps, and no two moves are the same. Each one has:

- a curved path that bends to one side, like a wrist arc
- a natural speed: slow start, fast middle, slow finish
- Fitts's law timing: longer for far or small targets
- an overshoot and correction on some long moves
- a slight tremor that fades near the target

![Different paths between the same two points](docs/img/trajectories.svg)

![Speed over time](docs/img/speed-profile.svg)

Around the target, behavior varies too. Some moves overshoot and come back, some fall short and finish with a second stroke, and long ones sometimes detour. Before a click the hand dwells, sometimes longer, sometimes with a tiny wobble. After a click it often drifts a few points away. Clicks, scrolling and typing get human timing too. If you grab the mouse mid-action, vnkvm stops, releases every key and button, and reports `user took control`.

## Install

One line (macOS 14+). It installs Go if needed (with Homebrew), builds vnkvm, links it to `~/.local/bin`, and runs a setup check:

```sh
curl -fsSL https://raw.githubusercontent.com/Lucklyric/virtual-nanokvm/main/install.sh | bash
```

Add `-s -- --claude --codex` after `bash` to install the agent plugins in the same step. Run `vnkvm doctor` at any time to see what is missing; `vnkvm doctor --fix` raises the macOS permission dialogs.

### As an agent plugin

**Claude Code**

```
/plugin marketplace add Lucklyric/virtual-nanokvm
/plugin install vnkvm@virtual-nanokvm
```

**Codex**

```sh
codex plugin marketplace add https://github.com/Lucklyric/virtual-nanokvm.git
codex plugin add vnkvm@virtual-nanokvm
```

Both install the MCP server and a `vnkvm` skill that teaches the agent the screenshot → act → check loop. The server builds itself on first start (Go and the Xcode command line tools are needed; the one-line installer sets them up). Then run `/vnkvm:setup` in Claude Code, or ask Codex to "run the vnkvm doctor": it checks and requests the Accessibility and Screen Recording permissions for the app running the agent. Set `VNKVM_INPUT=driver` to use the driver input instead of `sys`.

**Any other MCP client**

```sh
git clone https://github.com/Lucklyric/virtual-nanokvm.git
virtual-nanokvm/scripts/vnkvm-mcp   # MCP server on stdio
```

| Tool | Does |
|---|---|
| `screenshot` | Captures the screen, or one app window even when it is covered |
| `list_windows` | Lists app windows with ids and positions |
| `mouse_move`, `click`, `drag`, `scroll` | Human-like mouse actions |
| `type_text`, `key` | Typing and key combinations (`cmd+c`) |
| `cursor_position` | Where the cursor is |
| `doctor` | Checks permissions, display, driver input and Tailscale |

Each position comes back both global (whole desktop) and relative (pixels in the screenshot). The mouse tools accept coordinates straight from a window's screenshot.

### Watch and control from a browser

```sh
vnkvm serve --input sys              # on your local network
vnkvm serve --input sys --tailscale  # on your tailnet, from anywhere
```

It prints two links: a control link (watch, plus mouse and keyboard) and a view link (watch only). Click **Take control** to drive the Mac; your own hand movement is relayed as-is. One person controls at a time, and every key and button is released when they leave.

Video works as on the NanoKVM: H.264 (hardware-encoded with VideoToolbox, decoded in the browser with WebCodecs) when the page is HTTPS or localhost, otherwise MJPEG. The viewer picks automatically.

**Tailscale:** `--tailscale` shares the viewer on your tailnet only. If your tailnet has HTTPS certificates (Tailscale admin console > DNS > enable HTTPS Certificates), vnkvm binds localhost and runs `tailscale serve` on port 8443, so viewers get an `https://<mac>.<tailnet>.ts.net:8443` link and H.264. Without certificates it serves plain HTTP on the tailnet address (still encrypted by WireGuard), and viewers get MJPEG.

Flags: `--listen` (default `:8090`), `--fps` (default 10), `--width` (default 1280), `--kbps` (H.264 bitrate, default 3000), `--token` and `--view-token` (random by default). Without `--tailscale`, links travel over plain HTTP, so use your LAN only if you trust it. Some shortcuts such as `cmd+w` stay with the browser. Unlike a hardware KVM, macOS hides the screen from capture while it is locked or asleep.

### Command line

```sh
make service
bin/vnkvm move  --input sys 800 450
bin/vnkvm click --input sys 800 450
bin/vnkvm type  --input sys "hello"
```

### Driver input (real USB HID)

1. Install `Karabiner-DriverKit-VirtualHIDDevice-8.6.0.pkg` and approve its system extension.
2. Start the Karabiner daemon and the bridge as root:
   ```sh
   make bridge
   sudo bin/vnkvm-hid-bridge --socket /var/run/vnkvm-hid.sock --owner $(id -u)
   ```
3. Use `--input driver` (the default).

See [docs/design.md](docs/design.md) for the full design.

## Tests

```sh
make test
```

The engine is tested against a simulated screen with pointer acceleration. The tests check that moves land within 1 point, follow a bell-shaped speed curve, curve rather than run straight, differ every time, and stop when a hand takes over. The figures above come from the same simulation:

```sh
go test ./internal/human -run TestWriteFigures -figures "$PWD/docs/img"
```

## License

MIT
