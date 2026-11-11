# yonder v0.3 File Sharing (download) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `yonder <path>` serves a file or folder to other devices, behind the same token gate as port sharing.

**Architecture:** New `files.go` with `newFileServer(root *os.Root, only string) http.Handler`. `main.go` decides port vs path and swaps the proxy for the file server; `withAuth`, `withLog` and the conn logger wrap it unchanged.

**Tech Stack:** Go 1.27 standard library (`os.Root`, `html/template`, `http.ServeContent`).

**Spec:** `docs/agents/specs/2026-10-23-yonder-design.md`, section "v0.3: file sharing, download only".

## Global Constraints

- No new dependencies. Every file open goes through `*os.Root`.
- Any URL path segment starting with `.` gives 404 and never appears in a listing.
- Usage, exact: `usage: yonder <port|path> [--listen N] [--open]\n       yonder --version\n`.
- Missing path error, exact: `no such file or folder: <arg>` (printed as `yonder: ...`).
- Default listen port for paths: 18000 with OS fallback.
- Commits: Conventional Commits.

## Review Focus

1. A symlink inside the shared folder pointing to `~` or `/etc` must not be readable. Test in Task 1.
2. `/../`, `/%2e%2e/`, `/sub/../../x` cannot escape. Test in Task 1.
3. A file named `<script>.txt` or `a b&c.txt` lists safely (escaped HTML, working link). Test in Task 1.
4. Single-file mode: sibling files in the same folder give 404. Test in Task 1.
5. `.env` and `.git/config` give 404 even when requested directly. Test in Task 1.

---

### Task 1: `newFileServer`

**Files:** Create `files.go`, `files_test.go`.

**Interfaces:**
- Produces: `func newFileServer(root *os.Root, only string) http.Handler`. `only == ""` serves the whole root; otherwise only the file named `only` at `/<only>`, and `/` lists just it.
- `func humanSize(n int64) string`: `0 B`, `999 B`, `1.0 KB`, `1.5 MB` (1024 base, one decimal above bytes).

- [ ] **Step 1: Failing tests** (`t.TempDir()` fixture: `index.txt`, `sub/b.txt`, `.env`, `.git/config`, `<script>.txt`, `a b&c.txt`, symlink `out -> <another TempDir>/secret.txt`):
  - `TestFilesListing`: `GET /` 200, `Content-Type` starts `text/html`, body contains the viewport meta, `sub/` before `index.txt`, contains `&lt;script&gt;.txt`, contains `href="a%20b&amp;c.txt"`, does not contain `.env` or `.git`.
  - `TestFilesServeFile`: `GET /sub/b.txt` gives the file's bytes; `Range: bytes=0-1` gives 206 with 2 bytes.
  - `TestFilesHidden`: `/.env`, `/.git/config` give 404.
  - `TestFilesNoEscape`: `/../x`, `/%2e%2e/x`, `/sub/../../x`, `/out` all give 404 or 403 and never the secret's bytes.
  - `TestFilesSingle`: `newFileServer(root, "index.txt")`: `/index.txt` 200, `/sub/b.txt` 404, `/` lists only `index.txt`.
  - `TestHumanSize`: table from the Interfaces block.
- [ ] **Step 2:** `go test ./...` FAILS: `undefined: newFileServer`.
- [ ] **Step 3:** Implement. Clean with `path.Clean("/" + r.URL.Path)`, strip the leading `/`, empty means `.`; reject dot segments; `root.Stat`, then directories render the template from `root.Open(dir).ReadDir(-1)`, files use `root.Open` + `http.ServeContent`. Directory URLs without a trailing `/` redirect to add it (relative links depend on it).
- [ ] **Step 4:** `go vet ./... && go test ./...` PASS.
- [ ] **Step 5:** Commit `feat: serve a file or folder with a phone-friendly listing`.

### Task 2: CLI wiring and README

**Files:** Modify `main.go`, `main_test.go`, `e2e_test.go`, `README.md`.

**Interfaces:**
- Consumes: `newFileServer` (Task 1).
- Produces: `options` gains `path string`; `parseArgs` sets `port` when the positional argument is an integer, else `path`.

- [ ] **Step 1: Failing tests:** `TestParseArgs` rows `["./docs"]` gives `options{path: "./docs"}`, `["notes.pdf","--open"]` gives `options{path: "notes.pdf", open: true}`; `"abc"` moves from the bad list to a path row. `TestRunMissingPath`: `run(["/nonexistent/x"])` exits 1, stderr `yonder: no such file or folder: /nonexistent/x\n`. `TestEndToEndFolder`: temp folder with `hello.txt` = `hi`; start the binary on it; share link with a cookie jar, then GET `<base>/hello.txt` returns `hi`.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3:** Implement: path mode resolves `filepath.Abs`, `os.Stat`; folder gives `os.OpenRoot(abs)`, `only = ""`; file gives `os.OpenRoot(filepath.Dir(abs))`, `only = filepath.Base(abs)`; banner `yonder: sharing <abs>`; handler `newFileServer(...)`; `listen(8000, opts.listenPort)` so the default is 18000. Port mode is unchanged. Update `usage`.
- [ ] **Step 4:** PASS.
- [ ] **Step 5:** README: new "Share files" section (`yonder ./folder`, `yonder notes.pdf`, dotfiles hidden, download only, never share `~`). Commit `feat: yonder <path> shares files and folders`.
