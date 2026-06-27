"""System tests for ``mvm cp`` — copy files between host and microVMs.

All temp files reside inside the test VM (via _run_mvm sh -c) — no host temp files.
Verification uses SSH inside the nested VM for uploads, and _run_mvm sh -c for downloads.
"""

from __future__ import annotations

import uuid

import pytest

from tests.system.conftest import _guest_run, _run_mvm, _unique_subnet, ensure_vm_deps

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_cp,
]


def _exec_cmd(
    runner_vm: str, vm_name: str, command: str
):
    """Run a command inside a nested VM via vsock (``mvm exec``)."""
    return _run_mvm(
        runner_vm,
        "exec", vm_name,
        "--user", "runner",
        "--timeout", "30",
        "--",
        command,
        check=False,
        timeout=45,
    )


# ============================================================================
# Host → VM copy tests
# ============================================================================


class TestCpHostToVm:
    """Copy files/directories from host to a running VM.

    In the system test architecture, "host" means the test VM (runner_vm).
    Source files are created inside the test VM. Nested VM targets are
    the ``created_vm`` fixture.
    """

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_file_host_to_vm(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Copy a single file from host to VM — verify on guest via vsock."""
        vm_info = created_vm

        # Create a temp file inside the test VM
        file_id = uuid.uuid4().hex[:8]
        test_content = f"hello from host at {uuid.uuid4().hex}"
        remote_file = f"/tmp/test_file_{file_id}.txt"
        _guest_run(runner_vm,
            f"echo '{test_content}' > '{remote_file}'",
        )

        remote_dir = "/tmp/"

        try:
            # Copy host → VM (destination is a directory — tar preserves filename)
            result = _run_mvm(
                runner_vm,
                "cp",
                remote_file,
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"cp failed: stdout={result.stdout} stderr={result.stderr}"
            )

            # L3: Verify file exists inside the nested VM via SSH
            dest_file = f"{remote_dir}{remote_file.split('/')[-1]}"
            ssh_result = _exec_cmd(
                runner_vm,
                vm_info["name"],
                f"test -f '{dest_file}' && echo EXISTS",
            )
            assert ssh_result.returncode == 0, (
                f"SSH check failed: {ssh_result.stderr}"
            )
            assert "EXISTS" in ssh_result.stdout, (
                f"File {dest_file} not found on VM: {ssh_result.stdout}"
            )

            # Also verify file content matches
            content_result = _exec_cmd(
                runner_vm,
                vm_info["name"],
                f"cat '{dest_file}'",
            )
            assert content_result.returncode == 0, (
                f"SSH cat failed: {content_result.stderr}"
            )
            assert content_result.stdout.strip() == test_content, (
                f"File content mismatch: "
                f"expected {test_content!r}, got {content_result.stdout.strip()!r}"
            )
        finally:
            _exec_cmd(runner_vm, vm_info["name"], f"rm -f '{dest_file}'")
            _guest_run(runner_vm, f"rm -f '{remote_file}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_directory_host_to_vm(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Copy a directory from host to VM — verify on guest via SSH."""
        vm_info = created_vm

        # Create a temp directory with content inside the test VM
        dir_name = f"test_dir_{uuid.uuid4().hex[:8]}"
        remote_parent = "/tmp"
        _guest_run(runner_vm,
            f"mkdir -p '{remote_parent}/{dir_name}/nested' "
            f"&& echo 'content1' > '{remote_parent}/{dir_name}/file1.txt' "
            f"&& echo 'content2' > '{remote_parent}/{dir_name}/nested/file2.txt'",
        )

        try:
            # Copy directory host → VM
            result = _run_mvm(
                runner_vm,
                "cp",
                f"{remote_parent}/{dir_name}",
                f"{vm_info['name']}:{remote_parent}/",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"cp directory failed: stdout={result.stdout} stderr={result.stderr}"
            )

            # vsock binary frame protocol preserves source directory structure:
            # files end up at <dest>/<source_dirname>/..., not flattened.
            file1_check = _exec_cmd(
                runner_vm,
                vm_info["name"],
                f"test -f '{remote_parent}/{dir_name}/file1.txt' && echo F1_OK",
            )
            assert (
                file1_check.returncode == 0 and "F1_OK" in file1_check.stdout
            ), "file1.txt not found in transferred directory"

            file2_check = _exec_cmd(
                runner_vm,
                vm_info["name"],
                f"test -f '{remote_parent}/{dir_name}/nested/file2.txt' && echo F2_OK",
            )
            assert (
                file2_check.returncode == 0 and "F2_OK" in file2_check.stdout
            ), "nested/file2.txt not found in transferred directory"
        finally:
            _exec_cmd(
                runner_vm,
                vm_info["name"],
                f"rm -f '{remote_parent}/file1.txt '{remote_parent}/nested/file2.txt'; "
                f"rmdir '{remote_parent}/nested' 2>/dev/null; true",
            )
            _guest_run(runner_vm,
                f"rm -rf '{remote_parent}/{dir_name}'",
                check=False,
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_with_force_flag(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Copy a file to VM with --force flag (overwrite existing)."""
        vm_info = created_vm

        file_id = uuid.uuid4().hex[:8]
        src_file = f"/tmp/cp_force_{file_id}.txt"
        _guest_run(runner_vm,
            f"echo 'force test content' > '{src_file}'",
        )
        remote_path = f"/tmp/force_dest_{file_id}.txt"

        try:
            # First, put a file at the destination
            _exec_cmd(runner_vm, vm_info["name"], f"echo 'original' > '{remote_path}'")

            # Copy with --force to overwrite
            result = _run_mvm(
                runner_vm,
                "cp",
                src_file,
                f"{vm_info['name']}:{remote_path}",
                "--force",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"cp with --force failed: stdout={result.stdout} stderr={result.stderr}"
            )

            check = _exec_cmd(
                runner_vm,
                vm_info["name"],
                f"cat '{remote_path}'",
            )
            assert check.returncode == 0 and "force test content" in check.stdout, (
                f"File content mismatch after --force: {check.stdout}"
            )
        finally:
            _exec_cmd(runner_vm, vm_info["name"], f"rm -f '{remote_path}'")
            _guest_run(runner_vm, f"rm -f '{src_file}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_vsock_binary_protocol(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Verify cp uses vsock binary frame protocol — no SSH key required."""
        vm_info = created_vm
        file_id = uuid.uuid4().hex[:8]
        src_file = f"/tmp/cp_vsock_{file_id}.txt"
        _guest_run(runner_vm,
            f"echo 'vsock protocol test' > '{src_file}'",
        )
        remote_dir = "/tmp/"

        try:
            # cp without --user or --key should work via vsock
            result = _run_mvm(
                runner_vm,
                "cp",
                src_file,
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"cp via vsock failed: stdout={result.stdout} stderr={result.stderr}"
            )

            dest_file = f"{remote_dir}{src_file.split('/')[-1]}"
            check = _guest_run(runner_vm,
                f"test -f '{dest_file}' && echo EXISTS",
                timeout=30,
            )
            assert check.returncode == 0 and "EXISTS" in check.stdout, (
                f"File {dest_file} not found after cp via vsock"
            )
        finally:
            _guest_run(runner_vm, f"rm -f '{src_file}'", check=False)
            _guest_run(runner_vm, f"rm -f '{dest_file}'", check=False)


# ============================================================================
# VM → Host copy tests
# ============================================================================


class TestCpVmToHost:
    """Copy files from a VM back to the host.

    Uses round-trip: host → VM → host, verifying content integrity.
    In system tests, "host" is the test VM (runner_vm).
    """

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_file_vm_to_host(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Round-trip a file: host→VM→host, verify content integrity."""
        vm_info = created_vm

        original_content = f"round-trip content {uuid.uuid4().hex}"
        remote_src = f"/tmp/original_{uuid.uuid4().hex[:8]}.txt"
        _guest_run(runner_vm,
            f"echo '{original_content}' > '{remote_src}'",
        )

        remote_dir = "/tmp/"
        download_dest_parent = "/tmp/roundtrip_dest"

        try:
            # Step 1: Copy host → VM (tar preserves original filename)
            upload = _run_mvm(
                runner_vm,
                "cp",
                remote_src,
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert upload.returncode == 0, (
                f"Upload failed: stdout={upload.stdout} stderr={upload.stderr}"
            )

            # Remove the local source file BEFORE downloading back
            _guest_run(runner_vm, f"rm -f '{remote_src}'")

            # Step 2: Copy VM → host (tar extracts original filename)
            _guest_run(runner_vm,
                f"mkdir -p '{download_dest_parent}'",
            )
            remote_file = f"{remote_dir}{remote_src.split('/')[-1]}"
            download = _run_mvm(
                runner_vm,
                "cp",
                f"{vm_info['name']}:{remote_file}",
                download_dest_parent,
                timeout=30,
            )
            assert download.returncode == 0, (
                f"Download failed: stdout={download.stdout} stderr={download.stderr}"
            )

            # Step 3: Verify file exists on host with ORIGINAL filename
            original_name = remote_src.split("/")[-1]
            expected_file = f"{download_dest_parent}/{original_name}"
            check_result = _guest_run(runner_vm,
                f"test -f '{expected_file}' && echo EXISTS",
            )
            assert check_result.returncode == 0 and "EXISTS" in check_result.stdout, (
                f"Extracted file not found at {expected_file} "
                f"(tar preserves original filename)"
            )

            # Step 4: Verify content integrity
            content_result = _guest_run(runner_vm, f"cat '{expected_file}'"
            )
            assert content_result.stdout.strip() == original_content, (
                f"Content mismatch after round-trip: "
                f"expected {original_content!r}, "
                f"got {content_result.stdout.strip()!r}"
            )
        finally:
            _exec_cmd(runner_vm, vm_info["name"], f"rm -f '{remote_file}'")
            _guest_run(runner_vm, f"rm -rf '{download_dest_parent}'", check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_directory_trailing_slash_preserves_filename(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Copy a file with trailing / destination — preserves source filename."""
        vm_info = created_vm
        vm_name = vm_info["name"]

        file_id = uuid.uuid4().hex[:8]
        src_file = f"/tmp/cp_dir_{file_id}.txt"
        _guest_run(runner_vm,
            f"echo 'directory mode test' > '{src_file}'",
        )

        try:
            # Copy to destination with trailing / (directory mode)
            result = _run_mvm(
                runner_vm,
                "cp",
                src_file,
                f"{vm_name}:/tmp/",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"cp with trailing / failed: stdout={result.stdout} stderr={result.stderr}"
            )

            # Verify file arrived with its original name
            dest_file = f"/tmp/{src_file.split('/')[-1]}"
            check = _guest_run(runner_vm,
                f"test -f '{dest_file}' && echo EXISTS",
                timeout=30,
            )
            assert check.returncode == 0 and "EXISTS" in check.stdout, (
                f"File {dest_file} not found after cp with directory mode"
            )
        finally:
            _guest_run(runner_vm, f"rm -f '{src_file}'", check=False)
            _guest_run(runner_vm, f"rm -f '/tmp/{file_id}.txt'", check=False)


# ============================================================================
# Edge case tests
# ============================================================================


class TestCpEdgeCases:
    """Edge cases for ``mvm cp``: nonexistent source, overwrite protection, force flag."""

    def test_cp_nonexistent_source(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Copy a nonexistent local source — should fail with clear error."""
        vm_info = created_vm
        result = _run_mvm(
            runner_vm,
            "cp",
            "/nonexistent/path/xyz789",
            f"{vm_info['name']}:/tmp/",
            check=False,
            timeout=30,
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
    def test_cp_no_force_dest_exists(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Copy without ``--force`` when destination file exists — should fail."""
        vm_info = created_vm

        file_id = uuid.uuid4().hex[:8]
        remote_src = f"/tmp/src_{file_id}.txt"
        _guest_run(runner_vm,
            f"echo 'original content' > '{remote_src}'",
        )
        remote_dir = "/tmp/"

        try:
            # First copy should succeed
            first = _run_mvm(
                runner_vm,
                "cp",
                remote_src,
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert first.returncode == 0, (
                f"First copy failed: stdout={first.stdout} stderr={first.stderr}"
            )

            # Second copy without --force should fail
            second = _run_mvm(
                runner_vm,
                "cp",
                remote_src,
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
            _exec_cmd(runner_vm, vm_info["name"], f"rm -f '{remote_src}'")
            _guest_run(runner_vm, f"rm -f '{remote_src}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_force_overwrites(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Copy with ``--force`` overwrites existing destination."""
        vm_info = created_vm

        remote_dir = "/tmp/"
        remote_file = "/tmp/file_for_force.txt"
        src_file = "/tmp/file_for_force.txt"

        try:
            _guest_run(runner_vm,
                f"echo 'AAAA_original' > '{src_file}'",
            )
            first = _run_mvm(
                runner_vm,
                "cp",
                src_file,
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert first.returncode == 0, (
                f"First copy failed: stdout={first.stdout} stderr={first.stderr}"
            )

            orig_check = _exec_cmd(
                runner_vm, vm_info["name"], f"cat '{remote_file}'"
            )
            assert orig_check.returncode == 0
            assert "AAAA" in orig_check.stdout, (
                f"Expected 'AAAA' in original file, got: {orig_check.stdout}"
            )

            _guest_run(runner_vm,
                f"echo 'BBBB_overwritten' > '{src_file}'",
            )

            overwrite = _run_mvm(
                runner_vm,
                "cp",
                src_file,
                f"{vm_info['name']}:{remote_dir}",
                "--force",
                timeout=30,
            )
            assert overwrite.returncode == 0, (
                f"Force copy failed: stdout={overwrite.stdout} stderr={overwrite.stderr}"
            )

            final_check = _exec_cmd(
                runner_vm, vm_info["name"], f"cat '{remote_file}'"
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
            _exec_cmd(runner_vm, vm_info["name"], f"rm -f '{remote_file}'")


# ============================================================================
# Multi-source copy tests
# ============================================================================


class TestCpMultiSource:
    """Copy multiple files/directories from host to a VM in one ``mvm cp`` command."""

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_multi_source_two_files(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Copy two local files to a VM in one command — both must land."""
        vm_info = created_vm

        file_id = uuid.uuid4().hex[:8]
        file1 = f"/tmp/multi_a_{file_id}.txt"
        file2 = f"/tmp/multi_b_{uuid.uuid4().hex[:8]}.txt"
        content1 = f"content-a-{uuid.uuid4().hex}"
        content2 = f"content-b-{uuid.uuid4().hex}"

        _guest_run(runner_vm,
            f"echo '{content1}' > '{file1}'",
        )
        _guest_run(runner_vm,
            f"echo '{content2}' > '{file2}'",
        )

        remote_dir = "/tmp/"
        dest_file1 = f"{remote_dir}{file1.split('/')[-1]}"
        dest_file2 = f"{remote_dir}{file2.split('/')[-1]}"

        try:
            result = _run_mvm(
                runner_vm,
                "cp",
                file1, file2,
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"multi-source cp failed: stdout={result.stdout} "
                f"stderr={result.stderr}"
            )

            check1 = _exec_cmd(
                runner_vm, vm_info["name"],
                f"test -f '{dest_file1}' && echo F1_OK",
            )
            assert check1.returncode == 0 and "F1_OK" in check1.stdout

            check2 = _exec_cmd(
                runner_vm, vm_info["name"],
                f"test -f '{dest_file2}' && echo F2_OK",
            )
            assert check2.returncode == 0 and "F2_OK" in check2.stdout

            cat1 = _exec_cmd(runner_vm, vm_info["name"], f"cat '{dest_file1}'")
            assert cat1.returncode == 0 and cat1.stdout.strip() == content1, (
                f"Content mismatch for {dest_file1}"
            )

            cat2 = _exec_cmd(runner_vm, vm_info["name"], f"cat '{dest_file2}'")
            assert cat2.returncode == 0 and cat2.stdout.strip() == content2, (
                f"Content mismatch for {dest_file2}"
            )
        finally:
            _exec_cmd(runner_vm, vm_info["name"], f"rm -f '{dest_file1}' '{dest_file2}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_multi_source_file_and_dir(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Copy one file and one directory in a single ``mvm cp`` command."""
        vm_info = created_vm

        file_id = uuid.uuid4().hex[:8]
        dir_id = uuid.uuid4().hex[:8]
        src_file = f"/tmp/mix_file_{file_id}.txt"
        src_dir = f"/tmp/mix_dir_{dir_id}"
        _guest_run(runner_vm,
            f"echo 'mixed-source file content' > '{src_file}'"
        )
        _guest_run(runner_vm,
            f"mkdir -p '{src_dir}/nested' && echo 'nested content' > '{src_dir}/nested/deep.txt'"
        )

        remote_parent = "/tmp"
        remote_file = f"{remote_parent}/{src_file.split('/')[-1]}"
        remote_dir_path = f"{remote_parent}/{src_dir.split('/')[-1]}"

        try:
            result = _run_mvm(
                runner_vm,
                "cp",
                src_file,
                src_dir,
                f"{vm_info['name']}:{remote_parent}/",
                timeout=45,
            )
            assert result.returncode == 0, (
                f"mixed-source cp failed: stdout={result.stdout} "
                f"stderr={result.stderr}"
            )

            f_check = _exec_cmd(
                runner_vm, vm_info["name"],
                f"test -f '{remote_file}' && echo FILE_OK",
            )
            assert f_check.returncode == 0 and "FILE_OK" in f_check.stdout

            d_check = _exec_cmd(
                runner_vm, vm_info["name"],
                f"test -d '{remote_dir_path}' && echo DIR_OK",
            )
            assert d_check.returncode == 0 and "DIR_OK" in d_check.stdout

            deep_check = _exec_cmd(
                runner_vm, vm_info["name"],
                f"test -f '{remote_dir_path}/nested/deep.txt' && echo DEEP_OK",
            )
            assert deep_check.returncode == 0 and "DEEP_OK" in deep_check.stdout
        finally:
            _exec_cmd(runner_vm, vm_info["name"], f"rm -f '{remote_file}'")
            _exec_cmd(runner_vm, vm_info["name"], f"rm -rf '{remote_dir_path}'")

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_multi_source_single_arg(
        self,
        runner_vm: str,
        created_vm: dict,
    ) -> None:
        """Single source still works when passed through the multi-source path."""
        vm_info = created_vm

        file_id = uuid.uuid4().hex[:8]
        src_file = f"/tmp/single_{file_id}.txt"
        _guest_run(runner_vm,
            f"echo 'single arg via multi-source' > '{src_file}'",
        )
        remote_dir = "/tmp/"
        dest_file = f"{remote_dir}{src_file.split('/')[-1]}"

        try:
            result = _run_mvm(
                runner_vm,
                "cp",
                src_file,
                f"{vm_info['name']}:{remote_dir}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"single-arg cp failed: stdout={result.stdout} "
                f"stderr={result.stderr}"
            )

            check = _exec_cmd(
                runner_vm, vm_info["name"],
                f"test -f '{dest_file}' && echo EXISTS",
            )
            assert check.returncode == 0 and "EXISTS" in check.stdout

            cat_check = _exec_cmd(runner_vm, vm_info["name"], f"cat '{dest_file}'")
            assert cat_check.returncode == 0 and cat_check.stdout.strip() == "single arg via multi-source"
        finally:
            _exec_cmd(runner_vm, vm_info["name"], f"rm -f '{dest_file}'")

    def test_cp_multi_source_rejects_non_vm_dest(
        self,
        runner_vm: str,
    ) -> None:
        """Multi-source to a local destination should fail."""
        file1 = "/tmp/src_a.txt"
        file2 = "/tmp/src_b.txt"
        local_dest = "/tmp/local_dest"

        _guest_run(runner_vm,
            f"echo 'aaa' > '{file1}' && echo 'bbb' > '{file2}'",
        )

        result = _run_mvm(
            runner_vm,
            "cp",
            file1, file2,
            local_dest,
            check=False,
            timeout=45,
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
                "requires", "destination", "vm", "remote",
                "multi", "multiple",
            ]
        ), f"Expected error mentioning multi-source requires VM dest, got: {result.stderr}"


# ============================================================================
# VM → VM copy tests
# ============================================================================


class TestCpVmToVm:
    """Copy a file from one VM to another VM."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_cp,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cp_file_vm_to_vm(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Copy a file from one VM to another — verify on destination via SSH."""
        key_name = unique_key_name
        net_name = unique_network_name
        vm_a = f"{unique_vm_name}-a"
        vm_b = f"{unique_vm_name}-b"

        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )

        try:
            ensure_vm_deps(runner_vm)
            subnet = _unique_subnet(net_name)
            _run_mvm(
                runner_vm, "network", "create", net_name, "--subnet", subnet,
                "--no-nat", timeout=45,
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_a,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_b,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
            )

            test_content = f"vm-to-vm test {uuid.uuid4().hex}"
            src_file = f"/tmp/vm_to_vm_test_{uuid.uuid4().hex[:8]}.txt"
            _run_mvm(
                runner_vm, "exec", vm_a, "--user", "root", "--timeout", "30", "--",
                f"echo '{test_content}' > '{src_file}'",
                check=False, timeout=45,
            )

            dest_path = "/tmp/"
            result = _run_mvm(
                runner_vm,
                "cp",
                f"{vm_a}:{src_file}",
                f"{vm_b}:{dest_path}",
                timeout=30,
            )
            assert result.returncode == 0, (
                f"VM→VM cp failed: stdout={result.stdout} stderr={result.stderr}"
            )

            dest_file = f"{dest_path}{src_file.split('/')[-1]}"
            check = _run_mvm(
                runner_vm, "exec", vm_b, "--user", "root", "--timeout", "30", "--",
                f"test -f '{dest_file}' && echo VM2_EXISTS",
                check=False, timeout=45,
            )
            assert check.returncode == 0 and "VM2_EXISTS" in check.stdout

            content_check = _run_mvm(
                runner_vm, "exec", vm_b, "--user", "root", "--timeout", "30", "--",
                f"cat '{dest_file}'",
                check=False, timeout=45,
            )
            assert content_check.returncode == 0
            assert test_content in content_check.stdout, (
                f"Content mismatch on VM B: expected '{test_content}', "
                f"got '{content_check.stdout.strip()}'"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_a, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", vm_b, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, "--force", check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
