# virtual-nanokvm

A software KVM for macOS. It lets an AI agent, or a script, use your Mac's mouse and keyboard the way a person does.

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

A move never jumps. Every move is randomized:

- **Curved path** that bends to one side, like a wrist arc
- **Natural speed**: slow start, fast middle, slow finish
- **Fitts's law timing**: longer for far or small targets
- **Overshoot and correction** on long moves
- **Slight tremor** that fades near the target

![Different paths between the same two points](docs/img/trajectories.svg)

![Speed over time](docs/img/speed-profile.svg)

Around the target, behavior varies too. Some moves overshoot and come back, some fall short and finish with a second stroke, and long ones sometimes detour. Before a click the hand dwells, sometimes longer, sometimes with a tiny wobble. After a click it often drifts a few points away. Clicks, scrolling and typing get human timing too. If you grab the mouse mid-action, vnkvm stops, releases every key and button, and reports `user took control`.

## Install

Needs macOS, Go and the Xcode command line tools. The MCP server builds itself on first start.

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

Both install the MCP server and a `vnkvm` skill that teaches the agent the screenshot → act → check loop. On first use, give the app running the agent **Accessibility** (to control input) and **Screen Recording** (for screenshots) permission. Set `VNKVM_INPUT=driver` to use the driver input instead of `sys`.

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

Each position comes back both **global** (whole desktop) and **relative** (pixels in the screenshot). The mouse tools accept coordinates straight from a window's screenshot.

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
