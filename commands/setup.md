---
description: Check and fix vnkvm setup (permissions, display, driver, Tailscale)
---

Run the vnkvm `doctor` MCP tool with `fix: true`. It checks macOS, the Accessibility and Screen Recording permissions, whether the display can be captured, the driver input bridge, and Tailscale. With `fix`, macOS opens its permission dialogs.

Then report to the user, briefly:

- Every line marked `FIX`, with its fix in plain words. These block vnkvm.
- Lines marked `--` are optional features (driver input, Tailscale). Mention them in one line each.
- If Screen Recording was just granted, the app running the agent must be restarted before screenshots work.

If the `doctor` tool itself is missing, the MCP server did not start: Go and the Xcode command line tools are needed to build it. Suggest the installer:

```sh
curl -fsSL https://raw.githubusercontent.com/Lucklyric/virtual-nanokvm/main/install.sh | bash
```
