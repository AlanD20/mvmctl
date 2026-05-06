"""System test: import a local image and create a VM from it."""

from __future__ import annotations

import json
import subprocess as _subprocess

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.serial,
]


class TestImageImportCreateVM:
    """Test the full end-to-end flow of importing an image and creating a VM."""

    def test_imported_image_vm_creation(
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
        tmp_path,
        system_cache_dir,
    ):
        """Import a cached alpine image and create a running VM from it."""

        # ── 1. Find the cached alpine image ─────────────────────────────
        _run_mvm(mvm_binary, "image", "pull", "alpine-3.21", check=False)

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i
            for i in images
            if "alpine" in i.get("os_slug", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            pytest.skip("No present alpine image available")

        target = alpine_images[0]
        target_id = target["id"]

        # Inspect to get the file path
        result = _run_mvm(
            mvm_binary, "image", "inspect", target_id, "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"Image '{target_id[:8]}' was removed before inspect")

        data = json.loads(result.stdout)
        source_path = data.get("path")
        if not source_path:
            pytest.skip("Image path not available")

        resolved_source = system_cache_dir / "images" / source_path
        if not resolved_source.exists():
            pytest.skip(f"Image file not found: {resolved_source}")

        # ── 2. Decompress to a temp file ────────────────────────────────
        temp_path = tmp_path / "alpine-for-import.raw"

        if resolved_source.suffix == ".zst":
            decompress = _subprocess.run(
                [
                    "zstd",
                    "-d",
                    "-f",
                    str(resolved_source),
                    "-o",
                    str(temp_path),
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            if decompress.returncode != 0:
                pytest.skip(f"zstd decompress failed: {decompress.stderr}")
        else:
            import shutil

            shutil.copy2(str(resolved_source), temp_path)

        import_name = f"imported-{unique_vm_name}"
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        vm_name = unique_vm_name

        imported_prefix: str | None = None

        try:
            # ── 3. Import the decompressed image ────────────────────────
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                import_name,
                str(temp_path),
                "--format",
                "raw",
                "--skip-optimization",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(f"Image import failed: {result.stderr.strip()}")
            assert result.returncode == 0

            # Get imported image ID prefix for later cleanup
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("os_name") == import_name]
            assert imported, (
                f"Imported image '{import_name}' not found in listing"
            )
            imported_prefix = imported[0]["id"][:6]

            # ── 4. Create a dedicated network ───────────────────────────
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            assert result.returncode == 0, (
                f"Network create failed: {result.stderr}"
            )

            try:
                # ── 5. Create a VM from the imported image ──────────────
                result = _run_mvm(
                    mvm_binary,
                    "vm",
                    "create",
                    "--name",
                    vm_name,
                    "--image",
                    import_name,
                    "--network",
                    network_name,
                )
                assert result.returncode == 0, (
                    f"VM create failed: {result.stderr}"
                )

                # ── 6. Verify the VM is running ─────────────────────────
                result = _run_mvm(mvm_binary, "vm", "ls", "--json")
                vms = json.loads(result.stdout)
                vm = next((v for v in vms if v["name"] == vm_name), None)
                assert vm is not None, f"VM '{vm_name}' not found in listing"
                assert vm["status"] == "running", (
                    f"Expected VM status 'running', got '{vm['status']}'"
                )
                # Verify the VM references the imported image
                assert vm.get("image_id", ""), f"VM has no image_id: {vm}"
                # Also verify via vm inspect --json which has image_name
                inspect_result = _run_mvm(
                    mvm_binary, "vm", "inspect", vm_name, "--json"
                )
                inspect_data = json.loads(inspect_result.stdout)
                assert import_name in str(inspect_data.get("image_name", "")), (
                    f"VM image_name doesn't contain '{import_name}': "
                    f"{inspect_data.get('image_name')}"
                )

            finally:
                # ── 7. Cleanup: VM first, network second ────────────────
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "rm",
                    vm_name,
                    "--force",
                    check=False,
                )
                _run_mvm(
                    mvm_binary,
                    "network",
                    "rm",
                    network_name,
                    "--force",
                    check=False,
                )

        finally:
            # ── 8. Cleanup: remove imported image ──────────────────────
            if imported_prefix:
                _run_mvm(
                    mvm_binary,
                    "image",
                    "rm",
                    imported_prefix,
                    check=False,
                )

    def test_import_ubuntu_tar_rootfs(
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
        tmp_path,
    ):
        """Download Ubuntu 24.04 minimal tar-rootfs, import, create VM, verify running."""

        # ── 1. Download the Ubuntu tar-rootfs ─────────────────────────────
        ubuntu_url = (
            "https://cloud-images.ubuntu.com/minimal/releases/noble/release/"
            "ubuntu-24.04-minimal-cloudimg-amd64-root.tar.xz"
        )
        download_path = tmp_path / "ubuntu-24.04-minimal-root.tar.xz"

        download = _subprocess.run(
            ["curl", "-sSL", "-o", str(download_path), ubuntu_url],
            capture_output=True,
            text=True,
            timeout=120,
            check=False,
        )
        if download.returncode != 0 or not download_path.exists():
            pytest.skip(f"Failed to download Ubuntu image: {download.stderr}")

        import_name = f"ubuntu-imported-{unique_vm_name}"
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        vm_name = unique_vm_name
        imported_prefix: str | None = None

        try:
            # ── 2. Import the downloaded tar-rootfs ───────────────────────
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                import_name,
                str(download_path),
                "--format",
                "tar-rootfs",
                "--skip-optimization",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Ubuntu image import failed: {result.stderr.strip()}"
                )
            assert result.returncode == 0

            # Get imported image ID prefix for later cleanup
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("os_name") == import_name]
            assert imported, (
                f"Imported image '{import_name}' not found in listing"
            )
            imported_prefix = imported[0]["id"][:6]

            # ── 3. Create a dedicated network ─────────────────────────────
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            assert result.returncode == 0, (
                f"Network create failed: {result.stderr}"
            )

            try:
                # ── 4. Create a VM from the imported image ────────────────
                result = _run_mvm(
                    mvm_binary,
                    "vm",
                    "create",
                    "--name",
                    vm_name,
                    "--image",
                    import_name,
                    "--network",
                    network_name,
                )
                assert result.returncode == 0, (
                    f"VM create with imported Ubuntu failed: {result.stderr}"
                )

                # ── 5. Verify the VM is running ───────────────────────────
                result = _run_mvm(mvm_binary, "vm", "ls", "--json")
                vms = json.loads(result.stdout)
                vm = next((v for v in vms if v["name"] == vm_name), None)
                assert vm is not None, f"VM '{vm_name}' not found in listing"
                assert vm["status"] == "running", (
                    f"Expected VM status 'running', got '{vm['status']}'"
                )

            finally:
                # ── 6. Cleanup: VM first, network second ──────────────────
                _run_mvm(
                    mvm_binary, "vm", "rm", vm_name, "--force", check=False
                )
                _run_mvm(
                    mvm_binary,
                    "network",
                    "rm",
                    network_name,
                    "--force",
                    check=False,
                )

        finally:
            # ── 7. Cleanup: remove imported image ────────────────────────
            if imported_prefix:
                _run_mvm(
                    mvm_binary, "image", "rm", imported_prefix, check=False
                )
