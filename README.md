# yonder

> your site, over yonder

Open the site running on your `localhost` from your phone, your iPad, or a friend's laptop on the same Wi-Fi. One command, one QR code, nothing to install on the other device.

![yonder demo](docs/demo.gif)

## Install

**macOS / Linux, any distro**
```sh
curl -fsSL https://raw.githubusercontent.com/dpologdvinity/yonder/master/install.sh | sh
```

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

**Go**
```sh
go install github.com/dpologdvinity/yonder@latest
```

Or download a binary, `.deb`, `.rpm` or Arch package from [Releases](https://github.com/dpologdvinity/yonder/releases).

## Usage

```
yonder <port>             share localhost:<port> on the LAN
yonder <path>             share a file or folder
yonder <folder> --upload  also let other devices send files into the folder
yonder <port> --listen N  use proxy port N
yonder <port> --open      share without the access token
yonder --version
```

```
$ yonder 3000
yonder: sharing localhost:3000

  http://192.168.1.20:13000/?yonder=…
  <QR code>

Only people with this link or QR code can open it.
```

Scan the QR code or type the URL on the other device. Each request shows up as one log line.

yonder runs a small proxy in front of your dev server, so you don't need to change its config:

- It works with servers that only listen on `127.0.0.1` or `::1`.
- It rewrites the `Host` and `Origin` headers, so servers that check them (Vite, webpack, Next.js) accept the request.
- Hot reload keeps working: WebSockets and server-sent events pass through.
- The proxy port is your port + 10000, so it stays the same across restarts (handy for firewall rules). The access token does not: after a restart, scan the new QR code.

## Share files

```sh
yonder ./photos        # a folder: the phone gets a list of files to tap and download
yonder notes.pdf       # one file: nothing else in its folder is reachable
```

Same QR code and token as port sharing. The default port is 18000.

- Hidden files and folders (`.env`, `.git`, `.ssh`, anything starting with `.`) are never listed or served.
- Symlinks inside the shared folder are skipped, and `..` can't reach outside it.
- Share a dedicated folder, not your home directory.
- Add `--upload` to let the other device send files into the folder: the page gets an Upload button (on a phone it opens the photo picker or Files). Existing files are never overwritten (`photo (1).jpg`), hidden or path-like names are refused, and one upload is capped at 4 GB. Uploads are off unless you pass the flag.
- A folder named like a number is a port to yonder: use `yonder ./3000`.

## Is it safe?

The link and QR code carry a secret token. Anyone without it gets a 403 and never reaches your app. The first visit swaps the token for a cookie, so it disappears from the address bar. Every restart makes a new token, which cuts off old links. Only the one port you name is shared.

- `--open` turns the token off. Use it only on networks you trust.
- yonder speaks plain HTTP. On open (passwordless) Wi-Fi, or on WPA2 Wi-Fi with a shared password, others on the network can capture traffic and copy the token or cookie. Avoid sharing private data there. Enterprise Wi-Fi with per-user logins and WPA3 protect each device's traffic.
- Stop sharing with Ctrl-C.

## Known limitations

- Your app's own code may hardcode `localhost`. A frontend calling an API at `http://localhost:8080` fails on the phone, because there `localhost` is the phone. Use relative API URLs or your dev server's proxy.
- Response bodies are not rewritten. Absolute `http://localhost:<port>` links inside HTML or JS stay broken.
- `http://localhost` counts as a secure context; `http://192.168.x.x` does not. Service workers, camera, geolocation, clipboard and `crypto.subtle` won't work on the other device yet.
- Guest, campus and café networks often block device-to-device traffic. If the other device can't connect, try a phone hotspot.

## Firewalls

The first time yonder listens for connections, your OS may block or ask:

- **Windows:** allow `yonder.exe` in the Windows Defender Firewall prompt.
- **macOS:** allow incoming connections when the application firewall asks.
- **WSL (mirrored mode):** the Hyper-V firewall blocks inbound traffic to WSL by default, so the other device times out. See the WSL section below.

If the other device can't connect, watch yonder's output. No `device connected` line means the connection never reached yonder: a firewall or the network dropped it.

## WSL

In WSL's default NAT mode, other devices can't reach WSL at all; yonder prints a hint when it detects this. Either set `networkingMode=mirrored` under `[wsl2]` in `%UserProfile%\.wslconfig` and run `wsl --shutdown`, or run the Windows build, `yonder.exe`.

In mirrored mode, open yonder's port once in an **admin** PowerShell. For `yonder 8000` the port is 18000:

```powershell
New-NetFirewallHyperVRule -Name yonder -DisplayName yonder -Direction Inbound -VMCreatorId '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -Protocol TCP -LocalPorts 18000
New-NetFirewallRule -DisplayName yonder -Direction Inbound -Protocol TCP -LocalPort 18000 -Action Allow -Profile Any
```

Remove them later with `Remove-NetFirewallHyperVRule -Name yonder` and `Remove-NetFirewallRule -DisplayName yonder`.

## License

MIT
