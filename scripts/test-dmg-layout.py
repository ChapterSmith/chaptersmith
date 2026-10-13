"""Check the actual DMG's Finder auto-open metadata and drag-to-install contents."""

from pathlib import Path
import struct
import subprocess
import sys
import tempfile


def check(image):
    layout = Path(__file__).resolve().parents[1] / "build/darwin/dmg-layout.dsstore"
    with tempfile.TemporaryDirectory(prefix="chaptersmith-dmg-check-") as work:
        root = Path(work)
        raw = root / "uncompressed.dmg"
        subprocess.run(
            ["hdiutil", "convert", str(image), "-format", "UDRO", "-o", str(raw)],
            check=True, stdout=subprocess.DEVNULL,
        )
        with raw.open("rb") as source:
            source.seek(512)
            partition = source.read(512)
            assert partition[:2] == b"PM", "DMG has no HFS partition map for Finder auto-open"
            count = struct.unpack_from(">I", partition, 4)[0]
            assert 0 < count < 128, "Invalid partition count"
            for index in range(1, count + 1):
                source.seek(index * 512)
                partition = source.read(512)
                if partition[48:80].rstrip(b"\0") != b"Apple_HFS":
                    continue
                start = struct.unpack_from(">I", partition, 8)[0]
                source.seek(start * 512 + 1024)
                header = source.read(112)
                assert header[:2] == b"H+", "Expected HFS+ volume"
                open_folder = struct.unpack_from(">I", header, 88)[0]
                assert open_folder == 2, "Finder is not configured to open the volume root"
                break
            else:
                raise AssertionError("No HFS+ volume in DMG")

        mount = root / "mount"
        mount.mkdir()
        subprocess.run(
            ["hdiutil", "attach", str(image), "-readonly", "-nobrowse", "-noautoopen",
             "-mountpoint", str(mount)],
            check=True, stdout=subprocess.DEVNULL,
        )
        try:
            assert (mount / "ChapterSmith.app/Contents/MacOS/chaptersmith-gui").is_file()
            applications = mount / "Applications"
            assert applications.is_symlink() and applications.readlink() == Path("/Applications")
            assert (mount / ".DS_Store").read_bytes() == layout.read_bytes(), "Missing installation window layout"
        finally:
            subprocess.run(["hdiutil", "detach", str(mount)], check=True, stdout=subprocess.DEVNULL)
    print("test-dmg-layout: Finder auto-open and drag-to-install layout OK")


if __name__ == "__main__":
    check(Path(sys.argv[1]).resolve())
