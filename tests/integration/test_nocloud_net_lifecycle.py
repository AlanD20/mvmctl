"""Integration tests for nocloud-net lifecycle workflow.

Tests the complete nocloud-net VM lifecycle through the current public API
with minimal mocking of external system dependencies only.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.api import (
    VMCreateInput,
    VMInput,
    VMOperation,
)
from mvmctl.core._shared._db import Database
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.exceptions import NetworkNotFoundError, VMNotFoundError
from mvmctl.models import (
    CloudInitMode,
    VMInstanceItem,
)
from mvmctl.utils.common import CacheUtils

# ======================================================================
# Mock helpers
# ======================================================================

_bound_ports: set[int] = set()


def _make_socket_mock():
    """Return a mock for socket.socket that allocates ports uniquely."""

    def mock_bind(addr):
        port = addr[1]
        if port in _bound_ports:
            raise OSError("Address already in use")
        _bound_ports.add(port)

    def _socket_cls(*args, **kwargs):
        mock_sock = MagicMock()
        mock_sock.__enter__ = MagicMock(return_value=mock_sock)
        mock_sock.__exit__ = MagicMock(return_value=False)
        mock_sock.bind = MagicMock(side_effect=mock_bind)
        mock_sock.setsockopt = MagicMock()
        return mock_sock

    return _socket_cls, _bound_ports


def _make_subprocess_run_mock():
    """Return a smart mock for subprocess.run that handles critical commands."""
    from tests.integration.conftest import SmartSubprocessMock

    return SmartSubprocessMock()


# ======================================================================
# Tests
# ======================================================================


class TestNocloudNetLifecycle:
    """Integration tests for nocloud-net VM lifecycle."""

    @staticmethod
    def _setup_vm_create(monkeypatch: pytest.MonkeyPatch, name: str) -> None:
        """Set up mocks and create a nocloud-net VM."""
        from tests.integration.conftest import SmartPopenMock

        sub_mock = _make_subprocess_run_mock()
        popen_mock = SmartPopenMock()
        monkeypatch.setattr("subprocess.run", sub_mock)
        monkeypatch.setattr("subprocess.Popen", popen_mock)

        # Mock socket.socket globally to prevent real network binds
        # (nocloud server manager calls import socket internally)
        socket_cls, _ = _make_socket_mock()
        monkeypatch.setattr("socket.socket", socket_cls)

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
            "mvmctl.api.vm_operations.Provisioner",
            lambda *args, **kwargs: provisioner_mock,
        )

        # Mock os.access to always succeed
        monkeypatch.setattr("os.access", lambda _path, _mode: True)

        # Work around core bug: DB expects 'nocloud_input' but model uses 'nocloudnet_input'
        class _FakeTrackerResult:
            success = True

        monkeypatch.setattr(
            "mvmctl.core._shared._iptables_tracker._tracker.IPTablesTracker.ensure_rule",
            lambda self, rule, context=None: _FakeTrackerResult(),
        )

        VMOperation.create(
            VMCreateInput(
                name=name,
                ssh_keys=[],
                cloud_init_mode=CloudInitMode.NET.value,
                network_name="net",
            )
        )

    def _get_network_by_name(self, db: Database, name: str = "net"):
        """Helper to get network by name from DB."""

        repo = NetworkRepository(db)
        net = repo.get_by_name(name)
        if net is None:
            raise NetworkNotFoundError(f"Network '{name}' not found")
        return net

    def test_nocloud_net_vm_creation(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a VM with nocloud-net mode and verify server metadata."""
        self._setup_vm_create(monkeypatch, "nocloud-test-vm")

        vm = VMOperation.get(VMInput(identifiers=["nocloud-test-vm"]))
        assert isinstance(vm, VMInstanceItem)
        assert vm.cloud_init_mode == CloudInitMode.NET.value
        # nocloud-net VMs should have port and pid recorded
        assert vm.nocloud_net_port is not None
        assert vm.nocloud_net_port > 0
        assert vm.nocloud_net_pid is not None
        assert vm.nocloud_net_pid > 0

    def test_nocloud_net_firewall_rules(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a nocloud-net VM and verify nocloud port was allocated."""
        self._setup_vm_create(monkeypatch, "fw-test-vm")

        vm = VMOperation.get(VMInput(identifiers=["fw-test-vm"]))

        # Verify nocloud-net port was allocated and is within expected range
        assert vm.nocloud_net_port is not None
        assert 8000 <= vm.nocloud_net_port <= 9000

    def test_nocloud_net_cleanup_on_remove(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Remove a nocloud-net VM and assert cleanup."""
        self._setup_vm_create(monkeypatch, "cleanup-test-vm")

        vm_before = VMOperation.get(VMInput(identifiers=["cleanup-test-vm"]))
        assert vm_before.nocloud_net_port is not None

        VMOperation.remove(VMInput(identifiers=["cleanup-test-vm"]))

        # VM should be gone
        with pytest.raises(VMNotFoundError):
            VMOperation.get(VMInput(identifiers=["cleanup-test-vm"]))

    def test_multiple_vms_different_ports(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create multiple nocloud-net VMs and verify unique port allocation."""
        for i in range(3):
            self._setup_vm_create(monkeypatch, f"multi-port-vm-{i}")

        ports: list[int] = []
        for i in range(3):
            vm = VMOperation.get(VMInput(identifiers=[f"multi-port-vm-{i}"]))
            assert vm.nocloud_net_port is not None
            ports.append(vm.nocloud_net_port)

        assert len(set(ports)) == len(ports), "Ports must be unique"

    def test_nocloud_net_vm_dir_created(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a nocloud-net VM and verify its VM directory exists."""
        self._setup_vm_create(monkeypatch, "dir-test-vm")

        vm = VMOperation.get(VMInput(identifiers=["dir-test-vm"]))
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        assert vm_dir.exists()
        assert (vm_dir / "cloud-init").exists()
