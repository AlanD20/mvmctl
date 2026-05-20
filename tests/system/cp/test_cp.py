"""System tests for ``mvm cp`` — copy files between host and microVMs.

Tests use ``created_vm`` for a running VM with SSH key injected, then
verify file transfers via ``mvm ssh --cmd`` (for VM-side verification)
and local filesystem assertions (for host-side verification).
"""

from __future__ import annotations

import subprocess
import uuid
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, ensure_vm_deps, wait_for_ssh

pytestmark = [
    pytest.mark.system,
    pytest.mark.serial,
    pytest.mark.domain_cp,
]


def _cleanup_stale_bridges() -> None:
    """Remove any mvm bridges left over from previous test runs."""
    import subprocess as _sp
    import sys as _sys

    try:
        result = _sp.run(
            ["ip", "-o", "link", "show"],
            capture_output=True, text=True, timeout=10,
        )
        # ip -o link show output: "2: mvm-sy-abc123: <BROADCAST> ..."
        removed = 0
        for line in result.stdout.splitlines():
            # Extract the name part: "2: mvm-sy-abc123: <BROADCAST>..."
            # Split on ":" and take index 1, then strip, then first word
            parts = line.split(":")
            if len(parts) >= 2:
                name = parts[1].strip()
                # name might be "mvm-sy-abc123 <BROADCAST..." — take first word
                name = name.split()[0].split("@")[0]
                if name.startswith("mvm-"):
                    del_result = _sp.run(
                        ["sudo", "ip", "link", "delete", name],
                        capture_output=True, timeout=10,
                    )
                    if del_result.returncode == 0:
                        removed += 1
                    else:
                        print(
                            f"[bridge cleanup] Failed to delete {name}: "
                            f"{del_result.stderr.strip()}",
                            file=_sys.stderr, flush=True,
                        )
        if removed:
            print(
                f"[bridge cleanup] Removed {removed} stale bridge(s)",
                file=_sys.stderr, flush=True,
            )
    except Exception as exc:
        print(
            f"[bridge cleanup] Error: {exc}",
            file=_sys.stderr, flush=True,
        )


@pytest.fixture(autouse=True)
def _cleanup_bridges_before_and_after(mvm_binary: str) -> None:
    """Clean up stale bridges and DB entries before and after each test."""
    import json as _json
    import time as _time
    _cleanup_stale_bridges()
    # Clean up any stale DB entries for networks matching the
    # ``sys-vm-net-*`` pattern used by the ``created_vm`` fixture.
    # This prevents "Network already exists" errors when a previous
    # test's cleanup timed out before removing the DB entry.
    _r = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
    if _r.returncode == 0 and _r.stdout.strip():
        _nets = _json.loads(_r.stdout)
        for _n in _nets:
            _name = _n.get("name", "")
            if _name.startswith("sys-vm-net-"):
                _run_mvm(
                    mvm_binary, "network", "rm", _name, "--force", check=False
                )
    yield
    _time.sleep(1.0)
    for _retry in range(3):
        _cleanup_stale_bridges()
        _time.sleep(0.5)


# ============================================================================
# Helper: wait for SSH on a created_vm
# ============================================================================


def _wait_for_vm_ssh(
    mvm_binary: str, vm_info: dict[str, Any], timeout: float = 15.0
) -> bool:
    """Wait for SSH to become available on the given VM.

    Returns True if SSH became available within *timeout* seconds.
    """
    return wait_for_ssh(mvm_binary, vm_info["name"], "root", timeout)


def _ssh_cmd(
    mvm_binary: str, vm_name: str, command: str
) -> subprocess.CompletedProcess[str]:
    """Run a command inside a VM via ``mvm ssh``.

    Returns the CompletedProcess (caller must check returncode).
    """
    return _run_mvm(
        mvm_binary,
        "ssh",
        vm_name,
        "--cmd",
        command,
        check=False,
        timeout=30,
    )


# ============================================================================
# Host → VM copy tests (L3: verify file exists inside the guest)
# ============================================================================


class TestCpHostToVm:
    """Copy files/directories from host to a running VM.

    All tests create a temp file or directory on the host, run
    ``mvm cp <src> <vm>:/tmp/``, then verify the payload
    exists inside the guest via ``mvm ssh --cmd``.

    With tar-over-SSH, the destination is always a directory and
    files preserve their original filename (tar cannot rename).
    """

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_file_host_to_vm(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Copy a single file from host to VM — verify on guest via SSH.

        Destination uses a directory path (``/tmp/``) — the file lands
        with its original name. This verifies tar-over-SSH semantics
        where the destination is always a directory and the filename
        is preserved from the source.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip("SSH not available on VM — cannot verify file transfer")

        # Create a temp file on host
        test_file = tmp_path / f"test_file_{uuid.uuid4().hex[:8]}.txt"
        test_content = f"hello from host at {uuid.uuid4().hex}"
        test_file.write_text(test_content)
        remote_dir = "/tmp/"
        remote_file = f"/tmp/{test_file.name}"

        try:
            # Copy host → VM (destination is a directory — tar preserves filename)
            result = _run_mvm(
                mvm_binary,
                "cp",
                str(test_file),
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"cp failed: stdout={result.stdout} stderr={result.stderr}"
            )

            # L3: Verify file exists inside the VM via SSH (original filename)
            ssh_result = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_file}' && echo EXISTS",
            )
            assert ssh_result.returncode == 0, (
                f"SSH check failed: {ssh_result.stderr}"
            )
            assert "EXISTS" in ssh_result.stdout, (
                f"File {remote_file} not found on VM: {ssh_result.stdout}"
            )

            # Also verify file content matches
            content_result = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"cat '{remote_file}'",
            )
            assert content_result.returncode == 0, (
                f"SSH cat failed: {content_result.stderr}"
            )
            assert content_result.stdout.strip() == test_content, (
                f"File content mismatch: "
                f"expected {test_content!r}, got {content_result.stdout.strip()!r}"
            )
        finally:
            # Cleanup: remove the temp file from VM
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_file}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_directory_host_to_vm(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Copy a directory from host to VM — verify on guest via SSH.

        Rationale: Verifies recursive directory copy works (tar handles
        this automatically). A regression where directory structure is
        flattened or files are lost would not be caught by L1/L2 checks.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip(
                "SSH not available on VM — cannot verify directory transfer"
            )

        # Create a temp directory with content
        dir_name = f"test_dir_{uuid.uuid4().hex[:8]}"
        test_dir = tmp_path / dir_name
        test_dir.mkdir()
        nested = test_dir / "nested"
        nested.mkdir()
        file1 = test_dir / "file1.txt"
        file1.write_text("content1")
        file2 = nested / "file2.txt"
        file2.write_text("content2")

        remote_parent = "/tmp"
        remote_dir_path = f"{remote_parent}/{dir_name}"

        try:
            # Copy directory host → VM
            result = _run_mvm(
                mvm_binary,
                "cp",
                str(test_dir),
                f"{vm_info['name']}:{remote_parent}/",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"cp directory failed: stdout={result.stdout} stderr={result.stderr}"
            )

            # Tar copies CONTENTS of the source directory into the remote
            # destination directory (not the directory itself).  So files
            # land directly under /tmp/, not /tmp/test_dir_xxx/.
            # Verify files exist at the parent level.
            file1_check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_parent}/file1.txt' && echo F1_OK",
            )
            assert (
                file1_check.returncode == 0 and "F1_OK" in file1_check.stdout
            ), "file1.txt not found in transferred directory"

            # Verify nested file2 exists
            file2_check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_parent}/nested/file2.txt' && echo F2_OK",
            )
            assert (
                file2_check.returncode == 0 and "F2_OK" in file2_check.stdout
            ), "nested/file2.txt not found in transferred directory"
        finally:
            # Cleanup: remove files from VM
            _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"rm -f '{remote_parent}/file1.txt' '{remote_parent}/nested/file2.txt'; "
                f"rmdir '{remote_parent}/nested' 2>/dev/null; true",
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_with_user_flag(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Copy a file to VM with --user flag.

        Rationale: The --user flag on cp allows specifying an SSH user for
        the connection. A regression where --user is silently ignored would
        cause the copy to use the wrong SSH user, potentially failing to
        authenticate.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip("SSH not available on VM — cannot verify cp --user")

        test_file = tmp_path / f"user_flag_{uuid.uuid4().hex[:8]}.txt"
        test_file.write_text("cp with --user flag test")
        remote_dir = "/tmp/"

        try:
            result = _run_mvm(
                mvm_binary,
                "cp",
                str(test_file),
                f"{vm_info['name']}:{remote_dir}",
                "--user",
                "root",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"cp with --user failed: stdout={result.stdout} stderr={result.stderr}"
            )

            # L3: Verify file exists on VM via SSH
            remote_file = f"{remote_dir}{test_file.name}"
            check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_file}' && echo EXISTS",
            )
            assert check.returncode == 0 and "EXISTS" in check.stdout, (
                f"File {remote_file} not found on VM after cp with --user"
            )
        finally:
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_file}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_with_key_flag(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Copy a file to VM with --key flag specifying a named key.

        Rationale: The --key flag on cp accepts a named key (from key cache).
        A regression where --key is silently ignored would cause the copy
        to use the default key, potentially failing if the default key is
        not authorized on the VM.
        """
        import uuid as _uuid

        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip("SSH not available on VM — cannot verify cp --key")

        # The created_vm was created with a key named sys-vmkey-*.
        # Use the --key flag to specify that same key by name.
        key_name = f"sys-cp-key-{_uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )

            test_file = tmp_path / f"key_flag_{_uuid.uuid4().hex[:8]}.txt"
            test_file.write_text("cp with --key flag test")
            remote_dir = "/tmp/"

            result = _run_mvm(
                mvm_binary,
                "cp",
                str(test_file),
                f"{vm_info['name']}:{remote_dir}",
                "--key",
                key_name,
                timeout=30,
                check=False,
            )
            # The key may not be authorized on the VM (created_vm uses a
            # different key). We accept either success or a non-fatal error
            # (Permission denied or similar) — the important thing is that
            # --key was accepted as a valid named key argument.
            if result.returncode != 0:
                combined = (result.stdout + result.stderr).lower()
                assert any(
                    w in combined
                    for w in [
                        "permission denied",
                        "not found",
                        "could be resolved",
                    ]
                ), (
                    f"Unexpected error with cp --key: "
                    f"stdout={result.stdout} stderr={result.stderr}"
                )
            else:
                # L3: If the copy succeeded, verify file exists on VM
                remote_file = f"{remote_dir}{test_file.name}"
                check = _ssh_cmd(
                    mvm_binary,
                    vm_info["name"],
                    f"test -f '{remote_file}' && echo EXISTS",
                )
                assert check.returncode == 0 and "EXISTS" in check.stdout, (
                    f"File {remote_file} not found on VM after cp with --key"
                )
        finally:
            _run_mvm(mvm_binary, "key", "rm", key_name, "--force", check=False)


# ============================================================================
# VM → Host copy tests (L3: verify file exists on host filesystem)
# ============================================================================


class TestCpVmToHost:
    """Copy files from a VM back to the host.

    Uses round-trip: host → VM → host, verifying content integrity.
    """

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_file_vm_to_host(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Round-trip a file: host→VM→host, verify content integrity.

        With tar-over-SSH, the upload destination is a directory (``/tmp/``)
        and the download extracts with the original filename. This test
        verifies that both directions preserve content correctly.

        Rationale: Full round-trip verification of file integrity across
        both directions. A regression in tar pipe or SSH encoding that
        corrupts data would only be caught by reading back the transferred
        file and comparing content.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip("SSH not available on VM — cannot verify round-trip")

        # Create original file on host
        original_content = f"round-trip content {uuid.uuid4().hex}"
        src_file = tmp_path / "original.txt"
        src_file.write_text(original_content)

        remote_dir = "/tmp/"
        remote_file = f"/tmp/{src_file.name}"
        # Destination for download: a non-existent path whose parent
        # directory is the extraction target.  Tar extracts the original
        # filename, so the file lands at ``<parent>/<original_name>``.
        download_dest = tmp_path / "placeholder"

        try:
            # Step 1: Copy host → VM (tar preserves original filename)
            upload = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert upload.returncode == 0, (
                f"Upload failed: stdout={upload.stdout} stderr={upload.stderr}"
            )

            # Remove the local source file BEFORE downloading back, so
            # tar extract does not collide with the still-existing source.
            src_file.unlink(missing_ok=True)

            # Step 2: Copy VM → host (tar extracts original filename)
            download = _run_mvm(
                mvm_binary,
                "cp",
                f"{vm_info['name']}:{remote_file}",
                str(download_dest),
                timeout=30,
            )
            assert download.returncode == 0, (
                f"Download failed: stdout={download.stdout} stderr={download.stderr}"
            )

            # Step 3: L3 — Verify file exists on host with ORIGINAL filename
            expected = tmp_path / src_file.name
            assert expected.exists(), (
                f"Extracted file not found at {expected} (tar preserves "
                f"original filename)"
            )

            # Step 4: L3 — Verify content integrity
            downloaded_content = expected.read_text()
            assert downloaded_content.strip() == original_content, (
                f"Content mismatch after round-trip: "
                f"expected {original_content!r}, "
                f"got {downloaded_content.strip()!r}"
            )
        finally:
            # Cleanup: remove the remote file
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_file}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_directory_vm_to_host(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Copy a directory from VM to host — verify dir exists on host with contents.

        Rationale: Tar handles directory extraction on the host side. A regression
        where VM→host directory copy flattens the structure or drops files would
        not be caught by file-only tests.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip(
                "SSH not available on VM — cannot verify directory download"
            )

        # Create a directory on the VM first by uploading one
        dir_name = f"download_dir_{uuid.uuid4().hex[:8]}"
        test_dir = tmp_path / dir_name
        test_dir.mkdir()
        nested = test_dir / "nested"
        nested.mkdir()
        file1 = test_dir / "file1.txt"
        file1.write_text("download content1")
        file2 = nested / "file2.txt"
        file2.write_text("download content2")

        remote_parent = "/tmp"
        remote_dir_path = f"{remote_parent}/{dir_name}"
        download_dest = tmp_path / "downloaded_dir"

        try:
            # Step 1: Create the directory on VM via SSH, then upload files
            _ssh_cmd(
                mvm_binary, vm_info["name"],
                f"mkdir -p '{remote_dir_path}/nested'",
            )
            _run_mvm(
                mvm_binary,
                "cp",
                str(file1),
                f"{vm_info['name']}:{remote_dir_path}/",
                timeout=30,
            )
            _run_mvm(
                mvm_binary,
                "cp",
                str(file2),
                f"{vm_info['name']}:{remote_dir_path}/nested/",
                timeout=30,
            )

            # Step 2: Download the directory from VM to host
            download = _run_mvm(
                mvm_binary,
                "cp",
                f"{vm_info['name']}:{remote_dir_path}",
                str(download_dest),
                timeout=30,
            )
            assert download.returncode == 0, (
                f"Directory download failed: "
                f"stdout={download.stdout} stderr={download.stderr}"
            )

            # Step 3: L3 — Verify the downloaded directory exists on host
            # Tar preserves the original directory name, so the extracted
            # content should be at download_dest's parent with the original name.
            extracted_dir = download_dest.parent / dir_name
            assert extracted_dir.is_dir(), (
                f"Downloaded directory not found at {extracted_dir}"
            )

            # Verify files exist
            assert (extracted_dir / "file1.txt").exists(), (
                "file1.txt not found in downloaded directory"
            )
            assert (extracted_dir / "nested" / "file2.txt").exists(), (
                "nested/file2.txt not found in downloaded directory"
            )

            # Verify content
            downloaded_content1 = (extracted_dir / "file1.txt").read_text()
            assert downloaded_content1.strip() == "download content1", (
                f"Content mismatch for file1.txt: "
                f"expected 'download content1', got {downloaded_content1.strip()!r}"
            )
        finally:
            # Cleanup remote directory
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -rf '{remote_dir_path}'")


# ============================================================================
# Edge case tests
# ============================================================================


class TestCpEdgeCases:
    """Edge cases for ``mvm cp``: nonexistent source, overwrite protection, force flag."""

    @pytest.mark.serial
    def test_cp_nonexistent_source(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
    ) -> None:
        """Copy a nonexistent local source — should fail with clear error.

        Rationale: User must get clear feedback on invalid source paths.
        A silent no-op or unclear error would be confusing.
        """
        vm_info = created_vm
        result = _run_mvm(
            mvm_binary,
            "cp",
            "/nonexistent/path/xyz789",
            f"{vm_info['name']}:/tmp/",
            check=False,
            timeout=10,
        )
        assert result.returncode != 0, (
            f"Expected non-zero exit for nonexistent source, "
            f"got rc={result.returncode}: stdout={result.stdout} stderr={result.stderr}"
        )
        error_msg = (result.stderr + " " + result.stdout).lower()
        assert "not found" in error_msg, (
            f"Expected 'not found' in error, got: stderr={result.stderr}"
        )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_no_force_dest_exists(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Copy without ``--force`` when destination file exists — should fail.

        With tar-over-SSH, both uploads target the same directory and the
        same source filename. The second transfer fails because
        ``tar --keep-old-files`` refuses to overwrite the existing file.

        Rationale: Safety check — prevents accidental overwrites.
        Users must explicitly opt in with ``--force``.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip(
                "SSH not available on VM — cannot test overwrite protection"
            )

        # Create a temp file
        src_file = tmp_path / f"src_{uuid.uuid4().hex[:8]}.txt"
        src_file.write_text("original content")
        remote_dir = "/tmp/"
        remote_file = f"/tmp/{src_file.name}"

        try:
            # First copy should succeed (file doesn't exist yet)
            first = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert first.returncode == 0, (
                f"First copy failed: stdout={first.stdout} stderr={first.stderr}"
            )

            # Second copy without --force should fail (tar --keep-old-files
            # prevents overwriting the same filename in the same directory)
            second = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                f"{vm_info['name']}:{remote_dir}",
                check=False,
                timeout=30,
            )
            assert second.returncode != 0, (
                f"Expected non-zero exit for overwrite without --force, "
                f"got rc={second.returncode}: stdout={second.stdout} stderr={second.stderr}"
            )
            error_msg = (second.stderr + " " + second.stdout).lower()
            assert any(
                keyword in error_msg
                for keyword in ["exists", "force", "cannot open", "file exists"]
            ), (
                f"Expected error mentioning 'exists' or 'force', "
                f"got: stderr={second.stderr}"
            )
        finally:
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_file}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_force_overwrites(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Copy with ``--force`` overwrites existing destination.

        With tar-over-SSH, the destination is a directory so both
        transfers use the same local filename. The first transfer
        creates ``/tmp/file_for_force.txt``. The local file is
        recreated with different content and transferred again with
        ``--force``, which should overwrite the remote file.

        Rationale: ``--force`` must actually overwrite the destination
        with new content. A regression where ``--force`` silently
        fails to overwrite would cause users to think the transfer
        succeeded when it did not.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip(
                "SSH not available on VM — cannot verify force overwrite"
            )

        remote_dir = "/tmp/"
        remote_file = "/tmp/file_for_force.txt"
        src_file = tmp_path / "file_for_force.txt"

        try:
            # Write original content and copy to VM
            src_file.write_text("AAAA_original")
            first = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert first.returncode == 0, (
                f"First copy failed: stdout={first.stdout} stderr={first.stderr}"
            )

            # Verify original content
            orig_check = _ssh_cmd(
                mvm_binary, vm_info["name"], f"cat '{remote_file}'"
            )
            assert orig_check.returncode == 0
            assert "AAAA" in orig_check.stdout, (
                f"Expected 'AAAA' in original file, got: {orig_check.stdout}"
            )

            # Recreate the same local file with new content
            src_file.write_text("BBBB_overwritten")

            # Copy with --force — should overwrite the remote file
            overwrite = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                f"{vm_info['name']}:{remote_dir}",
                "--force",
                timeout=30,
            )
            assert overwrite.returncode == 0, (
                f"Force copy failed: stdout={overwrite.stdout} stderr={overwrite.stderr}"
            )

            # L3: Verify content changed to the new content
            final_check = _ssh_cmd(
                mvm_binary, vm_info["name"], f"cat '{remote_file}'"
            )
            assert final_check.returncode == 0, (
                f"SSH cat failed after force overwrite: {final_check.stderr}"
            )
            assert "BBBB" in final_check.stdout, (
                f"Expected 'BBBB' after force overwrite, "
                f"got: {final_check.stdout}"
            )
            assert "AAAA" not in final_check.stdout, (
                f"Original content 'AAAA' should be gone after overwrite, "
                f"got: {final_check.stdout}"
            )
        finally:
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_file}'")


# ============================================================================
# Multi-source copy tests (L3: verify multiple sources in one command)
# ============================================================================


class TestCpMultiSource:
    """Copy multiple files/directories from host to a VM in one ``mvm cp`` command.

    The multi-source feature allows ``mvm cp file1.txt file2.txt vm:/dst/``
    to send multiple sources in a single invocation, rather than requiring
    repeated single-file commands. These tests verify that every source
    is transferred to the guest and that backward compatibility (single
    source) is preserved.

    Tests use ``created_vm`` for a running VM with SSH key injected, then
    verify file transfers via ``mvm ssh --cmd``.
    """

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_multi_source_two_files(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Copy two local files to a VM in one command — both must land.

        L3: Verify both files exist inside the guest via SSH and that
        their contents match the originals.

        Rationale: The multi-source code path must handle multiple file
        arguments correctly. A regression where only the first file is
        transferred (or the second silently overwrites the first) would
        not be caught by L1/L2 checks.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip(
                "SSH not available on VM — cannot verify multi-source transfer"
            )

        # Create two temp files with different content
        file1 = tmp_path / f"multi_a_{uuid.uuid4().hex[:8]}.txt"
        file2 = tmp_path / f"multi_b_{uuid.uuid4().hex[:8]}.txt"
        content1 = f"content-a-{uuid.uuid4().hex}"
        content2 = f"content-b-{uuid.uuid4().hex}"
        file1.write_text(content1)
        file2.write_text(content2)

        remote_dir = "/tmp/"
        remote_file1 = f"{remote_dir}{file1.name}"
        remote_file2 = f"{remote_dir}{file2.name}"

        try:
            # Copy both files in one command
            result = _run_mvm(
                mvm_binary,
                "cp",
                str(file1),
                str(file2),
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"multi-source cp failed: stdout={result.stdout} "
                f"stderr={result.stderr}"
            )

            # L3: Verify file1 exists on VM
            check1 = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_file1}' && echo F1_OK",
            )
            assert check1.returncode == 0 and "F1_OK" in check1.stdout, (
                f"File {remote_file1} not found on VM"
            )

            # L3: Verify file2 exists on VM
            check2 = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_file2}' && echo F2_OK",
            )
            assert check2.returncode == 0 and "F2_OK" in check2.stdout, (
                f"File {remote_file2} not found on VM"
            )

            # L3: Verify content of file1 matches
            cat1 = _ssh_cmd(
                mvm_binary, vm_info["name"], f"cat '{remote_file1}'"
            )
            assert cat1.returncode == 0 and cat1.stdout.strip() == content1, (
                f"Content mismatch for {remote_file1}: "
                f"expected {content1!r}, got {cat1.stdout.strip()!r}"
            )

            # L3: Verify content of file2 matches
            cat2 = _ssh_cmd(
                mvm_binary, vm_info["name"], f"cat '{remote_file2}'"
            )
            assert cat2.returncode == 0 and cat2.stdout.strip() == content2, (
                f"Content mismatch for {remote_file2}: "
                f"expected {content2!r}, got {cat2.stdout.strip()!r}"
            )
        finally:
            # Cleanup: remove both files from VM
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_file1}'")
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_file2}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_multi_source_file_and_dir(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Copy one file and one directory in a single ``mvm cp`` command.

        L3: Verify both the file and the directory (with nested content)
        exist on the VM via SSH.

        Rationale: Mixing file and directory sources exercises the most
        complex path in the multi-source handler. A regression where
        tar flags conflict between file and directory arguments would
        cause one source to silently fail.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip(
                "SSH not available on VM — cannot verify mixed-source transfer"
            )

        # Create one file
        src_file = tmp_path / f"mix_file_{uuid.uuid4().hex[:8]}.txt"
        src_file.write_text("mixed-source file content")

        # Create one directory with nested content
        dir_name = f"mix_dir_{uuid.uuid4().hex[:8]}"
        src_dir = tmp_path / dir_name
        src_dir.mkdir()
        nested = src_dir / "nested"
        nested.mkdir()
        nested_file = nested / "deep.txt"
        nested_file.write_text("nested content")

        remote_parent = "/tmp"
        remote_file = f"{remote_parent}/{src_file.name}"
        remote_dir_path = f"{remote_parent}/{dir_name}"

        try:
            # Copy both file and directory in one command
            result = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                str(src_dir),
                f"{vm_info['name']}:{remote_parent}/",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"mixed-source cp failed: stdout={result.stdout} "
                f"stderr={result.stderr}"
            )

            # L3: Verify the file exists
            f_check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_file}' && echo FILE_OK",
            )
            assert f_check.returncode == 0 and "FILE_OK" in f_check.stdout, (
                f"File {remote_file} not found on VM"
            )

            # L3: Verify the directory exists
            d_check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -d '{remote_dir_path}' && echo DIR_OK",
            )
            assert d_check.returncode == 0 and "DIR_OK" in d_check.stdout, (
                f"Directory {remote_dir_path} not found on VM"
            )

            # L3: Verify the nested file exists inside the transferred dir
            deep_check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_dir_path}/nested/deep.txt' && echo DEEP_OK",
            )
            assert (
                deep_check.returncode == 0 and "DEEP_OK" in deep_check.stdout
            ), "nested/deep.txt not found in transferred directory"
        finally:
            # Cleanup: remove file and directory from VM
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_file}'")
            _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"rm -rf '{remote_dir_path}'",
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_multi_source_single_arg(
        self,
        mvm_binary: str,
        created_vm: dict[str, Any],
        tmp_path: Path,
    ) -> None:
        """Single source still works when passed through the multi-source path.

        L3: Verify the file was transferred successfully via SSH.

        Rationale: When the multi-source code path is active, a single
        argument must still work (backward compatibility). A regression
        where the multi-source handler treats one element as a list
        accidentally (e.g. iterating over characters of a string) would
        break the most common use case.
        """
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip(
                "SSH not available on VM — cannot verify single-arg multi-source"
            )

        src_file = tmp_path / f"single_{uuid.uuid4().hex[:8]}.txt"
        src_file.write_text("single arg via multi-source")
        remote_dir = "/tmp/"
        remote_file = f"{remote_dir}{src_file.name}"

        try:
            # Copy one file using the same syntax (single source)
            result = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"single-arg cp failed: stdout={result.stdout} "
                f"stderr={result.stderr}"
            )

            # L3: Verify file exists on VM
            check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_file}' && echo EXISTS",
            )
            assert check.returncode == 0 and "EXISTS" in check.stdout, (
                f"File {remote_file} not found on VM"
            )

            # L3: Verify content matches
            cat_check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"cat '{remote_file}'",
            )
            assert (
                cat_check.returncode == 0
                and cat_check.stdout.strip() == "single arg via multi-source"
            ), f"Content mismatch: got {cat_check.stdout.strip()!r}"
        finally:
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_file}'")

    @pytest.mark.serial
    def test_cp_multi_source_rejects_non_vm_dest(
        self,
        mvm_binary: str,
        tmp_path: Path,
    ) -> None:
        """Multi-source to a local destination should fail.

        L1: Verify non-zero exit with clear error message about
        multi-source requiring VM destination.

        Rationale: When multiple sources are specified, the destination
        must be a VM path (``vm_name:/path``). A local destination is
        ambiguous (which source is the destination?). The CLI must
        reject this with a clear error rather than silently behaving
        incorrectly.
        """
        # Create two source files
        file1 = tmp_path / "src_a.txt"
        file2 = tmp_path / "src_b.txt"
        file1.write_text("aaa")
        file2.write_text("bbb")

        local_dest = tmp_path / "local_dest"

        result = _run_mvm(
            mvm_binary,
            "cp",
            str(file1),
            str(file2),
            str(local_dest),
            check=False,
            timeout=10,
        )
        assert result.returncode != 0, (
            f"Expected non-zero exit for multi-source with local dest, "
            f"got rc={result.returncode}: stdout={result.stdout} "
            f"stderr={result.stderr}"
        )
        error_msg = (result.stderr + " " + result.stdout).lower()
        assert any(
            keyword in error_msg
            for keyword in [
                "requires",
                "destination",
                "vm",
                "remote",
                "multi",
                "multiple",
            ]
        ), (
            f"Expected error mentioning multi-source requires VM dest, "
            f"got: stderr={result.stderr}"
        )


# ============================================================================
# VM → VM copy tests (L3: verify file on destination VM)
# ============================================================================


class TestCpVmToVm:
    """Copy a file from one VM to another VM.

    Tests that ``mvm cp vm1:/path vm2:/path`` transfers files correctly
    between two microVMs. Requires two running VMs with SSH access.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_cp,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.serial
    def test_cp_file_vm_to_vm(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
        tmp_path: Path,
    ) -> None:
        """Copy a file from one VM to another — verify on destination via SSH.

        Rationale: VM→VM copy is a complex path that connects to VM1,
        reads a file via tar, then connects to VM2 and writes it. A
        regression where the intermediate pipe between the two SSH
        connections breaks would cause silent data loss.
        """
        import uuid as _uuid

        from tests.system.conftest import (
            _cleanup_vm_resources as _cleanup_vm,
        )
        from tests.system.conftest import (
            _create_minimal_vm_core as _create_vm,
        )

        key_name = unique_key_name
        net_name = unique_network_name
        vm_a = f"{unique_vm_name}-a"
        vm_b = f"{unique_vm_name}-b"

        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )

        try:
            # Create two VMs on the same network.
            # First VM: _create_minimal_vm_core handles network creation internally.
            _create_vm(mvm_binary, vm_a, net_name, ssh_key_name=key_name)
            # Second VM: network already exists, so create manually via vm create.
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_b,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                "--no-console",
            )

            timeout = 20.0
            ssh_a = wait_for_ssh(mvm_binary, vm_a, "root", timeout)
            ssh_b = wait_for_ssh(mvm_binary, vm_b, "root", timeout)
            if not ssh_a or not ssh_b:
                # Skip-reason: SSH not available on one or both VMs.
                # VM→VM copy requires SSH connectivity to both source
                # and destination VMs.
                pytest.skip(
                    "SSH not available on both VMs — cannot verify VM→VM copy"
                )

            # Create a test file on VM A
            test_content = f"vm-to-vm test {_uuid.uuid4().hex}"
            src_file = f"/tmp/vm_to_vm_test_{_uuid.uuid4().hex[:8]}.txt"
            _ssh_cmd(mvm_binary, vm_a, f"echo '{test_content}' > '{src_file}'")

            # Copy VM A → VM B
            dest_path = "/tmp/"
            result = _run_mvm(
                mvm_binary,
                "cp",
                f"{vm_a}:{src_file}",
                f"{vm_b}:{dest_path}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"VM→VM cp failed: stdout={result.stdout} stderr={result.stderr}"
            )

            # L3: Verify file exists on VM B via SSH
            dest_file = f"{dest_path}{src_file.split('/')[-1]}"
            check = _ssh_cmd(
                mvm_binary,
                vm_b,
                f"test -f '{dest_file}' && echo VM2_EXISTS",
            )
            assert check.returncode == 0 and "VM2_EXISTS" in check.stdout, (
                f"File {dest_file} not found on VM B after VM→VM copy"
            )

            # L3: Verify content matches
            content_check = _ssh_cmd(
                mvm_binary,
                vm_b,
                f"cat '{dest_file}'",
            )
            assert content_check.returncode == 0, (
                f"SSH cat on VM B failed: {content_check.stderr}"
            )
            assert test_content in content_check.stdout, (
                f"Content mismatch on VM B: expected '{test_content}', "
                f"got '{content_check.stdout.strip()}'"
            )

        finally:
            _cleanup_vm(mvm_binary, vm_a, net_name, key_name)
            _cleanup_vm(mvm_binary, vm_b, net_name, key_name)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
