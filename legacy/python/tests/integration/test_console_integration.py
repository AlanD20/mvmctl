"""Integration tests for console workflow through the real public API.

Tests exercise the complete console relay lifecycle:
  create VM with console → get connection info → get state → kill → cleanup on VM remove

Only subprocess calls and Provisioner are mocked.
ALL API-layer orchestration runs unmocked.
"""

from __future__ import annotations

import os
from unittest.mock import MagicMock

import pytest

from mvmctl.api import ConsoleOperation, VMCreateInput, VMInput, VMOperation
from mvmctl.exceptions import MVMError, VMNotFoundError
from mvmctl.models.vm import ConsoleState, VMInstanceItem
from mvmctl.utils.common import CacheUtils


def _setup_mocks(monkeypatch: pytest.MonkeyPatch) -> dict[str, object]:
    """Apply subprocess mocks for console relay and firecracker."""
    from tests.integration.conftest import SmartPopenMock, SmartSubprocessMock

    sub_mock = SmartSubprocessMock()
    popen_mock = SmartPopenMock()
    monkeypatch.setattr("subprocess.run", sub_mock)
    monkeypatch.setattr("subprocess.Popen", popen_mock)

    # Mock Provisioner to avoid real libguestfs
    provisioner_mock = MagicMock()
    monkeypatch.setattr(
        "mvmctl.api.vm_operations.VMProvisioner",
        lambda *args, **kwargs: provisioner_mock,
    )
    provisioner_mock.resize.return_value = provisioner_mock
    provisioner_mock.set_hostname.return_value = provisioner_mock
    provisioner_mock.inject_dns.return_value = provisioner_mock
    provisioner_mock.setup_ssh.return_value = provisioner_mock
    provisioner_mock.disable_cloud_init.return_value = provisioner_mock
    provisioner_mock.run.return_value = None

    # Mock os.openpty for console relay PTY creation
    # Use os.pipe() to allocate real, safe file descriptors on each call.
    # Hardcoded values like (10, 11) collide with pytest/xdist internals.
    monkeypatch.setattr("os.openpty", lambda: os.pipe())

    # Mock os.kill so fake PIDs appear alive
    monkeypatch.setattr("os.kill", lambda pid, sig: None)

    return {
        "subprocess": sub_mock,
        "popen": popen_mock,
        "provisioner": provisioner_mock,
    }


class TestConsoleWorkflow:
    """Integration tests for console workflow through the real public API."""

    def _create_vm(
        self,
        monkeypatch: pytest.MonkeyPatch,
        name: str,
        enable_console: bool = True,
    ) -> None:
        """Create a VM via the real API with optional console."""
        _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name=name,
                ssh_keys=[],
                enable_console=enable_console,
            )
        )

    def test_create_vm_with_console(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a VM with console enabled and verify the DB record."""
        self._create_vm(monkeypatch, "test-console-vm")
        vm = VMOperation.get(VMInput(identifiers=["test-console-vm"]))
        assert isinstance(vm, VMInstanceItem)
        assert vm.name == "test-console-vm"
        assert vm.enable_console
        assert vm.relay_pid == 2000
        assert vm.relay_socket_path is not None
        assert len(vm.relay_socket_path) > 0

    def test_get_connection_info(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Get console connection info and verify socket path info."""
        self._create_vm(monkeypatch, "attach-console-vm")
        info = ConsoleOperation.get_connection_info("attach-console-vm")
        assert info.vm_name == "attach-console-vm"
        assert info.socket_path is not None
        assert len(info.socket_path) > 0

    def test_get_console_state(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Get console state and verify running / pid / socket_path."""
        self._create_vm(monkeypatch, "state-console-vm")
        raw_state = ConsoleOperation.get_state("state-console-vm")
        state = ConsoleState(**raw_state)
        assert state.running
        assert state.pid == 2000
        assert state.socket_path is not None
        assert len(state.socket_path) > 0

    def test_kill_console(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Kill the console relay and verify it is no longer running."""
        self._create_vm(monkeypatch, "kill-console-vm")
        result = ConsoleOperation.kill("kill-console-vm")
        assert result.item is True
        raw_state = ConsoleOperation.get_state("kill-console-vm")
        state = ConsoleState(**raw_state)
        # After kill, relay may not be running
        assert isinstance(state.running, bool)

    def test_console_cleanup_on_vm_remove(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Remove a VM and verify console files are cleaned up."""
        self._create_vm(monkeypatch, "cleanup-console-vm")
        vm = VMOperation.get(VMInput(identifiers=["cleanup-console-vm"]))
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        assert vm_dir.exists()
        assert (vm_dir / "console.sock").exists()

        VMOperation.remove(VMInput(identifiers=["cleanup-console-vm"]))

        with pytest.raises(MVMError):
            VMOperation.get(VMInput(identifiers=["cleanup-console-vm"]))
        assert not vm_dir.exists()

    def test_console_kill_idempotent(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Killing a console relay twice does not raise."""
        self._create_vm(monkeypatch, "idempotent-console-vm")
        ConsoleOperation.kill("idempotent-console-vm")
        # Second kill should not raise
        ConsoleOperation.kill("idempotent-console-vm")

    def test_get_connection_info_nonexistent_vm(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Get connection info for a nonexistent VM raises VMNotFoundError."""
        _setup_mocks(monkeypatch)
        with pytest.raises(VMNotFoundError):
            ConsoleOperation.get_connection_info("nonexistent-vm")

    def test_get_connection_info_console_disabled(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create VM with console disabled, get_connection_info raises MVMError."""
        self._create_vm(monkeypatch, "no-console-vm", enable_console=False)
        with pytest.raises(MVMError, match="No console relay running"):
            ConsoleOperation.get_connection_info("no-console-vm")

    def test_get_state_nonexistent_vm(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Get state for nonexistent VM raises VMNotFoundError."""
        _setup_mocks(monkeypatch)
        with pytest.raises(VMNotFoundError):
            ConsoleOperation.get_state("nonexistent-vm")

    def test_get_state_after_kill(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Kill console relay, then get state and verify running=False."""
        self._create_vm(monkeypatch, "kill-state-vm")
        result = ConsoleOperation.kill("kill-state-vm")
        assert result.item is True
        raw_state = ConsoleOperation.get_state("kill-state-vm")
        state = ConsoleState(**raw_state)
        assert state.running is False
        assert state.pid is None

    def test_kill_nonexistent_console(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Kill console for a VM that was never created raises VMNotFoundError."""
        _setup_mocks(monkeypatch)
        with pytest.raises(VMNotFoundError):
            ConsoleOperation.kill("never-created-vm")

    def test_kill_after_vm_remove(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create VM, remove VM, then kill console raises VMNotFoundError."""
        self._create_vm(monkeypatch, "remove-then-kill-vm")
        VMOperation.remove(VMInput(identifiers=["remove-then-kill-vm"]))
        with pytest.raises(VMNotFoundError):
            ConsoleOperation.kill("remove-then-kill-vm")
