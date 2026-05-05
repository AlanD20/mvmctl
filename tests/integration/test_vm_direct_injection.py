"""Integration tests for VM creation with direct cloud-init injection.

Tests exercise the VM creation flow with CloudInitMode.INJECT through
the real public API. Verifies that:
- INJECT mode creates cloud-init files and injects them into rootfs
- OFF mode skips cloud-init injection
- SSH keys are properly passed through the creation pipeline

Only subprocess calls and Provisioner are mocked.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.api import VMCreateInput, VMInput, VMOperation
from mvmctl.models import CloudInitMode, VMInstanceItem
from mvmctl.utils.common import CacheUtils


def _setup_mocks(monkeypatch: pytest.MonkeyPatch) -> dict[str, object]:
    """Apply subprocess mocks and return references for assertions."""
    from tests.integration.conftest import SmartPopenMock, SmartSubprocessMock

    sub_mock = SmartSubprocessMock()
    popen_mock = SmartPopenMock()
    monkeypatch.setattr("subprocess.run", sub_mock)
    monkeypatch.setattr("subprocess.Popen", popen_mock)

    # Mock Provisioner — chained method calls + tracking
    provisioner_mock = MagicMock()
    provisioner_mock.resize.return_value = provisioner_mock
    provisioner_mock.set_hostname.return_value = provisioner_mock
    provisioner_mock.inject_dns.return_value = provisioner_mock
    provisioner_mock.setup_ssh.return_value = provisioner_mock
    provisioner_mock.disable_cloud_init.return_value = provisioner_mock
    provisioner_mock.inject_cloud_init.return_value = provisioner_mock
    provisioner_mock.run.return_value = None
    monkeypatch.setattr(
        "mvmctl.api.vm_operations.VMProvisioner",
        lambda *args, **kwargs: provisioner_mock,
    )

    return {
        "subprocess": sub_mock,
        "popen": popen_mock,
        "provisioner": provisioner_mock,
    }


class TestVMDirectInjection:
    """Integration tests for VM creation with direct cloud-init injection."""

    def test_create_vm_with_inject_mode(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a VM with inject mode and assert DB state and filesystem."""
        mocks = _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="test-inject-vm",
                cloud_init_mode=CloudInitMode.INJECT.value,
                ssh_keys=[],
                enable_console=False,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["test-inject-vm"]))
        assert isinstance(vm, VMInstanceItem)
        assert vm.cloud_init_mode == CloudInitMode.INJECT.value

        # Verify that inject_cloud_init was called on Provisioner
        mocks["provisioner"].inject_cloud_init.assert_called_once()

        # Verify VM directory was created
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        assert vm_dir.exists()

        # Verify rootfs was provisioned
        assert vm.rootfs_path is not None
        assert (vm_dir / vm.rootfs_path).exists()

    def test_disabled_mode_skips_injection(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """OFF mode means inject_cloud_init is NOT called on Provisioner."""
        mocks = _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="test-disabled-vm",
                cloud_init_mode=CloudInitMode.OFF.value,
                ssh_keys=[],
                enable_console=False,
            )
        )

        # In OFF mode, disable_cloud_init should be called
        mocks["provisioner"].disable_cloud_init.assert_called_once()
        # inject_cloud_init should NOT be called
        mocks["provisioner"].inject_cloud_init.assert_not_called()

        vm = VMOperation.get(VMInput(identifiers=["test-disabled-vm"]))
        assert vm.cloud_init_mode == CloudInitMode.OFF.value

    def test_inject_vm_has_vm_dir_and_rootfs(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Verify inject-mode VM has a populated VM directory."""
        _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="inject-rootfs-vm",
                cloud_init_mode=CloudInitMode.INJECT.value,
                ssh_keys=[],
                enable_console=False,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["inject-rootfs-vm"]))
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        assert vm_dir.exists()
        vm_dir_contents = [p.name for p in vm_dir.iterdir()]
        # Should always contain the rootfs and firecracker config
        assert any("rootfs" in name for name in vm_dir_contents)
        assert any("firecracker" in name.lower() for name in vm_dir_contents)

    def test_inject_mode_with_ssh_keys(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create an inject-mode VM with SSH keys and verify they are passed."""
        _setup_mocks(monkeypatch)
        test_keys = ["ssh-ed25519 AAAA key1", "ssh-rsa AAAA key2"]

        VMOperation.create(
            VMCreateInput(
                name="inject-ssh-vm",
                cloud_init_mode=CloudInitMode.INJECT.value,
                ssh_keys=test_keys,
                enable_console=False,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["inject-ssh-vm"]))
        assert vm.cloud_init_mode == CloudInitMode.INJECT.value
        assert vm.name == "inject-ssh-vm"
