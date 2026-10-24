"""Build one PyPI wheel per platform from a yonder GitHub release.

Each wheel only carries the prebuilt binary as a script, so `pip install yonder-cli`
puts `yonder` on PATH. Usage: python build_wheels.py 0.4.0 [outdir]
"""

import base64
import hashlib
import io
import stat
import sys
import tarfile
import urllib.request
import zipfile
from pathlib import Path

REPO = "dpologdvinity/yonder"
NAME = "yonder_cli"

# release asset suffix -> wheel platform tag. Go binaries are static, so one Linux
# build serves glibc (manylinux) and musl (musllinux). Go 1.27 needs macOS 12+.
PLATFORMS = {
    "linux_amd64.tar.gz": "manylinux_2_17_x86_64.manylinux2014_x86_64.musllinux_1_1_x86_64",
    "linux_arm64.tar.gz": "manylinux_2_17_aarch64.manylinux2014_aarch64.musllinux_1_1_aarch64",
    "darwin_amd64.tar.gz": "macosx_12_0_x86_64",
    "darwin_arm64.tar.gz": "macosx_12_0_arm64",
    "windows_amd64.zip": "win_amd64",
    "windows_arm64.zip": "win_arm64",
}


def fetch(url: str) -> bytes:
    with urllib.request.urlopen(url) as r:
        return r.read()


def binary_from(archive: bytes, suffix: str) -> tuple[str, bytes]:
    if suffix.endswith(".zip"):
        with zipfile.ZipFile(io.BytesIO(archive)) as z:
            return "yonder.exe", z.read("yonder.exe")
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as t:
        f = t.extractfile("yonder")
        if f is None:
            raise SystemExit("yonder binary missing from archive")
        return "yonder", f.read()


def record_line(path: str, data: bytes) -> str:
    digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
    return f"{path},sha256={digest},{len(data)}"


def build(version: str, outdir: Path) -> list[Path]:
    base = f"https://github.com/{REPO}/releases/download/v{version}"
    sums: dict[str, str] = {}
    for line in fetch(f"{base}/checksums.txt").decode().splitlines():
        if line.strip():
            digest, name = line.split()
            sums[name] = digest
    # PyPI shows the README without the repo, so relative image links must become absolute.
    readme = (Path(__file__).resolve().parent.parent / "README.md").read_text().replace(
        "](docs/", f"](https://raw.githubusercontent.com/{REPO}/master/docs/"
    )
    metadata = (
        "Metadata-Version: 2.1\n"
        f"Name: yonder-cli\nVersion: {version}\n"
        "Summary: your site, over yonder: share a localhost site or files with your phone via a QR code\n"
        f"Home-page: https://github.com/{REPO}\nLicense: MIT\nRequires-Python: >=3.8\n"
        "Description-Content-Type: text/markdown\n\n" + readme
    ).encode()

    outdir.mkdir(parents=True, exist_ok=True)
    wheels = []
    for suffix, tag in PLATFORMS.items():
        asset = f"yonder_{version}_{suffix}"
        archive = fetch(f"{base}/{asset}")
        if hashlib.sha256(archive).hexdigest() != sums.get(asset):
            raise SystemExit(f"checksum mismatch for {asset}")
        exe, binary = binary_from(archive, suffix)

        dist = f"{NAME}-{version}.dist-info"
        wheel_meta = f"Wheel-Version: 1.0\nGenerator: yonder build_wheels.py\nRoot-Is-Purelib: false\nTag: py3-none-{tag}\n".encode()
        files = {
            f"{NAME}-{version}.data/scripts/{exe}": binary,
            f"{dist}/METADATA": metadata,
            f"{dist}/WHEEL": wheel_meta,
        }
        record = "\n".join(record_line(p, d) for p, d in files.items()) + f"\n{dist}/RECORD,,\n"

        path = outdir / f"{NAME}-{version}-py3-none-{tag}.whl"
        with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
            for p, d in files.items():
                info = zipfile.ZipInfo(p, date_time=(2026, 1, 1, 0, 0, 0))
                # pip only marks scripts executable when the mode is a regular file with x bits.
                info.external_attr = (stat.S_IFREG | (0o755 if "/scripts/" in p else 0o644)) << 16
                info.compress_type = zipfile.ZIP_DEFLATED
                z.writestr(info, d)
            rec = zipfile.ZipInfo(f"{dist}/RECORD", date_time=(2026, 1, 1, 0, 0, 0))
            rec.external_attr = (stat.S_IFREG | 0o644) << 16
            z.writestr(rec, record)
        wheels.append(path)
        print(path.name)
    return wheels


if __name__ == "__main__":
    if len(sys.argv) < 2:
        raise SystemExit("usage: build_wheels.py VERSION [OUTDIR]")
    build(sys.argv[1].lstrip("v"), Path(sys.argv[2] if len(sys.argv) > 2 else "dist-pypi"))
