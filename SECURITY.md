# Security policy

yonder shares part of your computer with other devices on a network, so its security matters. Thank you for helping keep it safe.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report it privately through GitHub: <https://github.com/dpologdvinity/yonder/security/advisories/new> ("Report a vulnerability" on the repository's Security tab).

Please include:

- what you found and what an attacker could do with it,
- the yonder version (`yonder --version`), your operating system, and how you installed it,
- the steps or a small program that shows the problem,
- whether you have shared the details with anyone else.

You will get a reply from the maintainer as soon as they can; yonder is maintained by one person, so please be patient. A fix is released first, and then the problem is described publicly in the changelog and a GitHub security advisory, crediting you if you wish. Please give a reasonable time to fix a problem before you publish it.

## Supported versions

Only the latest release receives security fixes. Please update (`brew upgrade`, `pip install -U yonder-cli`, `apt upgrade` and so on) before reporting.

## What yonder is meant to protect

yonder assumes that other devices on your network are **not trusted** and that **web pages you visit can send requests to it**. It is meant to guarantee that:

- a request without the secret token never reaches the shared site, and never reads a shared file;
- only the one port, file or folder you named is reachable, and hidden files (names starting with `.`), symbolic links and anything outside the shared folder are never served;
- an upload cannot write outside the shared folder, cannot overwrite an existing file, and cannot create a hidden file;
- another website cannot make a browser upload files to you;
- an uploaded web page cannot run script on yonder's address;
- the token does not appear in logs or in `Referer` headers;
- with `--approve`, a device that has the link still sees nothing until the owner allows it, and a device the owner refused stays refused for the rest of the run;
- with `--for`, nothing is shared after the time is up.

[ARCHITECTURE.md](ARCHITECTURE.md#7-security-design) explains how each of these is enforced and tested. A way around any of them is a vulnerability.

## What is out of scope

These are known limits, described in the [README](README.md#known-limitations), not vulnerabilities:

- **Traffic is plain HTTP.** On open Wi-Fi, or on WPA2 with a shared password, someone on the network can read it and copy the token. Use a trusted network, or do not share private data.
- **`--open` has no token** by design. Use it only on networks you trust.
- **Cookies are not scoped by port.** Another web service on the same IP address receives yonder's cookie.
- **Your own app's bugs.** yonder forwards what your app serves to whoever has the link.
- **Attacks that need local access** to the computer running yonder.
- **Denial of service by a device that has the link**, beyond the limits yonder already sets.

## Safe use

- Share a dedicated folder, never your home directory.
- Stop yonder (Ctrl-C) when you are done; the link stops working at once.
- Verify downloads: every release has a `checksums.txt`, and the installers check it for you.
- The apt, dnf and pacman repositories are signed. The key fingerprint is `282804EB5779EA7EFFCF8308097AF7F74B2F93E5`.
