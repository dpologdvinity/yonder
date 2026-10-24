# Architecture

This document explains how yonder is built and why. It is written for someone who wants to change the code, review its security, or understand how a release reaches users. For how to *use* yonder, read the [README](README.md). For how to *work on* it, read [CONTRIBUTING.md](CONTRIBUTING.md).

## Contents

1. [What yonder does](#1-what-yonder-does)
2. [Technology](#2-technology)
3. [Source layout](#3-source-layout)
4. [Start-up and shutdown](#4-start-up-and-shutdown)
5. [How a request travels](#5-how-a-request-travels)
6. [Components in detail](#6-components-in-detail)
7. [Security design](#7-security-design)
8. [Design decisions](#8-design-decisions)
9. [Testing](#9-testing)
10. [Releases and distribution](#10-releases-and-distribution)
11. [Where to make a change](#11-where-to-make-a-change)

## 1. What yonder does

yonder makes one thing on your computer reachable from other devices on the same network:

- a **website** running on `localhost:<port>` (`yonder 3000`), or
- a **file or folder** (`yonder ./photos`), optionally accepting **uploads** (`--upload`).

It prints a link and a QR code. By default the link contains a secret token, and nothing is served to a request that does not carry it.

Everything runs inside one process. There is no daemon, no configuration file, no account and no server run by the project: the other device only needs a web browser.

## 2. Technology

| Part | Choice | Why |
|---|---|---|
| Language | Go 1.27 | One static binary per platform, a strong HTTP library in the standard library, easy cross-compiling. |
| HTTP | `net/http`, `net/http/httputil` | The reverse proxy, WebSocket upgrades, range requests and file serving come with the standard library. |
| File access | `os.Root` | The operating system, not string checks, keeps access inside the shared folder (see [section 7](#7-security-design)). |
| QR code | `github.com/mdp/qrterminal/v4` | The only third-party dependency. It draws the code in the terminal. |
| Packaging | GoReleaser (with nFPM) | Builds binaries, archives, `.deb`, `.rpm` and Arch packages from one config. |
| CI | GitHub Actions | Lint, test on three operating systems, dry-run the release. |

## 3. Source layout

All code is in one Go package, `main`, at the repository root. One file has one job.

| File | Responsibility |
|---|---|
| `main.go` | The command line: `parseArgs`, the `run` life cycle, `fileHandler` (turns a path into something to serve), version handling and output helpers. |
| `target.go` | `findTarget`: which loopback address (`127.0.0.1` or `::1`) the dev server really listens on. |
| `lan.go` | `systemLANAddrs` and `lanAddrs`: which address other devices should use, with virtual interfaces ranked last. |
| `port.go` | `listen`: opens the public socket and picks its port (shared port plus 10000, with fallbacks). |
| `auth.go` | The access gate `withAuth` and `withAuthDevices`, token creation, and the helpers that remove the token and cookie before a request moves on. |
| `approve.go` | Device approval for `--approve`: the table of devices, the questions asked at the terminal, and the description of a browser shown to the owner. |
| `proxy.go` | The reverse proxy `newProxy`, the request log `withLog`, and the "device connected" notice. |
| `files.go` | The file server `newFileServer`: listing page, downloads, and the rules about what may be served. |
| `upload.go` | Receiving uploads: streaming to a hidden temporary file and moving it into place safely. |
| `wsl.go` | Detects Windows Subsystem for Linux in NAT mode and explains the fix. |
| `*_test.go` | Tests. See [section 9](#9-testing). |

Outside the Go code:

| Path | Purpose |
|---|---|
| `install.sh` | The `curl \| sh` installer. |
| `npm/` | The `yonder-cli` npm package: a small wrapper that downloads and runs the binary. |
| `pypi/build_wheels.py` | Builds the `yonder-cli` PyPI wheels from a GitHub release. |
| `.goreleaser.yaml` | How a release is built and packaged. |
| `.github/workflows/` | `ci.yml` (checks on every push and pull request) and `release.yml` (publishing). |
| `docs/demo.gif` | The animation in the README. |

## 4. Start-up and shutdown

`run` in `main.go` is yonder's whole life cycle. `main` only wires it to the operating system's signals.

```
parseArgs ──▶ choose what to share ──▶ find a network address ──▶ open the public port
                  │                                                     │
        port: findTarget (target.go)                                    ▼
        path: fileHandler (main.go)                         build the handler chain
                                                                        │
                                                                        ▼
                                                      print link, QR code and warnings
                                                                        │
                                                                        ▼
                                                    serve until Ctrl-C, SIGTERM or SIGHUP
                                                                        │
                                                                        ▼
                                              close the server, wait up to 3 s for uploads
```

1. **Arguments.** `parseArgs` accepts options before or after the target. A number is a port (1 to 65535); anything else is a path. `--upload` needs a folder.
2. **Target.** For a port, `findTarget` checks which of `127.0.0.1` and `::1` accepts a connection. Node.js servers often listen on IPv6 only. For a path, `fileHandler` resolves symbolic links, checks the file name against the same rule the server uses, and opens an `os.Root` on the folder.
3. **Terminal.** With `--approve`, yonder checks that standard input is a real terminal and that yonder is in the foreground of it, because the owner's answers come from it.
4. **Address.** `systemLANAddrs` lists private IPv4 addresses. The one the operating system would use to reach the internet comes first; container, VM and VPN addresses go last. If there are none, `noAddrError` explains why.
5. **Public port.** `listen` opens `0.0.0.0:<port>`. The default is the shared port plus 10000, so the link stays the same across restarts; if that is taken, the operating system picks a free port.
6. **Handler chain.** See the next section. With `--approve`, a `deviceTable` is created and a goroutine starts asking the owner about each new device.
7. **Output.** The link, the QR code, the access warning, the uploads and time-limit lines and, on WSL in NAT mode, the fix.
8. **Serving.** `http.Server` runs with a header timeout (against slow-connection attacks) and an idle timeout. There is deliberately no write timeout, because WebSockets and event streams are long-lived.
9. **Shutdown.** On Ctrl-C, terminate or hang-up, or when the `--for` time is up (`expireAt` ends the serving context), `run` calls `Server.Close`, closes every WebSocket connection (`Server.Close` leaves those open), then waits up to three seconds for running uploads to remove their temporary files, and exits with code 0. `Close` is used instead of `Shutdown` because `Shutdown` waits for hijacked WebSocket connections that may never end.

## 5. How a request travels

```
                    ┌────────────────────────── http.Server ──────────────────────────┐
 browser ──TCP──▶   │ ConnState hook: print "device connected" for a new address       │
                    │        │                                                         │
                    │        ▼                                                         │
                    │   withLog ──────────────▶ prints one line after the request      │
                    │        │                                                         │
                    │        ▼                                                         │
                    │   withAuth   (skipped with --open)                               │
                    │        │  no token: 403, and the request stops here              │
                    │        ▼                                                         │
                    │   ┌── newProxy ──▶ your dev server on localhost                  │
                    │   └── newFileServer ──▶ the shared folder (read; or write)       │
                    └──────────────────────────────────────────────────────────────────┘
```

**First visit with the link** (`GET /?yonder=<token>`):

1. `withAuth` compares the token with a constant-time comparison.
2. It answers `302` to the same address without the token, and sets the cookie `yonder_<port>` (`HttpOnly`, `SameSite=Lax`). It also sends `Referrer-Policy: no-referrer`.
3. The browser follows the redirect. The page it shows has no token in its address, and requests the page makes carry no token in their `Referer` header. (The first address, with the token, may remain in the browser's history.)

A `POST` that carries the token is not redirected, because a redirect would lose its body. It is forwarded with the token removed.

**Every later request** has the cookie. `withAuth` accepts it, removes the `yonder` query parameter and every `yonder_<digits>` cookie, and passes the rest of the request on byte for byte. The app never sees yonder's token.

**In the proxy**, `newProxy` rewrites the request so the dev server believes it came from `localhost`: `Host` becomes `localhost:<port>`, and an `Origin` that matches the visitor's address becomes `http://localhost:<port>`. On the way back it points redirects that name localhost at the address the visitor used, and removes `Domain=localhost` from cookies, which browsers reject on an IP address. WebSockets and event streams pass through. If the dev server is down, the visitor gets a `502` with a plain message, and the next request works once it is back.

**In the file server**, a request path becomes a folder-relative name through `safeRel`, which rejects hidden names (starting with a dot), backslashes, and on Windows `:` and `~`. Every segment is then checked with `Lstat`: only plain folders and regular files are allowed, so symbolic links, junctions, pipes and devices are never followed. A folder produces the HTML listing; a file goes to `http.ServeContent`, which handles content types and range requests.

## 6. Components in detail

### Access gate (`auth.go`)

`withAuth(handler, token, listenPort)` returns a handler that only calls `handler` for authorised requests. A request is authorised if its `yonder` query parameter or its `yonder_<listenPort>` cookie equals the token. The token is 16 random bytes from `crypto/rand`, encoded as 22 URL-safe characters, and is new on every run.

Details that matter:

- The redirect target is built from the escaped path only and never starts with two slashes, so a crafted link cannot send a visitor to another website.
- Cookies are removed from the raw header text, not parsed and rebuilt, so values with quotes, JSON or non-ASCII text reach the app unchanged.
- A cookie such as `yonder_theme` belongs to the app and is kept; only `yonder_` followed by digits is yonder's.
- `redactToken` removes the token from the request line before it is logged, including in malformed queries (`?a;yonder=T`) that never authenticate.

### Proxy and logging (`proxy.go`)

`withLog` wraps a handler and prints `HH:MM:SS  METHOD /path?query  STATUS  address` once the request is done. It uses `statusRecorder` to learn the status code. The recorder passes `Hijack` (WebSocket upgrades), `Unwrap` (flushing for event streams) and `ReadFrom` (the operating system's fast file copy) through, so logging never changes behaviour. The log line is written in a deferred call, so a stream that is aborted still produces a line.

`newConnLogger` prints `device connected` the first time each visitor address opens a connection. That line separates "the connection never arrived" (a firewall or network problem) from "it arrived and the app failed".

### File server (`files.go`) and uploads (`upload.go`)

The file server is created with `newFileServer(root, only, upload)`:

- `root` is an `os.Root`. All reads and writes go through it, so `..` and symbolic links cannot leave the folder, even through a bug in the string checks.
- `only` is set for a single-file share: only that name is reachable, and the listing shows only it without reading the folder.
- `upload` turns on uploads.

The listing is rendered into a buffer before anything is sent, so a template failure becomes a clean `500`. It shows at most 5000 entries: entries are read in batches, hidden ones are skipped without counting, the rest are sorted (folders first, then by name) and cut.

With uploads on, every served file carries `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`. An uploaded HTML page then cannot run script on yonder's address, where the cookie is shared by every port on that IP.

**An upload**, in `receiveUpload`:

1. The request must pass the browser's cross-origin check (`http.CrossOriginProtection`), so another website's page cannot post into your folder.
2. The body is limited to 4 GiB with `http.MaxBytesReader` and read as a stream; nothing is held in memory.
3. For each file, only the last part of its name is used. Names that are hidden, empty, contain control or text-direction characters, or that the file system refuses are rejected with `400`.
4. `saveUnique` writes the content to a hidden temporary file `.yonder-part-<random>`, then claims the final name by creating an empty file with `O_EXCL` (which fails if the name exists) and renames the temporary file over it. If the name exists it tries `name (1).ext`, `name (2).ext`, up to 999. An existing file is never touched.
5. If anything fails, or the upload is cut off, the temporary file is removed. During shutdown, `waitUploads` gives running uploads up to three seconds to do that.
6. On success the browser is redirected back to the listing (`303`).

### Device approval (`approve.go`)

With `--approve`, the cookie value is `<token>.<device id>` instead of the token alone. A device id is 128 random bits, made on a browser's first visit with a valid token, and a `deviceTable` stores its state (`pending`, `approved` or `denied`), the visitor's address and its `User-Agent`.

- The first visit sets the cookie and redirects, as usual. Every later request is checked against the table: `approved` goes on to the app or file server; `pending` gets a `202` page that reloads every two seconds; `denied` gets `403`. A `POST` that carries the token on a first visit is never forwarded.
- `runPrompts` reads the owner's answers from standard input, one device at a time in arrival order. `y` or `yes` approves; anything else, or the end of the input, refuses.
- The text shown to the owner is built by `describeUserAgent` from fixed word lists only. A visitor chooses their `User-Agent`, so it is never printed as it arrived; this keeps terminal control sequences off the owner's screen.
- At most 10 devices can be pending (`maxPendingDevices`) and at most 2 from one address (`maxPendingPerAddress`), so one visitor with the link cannot fill every place. A waiting device that has not asked again for a minute (`pendingTTL`) is forgotten, along with its question. The waiting page reloads every two seconds, so a real visitor never reaches that. Answers are final, and the table lives only as long as the run.
- Waiting-page requests are kept out of the request log (`markQuiet`), so they do not scroll the owner's question away. The question shows the visitor's address without its port, which changes on every connection.
- Answers are read one byte at a time, never ahead, and the terminal's unread input is discarded before each question (`flushInput`), so text typed early cannot answer a question that has not been shown. `inForeground` makes `--approve` refuse to start in a background job, which the shell would freeze.

### Network address (`lan.go`, `port.go`, `target.go`, `wsl.go`)

- `lanAddrs` keeps only private IPv4 addresses on interfaces that are up and not loopback, removes duplicates, and ranks them: the address of the default route first (unless its interface is virtual), then ordinary interfaces, then Docker, VM, VPN and point-to-point interfaces. Interface names are matched case-insensitively and include Windows names such as "vEthernet" and "OpenVPN TAP-Windows6".
- `findTarget` tries IPv4 and then IPv6 loopback with a short timeout.
- `listen` reports a busy `--listen` port with a clear message on every operating system (`isAddrInUse` knows both the Unix and the Windows error value).
- `detectWSLHint` reads `/proc/sys/kernel/osrelease` and `wslinfo --networking-mode`. In NAT mode no other device can reach the machine, so yonder says how to fix it.

## 7. Security design

yonder exposes part of your computer to a network. The design assumes that **other people on that network are not trusted**, and that **web pages you visit can send requests to it**.

| Who or what | What they should not be able to do | How yonder prevents it |
|---|---|---|
| A device on the network without the link | Read the shared site or files | Every request needs the token (128 bits, compared in constant time). Without it: `403`, and the app never sees the request. |
| The same device after a restart | Reuse an old link or cookie | The token is new on every run. |
| A device that has the link, when you used `--approve` | Get in before you allow it | A new device waits on a page until you answer at the terminal; the app never sees its requests. A refusal is final until restart. |
| A device that has the link | Read anything outside what you shared | The proxy forwards only to the one port. The file server uses `os.Root`, refuses links, special files and hidden names. |
| Someone who sees the link in a screenshot or terminal log | Learn the token from the log | The log redacts it. The address bar and `Referer` lose it after the first redirect. |
| A website open in a browser on the network | Make the browser post into your folder or your app | Uploads check `Origin` and `Sec-Fetch-Site`. The cookie is `SameSite=Lax`. Redirects stay on the same site. |
| A file someone uploaded | Run script on yonder's address, or hide its real type | Served files get `nosniff` and a sandbox policy; names with control or bidi characters are refused. |
| A file name | Escape the folder or overwrite a file | Only the base name is used; `O_EXCL` plus rename never replaces a file; `os.Root` blocks escapes. |
| A slow or abandoned connection | Hold resources forever | Header timeout, idle timeout, a size cap per upload, and cleanup of half-written files. |
| A leaked dev setup | Expose the whole computer | Only one port, file or folder is shared, and hidden files such as `.env` and `.git` never are. |

**Limits of `--for`:** it stops the whole program, so nothing stays exposed after the time is up. `expireAt` counts with a timer and also compares the wall clock every two seconds, because a timer stops while a computer sleeps. It does not make the link single-use during that time.

**Not protected:**

- **The network.** yonder speaks plain HTTP. On open Wi-Fi, or WPA2 Wi-Fi with a shared password, someone who captures traffic can read it and copy the token or cookie. `--open` removes the token altogether.
- **Cookies across ports.** Browsers send cookies to every port of an address. Another web service on the same IP receives `yonder_<port>` cookies.
- **A hostile app.** The proxy forwards whatever your app serves to whoever has the link. It does not make your app safe.
- **A hostile local user.** Someone who can already run programs on your computer can read the shared files directly.

Each control above has a test. [SECURITY.md](SECURITY.md) explains how to report a problem.

## 8. Design decisions

**A reverse proxy instead of asking the dev server to listen on every interface.** Most dev servers listen on `127.0.0.1` only, and many reject requests whose `Host` is not `localhost`. A proxy works with any server without changing its configuration, and it is the single place where the token can be checked.

**The public port is the shared port plus 10000.** The link and any firewall rule survive restarts. The token does not, on purpose.

**A new token on every run.** Restarting is a complete revocation. The cost is scanning the QR code again.

**A cookie after the first visit.** A token in every URL would not work for pages that link to each other, for WebSockets, or for assets the app requests with its own URLs.

**`os.Root` plus refusing every link.** Checking strings such as `..` is easy to get wrong, especially with Windows paths. `os.Root` makes the operating system enforce the boundary. Refusing symbolic links as well closes the case of a link that stays inside the folder but points at a hidden file.

**Temporary file, then rename.** A half-finished upload never appears under its real name, and `O_EXCL` makes "never overwrite" safe even when two uploads race. The cost is a hidden file in the folder while an upload runs.

**Hidden files are decided by one function.** `safeRel` and `visibleName` are used by the file server, the listing, and the start-up check of a single file. A name the server would refuse is never listed and never shared as a link that always gives `404`.

**Only private addresses.** Putting a development site on a public address by accident is worse than an error message, so yonder refuses.

**Standard library first.** One dependency (the QR drawer) keeps the supply chain small and the binary static.

**Distribution from one source.** GitHub Releases is the only place binaries are built. Every other channel (Homebrew, Scoop, apt, dnf, pacman, npm, PyPI, `install.sh`) downloads from it and checks SHA-256 sums.

## 9. Testing

Run everything with `go test ./...`. Tests are written first for new behaviour, and a fix for a bug starts with a test that fails.

| Kind | Where | What it covers |
|---|---|---|
| Unit | most `*_test.go` | Pure functions and handlers, using `httptest` and temporary folders: the gate, header rewriting, listings, name rules, address ranking. |
| In-process | `main_test.go` | `run` with a cancelled context: the banner, exit codes and error messages. |
| End to end | `e2e_test.go` | The compiled binary against real servers: links, cookies, uploads, and Ctrl-C, terminate and hang-up during an upload. |
| Browser script | `upload_script_test.go` | The upload page's inline script, run in Node.js (skipped if Node is missing). |
| Documentation | `doccheck_test.go` | Every Go file has a header comment, and every declaration a doc comment that starts with its name. |

CI (`.github/workflows/ci.yml`) runs, in order and failing fast: dependency checks, then lint and static checks, then the tests on Linux, macOS and Windows (with the race detector on Linux and macOS), then a dry run of the release. Linux must keep total coverage at 85% or more; it is above 90% today.

Lint and static checks: `gofmt`, `go vet` (also for Windows and macOS), `golangci-lint` (with `errcheck`, `staticcheck`, `errorlint`, `bodyclose` and more, see `.golangci.yml`), `govulncheck`, `shellcheck`, `actionlint`, syntax checks of the npm and Python scripts, and `goreleaser check`.

**What tests cannot show:** behaviour on real phones, real firewalls, Windows 8.3 short names, and the networks of other people. The code paths for those are reasoned from documentation and covered by the cross-platform unit tests, but a person should try yonder on a real device after a release.

## 10. Releases and distribution

```
 git tag v0.5.0 ──▶ release.yml
                      │
                      ├─ verify   the ci workflow must have passed for this commit
                      ├─ release  GoReleaser builds archives, .deb, .rpm, Arch packages,
                      │           checksums.txt, and the GitHub release
                      ├─ pypi     wheels built from the release files (trusted publishing)
                      └─ npm      wrapper package (trusted publishing, with provenance)

 GitHub release ◀── downloaded by every channel below, each checking SHA-256 sums
        │
        ├─ install.sh                    curl | sh
        ├─ homebrew-tap   (repository)   updates itself every hour
        ├─ scoop-bucket   (repository)   updates itself every hour
        ├─ packages       (repository)   apt, dnf and pacman repositories on GitHub Pages,
        │                                rebuilt every hour and signed with a GPG key
        └─ winget-pkgs    (Microsoft)    a manual pull request per version
```

- **Version.** A release is a Git tag `vMAJOR.MINOR.PATCH`. GoReleaser stamps it into the binary (`main.version`). `go install` builds read it from the module.
- **File names.** `install.sh`, `npm/install.js`, `pypi/build_wheels.py` and the package repositories download files by name (`yonder_<version>_<os>_<arch>.tar.gz`). `.goreleaser.yaml` pins that name; CI checks that every expected file is produced.
- **No stored secrets in this repository.** PyPI and npm trust the release workflow's identity. The signing key for the apt, dnf and pacman repositories is stored as a secret of the `packages` repository only. The one-time setup (the two trusted publishers and that secret) is done by the repository owner in the PyPI, npm and GitHub settings.
- **Pinned actions.** Every GitHub Action is pinned to a commit hash, and Dependabot proposes updates.

## 11. Where to make a change

| You want to... | Start in |
|---|---|
| Add a command-line option | `parseArgs` and `options` in `main.go`, then the usage text and the README table |
| Change what is allowed in a shared folder | `safeRel` and `visibleName` in `files.go` (they apply to downloads, listings and uploads) |
| Change how a request is authorised | `withAuth` in `auth.go` |
| Change how requests reach a dev server | `newProxy` in `proxy.go` |
| Change the listing page | `listingTmpl` in `files.go` (its script is tested in `upload_script_test.go`) |
| Change the address shown in the QR code | `lanAddrs` and `isVirtual` in `lan.go` |
| Add an install channel | A new job or repository that downloads the release, and a section in this document and the README |

Features that are planned but not built: sharing over the internet and HTTPS on the local network. Each would sit in the handler chain in section 5 and would need its own threat analysis in section 7.
