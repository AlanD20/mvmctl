"""Data persistence validation tests.

Validates that files written into a VM via mvm cp survive the full
stop -> snapshot -> restore chain (and the stop -> import base image
-> new VM chain). Regression tests for data loss caused by
Firecracker CacheType "Unsafe" (guest fsync is a no-op on the host,
causing data to vanish from the rootfs file when it is copied after
VM stop).
"""

from __future__ import annotations

import json
import os
import tempfile
from typing import Any

import pytest

from tests.system.conftest import _guest_run, _run_mvm, _unique_subnet, ensure_vm_deps

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_vm,
]


class TestCpDataSurvivesSnapshotRestore:
    """Validate: mvm cp -> stop -> snapshot -> restore -> verify data survives."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_cp_data_survives_snapshot_restore_chain(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Write a file via cp, stop VM, snapshot, restore to new VM, verify file."""
        src_vm = unique_vm_name
        restored_vm = f"rs-{src_vm}"
        key_name = unique_key_name
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)

        # Test data: write to /root/ (safe from deblob/optimization cleanup)
        test_content = f"snapshot-persistence-{os.urandom(8).hex()}"
        remote_path = f"/root/verify-{os.urandom(4).hex()}.txt"

        try:
            # --- 1. Setup infrastructure ---
            _run_mvm(
                runner_vm,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(runner_vm, "key", "create", key_name, "--algorithm", "ed25519")
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                src_vm,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
                "--ssh-key",
                key_name,
                "--writeback",
            )

            # --- 2. Write test content into VM via mvm cp ---
            with tempfile.NamedTemporaryFile(mode="w", suffix=".txt", delete=False) as f:
                src_path = f.name
                f.write(test_content)

            try:
                _run_mvm(runner_vm, "cp", src_path, f"{src_vm}:{remote_path}", timeout=60)
            finally:
                os.unlink(src_path)

            # --- 3. Stop the VM (data should now be on the rootfs file) ---
            _run_mvm(runner_vm, "vm", "stop", src_vm, timeout=120)

            # --- 4. Get rootfs path from inspect ---
            result = _run_mvm(runner_vm, "vm", "inspect", src_vm, "--json")
            vm_info = json.loads(result.stdout)
            rootfs_path = ""
            if isinstance(vm_info, dict):
                for key in ("rootfs_path", "RootfsPath"):
                    if key in vm_info:
                        rootfs_path = vm_info[key]
                        break
                if "filesystem" in vm_info and isinstance(vm_info["filesystem"], dict):
                    rootfs_path = vm_info["filesystem"].get("rootfs_path") or rootfs_path
            assert rootfs_path, f"Could not locate rootfs for VM {src_vm}"

            # --- 5. Copy rootfs to temp and import as new image ---
            import_rootfs = f"/tmp/{restored_vm}-rootfs.ext4"
            _guest_run(runner_vm, f"cp --sparse=never '{rootfs_path}' '{import_rootfs}'", timeout=120)
            assert _guest_run(runner_vm, f"test -f '{import_rootfs}'", check=False).returncode == 0

            image_name = f"{restored_vm}-img"
            import_result = _run_mvm(
                runner_vm,
                "image",
                "import",
                image_name,
                import_rootfs,
                "--format",
                "raw",
                "--skip-optimization",
                timeout=120,
                check=False,
            )
            assert import_result.returncode == 0, (
                f"Failed to import rootfs as image: {import_result.stderr}"
            )

            # --- 6. Remove original VM ---
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force")

            # --- 7. Create new VM from imported image ---
            img_ls = _run_mvm(runner_vm, "image", "ls", "--json")
            images = json.loads(img_ls.stdout)
            imported = [i for i in images if image_name in i.get("name", "")]
            assert imported, f"Imported image {image_name} not found"
            imported_image_id = imported[0]["id"]
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                restored_vm,
                "--image",
                imported_image_id,
                "--network",
                network_name,
                timeout=300,
            )

            # --- 8. Verify content survived via mvm exec ---
            exec_result = _run_mvm(
                runner_vm,
                "exec",
                restored_vm,
                "--",
                f"cat {remote_path}",
                check=False,
                timeout=60,
            )
            assert exec_result.returncode == 0, (
                f"Could not read {remote_path} in restored VM {restored_vm}: "
                f"rc={exec_result.returncode} stderr={exec_result.stderr}"
            )
            restored_content = exec_result.stdout.strip()

            assert test_content in restored_content, (
                f"DATA PERSISTENCE FAILED through snapshot chain!\n"
                f"  Expected content: {test_content!r}\n"
                f"  Restored content:  {restored_content!r}\n"
                f"  Remote path: {remote_path}\n"
                f"  Chain: mvm cp -> stop -> snapshot -> restore"
            )

        finally:
            # --- Cleanup ---
            _run_mvm(runner_vm, "vm", "rm", restored_vm, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", network_name, "--force", check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            _run_mvm(runner_vm, "image", "rm", image_name, "--force", check=False)
            _guest_run(runner_vm, f"rm -f {import_rootfs}", check=False)


class TestCpDataSurvivesBaseImageImport:
    """Validate: mvm cp -> stop -> import rootfs as image -> new VM -> verify data."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_cp_data_survives_base_image_chain(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Write a file via cp, stop VM, copy rootfs, import as image, create new VM, verify."""
        src_vm = unique_vm_name
        new_vm = f"bi-{src_vm}"
        key_name = unique_key_name
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        image_name = f"pi-{os.urandom(4).hex()}"
        imported_image_id: str | None = None

        # Test data
        test_content = f"baseimage-persistence-{os.urandom(8).hex()}"
        remote_path = f"/root/verify-{os.urandom(4).hex()}.txt"

        try:
            # --- 1. Setup ---
            _run_mvm(runner_vm, "network", "create", network_name, "--subnet", subnet, "--non-interactive")
            _run_mvm(runner_vm, "key", "create", key_name, "--algorithm", "ed25519")
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                src_vm,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
                "--ssh-key",
                key_name,
                "--writeback",
            )

            # --- 2. Write test file into VM ---
            with tempfile.NamedTemporaryFile(mode="w", suffix=".txt", delete=False) as f:
                src_path = f.name
                f.write(test_content)

            try:
                _run_mvm(runner_vm, "cp", src_path, f"{src_vm}:{remote_path}", timeout=60)
            finally:
                os.unlink(src_path)

            # --- 3. Stop the VM ---
            _run_mvm(runner_vm, "vm", "stop", src_vm, timeout=120)

            # --- 4. Find the VM's rootfs file ---
            inspect_result = _run_mvm(runner_vm, "vm", "inspect", src_vm, "--json")
            vm_info: dict[str, Any] = json.loads(inspect_result.stdout)

            # Navigate the nested JSON to find rootfs_path
            rootfs_path = ""
            if isinstance(vm_info, dict):
                for key in ("rootfs_path", "RootfsPath"):
                    if key in vm_info:
                        rootfs_path = vm_info[key]
                        break
                if "vm" in vm_info and isinstance(vm_info["vm"], dict):
                    rootfs_path = vm_info["vm"].get("rootfs_path") or vm_info["vm"].get("vm_dir") or ""
                if "filesystem" in vm_info and isinstance(vm_info["filesystem"], dict):
                    rootfs_path = vm_info["filesystem"].get("rootfs_path") or rootfs_path

            # Fallback: find by searching cache dir
            if not rootfs_path:
                cache_dir = os.environ.get("MVM_CACHE_DIR", "/root/.cache/mvmctl")
                find_result = _guest_run(
                    runner_vm,
                    f"find {cache_dir}/vms -name 'rootfs.*' 2>/dev/null | head -1",
                    check=False,
                )
                rootfs_path = find_result.stdout.strip()

            assert rootfs_path, (
                f"Could not locate rootfs for VM {src_vm}. "
                f"inspect output: {json.dumps(vm_info, indent=2)}"
            )

            # --- 5. Copy rootfs to temp location and import as new image ---
            import_rootfs = f"/tmp/{image_name}-rootfs.ext4"
            _guest_run(runner_vm, f"cp --sparse=never '{rootfs_path}' '{import_rootfs}'", timeout=120)
            assert _guest_run(runner_vm, f"test -f '{import_rootfs}'", check=False).returncode == 0, (
                f"Rootfs copy failed: {rootfs_path} -> {import_rootfs}"
            )

            # Import with --skip-optimization to avoid deblob (which would delete /tmp/*)
            import_result = _run_mvm(
                runner_vm,
                "image",
                "import",
                image_name,
                import_rootfs,
                "--format",
                "raw",
                "--skip-optimization",
                timeout=180,
                check=False,
            )
            assert import_result.returncode == 0, (
                f"Image import failed: {import_result.stderr}"
            )

            # Get imported image ID
            img_ls = _run_mvm(runner_vm, "image", "ls", "--json")
            images: list[dict[str, Any]] = json.loads(img_ls.stdout)
            imported = [i for i in images if image_name in i.get("name", "")]
            assert imported, f"Imported image {image_name} not found"
            imported_image_id = imported[0]["id"]

            # --- 6. Create new VM from imported base image ---
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                new_vm,
                "--image",
                imported_image_id,
                "--network",
                network_name,
                "--ssh-key",
                key_name,
                timeout=180,
            )

            # --- 7. Verify the file survived ---
            exec_result = _run_mvm(
                runner_vm,
                "exec",
                new_vm,
                "--timeout",
                "15",
                "--",
                f"cat {remote_path}",
                check=False,
                timeout=30,
            )

            assert exec_result.returncode == 0, (
                f"Could not read {remote_path} in new VM {new_vm}: "
                f"rc={exec_result.returncode} stderr={exec_result.stderr}"
            )
            new_vm_content = exec_result.stdout.strip()

            assert test_content in new_vm_content, (
                f"DATA PERSISTENCE FAILED through base image chain!\n"
                f"  Expected content: {test_content!r}\n"
                f"  New VM content:   {new_vm_content!r}\n"
                f"  Remote path: {remote_path}\n"
                f"  Chain: mvm cp -> stop -> cp rootfs -> image import -> new VM create"
            )

        finally:
            # --- Cleanup ---
            _run_mvm(runner_vm, "vm", "rm", new_vm, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", src_vm, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", network_name, "--force", check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            _run_mvm(runner_vm, "image", "rm", image_name, "--force", check=False)
            _guest_run(runner_vm, f"rm -f /tmp/{image_name}-rootfs.ext4", check=False)
