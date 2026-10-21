#!/usr/bin/env bash
# Install vnkvm on macOS:
#
#   curl -fsSL https://raw.githubusercontent.com/Lucklyric/virtual-nanokvm/main/install.sh | bash
#
# Add agent plugins in the same step:
#
#   curl -fsSL .../install.sh | bash -s -- --claude --codex
#
# Re-running updates an existing install. VNKVM_HOME (default ~/.vnkvm) sets
# where the source lives, VNKVM_BIN (default ~/.local/bin) where vnkvm links.
set -euo pipefail

REPO="https://github.com/Lucklyric/virtual-nanokvm.git"
HOME_DIR="${VNKVM_HOME:-$HOME/.vnkvm}"
BIN_DIR="${VNKVM_BIN:-$HOME/.local/bin}"
WANT_CLAUDE=0
WANT_CODEX=0
for arg in "$@"; do
	case "$arg" in
	--claude) WANT_CLAUDE=1 ;;
	--codex) WANT_CODEX=1 ;;
	*) echo "unknown option: $arg (use --claude, --codex)" >&2; exit 2 ;;
	esac
done

say() { printf '\033[1m==>\033[0m %s\n' "$*"; }
die() { printf 'vnkvm install: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Darwin" ] || die "vnkvm runs on macOS only."

if ! xcode-select -p >/dev/null 2>&1; then
	say "Installing the Xcode command line tools (a macOS dialog opens)..."
	xcode-select --install || true
	die "Finish the Xcode tools install, then run this again."
fi

if ! command -v go >/dev/null 2>&1; then
	if command -v brew >/dev/null 2>&1; then
		say "Installing Go with Homebrew..."
		brew install go
	else
		die "Go is required: install it from https://go.dev/dl (or install Homebrew), then run this again."
	fi
fi

if [ -d "$HOME_DIR/.git" ]; then
	say "Updating $HOME_DIR..."
	git -C "$HOME_DIR" pull --ff-only --quiet
else
	say "Downloading vnkvm to $HOME_DIR..."
	git clone --quiet --depth 1 "$REPO" "$HOME_DIR"
fi

say "Building..."
(cd "$HOME_DIR" && go build -o bin/vnkvm ./cmd/vnkvm)
mkdir -p "$BIN_DIR"
ln -sf "$HOME_DIR/bin/vnkvm" "$BIN_DIR/vnkvm"

if [ "$WANT_CLAUDE" = 1 ]; then
	if command -v claude >/dev/null 2>&1; then
		say "Adding the Claude Code plugin..."
		claude plugin marketplace add Lucklyric/virtual-nanokvm || true
		claude plugin install vnkvm@virtual-nanokvm
	else
		echo "Claude Code not found; skipped. Later: /plugin marketplace add Lucklyric/virtual-nanokvm"
	fi
fi
if [ "$WANT_CODEX" = 1 ]; then
	if command -v codex >/dev/null 2>&1; then
		say "Adding the Codex plugin..."
		codex plugin marketplace add "$REPO" || true
		codex plugin add vnkvm@virtual-nanokvm
	else
		echo "Codex not found; skipped."
	fi
fi

echo
say "Installed: $BIN_DIR/vnkvm"
case ":$PATH:" in
*":$BIN_DIR:"*) ;;
*) echo "    Add it to your PATH:  echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.zshrc" ;;
esac
echo
say "Checking setup (macOS may ask for permissions):"
"$HOME_DIR/bin/vnkvm" doctor --fix || true
echo
echo "Next:"
echo "  vnkvm serve                 # watch and control from a browser on your network"
echo "  vnkvm serve --tailscale     # ...or on your tailnet"
echo "  vnkvm move --input sys 800 450"
