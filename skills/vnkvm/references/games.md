# Games with vnkvm

Games draw on a canvas: there are no controls for Accessibility to press, so background mode does not work. Use foreground mode, a window shot of the game, and plan from pixels.

## Ground rules

- Take shots of the game window only (`app`), at 1024 px. Zoom with `region` to tell small sprites apart.
- One `act` call carries up to 20 steps. Plan a stretch of play from one shot, then read the returned shot and plan the next stretch.
- Add `wait_for` with `until: "change"` after anything that opens a screen (a dialog, a map, a fight), and `"stable"` before reading text.
- Measure once, then reuse it: how many pixels a character covers per second of walking, how long a fight round takes. Save it with `lessons` (for example "Walking: about 180 px of the 1024 px shot per second"). Later tasks plan with it instead of looking after every step.

## Click to move along a path

Many older RPGs move the character to where you click. To follow a path, click waypoints in order and give the character time to arrive:

```json
{"shot": 12, "steps": [
  {"do": "click", "x": 610, "y": 402},
  {"do": "wait", "ms": 900},
  {"do": "click", "x": 700, "y": 350},
  {"do": "wait", "ms": 800},
  {"do": "click", "x": 760, "y": 280},
  {"do": "wait_for", "until": "stable"}
]}
```

- Choose waypoints on open ground you can see in the shot, around obstacles, about one screen-third apart.
- Wait time per waypoint = distance / walking speed (from your lessons), plus a little.
- If the view scrolls with the character, waypoints from the old shot drift. Keep each batch within the part of the map that stays on screen, or plan 2 to 3 waypoints at a time.
- Games that walk while the button is held: `move` to the character, then `drag` toward the goal.

## Walk with keys

For games that move with arrows or WASD, `hold` keeps keys down for a set time:

```json
{"shot": 12, "steps": [
  {"do": "hold", "keys": "right", "ms": 1200},
  {"do": "hold", "keys": "up right", "ms": 600},
  {"do": "key", "keys": "space"}
]}
```

Keys held together are separated by spaces (`"up right"` walks diagonally). Holding is faster and more exact than many clicks once you know the walking speed.

## Pick a target, act on it

- Click the target. For a small or moving sprite, zoom first, or use `mark` to check the aim.
- Right-click often opens the action menu; read it from the returned shot and click the action.
- Repeated actions (attack, gather, talk through dialog): chain key presses or clicks with short waits, for example 5 times `{"do": "key", "keys": "space"}` and `{"do": "wait", "ms": 400}`. End with `wait_for` and check the result once, not after every press.

## When things go wrong

- The character stopped early: an obstacle. Re-plan from the returned shot with a waypoint around it.
- Nothing happened: the game window may not have focus. Click inside the game once before keys.
- Do not automate anything a game's rules forbid, or play against other people with it.
