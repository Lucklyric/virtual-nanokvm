# CLAUDE.md

- Design and rationale: `docs/design.md`. Keep it in sync with behavior changes.
- Build with `make`, test with `make test`. Regenerate README figures with `go test ./internal/human -run TestWriteFigures -figures "$PWD/docs/img"` after engine changes.
- Commits use Conventional Commits.
- This repo is public: never commit real screenshots, host names, absolute local paths, emails or other personal data. Figures come from the simulator only. Agent lessons live in `~/.config/vnkvm/lessons/` on each Mac, never in this repo.
