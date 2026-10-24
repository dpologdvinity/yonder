# Changelog

All notable changes to yonder are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `--public` (and `--no-approve`): share over the internet through a Cloudflare quick tunnel, using an installed `cloudflared`. Defaults to `--for 1h` and `--approve`; refuses `--open`. The approval question shows the visitor's address and country. The tunnel points at a separate loopback port, and only that port believes Cloudflare's visitor headers. Over the tunnel, the proxy keeps `https` in same-site `Origin` checks and in redirects. A refused visitor is blocked for the rest of the run.

## [0.5.0] - 2026-10-24

### Added
- `--for TIME` (for example `30m`, `2h`, `1d`; at most `7d`): stop sharing after that time. yonder prints when the link stops working, then says so and exits.
- `--approve`: the owner allows each new device at the terminal before it can open anything. Devices are told apart by browser, wait on a page that reloads itself, and a refusal is final until restart. At most 10 devices can wait at once and 2 per address; a device that stops asking is forgotten after a minute. Typed-ahead input is discarded before each question.

## [0.4.4] - 2026-10-24

### Added
- `ARCHITECTURE.md`, `CONTRIBUTING.md`, `SECURITY.md` and this changelog; a much fuller README.
- A test that fails when any Go file lacks a header comment or any declaration lacks a doc comment; every file and declaration is now documented.
- A full CI pipeline: dependency checks, lint and static analysis, tests with coverage on Linux, macOS and Windows, and a dry run of the release. Coverage must stay at or above 85% on Linux.
- A release gate: publishing a tag now requires that CI passed for the same commit.
- `YONDER_VERSION` for `install.sh`, to install a specific version.
- A README for the npm package, and a clearer message when a download name is not a real release.
- When the network gives the computer a public address, yonder now says so instead of "no network connection found".
- Folder pages inside a subfolder have a `../` link to go back up.

### Changed
- Folder listings keep the alphabetically first entries when a folder is cut at 5000, instead of an arbitrary few, and the page says the folder has more.
- A single file whose name the server would refuse (for example `a\b.txt`, or `report~1.pdf` on Windows) is rejected at start-up instead of producing a link that always gives 404.
- Test coverage rose from 78% to 92%.

### Fixed
- When sharing a site on port 80, redirects to `http://localhost/...` (no port written) are now pointed back at the visitor. An `https://localhost/...` redirect, which means port 443, is left alone.
- The share token no longer appears in the terminal log, even in a malformed query such as `?a;yonder=T`.
- A response that sets its own `Referrer-Policy` can no longer replace yonder's `no-referrer` on requests that carry the token.
- File downloads use the operating system's fast copy again; the request log had been hiding it.
- Closing the terminal (hang-up) now stops yonder cleanly and removes half-finished uploads, like Ctrl-C.
- The upload page's size warning now counts the form overhead, so it no longer lets through uploads the server then rejects.
- The npm package on Windows no longer force-quits yonder on Ctrl-C before it can clean up.
- The PyPI page shows the demo image, and no generated `.pyc` file is tracked.

## [0.4.3] - 2026-10-23

### Fixed
- Uploaded or shared web pages can no longer run script on yonder's address when uploads are on: served files now carry a sandbox policy.
- Stopping yonder during an upload now waits for the upload's cleanup, so no hidden temporary file remains.
- The npm wrapper forwards termination signals, so stopping it also stops the share.
- On Windows, VPN and VM adapters with friendly names ("OpenVPN TAP-Windows6", "vEthernet") no longer end up in the QR code.
- Named pipes, devices and junctions in a shared folder are skipped instead of blocking a request.
- Usage errors now say why, for example `--upload needs a folder`.
- `install.sh` does nothing if its download is cut short.
- The CI token is read-only.

## [0.4.2] - 2026-10-23

### Fixed
- The token no longer appears in the log line of its first request.
- A crafted link can no longer redirect a visitor to another website.
- Cookies of other yonder instances are removed before a request reaches the app, but an app cookie such as `yonder_theme` is kept.
- Uploads: names the file system refuses give 400 instead of 500; names with control or text-direction characters are refused; the upload page warns before a too-large upload.
- A single-file share refuses uploads.
- Listings are named after the folder and capped at 5000 entries, and hidden files do not count toward the cap.

## [0.4.1] - 2026-10-23

### Added
- Install with `curl | sh`, npm and yarn (`yonder-cli`), pip (`yonder-cli`), Arch Linux (pacman), and Scoop.
- Signed apt, dnf and pacman repositories, and a Homebrew tap that updates itself.

## [0.4.0] - 2026-10-23

### Added
- `--upload`: let other devices send files into a shared folder. Existing files are never overwritten, uploads stream through a hidden temporary file, there is a 4 GB limit per upload, and pages from other websites cannot post into the folder.
- `.deb` and `.rpm` packages on every release.

## [0.3.0] - 2026-10-23

### Added
- Share files: `yonder ./folder` and `yonder file.pdf`, with a phone-friendly listing, range downloads, and the same token as port sharing.
- Hidden files, symbolic links and anything outside the folder are never served.

### Fixed
- The shared folder is closed on exit, which Windows requires.

## [0.2.0] - 2026-10-23

### Added
- Access control: the link and QR code carry a secret token, a first visit sets a cookie, and everything else gets 403. `--open` turns it off.
- A "device connected" line the first time each device reaches yonder.

### Fixed
- An app's own cookies pass through byte for byte, including values with quotes or non-ASCII text.

## [0.1.1] - 2026-10-23

### Fixed
- `yonder 3000 --version` prints the version; `--help` exits with code 0.
- Redirects with an uppercase or scheme-relative host are pointed back at the visitor.
- A stream that is aborted still gets a log line.
- Slow connections are cut off by a header timeout.

## [0.1.0] - 2026-10-23

### Added
- First release: `yonder <port>` shares a local site on the network with a link and QR code. A reverse proxy rewrites `Host`, `Origin`, redirects and cookies, passes WebSockets and event streams through, and works with servers that listen only on IPv6.
- A stable default port (shared port plus 10000), a request log, and a hint for WSL in NAT mode.

[Unreleased]: https://github.com/dpologdvinity/yonder/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/dpologdvinity/yonder/compare/v0.4.4...v0.5.0
[0.4.4]: https://github.com/dpologdvinity/yonder/compare/v0.4.3...v0.4.4
[0.4.3]: https://github.com/dpologdvinity/yonder/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/dpologdvinity/yonder/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/dpologdvinity/yonder/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/dpologdvinity/yonder/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/dpologdvinity/yonder/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/dpologdvinity/yonder/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/dpologdvinity/yonder/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/dpologdvinity/yonder/releases/tag/v0.1.0
