# Contributing to yonder

Thank you for helping. This guide shows how to set up, run the checks, and send a change that can be merged without back and forth. To understand how the code is organised first, read [ARCHITECTURE.md](ARCHITECTURE.md).

## Ground rules

- **Be kind.** Treat everyone with respect, in issues, pull requests and reviews.
- **Security problems are not public issues.** Follow [SECURITY.md](SECURITY.md).
- **Small, finished changes.** One logical change per pull request, with tests and documentation included.
- **Never commit secrets** (keys, tokens, `.env` files). `.env` is in `.gitignore`; keep it that way.

## Set up

You need:

| Tool | Version | Needed for |
|---|---|---|
| Go | 1.27 or newer (see `go.mod`) | building and testing |
| [golangci-lint](https://golangci-lint.run/welcome/install/) | 2.15 | linting (`.golangci.yml`) |
| Node.js | 18 or newer | one test that runs the upload page's script (skipped without it) |
| [ShellCheck](https://www.shellcheck.net/) | any recent | checking `install.sh` |
| [actionlint](https://github.com/rhysd/actionlint) | 1.7 | checking the workflows |
| [GoReleaser](https://goreleaser.com/install/) | 2.19 | checking and dry-running the release (optional) |

```sh
git clone https://github.com/dpologdvinity/yonder.git
cd yonder
go build -o yonder .       # builds ./yonder
go test ./...              # runs the whole test suite
```

## Try your change by hand

Share a folder with uploads, from a safe empty folder, and open the link on your phone:

```sh
mkdir -p /tmp/demo && echo hello > /tmp/demo/index.html
go run . /tmp/demo --upload
```

Or share a dev server:

```sh
python3 -m http.server 8000 --bind 127.0.0.1 &   # any site on a port
go run . 8000
```

Never share your home directory while testing. See the README's [Firewalls](README.md#firewalls) and [WSL](README.md#wsl) sections if a phone cannot connect.

## Run the checks

CI runs all of these. Run them before you push:

```sh
gofmt -l .                       # prints nothing when formatting is correct
go vet ./...
GOOS=windows go vet ./... && GOOS=darwin go vet ./...
golangci-lint run ./...          # must report "0 issues"
go test -race -count=1 ./...     # use "go test ./..." if the race detector is unavailable
shellcheck install.sh
actionlint
goreleaser check                 # if you changed .goreleaser.yaml
```

To see test coverage:

```sh
go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1
go tool cover -html=coverage.out   # opens a report in your browser
```

CI requires at least 85% total coverage on Linux. Please do not let it fall.

## How to make a change

1. **Open an issue first** for anything bigger than a small fix, so we agree on the approach before you write code.
2. **Branch** from `master`: `git switch -c fix/short-description`.
3. **Write the test first.** For a bug, write a test that fails because of the bug. For a feature, write a test for the behaviour you want. Run it and watch it fail for the right reason. Then write the code.
4. **Document as you go.** Every Go file starts with a comment that says what it is for, and every function, type, variable and constant has a doc comment that begins with its name. `TestEveryFileAndDeclarationIsDocumented` fails otherwise. Write for a reader who has not seen the code. Update the README and ARCHITECTURE.md if behaviour or structure changes, and add a line to `CHANGELOG.md` under "Unreleased".
5. **Handle every error.** `golangci-lint` rejects ignored errors. Where ignoring is right (for example best-effort cleanup), write `_ =` and a comment that says why.
6. **Mind other systems.** yonder runs on Linux, macOS and Windows. File names, signals and file permissions differ; tests that cannot run everywhere must skip with a clear reason. Code that only builds on some systems needs a build constraint.
7. **Commit with [Conventional Commits](https://www.conventionalcommits.org/)**, small and focused:
   - `feat: let uploads be turned off per folder`
   - `fix: keep the token out of the log`
   - `docs:`, `test:`, `refactor:`, `ci:`, `build:`, `chore:`
8. **Open a pull request** into `master`. Say what changed, why, and how you checked it. CI must be green. A maintainer reviews, and may ask for changes.

## Code style

- Standard Go style: `gofmt`, short functions, errors returned (not logged and ignored), `context` for cancellation.
- Prefer the standard library. A new dependency needs a strong reason, because every dependency is also shipped to users.
- Security-sensitive code (`auth.go`, `files.go`, `upload.go`) needs a test for each rule and, where possible, a test that fails when the rule is removed. After you write such a test, remove the fix once to prove the test notices.
- Messages shown to users are plain sentences that say what is wrong and what to do.

## Releases (maintainers)

1. Make sure `master` is green in CI and `CHANGELOG.md` lists the changes under a new version heading.
2. Tag the commit: `git tag v0.5.0 && git push origin v0.5.0`.
3. The release workflow checks that CI passed for that commit, builds and publishes everything, and the package repositories update themselves within the hour. See [ARCHITECTURE.md](ARCHITECTURE.md#10-releases-and-distribution).
4. Check the result: `yonder --version` after `brew upgrade`, `pip install -U yonder-cli`, and a look at the release page.

Never reuse or move a version tag: PyPI and npm do not allow a version number to be published twice.
