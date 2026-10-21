---
name: vnkvm
description: Operate macOS apps through the vnkvm MCP tools (screenshot, list_windows, click, type_text, key, scroll, drag). Use when a task needs clicking, typing or reading something on screen in a desktop app.
---

# Using vnkvm

vnkvm drives the real mouse and keyboard with human-like movement. The person at the Mac shares the same cursor, so act deliberately.

## Loop

1. **Look first.** Call `screenshot`. If you work in one app, pass `app` (for example `"Safari"`) or a `window_id` from `list_windows`. A window capture works even when other windows cover it.
2. **Act with that screenshot's pixels.** Pass the same `app` or `window_id` to `click`, `mouse_move` or `drag`, with `x, y` taken straight from the image. Give `size` for small targets (checkboxes, icons) so the approach is careful.
3. **Check the result.** Take a new screenshot after each action that changes the screen. Do not chain several clicks blind.

## Coordinates

- Results carry `global` (whole desktop, in points) and `relative` (pixels in the screenshot you used).
- `global = origin + pixel × scale`. Every screenshot reports its `origin` and `scale`.
- If a click is refused because another window covers the spot, bring the window forward first (click its visible part, or use `key` with `cmd+tab`), then take a new screenshot.

## Keyboard

- `type_text` types US-keyboard characters with human rhythm. Click the field first.
- `key` presses one key or a combination: `enter`, `escape`, `tab`, `cmd+a`, `cmd+c`, `cmd+v`, `cmd+shift+4`, arrow keys.

## When the person takes over

If a tool returns `user took control`, the person moved the mouse. vnkvm has already stopped and let go of every key and button. Do not retry right away. Tell the person what you were doing and wait until they say to continue.

## Care

- Confirm before anything destructive or hard to undo: deleting, sending, purchasing, submitting forms.
- Do not type passwords or secrets unless the person explicitly gives them for this purpose.
