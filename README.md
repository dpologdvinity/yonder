# yonder

> your site, over yonder

Open the site running on your `localhost` from your phone, your iPad, or a friend's laptop on the same Wi-Fi. One command, one QR code, nothing to install on the other device.

![yonder demo](docs/demo.gif)

## Install

```sh
brew install dpologdvinity/tap/yonder     # macOS and Linux
go install github.com/dpologdvinity/yonder@latest
```

Or download a binary for Linux, macOS or Windows from [Releases](https://github.com/dpologdvinity/yonder/releases).

## Usage

```
yonder <port>             share localhost:<port> on the LAN
yonder <port> --listen N  use proxy port N
yonder --version
```

```
$ yonder 3000
yonder: sharing localhost:3000

  http://192.168.1.20:13000
  <QR code>

Anyone on this network can open this URL. There is no access control yet.
```

Scan the QR code or type the URL on the other device. Each request shows up as one log line.

yonder runs a small proxy in front of your dev server, so you don't need to change its config:

- It works with servers that only listen on `127.0.0.1` or `::1`.
- It rewrites the `Host` and `Origin` headers, so servers that check them (Vite, webpack, Next.js) accept the request.
- Hot reload keeps working: WebSockets and server-sent events pass through.
- The proxy port is your port + 10000, so the URL stays the same across restarts. Just refresh the page.

## Is it safe?

Anyone on the same network can open the URL while yonder runs. There is no password or token yet. Use it on networks you trust, not on public Wi-Fi, and stop it with Ctrl-C when you're done. Only the one port you name is shared.

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
