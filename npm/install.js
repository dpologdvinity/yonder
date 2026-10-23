// Downloads the yonder release binary matching this package's version and platform,
// verifies its SHA-256 against the release checksums, and unpacks it into ./bin.
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

async function get(url) {
  const res = await fetch(url, { redirect: "follow" });
  if (!res.ok) throw new Error(`GET ${url}: ${res.status}`);
  return Buffer.from(await res.arrayBuffer());
}

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
    // tar ships with macOS, Linux and Windows 10+, and Windows' tar also reads zip.
    execFileSync("tar", ["-xf", archivePath, "-C", tmp, exe]);
    fs.mkdirSync(path.dirname(binPath), { recursive: true });
    fs.copyFileSync(path.join(tmp, exe), binPath);
    fs.chmodSync(binPath, 0o755);
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
