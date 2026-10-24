# yonder

> your site, over yonder

[![CI](https://github.com/dpologdvinity/yonder/actions/workflows/ci.yml/badge.svg)](https://github.com/dpologdvinity/yonder/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/dpologdvinity/yonder)](https://github.com/dpologdvinity/yonder/releases/latest)
[![npm](https://img.shields.io/npm/v/yonder-cli?label=npm)](https://www.npmjs.com/package/yonder-cli)
[![PyPI](https://img.shields.io/pypi/v/yonder-cli?label=PyPI)](https://pypi.org/project/yonder-cli/)
[![Go reference](https://pkg.go.dev/badge/github.com/dpologdvinity/yonder.svg)](https://pkg.go.dev/github.com/dpologdvinity/yonder)
[![Go version](https://img.shields.io/github/go-mod/go-version/dpologdvinity/yonder)](go.mod)
[![Node.js](https://img.shields.io/node/v/yonder-cli?label=node)](https://www.npmjs.com/package/yonder-cli)
[![Platforms](https://img.shields.io/badge/platforms-Linux%20%7C%20macOS%20%7C%20Windows-informational)](#install)
[![Downloads](https://img.shields.io/github/downloads/dpologdvinity/yonder/total)](https://github.com/dpologdvinity/yonder/releases)
[![Stars](https://img.shields.io/github/stars/dpologdvinity/yonder?style=flat)](https://github.com/dpologdvinity/yonder/stargazers)
[![Last commit](https://img.shields.io/github/last-commit/dpologdvinity/yonder)](https://github.com/dpologdvinity/yonder/commits/master)
[![Open issues](https://img.shields.io/github/issues/dpologdvinity/yonder)](https://github.com/dpologdvinity/yonder/issues)
[![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-yellow)](https://www.conventionalcommits.org/)
[![License: MIT](https://img.shields.io/github/license/dpologdvinity/yonder)](LICENSE)

yonder lets you open a website running on your own computer (`localhost`) from your phone, your iPad, or a friend's laptop on the same network. It can also share a file or a whole folder, and let the other device send files back. One command prints a link and a QR code. Nothing needs to be installed on the other device.

![yonder demo](docs/demo.gif)

**Contents:** [Install](#install) · [Quick start](#quick-start) · [Usage](#usage) · [Share files](#share-files) · [How it works](#how-it-works) · [Is it safe?](#is-it-safe) · [Known limitations](#known-limitations) · [Firewalls](#firewalls) · [WSL](#wsl) · [Documentation](#documentation)

## Install

**macOS / Linux, any distro**
```sh
curl -fsSL https://raw.githubusercontent.com/dpologdvinity/yonder/master/install.sh | sh
```
The script checks the download's SHA-256 before installing. Set `YONDER_VERSION=0.5.0` to install a specific version, or `YONDER_INSTALL_DIR` to choose the folder.

**Homebrew** (macOS and Linux)
```sh
brew install dpologdvinity/tap/yonder
```

**Debian / Ubuntu** (apt)
```sh
sudo curl -fsSLo /usr/share/keyrings/yonder.gpg https://dpologdvinity.github.io/packages/yonder.gpg
echo "deb [signed-by=/usr/share/keyrings/yonder.gpg] https://dpologdvinity.github.io/packages/apt ./" | sudo tee /etc/apt/sources.list.d/yonder.list
sudo apt update && sudo apt install yonder
```

**Fedora / RHEL** (dnf)
```sh
sudo curl -fsSLo /etc/yum.repos.d/yonder.repo https://dpologdvinity.github.io/packages/yonder.repo
sudo dnf install yonder
```

**Arch Linux** (pacman)
```sh
curl -fsSL https://dpologdvinity.github.io/packages/yonder.asc | sudo pacman-key --add -
sudo pacman-key --lsign-key 282804EB5779EA7EFFCF8308097AF7F74B2F93E5
printf '\n[yonder]\nServer = https://dpologdvinity.github.io/packages/arch/$arch\n' | sudo tee -a /etc/pacman.conf
sudo pacman -Sy yonder
```

**Windows** (Scoop)
```powershell
scoop bucket add yonder https://github.com/dpologdvinity/scoop-bucket
scoop install yonder
```

**npm / yarn / pip**
```sh
npm install -g yonder-cli     # or: yarn global add yonder-cli
pip install yonder-cli        # or: uv tool install yonder-cli
```

**Go**
```sh
go install github.com/dpologdvinity/yonder@latest
```

Or download a binary, `.deb`, `.rpm` or Arch package from [Releases](https://github.com/dpologdvinity/yonder/releases). Every release lists the SHA-256 of each file in `checksums.txt`.

## Quick start

1. Start your site as usual, for example on port 3000.
2. Run `yonder 3000`.
3. Scan the QR code with the other device, or type the link it prints into a browser.
4. Press Ctrl-C when you are done. The link stops working at once.

```
$ yonder 3000
yonder: sharing localhost:3000

  http://192.168.1.20:13000/?yonder=…
  <QR code>

Only people with this link or QR code can open it.

14:03:09  device connected  192.168.1.42
14:03:09  GET /  200  192.168.1.42
```

The other device must be on the same network as your computer. If it cannot connect, see [Known limitations](#known-limitations) and [Firewalls](#firewalls).

## Usage

```
yonder <port>               share the site running on localhost:<port>
yonder <path>               share a file or a folder
yonder <folder> --upload    also let other devices send files into the folder
yonder <target> --open      share without the secret token (trusted networks only)
yonder <target> --for 30m   stop sharing after 30 minutes (also 2h, 1d; at most 7d)
yonder <target> --approve   allow each new device yourself, at the terminal
yonder <target> --listen N  use N as the public port
yonder --version            print the version
```

| Option | What it does |
|---|---|
| `--listen N` | Uses port `N` for other devices. By default yonder uses the shared port plus 10000 (`3000` becomes `13000`), or 18000 when sharing files, so the link keeps the same port across restarts. If that port is busy, yonder picks a free one. With `--listen`, a busy port is an error. |
| `--for TIME` | Stops sharing after `TIME`, such as `90s`, `30m`, `2h` or `1d` (more than zero, at most `7d`). yonder prints when the link stops working, and at that time it says so and exits. The end time is checked against the clock on the wall, so a laptop that sleeps does not extend the link. Works with every other option. |
| `--approve` | Asks you at the terminal before each new device is let in. See [Approve each device](#approve-each-device). Needs the token, so it cannot be combined with `--open`. |
| `--open` | Turns the secret token off. Anyone on the network can open the link. |
| `--upload` | Lets other devices send files into a shared folder. Folders only. |
| `--version`, `--help` | Print the version, or the usage text. |

The terminal shows one line when a device first connects, and one line per request with the time, method, path, status and visitor address.

yonder runs a small proxy in front of your dev server, so you do not need to change its configuration:

- It works with servers that only listen on `127.0.0.1` or `::1`.
- It rewrites the `Host` and `Origin` headers, so servers that check them (Vite, webpack, Next.js) accept the request.
- Hot reload keeps working: WebSockets and server-sent events pass through.

## Approve each device

On a network you do not fully trust, the link can leak: someone can photograph the QR code, or read the address over a shoulder. `--approve` adds a second lock. A device that opens the link waits on a page that reloads by itself, and you decide at the terminal:

```
$ yonder 3000 --approve
...
Allow Safari on an iPhone at 192.168.1.42 to open this? [y/N] y
Allowed.
```

- Answer `y` or `yes` to allow. Anything else, including Enter, refuses. A refused device stays refused until yonder restarts.
- A device is a browser, not an address, so two people on the same Wi-Fi are asked about separately. Your own phone's browser is asked once; later visits go straight in.
- At most 10 devices can wait at once, and at most 2 from the same address, so one visitor cannot use up the places. A device whose waiting page is closed gives up its place after a minute.
- Anything you typed before a question appeared is thrown away, so a stray key cannot answer a question you have not seen.
- It needs a terminal to ask on, and yonder must be running in the foreground of it. It does not work with input redirected from a file, or with `yonder ... &`.
- Combine it with `--for` to share for a limited time, with each person approved: `yonder 3000 --for 1h --approve`.

## Share files

```sh
yonder ./photos        # a folder: the phone gets a list of files to tap and download
yonder notes.pdf       # one file: nothing else in its folder is reachable
```

The link, QR code and token work the same as when sharing a port.

- Hidden files and folders (`.env`, `.git`, `.ssh`, anything starting with `.`) are never listed or served.
- Symlinks inside the shared folder are skipped, and `..` cannot reach outside it.
- Folder pages show the first 5000 entries, alphabetically, with folders first. A subfolder page has a `../` link to go back up.
- Share a dedicated folder, not your home directory.
- Add `--upload` to let the other device send files into the folder: the page gets an Upload button (on a phone it opens the photo picker or Files). Existing files are never overwritten (`photo (1).jpg`), folder parts of a name are dropped (`../../x.txt` is saved as `x.txt`), hidden names are refused, and one upload is capped at 4 GB. Uploads are off unless you pass the flag. With uploads on, shared pages cannot run script (see [Is it safe?](#is-it-safe)).
- A folder named like a number is a port to yonder: use `yonder ./3000`.

## How it works

yonder is one program with no dependencies on the other device. It opens a server on all of your network interfaces and puts two things in front of what you share: the access gate and a request log.

```
phone ──▶ your computer, port 13000 ──▶ log ──▶ access gate ──┬──▶ proxy ──▶ your dev server (localhost:3000)
                                                              └──▶ file server ──▶ the shared folder
```

- The **access gate** only lets a request through if it carries the secret token: in the link on the first visit, in a cookie after that.
- The **proxy** forwards the request to your dev server and rewrites headers so the dev server believes the request came from `localhost`.
- The **file server** lists and serves a folder, and receives uploads, using only operating-system calls that cannot leave that folder.

[ARCHITECTURE.md](ARCHITECTURE.md) explains each part, the security design, and why it was built this way.

## Is it safe?

The link and QR code carry a secret token. Anyone without it gets a 403 and never reaches your app. The first visit swaps the token for a cookie, so it disappears from the address bar. Every restart makes a new token, which cuts off old links. Only the one port, file or folder you name is shared.

- `--open` turns the token off. Use it only on networks you trust.
- yonder speaks plain HTTP. On open (passwordless) Wi-Fi, or on WPA2 Wi-Fi with a shared password, others on the network can capture traffic and copy the token or cookie. Avoid sharing private data there. Enterprise Wi-Fi with per-user logins and WPA3 protect each device's traffic.
- With `--upload`, files that are served carry a sandbox policy, so a web page someone uploaded cannot run script on your computer's address. This also means an HTML site shared from an upload-enabled folder will not run its scripts.
- Stop sharing with Ctrl-C, or share for a limited time with `--for`.
- `--approve` makes a leaked link useless on its own: nobody gets in until you say so.

To report a vulnerability, see [SECURITY.md](SECURITY.md).

## Known limitations

- Your app's own code may hardcode `localhost`. A frontend calling an API at `http://localhost:8080` fails on the phone, because there `localhost` is the phone. Use relative API URLs or your dev server's proxy.
- Response bodies are not rewritten. Absolute `http://localhost:<port>` links inside HTML or JS stay broken.
- `http://localhost` counts as a secure context; `http://192.168.x.x` does not. Service workers, camera, geolocation, clipboard and `crypto.subtle` do not work on the other device yet.
- Guest, campus and café networks sometimes block device-to-device traffic. If the other device cannot connect, try a phone hotspot.
- yonder only shares on private networks (`10.x`, `172.16-31.x`, `192.168.x`). Some networks give every computer a public address; there yonder stops with a message instead of putting your site on the internet.
- Cookies do not include the port, so another web service on the same IP address receives yonder's cookie.

## Firewalls

The first time yonder listens for connections, your OS may block or ask:

- **Windows:** allow `yonder.exe` in the Windows Defender Firewall prompt.
- **macOS:** allow incoming connections when the application firewall asks.
- **WSL (mirrored mode):** the Hyper-V firewall blocks inbound traffic to WSL by default, so the other device times out. See the WSL section below.

If the other device cannot connect, watch yonder's output. No `device connected` line means the connection never reached yonder: a firewall or the network dropped it.

## WSL

In WSL's default NAT mode, other devices cannot reach WSL at all; yonder prints a hint when it detects this. Either set `networkingMode=mirrored` under `[wsl2]` in `%UserProfile%\.wslconfig` and run `wsl --shutdown`, or run the Windows build, `yonder.exe`.

In mirrored mode, open yonder's port once in an **admin** PowerShell. For `yonder 8000` the port is 18000:

```powershell
New-NetFirewallHyperVRule -Name yonder -DisplayName yonder -Direction Inbound -VMCreatorId '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -Protocol TCP -LocalPorts 18000
New-NetFirewallRule -DisplayName yonder -Direction Inbound -Protocol TCP -LocalPort 18000 -Action Allow -Profile Any
```

Remove them later with `Remove-NetFirewallHyperVRule -Name yonder` and `Remove-NetFirewallRule -DisplayName yonder`.

## Documentation

| Document | What is in it |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | How yonder is built: components, request flow, security design, decisions, testing and releases |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Set up a development environment, run the checks, and send a change |
| [SECURITY.md](SECURITY.md) | How to report a vulnerability, and what yonder does and does not protect against |
| [CHANGELOG.md](CHANGELOG.md) | What changed in each release |

## License

MIT. See [LICENSE](LICENSE).
