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

# GNU tar extras — only used when GNU tar is detected on the remote end
_GNU_EXTRACT_EXTRAS: list[str] = [
    "-p",
    "--same-owner",
    "--delay-directory-restore",
]
_GNU_CREATE_EXTRAS: list[str] = ["--xattrs", "--acls"]

# Tar capability cache: "user@host" → is_gnu: bool, "local" → host tar
_tar_cache: dict[str, bool] = {}


class CPService:
    """Stateless tar-over-SSH file copy service.

    All methods are static. Copy operations pipe tar archives between
    host and VM(s) using SSH, requiring only ``tar`` (POSIX-mandated)
    on the guest.
    """

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

    # ── Tar capability probing ─────────────────────────────────────

    @staticmethod
    def _probe_remote_tar(ssh_prefix: list[str]) -> bool:
        """Probe remote tar and return True if it's GNU tar.

        Results are cached per host to avoid repeated SSH calls.
        """
        target = ssh_prefix[-1] if ssh_prefix else "unknown"
        if target in _tar_cache:
            return _tar_cache[target]
        try:
            probe_cmd = "tar --version 2>/dev/null | head -1"
            cmd = [*ssh_prefix, probe_cmd]
            result = run_cmd(cmd, capture=True, check=True)
            is_gnu = "GNU tar" in result.stdout
        except Exception:
            is_gnu = False
        _tar_cache[target] = is_gnu
        return is_gnu

    @staticmethod
    def _is_local_tar_gnu() -> bool:
        """Check if local tar is GNU tar. Result cached."""
        if "local" in _tar_cache:
            return _tar_cache["local"]
        try:
            result = run_cmd(["tar", "--version"], capture=True, check=True)
            is_gnu = "GNU tar" in result.stdout
        except Exception:
            is_gnu = False
        _tar_cache["local"] = is_gnu
        return is_gnu

    # ── Tar command builders ────────────────────────────────────────

    @staticmethod
    def _build_source_tar(
        path: str, is_directory: bool, gnu_extras: bool = False
    ) -> list[str]:
        """Build the tar create command list for a local path."""
        extra = _GNU_CREATE_EXTRAS if gnu_extras else []
        if is_directory:
            return ["tar", "cf", "-", *extra, "-C", path, "."]
        parent = os.path.dirname(path) or "."
        base = os.path.basename(path)
        return [
            "tar",
            "cf",
            "-",
            *extra,
            "-C",
            parent,
            base,
        ]

    @staticmethod
    def _build_remote_source_tar(
        path: str, is_directory: bool, gnu_extras: bool = False
    ) -> str:
        """Build the tar create shell command string for a remote path.

        Returns a shell-safe string suitable for passing as a single
        argument to ``ssh <opts> "<command>"``.
        """
        extra_flags = " ".join(
            shlex.quote(f) for f in (_GNU_CREATE_EXTRAS if gnu_extras else [])
        )
        if is_directory:
            return f"tar cf - {extra_flags} -C {shlex.quote(path)} ."
        parent = os.path.dirname(path) or "."
        base = os.path.basename(path)
        return f"tar cf - {extra_flags} -C {shlex.quote(parent)} {shlex.quote(base)}"

    @staticmethod
    def _build_multi_source_tar(
        paths: list[str], gnu_extras: bool = False
    ) -> list[str]:
        """Build tar create command for multiple local paths.

        Uses repeated ``-C <parent> <base>`` pairs so each file keeps
        only its basename in the archive (no full path structure).
        Both GNU tar and BusyBox tar support multiple ``-C`` options.
        """
        extra = _GNU_CREATE_EXTRAS if gnu_extras else []
        cmd: list[str] = ["tar", "cf", "-", *extra]
        for path in paths:
            parent = os.path.dirname(path) or "."
            base = os.path.basename(path)
            cmd.extend(["-C", parent, base])
        return cmd

    @staticmethod
    def _build_dest_tar(
        dst_path: str, gnu_extras: bool = False, *, no_overwrite: bool = True
    ) -> list[str]:
        """Build the tar extract command list for a local destination.

        When ``no_overwrite=True`` (default), the ``-k`` (keep-old-files)
        flag is included. Pass ``no_overwrite=False`` to allow overwriting.
        """
        flags: list[str] = ["-k"] if no_overwrite else []
        extra: list[str] = _GNU_EXTRACT_EXTRAS if gnu_extras else []
        # When extracting as non-root, suppress ownership-change warnings
        # from tar (SSH copies files owned by root, and local archive
        # extraction cannot chown to root without privileges).
        extra.append("--no-same-owner")
        return ["tar", "xf", "-", *flags, *extra, "-C", dst_path]

    @staticmethod
    def _build_remote_dest_tar(
        dst_path: str, gnu_extras: bool = False, *, no_overwrite: bool = True
    ) -> str:
        """Build the tar extract shell command string for a remote path.

        When ``no_overwrite=True`` (default), the ``-k`` (keep-old-files)
        flag is included. Pass ``no_overwrite=False`` to allow overwriting.

        ``dst_path`` must be a **directory** (include trailing ``/`` or
        use an explicit directory path).  The tar archive will be extracted
        into that directory.
        """
        parts: list[str] = []
        if no_overwrite:
            parts.append("-k")
        elif gnu_extras:
            # GNU tar supports --overwrite; BusyBox tar (Alpine, etc.)
            # does not, but it also does not need it — it only needs
            # the absence of -k.
            parts.append("--overwrite")
        if gnu_extras:
            parts.extend(_GNU_EXTRACT_EXTRAS)
        parts.extend(["-C", shlex.quote(dst_path)])
        return f"tar xf - {' '.join(parts)}"

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
        local_paths: list[str],
        vm_ip: str,
        vm_user: str,
        vm_key_path: str | None,
        remote_dst: str,
        force: bool = False,
        on_progress: Callable[[int], None] | None = None,
    ) -> tuple[int, str]:
        """Copy files or directories from the host to a VM.

        Accepts one or more local paths. Multi-source copies archive
        each file with only its basename (parent-directory stripped).

        Returns:
            ``(total_bytes, message)``.

        Raises:
            CPSourceNotFoundError: If any local path does not exist.
            CPDestinationExistsError: If the remote destination exists
                and ``force`` is False (best-effort via tar flags).
            CPError: On other copy failures.

        """
        # Validate all paths exist
        for p in local_paths:
            if not os.path.isfile(p) and not os.path.isdir(p):
                raise CPSourceNotFoundError(
                    f"Local path not found: {p}",
                    code="cp.source_not_found",
                )

        ssh_prefix = CPService._build_ssh_prefix(vm_ip, vm_user, vm_key_path)
        remote_gnu = CPService._probe_remote_tar(ssh_prefix)
        local_gnu = CPService._is_local_tar_gnu()

        is_multi = len(local_paths) > 1

        if is_multi:
            # ── Multi-source copy ────────────────────────────────
            total_size = 0
            for p in local_paths:
                if os.path.isdir(p):
                    total_size += CPService._get_directory_size(p)
                else:
                    total_size += os.path.getsize(p)

            if on_progress:
                src_cmd = CPService._build_multi_source_tar(
                    local_paths, gnu_extras=local_gnu
                )
                dest_remote_cmd = CPService._build_remote_dest_tar(
                    remote_dst, gnu_extras=remote_gnu, no_overwrite=not force
                )
                dest_cmd = [*ssh_prefix, dest_remote_cmd]
                CPService._pipe_with_progress(
                    src_cmd, dest_cmd, total_size, on_progress
                )
            else:
                src_tar_str = " ".join(
                    shlex.quote(a)
                    for a in CPService._build_multi_source_tar(
                        local_paths, gnu_extras=local_gnu
                    )
                )
                dest_tar_str = CPService._build_remote_dest_tar(
                    remote_dst, gnu_extras=remote_gnu, no_overwrite=not force
                )
                ssh_opts_str = " ".join(shlex.quote(a) for a in ssh_prefix)
                pipe_cmd = (
                    f"set -o pipefail && {src_tar_str}"
                    f" | {ssh_opts_str} {shlex.quote(dest_tar_str)}"
                )
                result = run_cmd(
                    ["bash", "-c", pipe_cmd], capture=True, check=False
                )
                if result.returncode != 0:
                    stderr = (result.stderr or "").strip()
                    if stderr and (
                        "Cannot open" in stderr or "Exists" in stderr
                    ):
                        raise CPDestinationExistsError(
                            f"Destination exists: {stderr}",
                            code="cp.destination_exists",
                        )
                    raise CPError(
                        f"Copy failed (exit {result.returncode}): {stderr}",
                        code="cp.copy_failed",
                    )

            logger.info(
                "Copied %d items → %s@%s:%s (%d bytes)",
                len(local_paths),
                vm_user,
                vm_ip,
                remote_dst,
                total_size,
            )
            return (
                total_size,
                f"Copied {len(local_paths)} items to {vm_ip}:{remote_dst}",
            )

        # ── Single-source copy (existing behavior) ────────────────
        local_path = local_paths[0]
        is_directory = os.path.isdir(local_path)
        total_size = (
            CPService._get_directory_size(local_path)
            if is_directory
            else os.path.getsize(local_path)
        )
        basename = os.path.basename(local_path) or local_path

        if on_progress:
            src_cmd = CPService._build_source_tar(
                local_path, is_directory, gnu_extras=local_gnu
            )
            dest_remote_cmd = CPService._build_remote_dest_tar(
                remote_dst, gnu_extras=remote_gnu, no_overwrite=not force
            )
            dest_cmd = [*ssh_prefix, dest_remote_cmd]
            CPService._pipe_with_progress(
                src_cmd, dest_cmd, total_size, on_progress
            )
        else:
            src_tar_str = " ".join(
                shlex.quote(a)
                for a in CPService._build_source_tar(
                    local_path, is_directory, gnu_extras=local_gnu
                )
            )
            dest_tar_str = CPService._build_remote_dest_tar(
                remote_dst, gnu_extras=remote_gnu, no_overwrite=not force
            )
            ssh_opts_str = " ".join(shlex.quote(a) for a in ssh_prefix)
            pipe_cmd = (
                f"set -o pipefail && {src_tar_str}"
                f" | {ssh_opts_str} {shlex.quote(dest_tar_str)}"
            )
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
        remote_gnu = CPService._probe_remote_tar(ssh_prefix)
        local_gnu = CPService._is_local_tar_gnu()

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
            remote_path, is_directory, gnu_extras=remote_gnu
        )

        if on_progress:
            src_cmd = [*ssh_prefix, remote_tar_cmd]
            if is_directory:
                dest_cmd = CPService._build_dest_tar(
                    dst_path_obj, gnu_extras=local_gnu, no_overwrite=not force
                )
            else:
                dest_parent = os.path.dirname(dst_path_obj) or "."
                dest_cmd = CPService._build_dest_tar(
                    dest_parent, gnu_extras=local_gnu, no_overwrite=not force
                )
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
                    else (os.path.dirname(dst_path_obj) or "."),
                    gnu_extras=local_gnu,
                    no_overwrite=not force,
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
        src_gnu = CPService._probe_remote_tar(src_ssh_prefix)
        dst_gnu = CPService._probe_remote_tar(dst_ssh_prefix)

        # Probe source
        path_type, total_size = CPService._probe_remote_path(
            src_ssh_prefix, src_path
        )
        is_directory = path_type == "DIR"

        basename = os.path.basename(src_path.rstrip("/"))
        remote_src_tar = CPService._build_remote_source_tar(
            src_path, is_directory, gnu_extras=src_gnu
        )
        remote_dst_tar = CPService._build_remote_dest_tar(
            dst_path, gnu_extras=dst_gnu, no_overwrite=not force
        )

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
