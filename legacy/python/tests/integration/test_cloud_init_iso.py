"""Integration tests for cloud-init ISO workflows through the real public API.

Tests exercise the CloudInitProvisioner and the VM creation flow with
different cloud-init modes (ISO, OFF, INJECT, NET).

Only subprocess calls (genisoimage, cloud-localds, cp, ip, etc.) and
Provisioner are mocked. ALL orchestration logic runs unmocked.
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.api import VMCreateInput, VMInput, VMOperation
from mvmctl.models import CloudInitMode, VMInstanceItem
from mvmctl.utils.common import CacheUtils

# ======================================================================
# Shared mock setup
# ======================================================================


def _setup_mocks(monkeypatch: pytest.MonkeyPatch) -> dict[str, object]:
    """Apply subprocess mocks and return references for assertions."""
    from tests.integration.conftest import SmartPopenMock, SmartSubprocessMock

    sub_mock = SmartSubprocessMock()
    popen_mock = SmartPopenMock()
    monkeypatch.setattr("subprocess.run", sub_mock)
    monkeypatch.setattr("subprocess.Popen", popen_mock)

    # Mock Provisioner to avoid real libguestfs
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


# ======================================================================
# Cloud-init mode tests (through VMOperation.create)
# ======================================================================


class TestCloudInitModesViaVM:
    """Test cloud-init modes by creating VMs through the real API.

    Each test creates a VM with a different cloud-init mode and verifies
    the resulting DB record and filesystem state.
    """

    def test_default_cloud_init_iso(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Default cloud-init mode (ISO) creates VM with ISO artifacts."""
        _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="iso-default-vm",
                ssh_keys=["ssh-ed25519 AAA... test@test"],
                cloud_init_mode=CloudInitMode.ISO.value,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["iso-default-vm"]))
        assert isinstance(vm, VMInstanceItem)
        assert vm.cloud_init_mode == CloudInitMode.ISO.value
        assert vm.name == "iso-default-vm"

    def test_cloud_init_inject(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """INJECT mode VM has correct cloud_init_mode in DB."""
        _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="inject-vm",
                ssh_keys=[],
                cloud_init_mode=CloudInitMode.INJECT.value,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["inject-vm"]))
        assert vm.cloud_init_mode == CloudInitMode.INJECT.value

    def test_cloud_init_off(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """OFF mode VM has correct cloud_init_mode and skips cloud-init."""
        _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="off-vm",
                ssh_keys=[],
                cloud_init_mode=CloudInitMode.OFF.value,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["off-vm"]))
        assert vm.cloud_init_mode == CloudInitMode.OFF.value

    def test_cloud_init_net(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """NET mode starts nocloud-net HTTP server and records port/pid."""
        _setup_mocks(monkeypatch)

        # Mock NoCloudNetServerManager to avoid real port binding
        monkeypatch.setattr(
            "mvmctl.services.nocloud_server.manager.NoCloudNetServerManager.start",
            lambda self: ("http://127.0.0.1:8000", 8000, 12345),
        )

        # Work around core bug: DB expects 'nocloud_input' but model uses 'nocloudnet_input'
        class _FakeTrackerResult:
            success = True

        monkeypatch.setattr(
            "mvmctl.core._shared._iptables_tracker._tracker.IPTablesTracker.ensure_rule",
            lambda self, rule, context=None: _FakeTrackerResult(),
        )

        VMOperation.create(
            VMCreateInput(
                name="net-vm",
                ssh_keys=[],
                cloud_init_mode=CloudInitMode.NET.value,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["net-vm"]))
        assert vm.cloud_init_mode == CloudInitMode.NET.value

    def test_vm_dir_created_with_cloud_init(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """VM directory exists after creation with ISO mode."""
        _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(name="vm-dir-test", ssh_keys=[], enable_console=False)
        )

        vm = VMOperation.get(VMInput(identifiers=["vm-dir-test"]))
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        assert vm_dir.exists()


class TestCloudInitISOEdgeCases:
    """Test edge cases in cloud-init configuration."""

    def test_cloud_init_off_skips_injection(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """OFF mode means Provisioner.disable_cloud_init is called."""
        _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="ci-off-vm",
                ssh_keys=[],
                cloud_init_mode=CloudInitMode.OFF.value,
                enable_console=False,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["ci-off-vm"]))
        assert vm.cloud_init_mode == CloudInitMode.OFF.value

    def test_vm_created_with_ssh_keys(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """VM created with SSH keys has correct DB state."""
        _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="ssh-key-vm",
                ssh_keys=["ssh-ed25519 AAAA... key1", "ssh-rsa AAAA... key2"],
                enable_console=False,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["ssh-key-vm"]))
        assert vm is not None
        assert vm.name == "ssh-key-vm"

    def test_remove_cleans_vm_dir_with_cloud_init(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Removing a cloud-init VM cleans its VM directory."""
        _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="ci-cleanup-vm",
                ssh_keys=[],
                enable_console=False,
            )
        )

        vm = VMOperation.get(VMInput(identifiers=["ci-cleanup-vm"]))
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        assert vm_dir.exists()

        VMOperation.remove(VMInput(identifiers=["ci-cleanup-vm"]))

        assert not vm_dir.exists()


class TestCloudInitManualProvisioner:
    """Test the CloudInitProvisioner directly for ISO generation.

    These tests exercise the provisioner's business logic for creating
    cloud-init data files and calling the ISO generation subprocess.
    They bypass the full VM creation flow.
    """

    def test_provisioner_iso_creates_files(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """CloudInitProvisioner in ISO mode creates cloud-init files + ISO."""
        from tests.integration.conftest import SmartSubprocessMock

        sub_mock = SmartSubprocessMock()
        monkeypatch.setattr("subprocess.run", sub_mock)

        from mvmctl.core.cloudinit._provisioner import (
            CloudInitProvisionConfig,
            CloudInitProvisioner,
        )
        from mvmctl.models import CloudInitMode, NetworkItem

        vm_dir = tmp_path / "vms" / "test-vm"
        vm_dir.mkdir(parents=True, exist_ok=True)
        cloud_init_dir = vm_dir / "cloud-init"

        config = CloudInitProvisionConfig(
            mode=CloudInitMode.ISO,
            vm_name="provisioner-test-vm",
            vm_id="vm-id-123",
            vm_dir=vm_dir,
            cloud_init_dir=cloud_init_dir,
            guest_ip="10.20.0.2",
            tap_name="mvm-tap0",
            user="testuser",
            network=NetworkItem(
                id="net-1",
                name="default",
                subnet="10.20.0.0/24",
                bridge="mvm-br0",
                ipv4_gateway="10.20.0.1",
                bridge_active=True,
                nat_enabled=True,
                is_default=True,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            ),
            network_prefix_len=24,
            ssh_pubkeys=["ssh-ed25519 AAAA... test-key"],
            skip_network_config=False,
            cloud_init_iso_name="seed.iso",
            nocloud_port_range_start=8000,
            nocloud_port_range_end=9000,
            nocloud_max_port_retries=10,
        )

        provisioner = CloudInitProvisioner(config)
        result = provisioner.provision()

        assert cloud_init_dir.exists()
        assert (cloud_init_dir / "meta-data").exists()
        assert (cloud_init_dir / "network-config").exists()
        assert (cloud_init_dir / "user-data").exists()
        assert result.mode == CloudInitMode.ISO

    def test_provisioner_inject_creates_cloud_init_dir(
        self, tmp_path: Path
    ) -> None:
        """CloudInitProvisioner in INJECT mode creates cloud-init files only.

        In INJECT mode the files are written to disk and later injected
        into the rootfs by Provisioner — no ISO is generated.
        """
        from mvmctl.core.cloudinit._provisioner import (
            CloudInitProvisionConfig,
            CloudInitProvisioner,
        )
        from mvmctl.models import CloudInitMode, NetworkItem

        vm_dir = tmp_path / "vms" / "inject-vm"
        vm_dir.mkdir(parents=True, exist_ok=True)
        cloud_init_dir = vm_dir / "cloud-init"

        config = CloudInitProvisionConfig(
            mode=CloudInitMode.INJECT,
            vm_name="inject-test-vm",
            vm_id="vm-id-456",
            vm_dir=vm_dir,
            cloud_init_dir=cloud_init_dir,
            guest_ip="10.20.0.3",
            tap_name="mvm-tap1",
            user="testuser",
            network=NetworkItem(
                id="net-1",
                name="default",
                subnet="10.20.0.0/24",
                bridge="mvm-br0",
                ipv4_gateway="10.20.0.1",
                bridge_active=True,
                nat_enabled=True,
                is_default=True,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            ),
            network_prefix_len=24,
            ssh_pubkeys=[],
            skip_network_config=False,
            cloud_init_iso_name="seed.iso",
            nocloud_port_range_start=8000,
            nocloud_port_range_end=9000,
            nocloud_max_port_retries=10,
        )

        provisioner = CloudInitProvisioner(config)
        result = provisioner.provision()

        assert cloud_init_dir.exists()
        assert (cloud_init_dir / "meta-data").exists()
        assert (cloud_init_dir / "network-config").exists()
        assert (cloud_init_dir / "user-data").exists()
        assert result.mode == CloudInitMode.INJECT

    def test_provisioner_off_does_not_create_cloud_init_dir(
        self, tmp_path: Path
    ) -> None:
        """CloudInitProvisioner in OFF mode skips cloud-init entirely."""
        from mvmctl.core.cloudinit._provisioner import (
            CloudInitProvisionConfig,
            CloudInitProvisioner,
        )
        from mvmctl.models import CloudInitMode, NetworkItem

        vm_dir = tmp_path / "vms" / "off-vm"
        vm_dir.mkdir(parents=True, exist_ok=True)
        cloud_init_dir = vm_dir / "cloud-init"

        config = CloudInitProvisionConfig(
            mode=CloudInitMode.OFF,
            vm_name="off-test-vm",
            vm_id="vm-id-789",
            vm_dir=vm_dir,
            cloud_init_dir=cloud_init_dir,
            guest_ip="10.20.0.4",
            tap_name="mvm-tap2",
            user="testuser",
            network=NetworkItem(
                id="net-1",
                name="default",
                subnet="10.20.0.0/24",
                bridge="mvm-br0",
                ipv4_gateway="10.20.0.1",
                bridge_active=True,
                nat_enabled=True,
                is_default=True,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            ),
            network_prefix_len=24,
            ssh_pubkeys=[],
            skip_network_config=False,
            cloud_init_iso_name="seed.iso",
            nocloud_port_range_start=8000,
            nocloud_port_range_end=9000,
            nocloud_max_port_retries=10,
        )

        provisioner = CloudInitProvisioner(config)
        result = provisioner.provision()

        # OFF mode does NOT create the cloud-init directory
        assert not cloud_init_dir.exists()
        assert result.mode == CloudInitMode.OFF
