# yonder-cli

Share a website running on `localhost`, or a file or folder, with your phone or another computer on the same network. One command prints a link and a QR code.

```sh
npm install -g yonder-cli     # or: yarn global add yonder-cli
yonder 3000                   # share the site running on localhost:3000
yonder ./photos --upload      # share a folder, and let other devices send files into it
```

This package downloads the matching prebuilt `yonder` binary for your system when it is installed, and checks its SHA-256 against the release's checksum file. Full documentation, security notes and other ways to install: https://github.com/dpologdvinity/yonder
