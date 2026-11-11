# yonder v0.2 Access Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Only people holding the share link or QR code can reach the dev server through yonder, unless `--open` is given.

**Architecture:** A new `withAuth` middleware in `auth.go` sits between `withLog` and the proxy (`withLog(withAuth(newProxy(...)))`), so rejected requests are still logged. `main.go` generates the token, puts it in the share URL and QR code, and gains an `--open` flag through a new `options` struct.

**Tech Stack:** Go 1.27 standard library only (`crypto/rand`, `crypto/subtle`, `encoding/base64`, `net/http`).

**Spec:** `docs/agents/specs/2026-10-23-yonder-design.md`, section "v0.2: access control".

## Global Constraints

- No new dependencies.
- Query parameter name: `yonder`. Cookie name: `yonder_<listen-port>` (for example `yonder_18000`). Cookie attributes: `Path=/; HttpOnly; SameSite=Lax`, no expiry.
- 403 body, exact: `yonder: this link needs the access token. Scan the QR code from the person sharing.\n` with `Content-Type: text/plain; charset=utf-8`.
- Token comparison only through `crypto/subtle.ConstantTimeCompare`.
- Startup warnings, exact: gated `Only people with this link or QR code can open it.`; `--open` `Anyone on this network can open this URL (--open).`
- Usage, exact: `usage: yonder <port> [--listen N] [--open]\n       yonder --version\n`.
- Commits: Conventional Commits.
- Each task ends with `go vet ./... && go test ./...` green.

## Review Focus

1. Share link with other query parameters (`/?a=1&yonder=T`) redirects to `/?a=1`, not `/` or `/?a=1&`. Test in Task 1.
2. The app's own cookies survive: request with `yonder_18000=T; session=abc` reaches upstream with exactly `session=abc`. Test in Task 1.
3. A WebSocket upgrade without the cookie gets 403 and the upstream handler never runs. Test in Task 1.
4. An old link from a previous run (different token) gets 403 even with a stale cookie. Test in Task 1.
5. A wrong `yonder` parameter on a request that has a valid cookie still passes, and the bad parameter is not forwarded. Test in Task 1.

---

### Task 1: `withAuth` gate

**Files:**
- Create: `auth.go`, `auth_test.go`

**Interfaces:**
- Produces:
  - `func newToken() string`: 16 bytes from `crypto/rand.Read`, `base64.RawURLEncoding`. Panics only if `crypto/rand` fails.
  - `func withAuth(h http.Handler, token string, listenPort int) http.Handler`: implements the spec's gate rules exactly.

- [ ] **Step 1: Write the failing tests** in `auth_test.go`. Use a recording upstream handler (`called bool`, last `*http.Request`) wrapped by `withAuth(up, "T0K", 18000)` and driven with `httptest.NewRecorder()`:
  - `TestNewToken`: two calls differ; each is 22 characters; `base64.RawURLEncoding.DecodeString` gives 16 bytes.
  - `TestAuthNoToken`: `GET /` gives 403, exact body from Global Constraints, upstream not called.
  - `TestAuthWrongToken`: `GET /?yonder=nope` gives 403; cookie `yonder_18000=OLD` also gives 403 (Review Focus 4).
  - `TestAuthQueryTokenRedirects`: table. `GET /?yonder=T0K` gives 302 `Location: /`; `GET /app?a=1&yonder=T0K` gives `Location: /app?a=1`; `HEAD /?yonder=T0K` gives 302. Each response has `Set-Cookie` equal to `yonder_18000=T0K; Path=/; HttpOnly; SameSite=Lax`. Upstream not called (Review Focus 1).
  - `TestAuthQueryTokenPostForwards`: `POST /api?yonder=T0K&x=2` reaches upstream with `URL.RawQuery == "x=2"` and the response sets the cookie.
  - `TestAuthCookiePasses`: cookie `yonder_18000=T0K; session=abc` passes; upstream sees `Cookie` header exactly `session=abc` (Review Focus 2). With only `yonder_18000=T0K`, upstream sees no `Cookie` header.
  - `TestAuthCookieWithBadParam`: cookie valid plus `?yonder=nope&y=1` passes; upstream `RawQuery == "y=1"` (Review Focus 5).
  - `TestAuthUpgradeWithoutCookie`: request with `Connection: Upgrade`, `Upgrade: websocket`, no cookie gives 403, upstream not called (Review Focus 3).
- [ ] **Step 2:** Run `go test ./...`. Expected: FAIL, `undefined: withAuth`.
- [ ] **Step 3:** Implement `newToken` and `withAuth` in `auth.go`. Clone the request (`r.Clone(r.Context())`) before removing the parameter or cookie. Rebuild the `Cookie` header from `r.Cookies()` minus `yonder_<port>`, joined with `"; "`; delete the header if nothing remains. Build `RawQuery` with `url.Values.Del("yonder")` then `Encode()`.
- [ ] **Step 4:** Run `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 5:** Commit `feat: require the share token before proxying` (stage `auth.go auth_test.go`).

### Task 2: Wire the gate into the CLI

**Files:**
- Modify: `main.go`, `main_test.go`, `e2e_test.go`, `README.md`

**Interfaces:**
- Consumes: `newToken() string`, `withAuth(h, token, listenPort)` from Task 1.
- Produces: `type options struct { port, listenPort int; open, showVersion bool }` and `func parseArgs(args []string) (options, error)`, replacing the 4-value return. `--open` is a bool flag accepted before or after the port.

Behavior in `run`: unless `opts.open`, `token := newToken()`, share URL `fmt.Sprintf("http://%s:%d/?yonder=%s", addrs[0], actual, token)` (the QR code encodes it), `also:` lines carry the same `/?yonder=<token>` suffix, handler `withLog(withAuth(newProxy(target, port), token, actual), stdout, time.Now)`. With `opts.open`: URL without token, no `withAuth`. Print the matching warning from Global Constraints instead of the old line.

- [ ] **Step 1: Write the failing tests**
  - Update `TestParseArgs` to the `options` return. Add rows: `["3000","--open"]` and `["--open","3000"]` give `options{port: 3000, open: true}`.
  - `TestRunHelpExitsZero`: stdout now starts with `usage: yonder <port> [--listen N] [--open]`.
  - `TestEndToEnd`: client with `cookiejar.New(nil)`; URL regex `http://[0-9.]+:<port>/\?yonder=[A-Za-z0-9_-]{22}`; GET that URL follows the redirect and gets body `hello`; a second plain `http.Get` of `http://<ip>:<port>/` (no jar) gets 403.
  - `TestEndToEndOpen`: same binary with `--open`; URL regex has no `?yonder=`; plain GET gets `hello`. Share the build step with `TestEndToEnd` through a `buildBinary(t)` helper using `sync.Once`.
- [ ] **Step 2:** Run `go test ./...`. Expected: FAIL (compile error on `parseArgs` return, then e2e regex mismatch).
- [ ] **Step 3:** Implement in `main.go`; update the `usage` constant.
- [ ] **Step 4:** Run `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 5:** README: usage block adds `yonder <port> --open           share without the access token`; example output shows `http://192.168.1.20:13000/?yonder=…` and the gated warning; replace the "Is it safe?" section with: the link and QR code carry a secret token; anyone without it gets a 403 and never reaches your app; a restart makes a new token; `--open` turns it off for trusted networks; on open (passwordless) Wi-Fi someone sniffing traffic could copy the cookie, so use `--open` only on networks you trust and avoid sharing private data on open Wi-Fi.
- [ ] **Step 6:** Manual check: `cd /tmp/demo && python3 -m http.server 8000 --bind 127.0.0.1`, then `go run . 8000`; `curl -s -o /dev/null -w '%{http_code}' http://<lan-ip>:18000/` prints `403`; the printed URL opens in a browser.
- [ ] **Step 7:** Commit `feat: share link carries an access token; --open disables it` (stage `main.go main_test.go e2e_test.go README.md`).
