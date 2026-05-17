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

from tests.system.conftest import _run_mvm, wait_for_ssh

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_cp,
]


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
    ``mvm cp <src> <vm>:/tmp/<name>``, then verify the payload
    exists inside the guest via ``mvm ssh --cmd``.
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

        Rationale: Verifies a single file transfer from host to VM reaches
        the guest filesystem. A regression where the file silently fails to
        transfer would not be caught by returncode-only checks.
        """
        # Rationale: Needs a running VM with SSH (created_vm) to verify
        # file existence inside the guest. L3 verification requires
        # guest-side assertion.
        vm_info = created_vm
        if not _wait_for_vm_ssh(mvm_binary, vm_info):
            pytest.skip("SSH not available on VM — cannot verify file transfer")

        # Create a temp file on host
        test_file = tmp_path / f"test_file_{uuid.uuid4().hex[:8]}.txt"
        test_content = f"hello from host at {uuid.uuid4().hex}"
        test_file.write_text(test_content)
        remote_path = f"/tmp/{test_file.name}"

        try:
            # Copy host → VM
            result = _run_mvm(
                mvm_binary,
                "cp",
                str(test_file),
                f"{vm_info['name']}:{remote_path}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"cp failed: stdout={result.stdout} stderr={result.stderr}"
            )

            # L3: Verify file exists inside the VM via SSH
            ssh_result = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_path}' && echo EXISTS",
            )
            assert ssh_result.returncode == 0, (
                f"SSH check failed: {ssh_result.stderr}"
            )
            assert "EXISTS" in ssh_result.stdout, (
                f"File {remote_path} not found on VM: {ssh_result.stdout}"
            )

            # Also verify file content matches
            content_result = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"cat '{remote_path}'",
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
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_path}'")

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

            # L3: Verify directory exists inside the VM
            dir_check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -d '{remote_dir_path}' && echo DIR_OK",
            )
            assert dir_check.returncode == 0, (
                f"Directory check failed: {dir_check.stderr}"
            )
            assert "DIR_OK" in dir_check.stdout, (
                f"Directory {remote_dir_path} not found on VM"
            )

            # Verify file1 exists
            file1_check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_dir_path}/file1.txt' && echo F1_OK",
            )
            assert (
                file1_check.returncode == 0 and "F1_OK" in file1_check.stdout
            ), "file1.txt not found in transferred directory"

            # Verify nested file2 exists
            file2_check = _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"test -f '{remote_dir_path}/nested/file2.txt' && echo F2_OK",
            )
            assert (
                file2_check.returncode == 0 and "F2_OK" in file2_check.stdout
            ), "nested/file2.txt not found in transferred directory"
        finally:
            # Cleanup: remove the directory from VM
            _ssh_cmd(
                mvm_binary,
                vm_info["name"],
                f"rm -rf '{remote_dir_path}'",
            )


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

        remote_path = f"/tmp/roundtrip_{uuid.uuid4().hex[:8]}.txt"
        dest_file = tmp_path / "downloaded.txt"

        try:
            # Step 1: Copy host → VM
            upload = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                f"{vm_info['name']}:{remote_path}",
                timeout=30,
            )
            assert upload.returncode == 0, (
                f"Upload failed: stdout={upload.stdout} stderr={upload.stderr}"
            )

            # Step 2: Copy VM → host
            download = _run_mvm(
                mvm_binary,
                "cp",
                f"{vm_info['name']}:{remote_path}",
                str(dest_file),
                timeout=30,
            )
            assert download.returncode == 0, (
                f"Download failed: stdout={download.stdout} stderr={download.stderr}"
            )

            # Step 3: L3 — Verify file exists on host filesystem
            assert dest_file.exists(), (
                f"Downloaded file not found at {dest_file}"
            )

            # Step 4: L3 — Verify content integrity
            downloaded_content = dest_file.read_text()
            assert downloaded_content.strip() == original_content, (
                f"Content mismatch after round-trip: "
                f"expected {original_content!r}, "
                f"got {downloaded_content.strip()!r}"
            )
        finally:
            # Cleanup: remove the remote file
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_path}'")


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
        """Copy without ``--force`` when destination exists — should fail.

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
        remote_path = f"/tmp/no_force_test_{uuid.uuid4().hex[:8]}"

        try:
            # First copy should succeed
            first = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                f"{vm_info['name']}:{remote_path}",
                timeout=30,
            )
            assert first.returncode == 0, (
                f"First copy failed: stdout={first.stdout} stderr={first.stderr}"
            )

            # Second copy without --force should fail
            second = _run_mvm(
                mvm_binary,
                "cp",
                str(src_file),
                f"{vm_info['name']}:{remote_path}",
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
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_path}'")

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

        remote_path = f"/tmp/force_test_{uuid.uuid4().hex[:8]}"
        file_a = tmp_path / "file_a.txt"
        file_b = tmp_path / "file_b.txt"

        file_a.write_text("AAAA_original")
        file_b.write_text("BBBB_overwritten")

        try:
            # Copy file A to VM (first time — should succeed)
            first = _run_mvm(
                mvm_binary,
                "cp",
                str(file_a),
                f"{vm_info['name']}:{remote_path}",
                timeout=30,
            )
            assert first.returncode == 0, (
                f"First copy failed: stdout={first.stdout} stderr={first.stderr}"
            )

            # Verify original content
            orig_check = _ssh_cmd(
                mvm_binary, vm_info["name"], f"cat '{remote_path}'"
            )
            assert orig_check.returncode == 0
            assert "AAAA" in orig_check.stdout, (
                f"Expected 'AAAA' in original file, got: {orig_check.stdout}"
            )

            # Copy file B with --force — should overwrite
            overwrite = _run_mvm(
                mvm_binary,
                "cp",
                str(file_b),
                f"{vm_info['name']}:{remote_path}",
                "--force",
                timeout=30,
            )
            assert overwrite.returncode == 0, (
                f"Force copy failed: stdout={overwrite.stdout} stderr={overwrite.stderr}"
            )

            # L3: Verify content changed to file B's content
            final_check = _ssh_cmd(
                mvm_binary, vm_info["name"], f"cat '{remote_path}'"
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
            _ssh_cmd(mvm_binary, vm_info["name"], f"rm -f '{remote_path}'")
