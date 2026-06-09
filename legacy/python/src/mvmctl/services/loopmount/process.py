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
import datetime
import json
import os
import signal
import subprocess
import sys
import tempfile
from typing import Any, Callable

_LINUX_FS_TYPES = frozenset({"ext2", "ext3", "ext4", "btrfs"})

_DEBUG_LOG_PATH = "/tmp/mvm-provision-debug.log"

_DEFAULT_SHELLS = [
    "/bin/sh",
    "/bin/bash",
    "/bin/dash",
    "/bin/ash",
    "/usr/bin/sh",
    "/usr/bin/bash",
    "/bin/busybox",
    "/usr/bin/busybox",
]


class Provisioner:
    """Loop-mount provisioner for root filesystem images.

    Handles partition detection, mounting, file operations, chroot
    commands, and filesystem resize (ext4/btrfs grow and shrink).
    All operations use subprocess calls to system tools.
    """

    BATCH_SIZE = 10

    def __init__(self, ops: dict[str, Any]) -> None:
        self._ops = ops
        self._image: str = ops["image"]
        self._fs_type_hint: str | None = ops.get("fs_type")
        self._action: str = ops.get("action", "provision")
        self._debug: bool = ops.get("debug", False)

        self._target_fs: str = ops.get("target_fs", "ext4")

        raw_ops: Any = ops.get("operations", {})
        self._operations: dict[str, Any] = (
            raw_ops if isinstance(raw_ops, dict) else {}
        )
        raw_resize: Any = self._operations.get("resize")
        self._resize: dict[str, Any] | None = (
            raw_resize if isinstance(raw_resize, dict) else None
        )

        # Chroot command buffer — accumulated and flushed in batches
        self._chroot_cmd_buffer: list[str] = []

        # Runtime state — set during run()
        self._loop_dev: str | None = None
        self._mount_point: str | None = None
        self._root_part: str = ""
        self._fs_type: str = "ext4"
        self._current_step: str = "init"
        self._resize_new_bytes: int | None = None

    def _debug_log(self, msg: str) -> None:
        if not self._debug:
            return
        ts = datetime.datetime.now(datetime.timezone.utc).isoformat()
        with open(_DEBUG_LOG_PATH, "a") as f:
            f.write(
                f"[{ts}] [PID={os.getpid()}] [step={self._current_step}] {msg}\n"
            )

    def _run_action(
        self, action_method: Callable[[], dict[str, Any]]
    ) -> dict[str, Any]:
        """Execute an action method with cleanup and error wrapping.

        Wraps the call in try/except/finally to return a consistent
        result dict on success or error, always running cleanup.
        """
        try:
            return action_method()
        except Exception as exc:
            return {
                "status": "error",
                "error": str(exc),
                "step": self._current_step,
            }
        finally:
            self._cleanup()

    # ── Public API ──────────────────────────────────────────────────────

    def run(self) -> dict[str, Any]:
        """Execute all provisioning operations.

        Returns a result dict with ``status``, ``files_written``, and
        ``commands_run`` on success, or ``status``, ``error``, and
        ``step`` on failure.

        When ``self._action == "detect_os"``, returns ``status`` and
        ``os_type`` instead.

        When ``self._action == "convert_fs"``, returns ``status``,
        ``new_fs_type``, and ``new_size_bytes`` instead.

        Cleanup (umount, detach loop) always runs via ``finally``.
        """
        self._current_step = "parse"

        # Short-circuit for detect_os action
        if self._action == "detect_os":
            return self._run_action(self.detect_os)

        # Short-circuit for convert_fs action
        if self._action == "convert_fs":
            return self._run_action(lambda: self.convert_fs(self._target_fs))

        files_written = 0
        commands_run = 0
        result = {}

        try:
            # Pre-loop resize: grow (truncate file before mounting)
            if (
                self._resize is not None
                and self._resize.get("action") == "grow"
            ):
                self._current_step = "resize"
                self._truncate_file(self._resize["bytes"])

            if self._debug:
                self._debug_log(
                    f"uid={os.getuid()} gid={os.getgid()} euid={os.geteuid()}"
                )
                self._debug_log(
                    f"image={self._image} fs_type_hint={self._fs_type_hint}"
                )
                try:
                    with open(f"/proc/{os.getpid()}/status") as f:
                        for line in f:
                            if line.startswith("Cap"):
                                self._debug_log(f"cap: {line.strip()}")
                except OSError as exc:
                    self._debug_log(f"cannot read /proc/self/status: {exc}")

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

            # Flush any pending chroot commands before file operations
            self._flush_chroot_buffer()

            # Write files
            for file_op in self._operations.get("files", []):
                self._current_step = "write"
                self._write_file(file_op)
                files_written += 1

            # Flush any pending chroot commands before copy directories
            self._flush_chroot_buffer()

            # Copy directories
            for copy_op in self._operations.get("copy_dirs", []):
                self._current_step = "copy_dir"
                files_written += self._copy_directory(copy_op)

            # Chroot commands (buffered, then flushed in batches)
            for cmd in self._operations.get("commands", []):
                self._current_step = "chroot"
                self._run_chroot_command(cmd)
                commands_run += 1

            # Flush any remaining buffered chroot commands
            self._flush_chroot_buffer()

            # Post-mount resize: shrink
            if (
                self._resize is not None
                and self._resize.get("action") == "shrink"
            ):
                self._current_step = "resize"
                headroom = self._resize.get("headroom", 0)
                self._shrink(headroom)

            # Post-mount resize: grow (ext4 only; btrfs was grown after truncate)
            if (
                self._resize is not None
                and self._resize.get("action") == "grow"
                and self._fs_type != "btrfs"
            ):
                self._current_step = "resize"
                self._unmount()
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

    # ── OS detection ────────────────────────────────────────────────────

    def detect_os(self) -> dict[str, Any]:
        """Detect the OS from the mounted image.

        Mounts the image, reads ``/etc/os-release`` to extract the ``ID``
        field, and returns the OS type. Cleanup happens in the caller's
        ``finally`` block.

        Returns:
            A dict with ``status`` and ``os_type`` on success, or
            ``status``, ``error``, and ``step`` on failure.
        """
        self._current_step = "loop"
        self._setup_loop()

        self._current_step = "partition"
        self._find_root_partition()

        self._current_step = "detect_fs"
        self._detect_fs_type()

        self._current_step = "mount"
        self._mount_point = tempfile.mkdtemp(prefix="mvm-provision-")
        self._mount()

        # Read /etc/os-release
        os_release_path = os.path.join(self._mount_point, "etc", "os-release")
        os_type: str | None = None
        if os.path.exists(os_release_path):
            with open(os_release_path) as f:
                for line in f:
                    if line.startswith("ID="):
                        raw = line.split("=", 1)[1].strip().strip('"')
                        os_type = raw
                        break

        if os_type is None:
            return {
                "status": "ok",
                "os_type": "linux",
                "note": "could not detect specific OS",
            }

        return {"status": "ok", "os_type": os_type}

    # ── Filesystem conversion ───────────────────────────────────────────

    def convert_fs(self, target_fs: str) -> dict[str, Any]:
        """Convert the image filesystem to *target_fs*.

        Mounts the image, calculates the data size, creates a new sparse
        target filesystem populated with all files, then replaces the
        original.  Cleanup runs in the caller's ``finally`` block.

        Args:
            target_fs: Target filesystem type (only ``"ext4"`` is
                currently implemented).

        Returns:
            A dict with ``status``, ``new_fs_type``, and ``new_size_bytes``
            on success, or ``status``, ``error``, and ``step`` on failure.

        Raises:
            ValueError: If *target_fs* is not yet supported.

        """
        if target_fs != "ext4":
            raise ValueError(
                f"Unsupported target filesystem: {target_fs!r}. "
                "Only 'ext4' is supported."
            )

        self._current_step = "loop"
        self._setup_loop()

        self._current_step = "partition"
        self._find_root_partition()

        self._current_step = "detect_fs"
        self._detect_fs_type()

        self._current_step = "mount"
        self._mount_point = tempfile.mkdtemp(prefix="mvm-provision-")
        self._mount()

        # Get actual data size from the mounted filesystem
        self._current_step = "du"
        du_result = subprocess.run(
            ["du", "-sb", self._mount_point],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if du_result.returncode != 0:
            raise RuntimeError(
                f"du failed for {self._mount_point}: {du_result.stderr}"
            )
        data_bytes = int(du_result.stdout.split()[0])

        # Calculate ext4 size: data + 150 MiB buffer, rounded up to next MiB
        # 150 MiB = ext4 journal (128 MiB default) + inode table + block group
        # descriptors + 5 % reserved blocks.  See CONST_ROOTFS_MIN_HEADROOM_BYTES
        # in constants.py for the canonical definition.
        _HEADROOM = 150 * 1024 * 1024
        _MEBI = 1024 * 1024
        size_bytes = data_bytes + _HEADROOM
        size_bytes = ((size_bytes + _MEBI - 1) // _MEBI) * _MEBI
        size_mib = size_bytes // _MEBI

        output_path = self._image + ".ext4"

        # Create sparse output file
        self._current_step = "truncate"
        subprocess.run(
            ["truncate", "-s", f"{size_mib}M", output_path],
            capture_output=True,
            check=True,
            timeout=30,
        )

        # Create ext4 filesystem populated with data from the mount point.
        # capture_output=True prevents mkfs progress output from leaking
        # into the JSON response on stdout.
        self._current_step = "mkfs"
        subprocess.run(
            [
                "mkfs.ext4",
                "-d",
                self._mount_point,
                "-L",
                "rootfs",
                "-F",
                output_path,
            ],
            capture_output=True,
            check=True,
            timeout=300,
        )

        # Cleanup (unmount + detach) before replacing the file
        self._cleanup()

        # Replace original with the new ext4 file
        self._current_step = "replace"
        os.remove(self._image)
        os.rename(output_path, self._image)

        return {
            "status": "ok",
            "new_fs_type": target_fs,
            "new_size_bytes": size_bytes,
        }

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
            ["losetup", "-f", "-P", "--show", "--direct-io=on", self._image],
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
                capture_output=True,
                check=True,
                timeout=15,
            )
        else:
            subprocess.run(
                ["mount", self._root_part, self._mount_point],
                capture_output=True,
                check=True,
                timeout=15,
            )

    def _unmount(self) -> None:
        """Unmount the mount point. Safe to call when not mounted."""
        if self._mount_point is None:
            return
        subprocess.run(
            ["umount", self._mount_point],
            capture_output=True,
            check=False,
            timeout=15,
        )

    # ── File operations ─────────────────────────────────────────────────

    def _write_file(self, file_op: dict[str, Any]) -> None:
        if self._mount_point is None:
            raise RuntimeError("Not mounted")
        path: str = file_op["path"]
        data_b64: str = file_op["data"]
        mode: int = file_op.get("mode", 0o644)
        uid: int = file_op.get("uid", 0)
        gid: int = file_op.get("gid", 0)

        full_path = os.path.join(self._mount_point, path.lstrip("/"))
        self._debug_log(f"write: path={path} full={full_path}")

        # Remove existing path (handles symlinks, sockets, FIFOs, hardlinks)
        if os.path.lexists(full_path):
            try:
                self._debug_log(f"write: removing existing at {path}")
                os.unlink(full_path)
            except OSError as exc:
                self._debug_log(f"write: failed to remove {path}: {exc}")
                raise RuntimeError(f"Cannot remove existing path {path}: {exc}")

        parent = os.path.dirname(full_path)
        os.makedirs(parent, exist_ok=True)

        data = base64.b64decode(data_b64)
        self._debug_log(f"write: writing {len(data)} bytes to {path}")
        with open(full_path, "wb") as f:
            f.write(data)

        # Set permissions (best effort — root in container may lack CAP_CHOWN)
        try:
            os.chmod(full_path, mode)
        except OSError as exc:
            self._debug_log(f"write: chmod failed for {path}: {exc}")
        try:
            os.chown(full_path, uid, gid)
        except OSError as exc:
            self._debug_log(f"write: chown failed for {path}: {exc}")

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
        """Buffer a single chroot command for batched execution.

        Commands are accumulated in ``_chroot_cmd_buffer`` and flushed
        as a single ``chroot`` invocation joined with `` && `` when the
        buffer reaches ``BATCH_SIZE``.
        """
        self._chroot_cmd_buffer.append(command)
        if len(self._chroot_cmd_buffer) >= self.BATCH_SIZE:
            self._flush_chroot_buffer()

    def _flush_chroot_buffer(self) -> None:
        """Execute all buffered chroot commands as a single chroot invocation.

        Joins commands with `` && `` and runs them inside a single
        chroot subprocess.  This is a no-op when the buffer is empty.
        """
        if not self._chroot_cmd_buffer:
            return
        if self._mount_point is None:
            raise RuntimeError("Not mounted")

        command = " && ".join(self._chroot_cmd_buffer)
        count = len(self._chroot_cmd_buffer)
        self._chroot_cmd_buffer.clear()

        # Ensure /dev/null exists in the chroot — missing on some images
        null_path = os.path.join(self._mount_point, "dev", "null")
        if not os.path.exists(null_path):
            dev_dir = os.path.dirname(null_path)
            os.makedirs(dev_dir, exist_ok=True)
            os.mknod(null_path, 0o666, os.makedev(1, 3))

        custom_shell = self._ops.get("shell")
        shells = [custom_shell] if custom_shell else _DEFAULT_SHELLS

        env = os.environ.copy()
        env["PATH"] = (
            "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
        )

        self._debug_log(
            f"chroot: executing batch of {count} commands, "
            f"first={command[:100]}"
        )

        last_error = ""
        for shell in shells:
            shell_in_chroot = os.path.join(self._mount_point, shell.lstrip("/"))
            if not os.path.exists(shell_in_chroot):
                continue

            self._debug_log(f"chroot: trying shell={shell}")
            # busybox needs "sh" as the applet name before "-c"
            if os.path.basename(shell) == "busybox":
                cmd = [
                    "chroot",
                    self._mount_point,
                    shell,
                    "sh",
                    "-c",
                    command,
                ]
            else:
                cmd = ["chroot", self._mount_point, shell, "-c", command]
            proc = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                env=env,
            )
            try:
                stdout, stderr = proc.communicate(timeout=60)
            except subprocess.TimeoutExpired:
                self._debug_log("chroot: timeout after 60s, killing")
                proc.kill()
                proc.wait(timeout=5)
                raise RuntimeError(f"chroot command timed out: {command[:100]}")

            self._debug_log(f"chroot: shell={shell} exit={proc.returncode}")
            if proc.returncode == 0:
                return
            last_error = (
                stderr.decode("utf-8", errors="replace") if stderr else ""
            )

        self._debug_log(
            f"chroot: all shells failed, last_error={last_error[:200]}"
        )
        raise RuntimeError(
            f"chroot failed (no working shell found): {last_error[:500]}"
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
            capture_output=True,
            check=True,
            timeout=120,
        )

    def _run_resize2fs(self) -> None:
        """Grow an ext4 filesystem to fill the available device space."""
        subprocess.run(
            ["resize2fs", self._root_part],
            capture_output=True,
            check=True,
            timeout=120,
        )

    def _run_resize2fs_min(self) -> None:
        """Shrink an ext4 filesystem to its minimum size."""
        subprocess.run(
            ["resize2fs", "-M", self._root_part],
            capture_output=True,
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
            capture_output=True,
            check=True,
            timeout=120,
        )

    def _calc_btrfs_min_size(self) -> int:
        """Calculate minimum file size for a btrfs filesystem.

        Uses ``btrfs filesystem usage`` to determine used space, then
        adds at least 100% headroom for metadata relocation.

        Returns minimum size in bytes (0 if unresolvable).
        """
        assert self._mount_point is not None
        import re as _re

        # Get current device size as upper bound
        dev_result = subprocess.run(
            ["blockdev", "--getsize64", self._root_part],
            capture_output=True,
            text=True,
            timeout=10,
        )
        current_size = 0
        if dev_result.returncode == 0 and dev_result.stdout.strip():
            try:
                current_size = int(dev_result.stdout.strip())
            except ValueError:
                pass

        result = subprocess.run(
            ["btrfs", "filesystem", "usage", "-b", self._mount_point],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode != 0:
            if current_size:
                return current_size
            return 0

        used_bytes = 0
        for line in result.stdout.splitlines():
            stripped = line.strip()
            if stripped.startswith("Used:"):
                m = _re.search(r"[\d.]+", stripped)
                if m:
                    try:
                        used_bytes = int(float(m.group()))
                    except ValueError:
                        pass
                break

        if used_bytes == 0:
            return current_size or 0

        # btrfs needs free space for metadata relocation during shrink.
        # Use generous headroom: used + min(used, 2 GiB) + 1 GiB buffer
        headroom = min(used_bytes, 2 * 1024 * 1024 * 1024) + (
            1024 * 1024 * 1024
        )
        target = used_bytes + headroom

        # Clamp to current size (can't grow during shrink)
        if current_size and target > current_size:
            # Shrink by at most 256 MiB if we can't make meaningful progress
            target = max(
                current_size - (256 * 1024 * 1024),
                used_bytes + (512 * 1024 * 1024),
            )

        return target

    def _shrink_btrfs(self, target_bytes: int) -> None:
        """Resize a btrfs filesystem to a specific size.

        When *target_bytes* is 0, calculates the minimum size via
        ``_calc_btrfs_min_size()`` instead (no kernel-native
        "shrink-to-minimum" for btrfs).
        """
        assert self._mount_point is not None

        # fstrim before shrink to free unused blocks
        subprocess.run(
            ["fstrim", self._mount_point],
            capture_output=True,
            timeout=30,
        )

        if target_bytes == 0:
            target_bytes = self._calc_btrfs_min_size()

        if target_bytes == 0:
            raise RuntimeError("Cannot determine btrfs shrink target size")

        result = subprocess.run(
            [
                "btrfs",
                "filesystem",
                "resize",
                str(target_bytes),
                self._mount_point,
            ],
            capture_output=True,
            timeout=120,
        )
        if result.returncode != 0:
            stderr_text = result.stderr.decode() if result.stderr else ""
            raise RuntimeError(
                f"btrfs filesystem resize to {target_bytes} failed "
                f"(exit {result.returncode}): {stderr_text}"
            )

    def _get_btrfs_device_size(self) -> int:
        """Get the btrfs filesystem device size in bytes from ``btrfs filesystem show``."""
        assert self._mount_point is not None
        import re as _re

        result = subprocess.run(
            ["btrfs", "filesystem", "show", self._mount_point],
            capture_output=True,
            text=True,
            timeout=15,
        )
        if result.returncode != 0:
            return 0
        # Parse line like:   devid    1 size 1.75GiB used 1.32GiB path /dev/loop0
        for line in result.stdout.splitlines():
            if "devid" in line and "size" in line:
                m = _re.search(r"size\s+([\d.]+)([kKmMgGtTbB])", line)
                if m:
                    val = float(m.group(1))
                    unit = m.group(2).lower()
                    multipliers = {
                        "k": 1024,
                        "m": 1024**2,
                        "g": 1024**3,
                        "t": 1024**4,
                        "b": 1,
                    }
                    return int(val * multipliers.get(unit, 1))
        return 0

    def _shrink(self, headroom: int = 0) -> None:
        """Execute the shrink resize operation."""
        assert self._resize is not None
        if self._fs_type == "btrfs":
            self._shrink_btrfs(self._resize["bytes"])
            self._resize_new_bytes = self._get_btrfs_device_size()
        else:
            self._unmount()
            self._run_e2fsck()
            self._run_resize2fs_min()
            if headroom > 0:
                self._run_resize2fs_headroom(headroom)
            self._resize_new_bytes = self._get_fs_byte_size()

    def _run_resize2fs_headroom(self, headroom_bytes: int) -> None:
        """Grow filesystem by headroom bytes after shrinking to minimum."""
        current_size = self._get_fs_byte_size()
        target_size = current_size + headroom_bytes
        subprocess.run(
            ["resize2fs", self._root_part, str(target_size)],
            capture_output=True,
            check=True,
            timeout=120,
        )

    # ── Cleanup ─────────────────────────────────────────────────────────

    @staticmethod
    def _cleanup_mount(mount_point: str) -> bool:
        """Unmount and remove a mount point.

        Fast path: tries ``umount`` first.  If it fails (e.g. orphaned
        gpg-agent from a batched chroot session), scans ``/proc`` for
        processes whose root is inside the mount point, kills them,
        and retries.  The process scan is only on the slow path.
        """
        # Fast path: try umount directly (succeeds ~99% of the time)
        result = subprocess.run(
            ["umount", mount_point],
            capture_output=True,
            check=False,
            timeout=15,
        )
        if result.returncode == 0:
            try:
                os.rmdir(mount_point)
            except OSError:
                pass
            return True

        # Slow path: umount failed — find and kill orphaned processes
        mount_point_real = os.path.realpath(mount_point)
        for entry in os.listdir("/proc"):
            if not entry.isdigit():
                continue
            try:
                root_link = f"/proc/{entry}/root"
                if os.path.islink(root_link):
                    target = os.readlink(root_link)
                    if target == mount_point_real:
                        os.kill(int(entry), signal.SIGKILL)
            except (OSError, ProcessLookupError):
                pass

        # Retry umount after killing orphaned processes
        result = subprocess.run(
            ["umount", mount_point],
            capture_output=True,
            check=False,
            timeout=15,
        )
        unmount_ok = result.returncode == 0
        try:
            os.rmdir(mount_point)
            rmdir_ok = True
        except OSError:
            rmdir_ok = False
        return unmount_ok and rmdir_ok

    def _cleanup(self) -> None:
        """Unmount and detach loop device. Always runs on finally."""
        if self._mount_point is not None:
            self._cleanup_mount(self._mount_point)
            self._mount_point = None
        if self._loop_dev is not None:
            self._detach_loop()
            self._loop_dev = None


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
    parser.add_argument(
        "--umount",
        help="Unmount and remove a mount point, then exit (no JSON input needed)",
    )
    args = parser.parse_args()

    # --umount shortcut: no JSON, no image, just unmount + rmdir
    if args.umount:
        success = Provisioner._cleanup_mount(args.umount)
        return 0 if success else 1

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
