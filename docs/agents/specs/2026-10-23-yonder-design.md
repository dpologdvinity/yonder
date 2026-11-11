# yonder v1 design

> your site, over yonder

## Purpose

When a site runs on `localhost`, `yonder <port>` makes it reachable from other devices on the same network (phone, iPad, a colleague's laptop in the same room). The tool prints a URL and a terminal QR code. The viewer installs nothing and only needs a browser.

Goal of the project: public release aimed at real users and GitHub stars. That means a one-line install, zero runtime dependencies, and a result that fits in a 10-second terminal GIF.

## Scope

**In v1**
- LAN sharing of one local port, with a reverse proxy.
- Terminal URL and QR code.
- Request log line per request.
- Printed warning that anyone on the network can open the URL.

**Out of v1** (planned later, in this order)
1. Access token, expiry (`--for`), per-device approval (`--approve`). Security hardening.
2. Public mode through a tunnel (`ssh -R` first, `cloudflared` optional). Must ship together with item 1, never without it.
3. HTTPS on the LAN.
4. Self-hosted relay.

No config file, no flags beyond those listed in the CLI section.

## Why a proxy

- Most dev servers listen only on `127.0.0.1`, so a phone on the Wi-Fi cannot reach them. The proxy listens on `0.0.0.0` and forwards to loopback, so the dev server needs no change.
- Some dev servers (Vite `allowedHosts`, webpack `allowedHosts`) reject requests whose `Host` header is not `localhost`. The proxy rewrites it.

## CLI

```
yonder <port>             share localhost:<port> on the LAN
yonder <port> --listen N  use proxy port N
yonder --version
```

Default proxy port is `<port> + 10000` (3000 → 13000, 5173 → 15173), so the URL stays the same across restarts and a phone tab only needs a refresh. If that port is above 65535 or busy, fall back to a free port chosen by the OS. With `--listen N`, a busy port is an error, not a fallback.

Exit codes: `0` on clean shutdown (Ctrl-C), `1` on any startup error.

## Components

Language: Go, one static binary. Dependencies: standard library plus one QR library (`github.com/mdp/qrterminal/v4`).

| Unit | Job | Depends on |
|---|---|---|
| `target` | Find which loopback address accepts a TCP connection on `<port>`: `127.0.0.1` first, then `[::1]`. Return that address; the proxy forwards to it. Node 17+ resolves `localhost` to `::1`, so Vite and other Node servers often listen on IPv6 only. Error if neither answers. | `net` |
| `lan` | Return private IPv4 addresses (`10/8`, `172.16/12`, `192.168/16`) of up-interfaces, best guess first. Best guess is the local address of a UDP "connect" to a public IP (no packet is sent). Skip every address on an interface flagged loopback, not only `127/8`: WSL mirrored mode puts `10.255.255.254` on `lo`. Sort Docker and bridge interfaces (`docker*`, `br-*`, `veth*`) last. | `net` |
| `proxy` | `httputil.ReverseProxy` to the target address. Rewrites `Host` to `localhost:<port>`. Rewrites `Origin` the same way when it equals `http://<host the client used>` (frameworks such as Next.js server actions compare `Origin` with `Host`); any other `Origin` passes unchanged so CSRF checks still work. Does not add `X-Forwarded-Host`, which those frameworks also compare. Rewrites `Location` headers that point at `localhost:<port>` or `127.0.0.1:<port>` to the address the client used. Removes `Domain=localhost` from `Set-Cookie`, because browsers drop that cookie on an IP host. Passes WebSockets through. | `net/http` |
| `wsl` | On WSL (`/proc/sys/kernel/osrelease` contains `microsoft`), run `wslinfo --networking-mode`. If it prints `nat`, print a hint: the LAN cannot reach WSL in NAT mode; set `networkingMode=mirrored` in `%UserProfile%\.wslconfig`, or run the Windows build `yonder.exe`. Any error running `wslinfo` means no hint. | `os`, `os/exec` |
| `main` | Parse args, wire the units, print output, handle Ctrl-C shutdown. | all above |

Request log format, one line per request: `HH:MM:SS  GET /path  200  192.168.1.42`.

## Output

```
yonder: sharing localhost:3000

  http://192.168.1.20:54321
  <QR code>

Anyone on this network can open this URL. There is no access control yet.
```

With several LAN addresses, print the best guess with the QR code and list the others below it.

## Error handling

- Nothing listening on the port: `yonder: nothing is listening on localhost:3000`, exit 1.
- Invalid port argument: usage message, exit 1.
- No LAN address found: `yonder: no network connection found`, exit 1.
- Port given with `--listen` already in use: `yonder: port N is in use`, exit 1.
- Upstream goes away while running: the proxy returns 502 with a short body and keeps running. Requests succeed again when the dev server returns.

## Testing

- `proxy`: `httptest` upstream. Cases: `Host` rewrite, `Location` rewrite, `Origin` rewrite, `Set-Cookie` domain removal, WebSocket echo through the proxy, 502 when upstream is down.
- `lan`: address ordering and filtering from a fake interface list, including a private address on a loopback interface, a `docker0` address, and a full-tunnel VPN (`tun0`) owning the default route. VPN tunnels (`tun`, `utun`, `tap`, `wg`, `ppp`) and VM networks (`vboxnet`, `vmnet`, `bridge`) sort last with Docker and never win best guess.
- `target`: IPv4 open, IPv6-only open, both closed.
- `wsl`: hint shown only for `nat` output.
- Default port: `<port> + 10000`, fallback when busy, fallback when above 65535.
- One end-to-end test: start a dummy server, run the built binary, fetch through the printed URL.

## Release

- GitHub Releases built by goreleaser for Linux, macOS, Windows (amd64 and arm64).
- `go install` supported.
- Homebrew tap after the first tagged release.
- README opens with the slogan, a demo GIF, and a one-line install.

## Known limitations (README must list them)

- The app's own code may hardcode `localhost`. A frontend on `:3000` calling an API at `http://localhost:8080` fails on the phone, because there `localhost` is the phone. v1 does not fix this. Workaround: relative API URLs or a dev-server proxy.
- Response bodies are not rewritten. Absolute `http://localhost:<port>` links inside HTML or JS stay broken.
- `http://localhost` counts as a secure context; `http://192.168.x.x` does not. Service workers, camera, geolocation, clipboard and `crypto.subtle` fail on the phone until HTTPS mode exists.
- Guest, campus and café networks often block device-to-device traffic (client isolation). Nothing on the laptop can fix that.

## Open items

- Confirm the name `yonder` is free on pkg.go.dev, GitHub (command and module path), and domain before the first release. Homebrew formula `yonder` was free on 2026-10-22.
- Firewalls may block or prompt on the first listen on `0.0.0.0`: Windows Defender, macOS application firewall, and the Hyper-V firewall in WSL mirrored mode. Document each in the README.
- Unverified: whether any dev server rejects the rewritten `Origin` on WebSocket connections.

## v0.2: access control (approved in chat 2026-10-23)

Every request must carry a secret token unless `--open` is given.

- **Token:** 16 random bytes from `crypto/rand`, encoded with `base64.RawURLEncoding` (22 characters). New on every run; restarting yonder invalidates old links and cookies.
- **Share URL:** `http://<lan-ip>:<listen-port>/?yonder=<token>`. The QR code encodes this URL.
- **Gate**, in front of the proxy:
  - A request is allowed if its `yonder` query parameter equals the token, or its `yonder_<listen-port>` cookie equals the token. Compare with `crypto/subtle.ConstantTimeCompare`.
  - Allowed via query parameter: set cookie `yonder_<listen-port>=<token>; Path=/; HttpOnly; SameSite=Lax`. For `GET` and `HEAD`, reply `302` to the same path and query with the `yonder` parameter removed. Other methods are forwarded with the parameter removed.
  - Before forwarding, remove the `yonder` query parameter and the `yonder_<listen-port>` cookie, keeping every other parameter and cookie.
  - Not allowed: `403`, `Content-Type: text/plain; charset=utf-8`, body `yonder: this link needs the access token. Scan the QR code from the person sharing.\n`. The upstream is never contacted. WebSocket upgrades follow the same rule.
- **`--open`:** no gate, URL without token.
- **Startup warning:** with the gate, `Only people with this link or QR code can open it.`; with `--open`, `Anyone on this network can open this URL (--open).`
- **Usage:** `usage: yonder <port> [--listen N] [--open]` / `       yonder --version`.
- **Limit:** plain HTTP. On open (unencrypted) Wi-Fi the cookie can be sniffed. README must say so.
- **Not in v0.2:** expiry (`--for`), per-device approval (`--approve`), rate limiting (128-bit token), stable tokens across restarts.

## v0.3: file sharing, download only (2026-10-23)

`yonder <path>` shares a file or folder; `yonder <port>` keeps working as before. Upload is a later, separate design.

- **Argument:** if it parses as an integer, it is a port (1-65535, else usage error). Otherwise it is a path; it must exist (`yonder: no such file or folder: <arg>`, exit 1). A folder literally named like a number is shared with `./3000`.
- **Serving:** no proxy. All file access goes through `os.OpenRoot` on the folder (or the file's parent folder), so `..` and symlinks pointing outside cannot escape.
- **Hidden files:** any path segment starting with `.` is refused with 404 and hidden from listings (protects `.git`, `.env`, `.ssh`).
- **Single file:** only that file is reachable, at `/<name>`; `/` lists just that file; anything else is 404.
- **Listing page:** minimal HTML with `<meta name="viewport" content="width=device-width, initial-scale=1">`, title = shared name, folders first then files, each sorted by name, folders end in `/`, files show size (`12 KB` style). Names are HTML-escaped. Links are URL-escaped.
- **Files:** `http.ServeContent` (range requests, Content-Type by extension, inline display; the phone's browser offers download).
- **Same gate as ports:** token, cookie, `--open`, request log, `device connected` all apply unchanged.
- **Default listen port:** 18000, falling back to an OS-chosen port if busy. `--listen N` works as before.
- **Banner:** `yonder: sharing <absolute path>`.
- **Usage:** `usage: yonder <port|path> [--listen N] [--open]` / `       yonder --version`.

## v0.4: upload (2026-10-23)

- **Opt-in:** `--upload`, only with a folder. With a port or a single file: usage error `--upload needs a folder`.
- **Page:** folder listings get a form (`method=post`, `enctype=multipart/form-data`, `<input type=file name=file multiple>`, button `Upload`). Without `--upload`: no form, and `POST` gives 405.
- **Destination:** the folder whose listing posted the form. The folder path passes the same hidden/symlink/Windows checks as downloads.
- **Names:** only the base name of each part's filename is used (text after the last `/` or `\`). Refused (400, nothing written): empty, `.`/`..`, starting with `.`, containing NUL, or failing the Windows rules (`:`, `~`). Other characters are kept.
- **No overwrite:** create with `O_CREATE|O_EXCL` through `os.Root`; on conflict try `name (1).ext`, `name (2).ext` ... up to `(999)`.
- **Size:** at most 4 GiB per request (`http.MaxBytesReader`). Over the limit: 413, and the partial file is removed. Any write error also removes the partial file.
- **Streaming:** `r.MultipartReader()`; never buffered in memory or temp files.
- **Success:** `303 See Other` back to the folder URL.
- **Banner:** extra line `Uploads are on: files sent from other devices land in <abs>.`
- The token gate still applies; `SameSite=Lax` keeps cross-site form posts out.

## Amendments after review (2026-10-23, v0.4.3)

- Uploads stream to a hidden temp file `.yonder-part-<token>` in the target folder, then claim the final name with `O_EXCL` and rename (supersedes "never temp files"). Shutdown waits up to 3 s for in-flight uploads so temp files are removed.
- Every path segment must be a plain folder or regular file; symlinks (even inside the folder), junctions, FIFOs and devices are refused and unlisted.
- Upload names refuse control and Unicode bidi-control characters; names the filesystem rejects give 400.
- The gate strips every cookie named `yonder_<digits>` before forwarding. Other `yonder_*` names pass through.
- With `--upload`, file responses carry `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`, so an uploaded `.html` cannot run script on yonder's origin (cookies are shared across ports on one IP).
- Usage errors print `yonder: <reason>` before the usage text. `--help` prints usage to stdout, exit 0.
- Virtual interfaces (docker, VPN, VM, Windows friendly names, point-to-point) sort last and never win "best".
- Folder listings show at most 5000 visible entries (hidden files and links do not count); single-file shares never scan the folder.
- Known limit: cookies are not scoped by port, so another service on the same IP can receive `yonder_<port>` cookies.

## v0.5: expiring links and device approval (2026-10-24)

Approved by the owner on 2026-10-24 ("expiring links and approving each new device"). The two options are independent and can be combined.

### `--for DURATION`
- DURATION is a Go duration (`90s`, `30m`, `2h`) or a number of days with `d` (`1d`, `1d12h`). It must be greater than zero and at most `7d`.
- yonder prints `This link works for <duration>, until HH:MM.` in its banner. For a limit of a day or more the end time includes the weekday and date (`Sat 31 Oct 14:30`), and the duration is shown in days (`7d`).
- The deadline is checked against the wall clock every two seconds as well as with a timer, because the timer stops while a computer sleeps.
- When the time is up, WebSocket connections are closed too, since `Server.Close` leaves hijacked connections open.
- When the time is up, yonder prints `The time is up, so yonder is stopping.`, stops accepting requests, waits for running uploads like a normal shutdown, and exits with code 0. (Stopping is safer than answering 403 forever: nothing stays exposed.)
- It works with a port, a path, `--upload`, `--open` and `--approve`.

### `--approve`
- Needs the token, so `--approve` with `--open` is a usage error. It needs a terminal to ask on: if standard input is a file that is not a terminal, startup fails with `--approve needs a terminal to ask you on`.
- Each device is a browser profile with its own **device id** (128 random bits). The first visit with the valid token creates the id in state `pending`, records the visitor's address and `User-Agent`, and sets the cookie `yonder_<port>=<token>.<device id>`. Later requests need the right token **and** an `approved` id.
- Terminal prompt, one device at a time, in arrival order: `Allow <browser> on <os> at <address> to open this? [y/N]`. `y` or `yes` approves; anything else, or end of input, denies.
- While `pending`: `202 Accepted`, `Retry-After: 2`, and a small page that reloads itself every 2 seconds and says that the owner has to approve. While `denied`: `403` with `The owner did not allow this device.` A device id is never reused: denied stays denied until yonder restarts.
- Limits: at most 10 devices can be `pending` at once and at most 2 from one visitor address; more get `429` with `Too many devices are waiting for approval.` A pending device that has not asked again for 60 seconds is forgotten (its id becomes unknown and its question is dropped), so closed tabs and bots do not hold places.
- The waiting page reloads every 2 seconds and those requests are not logged. The question shows the address without its port. Answers are read one byte at a time and the terminal's unread input is discarded before each question. `--approve` also refuses to start when yonder is not in the foreground of its terminal. An id that yonder does not know (for example after a restart) is treated as a first visit and needs the link again.
- The device id is checked before the request moves on and is stripped with the other yonder cookies, so the app never sees it. `isYonderCookie` accepts the same cookie name; only its value changes.
- Approval state lives in memory only and ends with the run, like the token.

### Out of scope
Remembering approvals across runs, revoking one device while running, and approving by address instead of by browser (addresses are shared by NAT).

### Interfaces
- `parseDuration(s string) (time.Duration, error)` in `main.go`; `options.forDur time.Duration`, `options.approve bool`.
- `run(args []string, stdin io.Reader, stdout, stderr io.Writer, ctx context.Context) int` gains `stdin`.
- `approve.go`: `type deviceTable`, `newDeviceTable()`, `(*deviceTable).begin(addr, ua string) (id string, ok bool)`, `.state(id string) (deviceState, bool)`, `.touch(id string)`, `.waiting() int`, `.decide(id string, allow bool)`, `.next(ctx context.Context) *device`, `runPrompts(ctx, in io.Reader, out io.Writer, t *deviceTable)`, `readLine(r io.Reader) string`, `describeUserAgent(ua string) string`. Terminal helpers `flushInput` and `inForeground` live in `tty_*.go`.
- `withAuthDevices(h, token, listenPort, devices *deviceTable)`; `withAuth` is `withAuthDevices` with `nil` devices.

## v0.6: `--public` (share over the internet) (2026-10-24)

Approved by the owner on 2026-10-24. Design settled with an independent Opus review: the Cloudflare quick tunnel, run as an external `cloudflared`, with safe defaults.

- **Usage:** `yonder <target> --public`. Needs `cloudflared` on PATH (`exec.LookPath`); if it is missing yonder exits 1 and prints install commands (`brew install cloudflared`, `winget install --id Cloudflare.cloudflared`, Cloudflare's apt and rpm repositories). yonder never downloads it.
- **Safe defaults:** `--public` with `--open` is a usage error. `--for` defaults to `1h` (an explicit `--for` up to 7d wins). `--approve` is on unless `--no-approve` is given (`--no-approve` is only valid with `--public`). `--approve` needs a terminal, so `--public` without a terminal needs `--no-approve` or fails with the usual message.
- **Two listeners, one handler chain.** The LAN listener works as before. A second listener on `127.0.0.1:<random>` is the tunnel's only target. It serves the same handler chain, the same token and the same device table. `cloudflared tunnel --no-autoupdate --url http://127.0.0.1:<that port>` is started with no token on its command line; the tunnel never points at the app.
- **Header trust is decided by the listener, never by the header.** The tunnel listener takes the visitor's address from `CF-Connecting-IP` (only if it parses as an IP; otherwise the connection's own address is kept) and the country from `CF-IPCountry` (only `AA`-style codes, `XX` or `T1`), and marks the request as secure (https). Both listeners remove `CF-*`, `X-Forwarded-*`, `Forwarded` and `True-Client-IP` headers before anything else runs, so nothing a LAN visitor sends can pretend to come from the tunnel and the app never sees them.
- **Secure requests:** the gate sets `Secure` on the yonder cookie when the request is secure.
- **Per-address limit groups IPv6 by `/64`.** The global limit of 10 waiting devices stays. The approval question shows the country in brackets: `Allow Safari on an iPhone at 203.0.113.9 (US) to open this? [y/N]`.
- **Finding the public address:** read `https://<words>.trycloudflare.com` from `cloudflared`'s output (regular expression `https://[a-z0-9-]+\.trycloudflare\.com`), within 30 seconds, else fail and show the last lines of its output. Quick tunnels are the one mode that needs no account.
- **Banner:** the public link with a QR code, the LAN link, and a warning: `Public on the internet. Cloudflare carries this traffic and can read it. Anyone with the link, anywhere, can ask to get in.` plus the existing lines for `--for` and `--approve`.
- **Lifecycle:** if `cloudflared` exits, yonder stops with `the tunnel stopped` and exit code 1. When yonder stops, it kills `cloudflared`.
- **Known limits (README, SECURITY.md):** Cloudflare terminates TLS and can read traffic and the token; quick tunnels have no server-sent events, a cap of about 200 simultaneous requests, no uptime guarantee, and a 100 MB request body limit on the free plan (large uploads fail); some school and company filters block `trycloudflare.com`.
- **Out of scope:** named tunnels on the user's own Cloudflare domain, other tunnel providers (Tailscale Funnel is the natural second backend), embedding a tunnel client.
- **Test plan:** a fake `cloudflared` (the test binary re-executed) prints a URL and forwards to the loopback listener; unit tests for header stripping, forged `CF-Connecting-IP` on the LAN listener, `/64` grouping, `Secure` cookies and option validation. The owner tries a real phone on mobile data.

## v0.7: `--https` (HTTPS on the local network) (2026-10-24)

Approved by the owner on 2026-10-24. Design from an independent Opus review; not built yet.

- Local certificate authority, opt-in with `--https`. First use creates a root CA and signs one **intermediate CA** with it, then discards the root key (never written to disk). The intermediate (certificate and key) and the public `ca.crt` are kept in `os.UserConfigDir()/yonder/` (directory `0700`, key `0600`).
- The intermediate has `BasicConstraints CA:true, pathLen 0` (critical), `KeyUsage keyCertSign|cRLSign` (critical), `ExtKeyUsage serverAuth`, and **critical** NameConstraints: permitted IP ranges `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `fc00::/7`, and permitted DNS domain `invalid` only (so it cannot sign any real website name).
- Each run makes a fresh ECDSA P-256 leaf key in memory (never written): SAN = the exact IP addresses served as `iPAddress` entries, no CN host name, `ExtKeyUsage serverAuth`, `KeyUsage digitalSignature` (critical), `CA:false`, random 64-bit or longer serial, valid 30 days, `NotBefore` one hour in the past. The full chain (leaf, intermediate) is sent.
- Trust onboarding: a download of `ca.crt` (DER, `application/x-x509-ca-cert`) at a token-protected path, a second QR, the SHA-256 fingerprint printed in the terminal and on the page, and per-system steps for iOS and Android. `yonder ca` prints the path and the fingerprint and can remove the files.
- The yonder cookie gets `Secure`; plain-HTTP requests to the HTTPS port are not served.
- Without the CA installed, phones show the normal certificate warning and can click through (secure context in Chrome; service workers are refused by every browser on a certificate error).
- Risks to document: the CA download can be swapped by an attacker on the same network, so the fingerprint check matters; a stolen intermediate key can intercept private-network traffic of devices that trust it; this adds persistent state to a tool that had none.
