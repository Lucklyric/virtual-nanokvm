---
name: vnkvm
description: Operate macOS apps through the vnkvm MCP tools (screenshot, act, click, type_text, key, scroll, drag). Use when a task needs clicking, typing or reading something on screen in a desktop app.
---

# Using vnkvm

vnkvm drives the real mouse and keyboard with human-like movement, and sees the screen only through screenshots. The person at the Mac shares the same cursor, so act deliberately.

Each model turn costs seconds; each vnkvm action costs about one. The goal is fewer turns: look cheaply, plan several steps from one look, and let `act` return the next look.

## Setup

If a tool fails, or on first use, call `doctor` (with `fix: true` to raise the macOS permission dialogs) and tell the user each `FIX` line. A locked Mac or sleeping display cannot be captured.

## Shots and coordinates

- Every image has a shot id. Coordinates are always pixels of a named shot: pass `shot` with every `x, y`.
- The server maps them to the screen. It follows a window that moved since the shot, and refuses a window that changed size or a point another window covers. In those cases take a new shot.
- A zoom is its own shot with its own pixels. Never reuse coordinates from one shot with another shot's id.

## The loop

1. **Look low.** `screenshot` with `app` (or `window_id` from `list_windows`) at the default 1024 px. Use the whole display only when the task spans apps.
2. **Plan a sequence.** Before acting, write down the steps and what the screen should look like after each one. Put in one `act` call every step whose target you can already see, or whose effect you can predict with confidence.
3. **Zoom only when needed.** If a target is small, its text is hard to read, or two targets sit close together, call `screenshot` with `shot` and `region` around it. To check aim before an important click, call `screenshot` with `shot` and `mark` at the planned pixel: the crosshair shows where it will land.
4. **Act.** Send the planned steps to `act`. It returns what each step did and a new low shot after the screen settles.
5. **Check against the plan.** Compare the new shot with what you predicted. Zoom to read details only when the answer is not visible at low detail. Then plan the next sequence from this shot.

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
