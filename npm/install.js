/**
 * Install script of the yonder-cli npm package.
 *
 * Downloads the yonder release binary that matches this package's version and this machine's
 * operating system and CPU, verifies its SHA-256 against the release's checksums.txt, and
 * unpacks it into ./bin. It runs after `npm install`, and again from bin/yonder.js on first
 * use if the install script was skipped (for example with `--ignore-scripts`).
 */
"use strict";
const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");
const { execFileSync } = require("child_process");

const version = require("./package.json").version;
const exe = process.platform === "win32" ? "yonder.exe" : "yonder";
const binPath = path.join(__dirname, "bin", exe);

const osName = { linux: "linux", darwin: "darwin", win32: "windows" }[process.platform];
const arch = { x64: "amd64", arm64: "arm64" }[process.arch];

/**
 * Downloads url and returns its content.
 * @param {string} url
 * @returns {Promise<Buffer>}
 * @throws {Error} when the server answers with an error status.
 */
async function get(url) {
  const res = await fetch(url, { redirect: "follow" });
  if (!res.ok) throw new Error(`GET ${url}: ${res.status}`);
  return Buffer.from(await res.arrayBuffer());
}

/**
 * Makes sure the yonder binary exists in ./bin, downloading and verifying it if not.
 * @returns {Promise<string>} the path of the binary.
 * @throws {Error} on an unsupported platform, a failed download, or a checksum mismatch.
 */
async function install() {
  if (fs.existsSync(binPath)) return binPath;
  if (!osName || !arch) throw new Error(`unsupported platform ${process.platform}/${process.arch}`);
  const file = `yonder_${version}_${osName}_${arch}.${osName === "windows" ? "zip" : "tar.gz"}`;
  const base = `https://github.com/dpologdvinity/yonder/releases/download/v${version}`;
  const [archive, sums] = await Promise.all([get(`${base}/${file}`), get(`${base}/checksums.txt`)]);
  const want = sums.toString().split("\n").map((l) => l.trim().split(/\s+/)).find((p) => p[1] === file)?.[0];
  const got = crypto.createHash("sha256").update(archive).digest("hex");
  if (!want || want !== got) throw new Error(`checksum mismatch for ${file}`);

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "yonder-"));
  try {
    const archivePath = path.join(tmp, file);
    fs.writeFileSync(archivePath, archive);
    // tar ships with macOS, Linux and Windows 10+, and Windows' bsdtar also reads zip.
    // On Windows use the system one: Git Bash's GNU tar cannot read zip and treats "C:" as a host.
    const tar = process.platform === "win32" ? path.join(process.env.SystemRoot || "C:\\Windows", "System32", "tar.exe") : "tar";
    execFileSync(tar, ["-xf", archivePath, "-C", tmp, exe]);
    fs.mkdirSync(path.dirname(binPath), { recursive: true });
    // Copy next to the target, then rename: a killed install must not leave a half-written binary.
    const part = `${binPath}.${process.pid}.part`;
    fs.copyFileSync(path.join(tmp, exe), part);
    fs.chmodSync(part, 0o755);
    fs.renameSync(part, binPath);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
  return binPath;
}

module.exports = { install };

if (require.main === module) {
  install().catch((err) => {
    // Not fatal: bin/yonder.js retries on first run (e.g. after --ignore-scripts).
    console.warn(`yonder: could not download the binary now (${err.message}); it will be fetched on first run.`);
  });
}
