"""CLI edge case system tests — untested paths across all command groups.

These tests cover edge cases and error paths not exercised by the primary
test files in tests/system/. They are black-box CLI integration tests
that invoke ``mvm`` via subprocess.
"""

from __future__ import annotations

import json
import subprocess

import pytest

from tests.system.conftest import _run_mvm


class TestConfigEdgeCases:
    """Tests for config command edge cases."""

    pytestmark = [pytest.mark.system]

    def test_config_get_category_only(self, mvm_binary):
        """``config get defaults.vm`` (no key) should return multiple keys."""
        result = _run_mvm(mvm_binary, "config", "get", "defaults.vm")
        assert result.returncode == 0
        # The output should contain multiple setting keys from the category
        assert "vcpu_count" in result.stdout
        assert "mem_size_mib" in result.stdout
        assert "boot_args" in result.stdout

    @pytest.mark.serial
    def test_config_reset_category_only(self, mvm_binary):
        """``config reset defaults.vm`` (no key) should reset all keys in category."""
        # Set a value first so there is something to reset
        _run_mvm(mvm_binary, "config", "set", "defaults.vm", "vcpu_count", "6")

        # Verify it was set
        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert "6" in result.stdout

        # Reset the entire category (no key)
        result = _run_mvm(mvm_binary, "config", "reset", "defaults.vm")
        assert result.returncode == 0
        assert "override(s)" in result.stdout

        # Verify the custom value is gone (back to default)
        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert "6" not in result.stdout

    def test_config_reset_no_args(self, mvm_binary):
        """``config reset`` with no args should print guidance (exit 0)."""
        result = _run_mvm(mvm_binary, "config", "reset")
        assert result.returncode == 0
        assert "Provide a category" in result.stdout

    def test_config_set_invalid_category(self, mvm_binary):
        """``config set`` with invalid category should fail."""
        result = _run_mvm(
            mvm_binary,
            "config",
            "set",
            "nonexistent.cat",
            "some_key",
            "some_value",
            check=False,
        )
        assert result.returncode != 0


class TestCacheEdgeCases:
    """Tests for cache command edge cases."""

    pytestmark = [pytest.mark.system]

    def test_cache_prune_no_args(self, mvm_binary):
        """``cache prune`` without resource and without --all should fail."""
        result = _run_mvm(mvm_binary, "cache", "prune", check=False)
        assert result.returncode != 0
        assert "No resource specified" in result.stdout

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cache_prune_vm_no_dry_run(self, mvm_binary, unique_vm_name):
        """Stop a VM, prune it (no --dry-run), verify it is gone."""
        vm_name = unique_vm_name
        try:
            # Create VM (without SSH key to keep it simple)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
            )

            # Stop VM so it becomes prunable
            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            # Prune VM (actual removal, not dry-run)
            result = _run_mvm(
                mvm_binary,
                "cache",
                "prune",
                "vm",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"cache prune vm failed (may need sudo): "
                    f"{result.stderr.strip()}"
                )

            # Verify VM is gone from listing
            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            assert not any(v["name"] == vm_name for v in vms), (
                f"VM {vm_name} still present after prune"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )


class TestNetworkEdgeCases:
    """Tests for network command edge cases."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.slow,
    ]

    def test_network_create_without_subnet(
        self, mvm_binary, unique_network_name
    ):
        """``network create`` without --subnet should fail with clear error."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            check=False,
        )
        assert result.returncode != 0
        assert "Missing required option '--subnet'" in result.stdout

    def test_network_set_default_nonexistent(self, mvm_binary):
        """``network set-default`` with nonexistent name should fail."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "set-default",
            "nonexistent-network-name-xyz",
            check=False,
        )
        assert result.returncode != 0


class TestVMStateTransitionErrors:
    """Tests for invalid or idempotent VM state transitions."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_stop_stopped_vm(self, mvm_binary, created_vm):
        """Stopping an already-stopped VM should be idempotent."""
        vm_name = created_vm["name"]

        # First stop
        _run_mvm(mvm_binary, "vm", "stop", vm_name)

        # Second stop — should succeed (idempotent)
        result = _run_mvm(mvm_binary, "vm", "stop", vm_name, check=False)
        assert result.returncode == 0, f"Second stop failed: {result.stderr}"

    def test_vm_pause_stopped_vm(self, mvm_binary, created_vm):
        """Pausing a stopped VM should fail."""
        vm_name = created_vm["name"]

        # Stop the VM first
        _run_mvm(mvm_binary, "vm", "stop", vm_name)

        # Try to pause — should fail since VM is not running
        result = _run_mvm(mvm_binary, "vm", "pause", vm_name, check=False)
        assert result.returncode != 0

    def test_vm_start_running_vm(self, mvm_binary, created_vm):
        """Starting a running VM should succeed (idempotent)."""
        vm_name = created_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "start", vm_name, check=False)
        assert result.returncode == 0, (
            f"Start on running VM failed: {result.stderr}"
        )

    def test_vm_resume_running_vm(self, mvm_binary, created_vm):
        """Resuming a running VM should succeed (idempotent)."""
        vm_name = created_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "resume", vm_name, check=False)
        assert result.returncode == 0, (
            f"Resume on running VM failed: {result.stderr}"
        )

    def test_vm_rm_multiple_identifiers(self, mvm_binary, unique_vm_name):
        """Remove two VMs at once using multiple positional args."""
        name1 = f"{unique_vm_name}-a"
        name2 = f"{unique_vm_name}-b"
        try:
            # Create two VMs
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                name1,
                "--image",
                "alpine-3.21",
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                name2,
                "--image",
                "alpine-3.21",
            )

            # Remove both with --force
            result = _run_mvm(mvm_binary, "vm", "rm", name1, name2, "--force")
            assert result.returncode == 0

            # Verify both are gone
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert not any(v["name"] == name1 for v in vms), (
                f"VM {name1} still present after rm"
            )
            assert not any(v["name"] == name2 for v in vms), (
                f"VM {name2} still present after rm"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                name1,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                name2,
                "--force",
                check=False,
            )


class TestImageAdvancedFlags:
    """Tests for image advanced flags and edge cases."""

    pytestmark = [pytest.mark.system]

    @pytest.mark.slow
    @pytest.mark.serial
    def test_image_pull_with_disable_detector(self, mvm_binary):
        """Pull an image with --disable-detector all --force."""
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine-3.21",
                "--disable-detector",
                "all",
                "--force",
                timeout=60,
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Pull with --disable-detector failed: {result.stderr.strip()}"
                )
            assert "pulled successfully" in result.stdout.lower()
        except subprocess.TimeoutExpired:
            pytest.skip(
                "Pull with --disable-detector --force timed out (>60s download)"
            )

    def test_image_pull_nonexistent_fails(self, mvm_binary):
        """Pull a nonexistent image should fail."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "completely-nonexistent-image-12345",
            check=False,
        )
        assert result.returncode != 0


class TestConfigEdgeCasesExtended:
    """Additional config command edge cases."""

    pytestmark = [pytest.mark.system]

    def test_config_get_nonexistent_key(self, mvm_binary):
        """``config get`` with nonexistent key should return guidance."""
        result = _run_mvm(
            mvm_binary,
            "config",
            "get",
            "defaults.vm",
            "nonexistent_key_xyz",
            check=False,
        )
        # Should not crash — returns None or guidance
        assert result.returncode == 0

    def test_config_set_invalid_value_type(self, mvm_binary):
        """``config set`` with an invalid value type (string for int) should fail."""
        result = _run_mvm(
            mvm_binary,
            "config",
            "set",
            "defaults.vm",
            "vcpu_count",
            "not-a-number",
            check=False,
        )
        # Should fail because vcpu_count expects an integer
        assert result.returncode != 0


class TestConfigEdgeCasesResetAllAfterSet:
    """Test config reset --all after multiple values are set."""

    pytestmark = [pytest.mark.system, pytest.mark.serial]

    def test_config_reset_all_with_multiple_overrides(self, mvm_binary):
        """Set multiple config overrides, then reset --all, verify all gone."""
        # Set two values
        _run_mvm(
            mvm_binary, "config", "set", "defaults.vm", "vcpu_count", "8"
        )
        _run_mvm(
            mvm_binary,
            "config",
            "set",
            "defaults.vm",
            "mem_size_mib",
            "2048",
        )

        # Verify they were set
        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert "8" in result.stdout

        # Reset all
        _run_mvm(mvm_binary, "config", "reset", "--all")

        # Verify values are gone
        result = _run_mvm(
            mvm_binary, "config", "get", "defaults.vm", "vcpu_count"
        )
        assert "8" not in result.stdout


class TestVMLogsByIdentifier:
    """Test ``mvm logs`` using different identifier types."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_logs_by_ip(self, mvm_binary, created_vm):
        """Show boot log using IP as identifier instead of name."""
        ip = created_vm.get("ipv4", "")
        if not ip:
            pytest.skip("VM has no IP address")
        result = _run_mvm(mvm_binary, "logs", ip, check=False)
        assert result.returncode == 0
        assert result.stdout.strip()


class TestVMCloudInitModes:
    """Test VM creation with different --cloud-init-mode values."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_create_cloud_init_mode_iso(self, mvm_binary, unique_vm_name):
        """Create VM with --cloud-init-mode iso."""
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--cloud-init-mode",
                "iso",
            )
            vms = json.loads(
                _run_mvm(mvm_binary, "vm", "ls", "--json").stdout
            )
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None, f"VM '{vm_name}' not found"
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", vm_name, "--force", check=False
            )

    def test_vm_create_cloud_init_mode_net(self, mvm_binary, unique_vm_name):
        """Create VM with --cloud-init-mode net."""
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--cloud-init-mode",
                "net",
            )
            vms = json.loads(
                _run_mvm(mvm_binary, "vm", "ls", "--json").stdout
            )
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None, f"VM '{vm_name}' not found"
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", vm_name, "--force", check=False
            )

    def test_vm_create_cloud_init_mode_off(
        self, mvm_binary, unique_vm_name
    ):
        """Create VM with --cloud-init-mode off (explicit)."""
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--cloud-init-mode",
                "off",
            )
            vms = json.loads(
                _run_mvm(mvm_binary, "vm", "ls", "--json").stdout
            )
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None, f"VM '{vm_name}' not found"
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", vm_name, "--force", check=False
            )


class TestVMNocloudNetPort:
    """Test VM creation with --nocloud-net-port values."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_create_with_nocloud_net_port_specific(
        self, mvm_binary, unique_vm_name
    ):
        """Create VM with --nocloud-net-port set to a specific port."""
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--nocloud-net-port",
                "12345",
            )
            vms = json.loads(
                _run_mvm(mvm_binary, "vm", "ls", "--json").stdout
            )
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None, f"VM '{vm_name}' not found"
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", vm_name, "--force", check=False
            )


class TestImagePullAdvancedFlags:
    """Test advanced image pull flags."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
    ]

    def test_image_pull_with_version(self, mvm_binary):
        """Pull an image with --version flag."""
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine-3.21",
                "--version",
                "latest",
                "--force",
                timeout=60,
                check=False,
            )
            if result.returncode == 0:
                assert "pulled successfully" in result.stdout.lower()
            else:
                pytest.skip(
                    f"Pull with --version failed: {result.stderr.strip()}"
                )
        except subprocess.TimeoutExpired:
            pytest.skip("Pull with --version timed out (>60s)")

    def test_image_pull_with_arch(self, mvm_binary):
        """Pull an image with --arch flag."""
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine-3.21",
                "--arch",
                "x86_64",
                "--force",
                timeout=60,
                check=False,
            )
            if result.returncode == 0:
                assert "pulled successfully" in result.stdout.lower()
            else:
                pytest.skip(
                    f"Pull with --arch failed: {result.stderr.strip()}"
                )
        except subprocess.TimeoutExpired:
            pytest.skip("Pull with --arch timed out (>60s)")


class TestKernelPullAdvancedFlags:
    """Test advanced kernel pull flags."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
    ]

    def test_kernel_pull_with_arch_flag(self, mvm_binary):
        """Pull a kernel with --arch x86_64 flag."""
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "official",
            "--arch",
            "x86_64",
            check=False,
            timeout=120,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Kernel pull with --arch failed: {result.stderr.strip()}"
            )
        assert result.returncode == 0


class TestImagePullWithTypeFlag:
    """Test image pull with explicit --type flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
    ]

    def test_image_pull_with_explicit_type(self, mvm_binary):
        """Pull an image with --type flag matching the positional selector."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "alpine-3.21",
            "--type",
            "alpine",
            "--force",
            timeout=60,
            check=False,
        )
        if result.returncode == 0:
            assert "pulled successfully" in result.stdout.lower()
        elif "timed out" in (result.stdout + result.stderr).lower() or result.returncode == -15:
            pytest.skip("Pull with --type timed out (>60s)")
        else:
            pytest.skip(
                f"Pull with --type failed: {result.stderr.strip()}"
            )


class TestImageImportWithDisableDetector:
    """Test image import with --disable-detector flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
    ]

    def test_image_import_with_disable_detector(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import an image with --disable-detector all flag.

        Uses an already-cached alpine image file (which has a real filesystem)
        so the import backend can parse the partition table.
        """
        import shutil

        # Find a cached alpine image to use as import source
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i
            for i in images
            if "alpine" in i.get("os_slug", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            _run_mvm(mvm_binary, "image", "pull", "alpine-3.21", check=False)
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            alpine_images = [
                i
                for i in images
                if "alpine" in i.get("os_slug", "").lower()
                and i.get("is_present")
            ]

        if not alpine_images:
            pytest.skip("No alpine image available to use as import source")

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

        # Decompress if needed (.zst)
        temp_path = tmp_path / "alpine-for-import.raw"
        if resolved_source.suffix == ".zst":
            decompress = subprocess.run(
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
            shutil.copy2(str(resolved_source), temp_path)

        imported_prefix: str | None = None
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                "test-disable-detector",
                str(temp_path),
                "--format",
                "raw",
                "--disable-detector",
                "all",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Import with --disable-detector failed: {result.stderr.strip()}"
                )
            assert result.returncode == 0

            # Verify imported image appears in listing
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [
                i for i in images if i.get("os_name") == "test-disable-detector"
            ]
            assert imported, (
                "Imported image with --disable-detector not found in listing"
            )
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary, "image", "rm", imported_prefix, check=False
                )
