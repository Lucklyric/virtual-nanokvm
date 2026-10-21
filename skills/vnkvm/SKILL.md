---
name: vnkvm
description: Operate macOS apps through the vnkvm MCP tools (screenshot, act, click, type_text, key, scroll, drag), in the foreground with the real cursor or in the background without it. Use when a task needs clicking, typing or reading something on screen in a desktop app or a game.
---

# Using vnkvm

vnkvm sees the screen only through screenshots of one app, and acts in one of two modes:

- **Foreground** (default): the real mouse and keyboard, with human-like movement. The person shares the cursor, so act deliberately; if they move the mouse, vnkvm stops.
- **Background** (`"mode": "background"` on `act` and the action tools): no cursor, and the app stays where it is, so the person keeps working. Controls are pressed through Accessibility, and keys go to that app only.

Each model turn costs seconds; each vnkvm action costs about one. The goal is fewer turns: look cheaply, plan several steps from one look, and let `act` return the next look.

## Setup

If a tool fails, or on first use, call `doctor` (with `fix: true` to raise the macOS permission dialogs) and tell the user each `FIX` line. A locked Mac or sleeping display cannot be captured.

## Shots and coordinates

- Every image has a shot id. Coordinates are always pixels of a named shot: pass `shot` with every `x, y`.
- The server maps them to the screen. It follows a window that moved since the shot, and refuses a window that changed size or a point another window covers. In those cases take a new shot.
- A zoom is its own shot with its own pixels. Never reuse coordinates from one shot with another shot's id.

## The loop

0. **Read the lessons.** Call `lessons` with the app name. Apply what earlier tasks learned there before planning.
1. **Look low, at one app.** `screenshot` with `app` (or `window_id` from `list_windows`) at the default 1024 px. Screenshots cover one app's window only; whole-display capture is off unless the person turned it on. Look only at the app the task needs.
2. **Plan a sequence.** Before acting, write down the steps and what the screen should look like after each one. Put in one `act` call every step whose target you can already see, or whose effect you can predict with confidence.
3. **Zoom only when needed.** If a target is small, its text is hard to read, or two targets sit close together, call `screenshot` with `shot` and `region` around it. To check aim before an important click, call `screenshot` with `shot` and `mark` at the planned pixel: the crosshair shows where it will land.
4. **Act.** Send the planned steps to `act`. It returns what each step did and a new low shot after the screen settles.
5. **Check against the plan.** Compare the new shot with what you predicted. Zoom to read details only when the answer is not visible at low detail. Then plan the next sequence from this shot.

## Choosing a mode

Use the mode the person asks for. When they say "in the background", "don't take my mouse" or "while I work", use background. Otherwise use foreground.

Background mode works by control, not by pixel:

| Works in background | Does not |
|---|---|
| Buttons, checkboxes, tabs, menu items in native apps (Calculator, TextEdit, Settings) | Right-click (it brings the app forward), double-click, drag, hover |
| Buttons and links on web pages in Chrome (focused, then Space or Enter) | Canvases, games, maps, drawing areas: no controls to press |
| Text fields: the click focuses the field, then `type` and plain keys arrive | `cmd` shortcuts that run menu commands (Select All, Copy): use a `menu` step for app-wide commands instead |
| `menu` steps for app-wide commands (File > New) | Commands on the current document (Edit > Select All) when the app is in the background |
| Scrolling, `hold`, `wait_for` | A code editor's main text area in VS Code (focus a text field instead) |

If a background step fails with "nothing to press there", the spot is not a control. Tell the person and offer foreground mode; do not switch on your own, since foreground takes their cursor.

## Fast in the foreground

The model's turns cost the most, then cursor travel (about 0.5 to 1 s per human-like move), then typing. In order of gain:

1. **One look, one plan, one `act`.** Most tasks need 2 to 4 turns, not one turn per click.
2. **Keyboard before mouse.** A shortcut costs a fraction of a second and needs no coordinates: `tab` through form fields instead of clicking each one, `cmd+l`, `cmd+f`, `enter`, arrow keys in lists.
3. **`menu` for commands with no shortcut you know,** instead of opening menus by clicking.
4. **Fewer moves.** Order clicks so the cursor travels short distances; click in place (no `x, y`) when the cursor is already there.
5. **`size` only for small targets.** A small `size` slows the approach on purpose; leave it out for normal buttons.
6. **Low detail, zoom only on doubt, `mark` only before risky clicks.**
7. **Do not check every step.** Let the observation at the end of `act` show the result; add `wait_for` only where the next step depends on something loading.
8. **Stay in one app.** Each switch costs a new look.
9. **Record lessons** when something was slow, so the next task in that app skips the detour.

## Games and other canvas apps

Games draw everything themselves, so use foreground mode and plan from pixels. See `references/games.md` for click-to-move paths, walking with held keys, picking targets and repeated actions.

## Planning a good sequence

- **Chain what is visible now.** Click a field, type, press `tab`, type, press `enter`: one call.
- **Prefer the keyboard when it is reliable.** Shortcuts need no coordinates and do not depend on layout: `cmd+l` (address bar), `cmd+f` (find), `cmd+n` (new), `tab` and `shift+tab` between fields, `enter`, `escape`, `cmd+a` then type to replace text.
- **End the sequence at uncertainty.** Stop when the next target appears only after something opens, loads or moves: a menu, a dialog, a new page, search results. Add `wait_for` with `until: "change"` or `"stable"` as the last step, so the returned shot shows the result.
- **Use `wait_for` between dependent steps** inside a sequence when the next step needs the result of the previous one and its position is fixed (for example, a button that appears in a known place).
- **Give `size`** for small targets (checkboxes, small icons, close buttons) so the approach slows down.
- **Do not chain clicks on targets you predicted but have not seen.** A wrong guess wastes the steps after it.

Example: search from a Safari window seen in shot 4. Only the keyboard is needed, and the last step waits for the results.

```json
{"shot": 4, "steps": [
  {"do": "key", "keys": "cmd+l"},
  {"do": "type", "text": "weather tomorrow"},
  {"do": "key", "keys": "enter"},
  {"do": "wait_for", "until": "change"}
]}
```

## Learn from slow tasks

The server watches the loop and adds an `Efficiency:` hint to a tool result when it looks slow: three looks without an action, three single actions in a row, or the same failure twice. `lessons` also returns the session's counters.

When you see a hint, or a task took clearly more turns than its steps needed, record what you learned with `lessons` and `add`, in one line:

- what slowed you down, and what worked instead (for example: "The search field needs a click before cmd+f works; then one act call does the whole search.");
- a shortcut, a stable position or a wait that this app needs.

Keep lessons short and general. Merge or drop outdated ones with `set`. Never record passwords, personal text, names, or anything read from the window.

## When a sequence fails

- The report says which step failed and why, and the returned shot shows the screen now. Re-plan from that shot; do not repeat the whole sequence blindly.
- `changed size` or `is gone`: take a new shot of the window.
- `covered`: bring the window forward (click a visible part of it, or `key` with `cmd+tab`), then take a new shot.
- `user took control`: the person moved the mouse. vnkvm has already let go of every key and button. Do not retry. Tell the person what you were doing and wait until they say to continue.

## Keyboard

- Keys go to the frontmost app. If the target window is behind another, click it first.
- `type` (in `act`) and `type_text` type US-keyboard characters with human rhythm. Click the field first.
- `key` presses one key or a combination: `enter`, `escape`, `tab`, `cmd+a`, `cmd+c`, `cmd+v`, `cmd+shift+4`, arrow keys.

## Care

- Confirm before anything destructive or hard to undo: deleting, sending, purchasing, submitting forms. Planning a sequence does not replace this: stop the sequence before the irreversible step and ask.
- Do not type passwords or secrets unless the person explicitly gives them for this purpose.
- Look only at the app the task needs. `list_windows` hides other apps' window titles; do not open other apps to look around.
