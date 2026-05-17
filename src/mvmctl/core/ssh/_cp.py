"""CPService — tar-over-SSH file copy between host and Firecracker microVMs."""

from __future__ import annotations

import logging
import os
import shlex
import subprocess
from collections.abc import Callable

from mvmctl.exceptions import (
    CPDestinationExistsError,
    CPError,
    CPSourceNotFoundError,
)
from mvmctl.utils._system import run_cmd

logger = logging.getLogger(__name__)

_PIPE_CHUNK_SIZE: int = 65536


class CPService:
    """Stateless tar-over-SSH file copy service.

    All methods are static. Copy operations pipe tar archives between
    host and VM(s) using SSH, requiring only ``tar`` (POSIX-mandated)
    on the guest.
    """

    CREATE_FLAGS: list[str] = ["--sparse", "--xattrs", "--acls"]
    EXTRACT_FLAGS: list[str] = [
        "--keep-old-files",
        "--delay-directory-restore",
        "--preserve-permissions",
        "--same-owner",
        "--sparse",
        "--xattrs",
        "--acls",
    ]

    # ── Path parsing ────────────────────────────────────────────────

    @staticmethod
    def _parse_vm_path(path: str) -> tuple[str | None, str]:
        """Split a path into optional ``(vm_identifier, remote_path)``.

        If the path contains ``:``, split at the first occurrence.
        Otherwise return ``(None, path)``.
        """
        if ":" in path:
            idx = path.index(":")
            return (path[:idx], path[idx + 1 :])
        return (None, path)

    # ── Remote path probing ─────────────────────────────────────────

    @staticmethod
    def _probe_remote_path(
        ssh_cmd_prefix: list[str], remote_path: str
    ) -> tuple[str, int]:
        """Probe a remote path and return ``(type, size_in_bytes)``.

        Type is ``"FILE"`` or ``"DIR"``.

        Raises:
            CPSourceNotFoundError: If the remote path does not exist.

        """
        probe_cmd = (
            f"test -f '{remote_path}' && echo FILE && stat -c%s '{remote_path}'"
            f" || (test -d '{remote_path}' && echo DIR"
            f" && du -sb '{remote_path}' | cut -f1)"
            f" || echo NONE"
        )
        cmd = [*ssh_cmd_prefix, probe_cmd]
        result = run_cmd(cmd, capture=True, check=True)
        lines = result.stdout.strip().splitlines()
        if not lines:
            raise CPSourceNotFoundError(
                f"Remote path not found: {remote_path}",
                code="cp.source_not_found",
            )
        path_type = lines[0].strip()
        if path_type == "NONE":
            raise CPSourceNotFoundError(
                f"Remote path not found: {remote_path}",
                code="cp.source_not_found",
            )
        size = int(lines[1].strip()) if len(lines) > 1 else 0
        return (path_type, size)

    # ── Tar command builders ────────────────────────────────────────

    @staticmethod
    def _build_source_tar(path: str, is_directory: bool) -> list[str]:
        """Build the tar create command list for a local path."""
        if is_directory:
            return ["tar", "cf", "-", *CPService.CREATE_FLAGS, "-C", path, "."]
        parent = os.path.dirname(path) or "."
        base = os.path.basename(path)
        return [
            "tar",
            "cf",
            "-",
            *CPService.CREATE_FLAGS,
            "-C",
            parent,
            base,
        ]

    @staticmethod
    def _build_remote_source_tar(path: str, is_directory: bool) -> str:
        """Build the tar create shell command string for a remote path.

        Returns a shell-safe string suitable for passing as a single
        argument to ``ssh <opts> "<command>"``.
        """
        flags = " ".join(shlex.quote(f) for f in CPService.CREATE_FLAGS)
        if is_directory:
            return f"tar cf - {flags} -C {shlex.quote(path)} ."
        parent = os.path.dirname(path) or "."
        base = os.path.basename(path)
        return f"tar cf - {flags} -C {shlex.quote(parent)} {shlex.quote(base)}"

    @staticmethod
    def _build_dest_tar(dst_path: str) -> list[str]:
        """Build the tar extract command list for a local destination."""
        return [
            "tar",
            "xf",
            "-",
            *CPService.EXTRACT_FLAGS,
            "-C",
            dst_path,
        ]

    @staticmethod
    def _build_remote_dest_tar(dst_path: str) -> str:
        """Build the tar extract shell command string for a remote path."""
        flags = " ".join(shlex.quote(f) for f in CPService.EXTRACT_FLAGS)
        return f"tar xf - {flags} -C {shlex.quote(dst_path)}"

    # ── SSH command prefix ──────────────────────────────────────────

    @staticmethod
    def _build_ssh_prefix(
        ip: str, user: str, key_path: str | None
    ) -> list[str]:
        """Build the SSH command prefix list."""
        prefix: list[str] = [
            "ssh",
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "UserKnownHostsFile=/dev/null",
            "-o",
            "BatchMode=yes",
            "-o",
            "ServerAliveInterval=2",
            "-o",
            "ServerAliveCountMax=3",
            "-o",
            "ConnectTimeout=5",
        ]
        if key_path:
            prefix.extend(["-i", key_path])
        prefix.append(f"{user}@{ip}")
        return prefix

    # ── Pipe with progress ──────────────────────────────────────────

    @staticmethod
    def _pipe_with_progress(
        source_cmd: list[str],
        dest_cmd: list[str],
        total_size: int,
        on_progress: Callable[[int], None] | None = None,
    ) -> None:
        """Pipe source stdout into dest stdin, reporting progress.

        Reads source stdout in 64 KiB chunks, writes each chunk to
        dest stdin, and calls ``on_progress(len(chunk))`` when set.

        Raises:
            CPError: If the source process fails.
            CPDestinationExistsError: If the dest process fails (likely
                overwrite rejection).
            CPError: On other dest failures.

        """
        src_proc = subprocess.Popen(source_cmd, stdout=subprocess.PIPE)
        dest_proc = subprocess.Popen(
            dest_cmd, stdin=subprocess.PIPE, stderr=subprocess.PIPE
        )
        if src_proc.stdout is None or dest_proc.stdin is None:
            raise CPError("Failed to set up pipe between processes")

        try:
            while True:
                chunk = src_proc.stdout.read(_PIPE_CHUNK_SIZE)
                if not chunk:
                    break
                dest_proc.stdin.write(chunk)
                if on_progress:
                    on_progress(len(chunk))
        finally:
            dest_proc.stdin.close()
            src_proc.stdout.close()

        src_rc = src_proc.wait()
        dest_rc = dest_proc.wait()
        dest_stderr = (
            dest_proc.stderr.read().decode() if dest_proc.stderr else ""
        )

        if src_rc != 0:
            raise CPError(
                f"Source tar process failed (exit {src_rc})",
                code="cp.source_failed",
            )
        if dest_rc != 0:
            msg = (
                dest_stderr.strip()
                or f"Destination process failed (exit {dest_rc})"
            )
            # The tar --keep-old-files flag exits with 1 on existing files
            if "Cannot open" in msg or "Exists" in msg or "File exists" in msg:
                raise CPDestinationExistsError(
                    f"Destination exists: {msg}",
                    code="cp.destination_exists",
                )
            raise CPError(msg, code="cp.destination_failed")

    # ── Directory size helper ───────────────────────────────────────

    @staticmethod
    def _get_directory_size(path: str) -> int:
        """Approximate total size of a directory by summing file sizes."""
        total = 0
        for dirpath, _dirnames, filenames in os.walk(path):
            for f in filenames:
                fp = os.path.join(dirpath, f)
                try:
                    total += os.path.getsize(fp)
                except OSError:
                    pass
        return total

    # ── Copy directions ─────────────────────────────────────────────

    @staticmethod
    def copy_host_to_vm(
        local_path: str,
        vm_ip: str,
        vm_user: str,
        vm_key_path: str | None,
        remote_dst: str,
        force: bool = False,
        on_progress: Callable[[int], None] | None = None,
    ) -> tuple[int, str]:
        """Copy a file or directory from the host to a VM.

        Returns:
            ``(total_bytes, message)``.

        Raises:
            CPSourceNotFoundError: If the local path does not exist.
            CPDestinationExistsError: If the remote destination exists
                and ``force`` is False (best-effort via tar flags).
            CPError: On other copy failures.

        """
        if not os.path.isfile(local_path) and not os.path.isdir(local_path):
            raise CPSourceNotFoundError(
                f"Local path not found: {local_path}",
                code="cp.source_not_found",
            )

        is_directory = os.path.isdir(local_path)
        total_size: int
        if is_directory:
            total_size = CPService._get_directory_size(local_path)
        else:
            total_size = os.path.getsize(local_path)

        basename = os.path.basename(local_path) or local_path
        ssh_prefix = CPService._build_ssh_prefix(vm_ip, vm_user, vm_key_path)

        # Build the tar pipe
        if on_progress:
            src_cmd = CPService._build_source_tar(local_path, is_directory)
            dest_remote_cmd = CPService._build_remote_dest_tar(remote_dst)
            dest_cmd = [*ssh_prefix, dest_remote_cmd]
            CPService._pipe_with_progress(
                src_cmd, dest_cmd, total_size, on_progress
            )
        else:
            src_tar_str = " ".join(
                shlex.quote(a)
                for a in CPService._build_source_tar(local_path, is_directory)
            )
            dest_tar_str = CPService._build_remote_dest_tar(remote_dst)
            ssh_opts_str = " ".join(shlex.quote(a) for a in ssh_prefix)
            pipe_cmd = f"set -o pipefail && {src_tar_str} | {ssh_opts_str} {shlex.quote(dest_tar_str)}"
            result = run_cmd(
                ["bash", "-c", pipe_cmd], capture=True, check=False
            )
            if result.returncode != 0:
                stderr = (result.stderr or "").strip()
                if stderr and ("Cannot open" in stderr or "Exists" in stderr):
                    raise CPDestinationExistsError(
                        f"Destination exists: {stderr}",
                        code="cp.destination_exists",
                    )
                raise CPError(
                    f"Copy failed (exit {result.returncode}): {stderr}",
                    code="cp.copy_failed",
                )

        logger.info(
            "Copied %s → %s@%s:%s (%d bytes)",
            local_path,
            vm_user,
            vm_ip,
            remote_dst,
            total_size,
        )
        return (total_size, f"Copied {basename} to {vm_ip}:{remote_dst}")

    @staticmethod
    def copy_vm_to_host(
        vm_ip: str,
        vm_user: str,
        vm_key_path: str | None,
        remote_path: str,
        local_dst: str,
        force: bool = False,
        on_progress: Callable[[int], None] | None = None,
    ) -> tuple[int, str]:
        """Copy a file or directory from a VM to the host.

        Returns:
            ``(total_bytes, message)``.

        Raises:
            CPSourceNotFoundError: If the remote path does not exist.
            CPDestinationExistsError: If the local destination exists
                and ``force`` is not set.
            CPError: On other copy failures.

        """
        ssh_prefix = CPService._build_ssh_prefix(vm_ip, vm_user, vm_key_path)

        # Probe remote path to determine type and size
        path_type, total_size = CPService._probe_remote_path(
            ssh_prefix, remote_path
        )
        is_directory = path_type == "DIR"

        # Check local destination
        dst_path_obj = os.path.abspath(local_dst)
        if is_directory:
            # For directories, local_dst is the parent directory
            dst_dir = dst_path_obj
            if not os.path.exists(dst_dir):
                os.makedirs(dst_dir, exist_ok=True)
        else:
            # For files, check if the target file exists
            if os.path.exists(dst_path_obj) and not force:
                raise CPDestinationExistsError(
                    f"Local destination exists: {local_dst}. Use --force to overwrite.",
                    code="cp.destination_exists",
                )
            # Ensure parent directory exists
            parent_dir = os.path.dirname(dst_path_obj) or "."
            if not os.path.exists(parent_dir):
                os.makedirs(parent_dir, exist_ok=True)

        # Build the tar pipe
        basename = os.path.basename(remote_path.rstrip("/"))
        remote_tar_cmd = CPService._build_remote_source_tar(
            remote_path, is_directory
        )

        if on_progress:
            src_cmd = [*ssh_prefix, remote_tar_cmd]
            if is_directory:
                dest_cmd = CPService._build_dest_tar(dst_path_obj)
            else:
                dest_parent = os.path.dirname(dst_path_obj) or "."
                dest_cmd = CPService._build_dest_tar(dest_parent)
            CPService._pipe_with_progress(
                src_cmd, dest_cmd, total_size, on_progress
            )
        else:
            src_ssh_str = " ".join(shlex.quote(a) for a in ssh_prefix)
            dest_tar_str = " ".join(
                shlex.quote(a)
                for a in CPService._build_dest_tar(
                    dst_path_obj
                    if is_directory
                    else (os.path.dirname(dst_path_obj) or ".")
                )
            )
            pipe_cmd = (
                f"set -o pipefail && {src_ssh_str} {shlex.quote(remote_tar_cmd)}"
                f" | {dest_tar_str}"
            )
            result = run_cmd(
                ["bash", "-c", pipe_cmd], capture=True, check=False
            )
            if result.returncode != 0:
                raise CPError(
                    f"Copy failed (exit {result.returncode}): {(result.stderr or '').strip()}",
                    code="cp.copy_failed",
                )

        logger.info(
            "Copied %s@%s:%s → %s (%d bytes)",
            vm_user,
            vm_ip,
            remote_path,
            local_dst,
            total_size,
        )
        return (total_size, f"Copied {basename} from {vm_ip}:{remote_path}")

    @staticmethod
    def copy_vm_to_vm(
        src_ip: str,
        src_user: str,
        src_key_path: str | None,
        src_path: str,
        dst_ip: str,
        dst_user: str,
        dst_key_path: str | None,
        dst_path: str,
        force: bool = False,
        on_progress: Callable[[int], None] | None = None,
    ) -> tuple[int, str]:
        """Copy a file or directory from one VM to another.

        Returns:
            ``(total_bytes, message)``.

        """
        src_ssh_prefix = CPService._build_ssh_prefix(
            src_ip, src_user, src_key_path
        )
        dst_ssh_prefix = CPService._build_ssh_prefix(
            dst_ip, dst_user, dst_key_path
        )

        # Probe source
        path_type, total_size = CPService._probe_remote_path(
            src_ssh_prefix, src_path
        )
        is_directory = path_type == "DIR"

        basename = os.path.basename(src_path.rstrip("/"))
        remote_src_tar = CPService._build_remote_source_tar(
            src_path, is_directory
        )
        remote_dst_tar = CPService._build_remote_dest_tar(dst_path)

        if on_progress:
            src_cmd = [*src_ssh_prefix, remote_src_tar]
            dest_cmd = [*dst_ssh_prefix, remote_dst_tar]
            CPService._pipe_with_progress(
                src_cmd, dest_cmd, total_size, on_progress
            )
        else:
            src_ssh_str = " ".join(shlex.quote(a) for a in src_ssh_prefix)
            dst_ssh_str = " ".join(shlex.quote(a) for a in dst_ssh_prefix)
            pipe_cmd = (
                f"set -o pipefail && {src_ssh_str} {shlex.quote(remote_src_tar)}"
                f" | {dst_ssh_str} {shlex.quote(remote_dst_tar)}"
            )
            result = run_cmd(
                ["bash", "-c", pipe_cmd], capture=True, check=False
            )
            if result.returncode != 0:
                raise CPError(
                    f"Copy failed (exit {result.returncode}): {(result.stderr or '').strip()}",
                    code="cp.copy_failed",
                )

        logger.info(
            "Copied %s@%s:%s → %s@%s:%s (%d bytes)",
            src_user,
            src_ip,
            src_path,
            dst_user,
            dst_ip,
            dst_path,
            total_size,
        )
        return (
            total_size,
            f"Copied {basename} from {src_ip}:{src_path} to {dst_ip}:{dst_path}",
        )
