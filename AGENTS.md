# AGENTS.md

Instructions for coding agents (Claude Code, Codex and others) working in this repository. `CLAUDE.md` only imports this file.

## What this is

yonder is a single Go binary that shares a local port or folder with other devices on the network (URL plus terminal QR code, access token, optional uploads, expiry and per-device approval). It ships through GoReleaser to GitHub releases, Homebrew, Scoop, apt/dnf/pacman, npm (`npm/`) and PyPI (`pypi/`). Read [ARCHITECTURE.md](ARCHITECTURE.md) before changing structure and [CONTRIBUTING.md](CONTRIBUTING.md) for the human workflow.

## Setup and checks

Go version comes from `go.mod`. No environment variables are needed to build or test.

```sh
go build -o yonder .
gofmt -l .                                   # must print nothing
go vet ./... && GOOS=windows go vet ./... && GOOS=darwin go vet ./...
golangci-lint run ./...                      # must report "0 issues" (v2.15, .golangci.yml)
go test -race -count=1 ./...
shellcheck install.sh && actionlint          # when scripts or workflows change
```

CI (`.github/workflows/ci.yml`) runs these checks on Linux and the tests on Linux, macOS and Windows; it fails under 85% total coverage on Linux. Releases are tag-driven; follow "Releases" in CONTRIBUTING.md.

## Conventions

- Every Go file starts with a header comment, and every top-level declaration has a doc comment that begins with its name. `TestEveryFileAndDeclarationIsDocumented` (`doccheck_test.go`) enforces it.
- Tests first: write the failing test, watch it fail for the right reason, then implement. For security-sensitive code (`auth.go`, `approve.go`, `files.go`, `upload.go`), break the rule on purpose once and confirm a test fails; the broken code must still compile, or the check proves nothing.
- Handle every error; where ignoring is right, write `_ =` with a comment saying why.
- Code must work on Linux, macOS and Windows. Platform-only code needs a build constraint; a `//go:build` line goes after the file's header comment (see `tty_unix.go`). File names ending in `_linux`, `_windows`, `_js` and similar are build constraints too.
- Standard library first; a new dependency needs a strong reason because it ships to users.
- Keep README, ARCHITECTURE.md, CONTRIBUTING.md, SECURITY.md and CHANGELOG.md ("Unreleased") accurate in the same change.
- Commits: [Conventional Commits](https://www.conventionalcommits.org/), small and atomic, green at every commit. The default branch is `master`.
- Never commit secrets. `.env` and `*.local.*` are gitignored.

## Agent workflow

This repo is built with the [superpowers](https://github.com/obra/superpowers) skills:

1. **Brainstorm** a feature into a section of the design spec, settled with the owner and reviewed by an independent agent before code.
2. **Plan** it as small, test-first tasks.
3. **Execute** the plan task by task with subagents, then request an independent code review of the whole diff and fix every confirmed finding with a failing test first.

Design specs live in [`docs/agents/specs/`](docs/agents/specs/) and implementation plans in [`docs/agents/plans/`](docs/agents/plans/), both dated. The spec is one living document with a section per version; it records decisions that the code alone cannot show.

Project agent config: [`.claude/settings.json`](.claude/settings.json) enables the `gopls-lsp`, `commit-commands` and `ponytail` plugins and registers the `ponytail` marketplace, and [`.codex/config.toml`](.codex/config.toml) enables `ponytail` for Codex. Personal overrides go in `.claude/settings.local.json`, which is gitignored.

## Gotchas

- Never run `pkill -f` or `pgrep -f` with a pattern that also appears in your own command line; it matches and kills your shell. Use `pkill -x name`.
- In PowerShell, quote `"-coverprofile=coverage.out"`; unquoted, it is split into two arguments.
- CI's ShellCheck can be newer than a local one: prefer plain `if` statements over `A && B || C`.
- Running yonder with real network exposure (for example `--public`) opens files to other people. Ask the owner before doing it.
