"""
mvm-provision standalone binary.

Reads JSON operations from stdin, performs loop-mount provisioning
on a root filesystem image, and writes JSON results to stdout.

This is a standalone binary compiled with Nuitka. It uses only stdlib.
No mvmctl imports are allowed.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import signal
import subprocess
import sys
import tempfile
from typing import Any

_LINUX_FS_TYPES = frozenset({"ext2", "ext3", "ext4", "btrfs"})


class Provisioner:
    """Loop-mount provisioner for root filesystem images.

    Handles partition detection, mounting, file operations, chroot
    commands, and filesystem resize (ext4/btrfs grow and shrink).
    All operations use subprocess calls to system tools.
    """

    def __init__(self, ops: dict[str, Any]) -> None:
        self._ops = ops
        self._image: str = ops["image"]
        self._fs_type_hint: str | None = ops.get("fs_type")

        raw_ops: Any = ops.get("operations", {})
        self._operations: dict[str, Any] = (
            raw_ops if isinstance(raw_ops, dict) else {}
        )
        raw_resize: Any = self._operations.get("resize")
        self._resize: dict[str, Any] | None = (
            raw_resize if isinstance(raw_resize, dict) else None
        )

        # Runtime state — set during run()
        self._loop_dev: str | None = None
        self._mount_point: str | None = None
        self._root_part: str = ""
        self._fs_type: str = "ext4"
        self._current_step: str = "init"
        self._resize_new_bytes: int | None = None

    # ── Public API ──────────────────────────────────────────────────────

    def run(self) -> dict[str, Any]:
        """Execute all provisioning operations.

        Returns a result dict with ``status``, ``files_written``, and
        ``commands_run`` on success, or ``status``, ``error``, and
        ``step`` on failure.

        Cleanup (umount, detach loop) always runs via ``finally``.
        """
        self._current_step = "parse"
        files_written = 0
        commands_run = 0
        result: dict[str, Any] = {}

        try:
            # Pre-loop resize: grow (truncate file before mounting)
            if (
                self._resize is not None
                and self._resize.get("action") == "grow"
            ):
                self._current_step = "resize"
                self._truncate_file(self._resize["bytes"])

            # Loop device setup
            self._current_step = "loop"
            self._setup_loop()

            # Root partition detection
            self._current_step = "partition"
            self._find_root_partition()

            # Filesystem type detection
            self._current_step = "detect_fs"
            self._detect_fs_type()

            # Mount
            self._current_step = "mount"
            self._mount_point = tempfile.mkdtemp(prefix="mvm-provision-")
            self._mount()

            # Write files
            for file_op in self._operations.get("files", []):
                self._current_step = "write"
                self._write_file(file_op)
                files_written += 1

            # Copy directories
            for copy_op in self._operations.get("copy_dirs", []):
                self._current_step = "copy_dir"
                files_written += self._copy_directory(copy_op)

            # Chroot commands
            for cmd in self._operations.get("commands", []):
                self._current_step = "chroot"
                self._run_chroot_command(cmd)
                commands_run += 1

            # Post-mount resize: shrink
            if (
                self._resize is not None
                and self._resize.get("action") == "shrink"
            ):
                self._current_step = "resize"
                self._shrink()

            # Post-mount resize: grow (ext4 only; btrfs was grown after truncate)
            if (
                self._resize is not None
                and self._resize.get("action") == "grow"
                and self._fs_type != "btrfs"
            ):
                self._current_step = "resize"
                self._run_e2fsck()
                self._run_resize2fs()

            result = {
                "status": "ok",
                "files_written": files_written,
                "commands_run": commands_run,
            }

        except Exception as exc:
            result = {
                "status": "error",
                "error": str(exc),
                "step": self._current_step,
            }

        finally:
            self._cleanup()

        # Post-detach truncation for shrink
        if self._resize_new_bytes is not None:
            self._truncate_file(self._resize_new_bytes)

        return result

    # ── Signal handling ─────────────────────────────────────────────────

    @staticmethod
    def _signal_handler(signum: int, _frame: object) -> None:
        """Handle SIGTERM/SIGINT by raising SystemExit to trigger finally."""
        sys.exit(1)

    def _setup_signals(self) -> None:
        signal.signal(signal.SIGTERM, self._signal_handler)
        signal.signal(signal.SIGINT, self._signal_handler)

    # ── Loop device management ──────────────────────────────────────────

    def _setup_loop(self) -> None:
        """Set up a loop device with partition scanning."""
        result = subprocess.run(
            ["losetup", "-f", "-P", "--show", self._image],
            capture_output=True,
            text=True,
            check=True,
            timeout=15,
        )
        self._loop_dev = result.stdout.strip()

    def _detach_loop(self) -> None:
        """Detach the loop device. Safe to call when not set up."""
        if self._loop_dev is None:
            return
        subprocess.run(
            ["losetup", "-d", self._loop_dev], check=False, timeout=10
        )

    # ── Partition detection ─────────────────────────────────────────────

    def _list_partitions(self) -> list[str]:
        """List partition devices for the current loop device.

        Checks /dev/loopNp1 through /dev/loopNp16.
        Returns empty list for raw filesystem images (no partition table).
        """
        if self._loop_dev is None:
            return []
        partitions: list[str] = []
        for i in range(1, 17):
            p = f"{self._loop_dev}p{i}"
            if os.path.exists(p):
                partitions.append(p)
        return partitions

    @staticmethod
    def _get_device_size(dev: str) -> int:
        """Get the size of a block device in bytes via blockdev."""
        try:
            result = subprocess.run(
                ["blockdev", "--getsize64", dev],
                capture_output=True,
                text=True,
                timeout=10,
            )
            if result.returncode == 0 and result.stdout.strip():
                return int(result.stdout.strip())
        except (OSError, ValueError, subprocess.TimeoutExpired):
            pass
        return 0

    def _find_root_partition(self) -> None:
        """Find the root partition, matching guestfs logic.

        Scans all partitions for Linux filesystems (ext4, btrfs, xfs).
        Tries p1, p2 in order first. If multiple Linux filesystems
        exist, picks the largest one. Falls back to p1, then to
        the raw loop device for raw filesystem images.
        """
        partitions = self._list_partitions()

        # No partitions — raw filesystem image
        if not partitions:
            self._root_part = self._loop_dev or ""
            return

        # Collect Linux filesystem partitions with size
        linux_parts: list[tuple[str, int]] = []
        for p in partitions:
            fs_type = self._detect_fs_type(p)
            if fs_type in _LINUX_FS_TYPES:
                size = self._get_device_size(p)
                linux_parts.append((p, size))

        if not linux_parts:
            # No Linux filesystem — fall back to p1
            self._root_part = partitions[0]
            return

        # Try p1, then p2 in order
        linux_devices = {dev for dev, _ in linux_parts}
        for candidate in partitions[:2]:
            if candidate in linux_devices:
                self._root_part = candidate
                return

        # Multiple candidates — pick largest by device size
        linux_parts.sort(key=lambda x: x[1], reverse=True)
        self._root_part = linux_parts[0][0]

    # ── Filesystem type detection ───────────────────────────────────────

    def _detect_fs_type(self, dev: str | None = None) -> str:
        """Detect filesystem type using blkid.

        Falls back to 'ext4' if detection fails.
        If a hint was provided in the input JSON, uses that instead.
        """
        if self._fs_type_hint:
            self._fs_type = self._fs_type_hint
            return self._fs_type

        target = dev or self._root_part
        if not target:
            self._fs_type = "ext4"
            return self._fs_type

        try:
            result = subprocess.run(
                ["blkid", "-o", "value", "-s", "TYPE", target],
                capture_output=True,
                text=True,
                timeout=10,
            )
            if result.returncode == 0 and result.stdout.strip():
                self._fs_type = result.stdout.strip()
                return self._fs_type
        except (OSError, subprocess.TimeoutExpired):
            pass

        self._fs_type = "ext4"
        return self._fs_type

    # ── Mount / umount ──────────────────────────────────────────────────

    def _mount(self) -> None:
        """Mount the root partition."""
        if self._mount_point is None:
            raise RuntimeError("Mount point not set")
        if self._fs_type == "btrfs":
            subprocess.run(
                ["mount", "-t", "btrfs", self._root_part, self._mount_point],
                check=True,
                timeout=15,
            )
        else:
            subprocess.run(
                ["mount", self._root_part, self._mount_point],
                check=True,
                timeout=15,
            )

    def _unmount(self) -> None:
        """Unmount the mount point. Safe to call when not mounted."""
        if self._mount_point is None:
            return
        subprocess.run(["umount", self._mount_point], check=False, timeout=15)

    # ── File operations ─────────────────────────────────────────────────

    def _write_file(self, file_op: dict[str, Any]) -> None:
        """Write a single file inside the mount point."""
        if self._mount_point is None:
            raise RuntimeError("Not mounted")
        path: str = file_op["path"]
        data_b64: str = file_op["data"]
        mode: int = file_op.get("mode", 0o644)
        uid: int = file_op.get("uid", 0)
        gid: int = file_op.get("gid", 0)

        full_path = os.path.join(self._mount_point, path.lstrip("/"))
        parent = os.path.dirname(full_path)
        os.makedirs(parent, exist_ok=True)

        data = base64.b64decode(data_b64)
        with open(full_path, "wb") as f:
            f.write(data)
        os.chmod(full_path, mode)
        os.chown(full_path, uid, gid)

    def _copy_directory(self, copy_op: dict[str, Any]) -> int:
        """Copy a directory tree into the mount point.

        Returns the number of files copied.
        """
        if self._mount_point is None:
            raise RuntimeError("Not mounted")
        src: str = copy_op["src"]
        dst: str = copy_op["dst"]
        mode: int = copy_op.get("mode", 0o755)

        count = 0
        for root, _dirs, files in os.walk(src):
            for name in files:
                src_path = os.path.join(root, name)
                rel_path = os.path.relpath(src_path, src)
                dst_path = os.path.join(
                    self._mount_point, dst.lstrip("/"), rel_path
                )

                parent = os.path.dirname(dst_path)
                os.makedirs(parent, exist_ok=True)

                with open(src_path, "rb") as sf:
                    with open(dst_path, "wb") as df:
                        while True:
                            chunk = sf.read(65536)
                            if not chunk:
                                break
                            df.write(chunk)

                try:
                    os.chmod(dst_path, mode)
                except OSError:
                    pass
                count += 1

        return count

    # ── Chroot commands ─────────────────────────────────────────────────

    def _run_chroot_command(self, command: str) -> None:
        """Run a shell command inside a chroot environment."""
        if self._mount_point is None:
            raise RuntimeError("Not mounted")
        subprocess.run(
            ["chroot", self._mount_point, "sh", "-c", command],
            check=True,
            timeout=60,
        )

    # ── Resize helpers ──────────────────────────────────────────────────

    def _truncate_file(self, size_bytes: int) -> None:
        """Truncate (or extend) the image file to the specified size."""
        with open(self._image, "ab") as f:
            f.truncate(size_bytes)

    def _run_e2fsck(self) -> None:
        """Run e2fsck. Required before any resize2fs operation."""
        subprocess.run(
            ["e2fsck", "-f", "-y", self._root_part],
            check=True,
            timeout=120,
        )

    def _run_resize2fs(self) -> None:
        """Grow an ext4 filesystem to fill the available device space."""
        subprocess.run(
            ["resize2fs", self._root_part],
            check=True,
            timeout=120,
        )

    def _run_resize2fs_min(self) -> None:
        """Shrink an ext4 filesystem to its minimum size."""
        subprocess.run(
            ["resize2fs", "-M", self._root_part],
            check=True,
            timeout=120,
        )

    def _get_fs_byte_size(self) -> int:
        """Get the ext4 filesystem size in bytes using tune2fs."""
        result = subprocess.run(
            ["tune2fs", "-l", self._root_part],
            capture_output=True,
            text=True,
            check=True,
            timeout=15,
        )
        block_count = 0
        block_size = 0
        for line in result.stdout.splitlines():
            stripped = line.strip()
            if stripped.startswith("Block count:"):
                block_count = int(stripped.split(":")[1].strip())
            elif stripped.startswith("Block size:"):
                block_size = int(stripped.split(":")[1].strip())
        return block_count * block_size

    def _grow_btrfs(self) -> None:
        """Grow a btrfs filesystem to fill the available device space."""
        assert self._mount_point is not None
        subprocess.run(
            ["btrfs", "filesystem", "resize", "max", self._mount_point],
            check=True,
            timeout=120,
        )

    def _shrink_btrfs(self, target_bytes: int) -> None:
        """Resize a btrfs filesystem to a specific size."""
        assert self._mount_point is not None
        subprocess.run(
            [
                "btrfs",
                "filesystem",
                "resize",
                str(target_bytes),
                self._mount_point,
            ],
            check=True,
            timeout=120,
        )

    def _shrink(self) -> None:
        """Execute the shrink resize operation."""
        assert self._resize is not None
        if self._fs_type == "btrfs":
            self._shrink_btrfs(self._resize["bytes"])
            self._resize_new_bytes = self._resize["bytes"]
        else:
            self._run_e2fsck()
            self._run_resize2fs_min()
            self._resize_new_bytes = self._get_fs_byte_size()

    # ── Cleanup ─────────────────────────────────────────────────────────

    def _cleanup(self) -> None:
        """Unmount and detach loop device. Always runs on finally."""
        if self._mount_point is not None:
            self._unmount()
            try:
                os.rmdir(self._mount_point)
            except OSError:
                pass
        if self._loop_dev is not None:
            self._detach_loop()


# ── Entry point ──────────────────────────────────────────────────────────


def main() -> int:
    """Read JSON from stdin, execute operations, write JSON result to stdout."""
    parser = argparse.ArgumentParser(
        description="mvm-provision — loop-mount rootfs provisioning"
    )
    parser.add_argument(
        "--input-json",
        help="Read JSON from file instead of stdin (for testing)",
    )
    args = parser.parse_args()

    if args.input_json:
        with open(args.input_json) as f:
            raw = f.read()
    else:
        raw = sys.stdin.read()

    try:
        ops = json.loads(raw)
    except json.JSONDecodeError as e:
        result = {
            "status": "error",
            "error": f"Invalid JSON: {e}",
            "step": "parse",
        }
        print(json.dumps(result))
        return 1

    provisioner = Provisioner(ops)
    result = provisioner.run()
    print(json.dumps(result))
    return 0 if result.get("status") == "ok" else 1


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(1))
    signal.signal(signal.SIGINT, lambda *_: sys.exit(1))
    sys.exit(main())
