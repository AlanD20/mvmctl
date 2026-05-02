"""Integration tests for SSH workflow through the real public API.

Tests exercise the complete SSH orchestration flow:
  create VM → connect via SSH (mocked subprocess)

Only subprocess and os.execvp are mocked.
ALL resolution logic in api/ and core/ runs unmocked.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any
from unittest.mock import MagicMock

import pytest

from mvmctl.api import SSHOperation, VMCreateInput, VMInput, VMOperation
from mvmctl.api.inputs import SSHInput
from mvmctl.exceptions import SSHError, VMNotFoundError
from mvmctl.models.result import OperationResult
from mvmctl.models import VMInstanceItem


def _setup_mocks(monkeypatch: pytest.MonkeyPatch) -> dict[str, Any]:
    """Apply subprocess mocks for VM creation and SSH."""
    from tests.integration.conftest import SmartPopenMock, SmartSubprocessMock

    sub_mock = SmartSubprocessMock()
    popen_mock = SmartPopenMock()
    monkeypatch.setattr("subprocess.run", sub_mock)
    monkeypatch.setattr("subprocess.Popen", popen_mock)

    # Mock GuestfsProvisioner to avoid real libguestfs
    gp_mock = MagicMock()
    gp_mock.resize.return_value = gp_mock
    gp_mock.set_hostname.return_value = gp_mock
    gp_mock.inject_dns.return_value = gp_mock
    gp_mock.setup_ssh.return_value = gp_mock
    gp_mock.run.return_value = None
    monkeypatch.setattr(
        "mvmctl.api.vm_operations.GuestfsProvisioner",
        lambda *args, **kwargs: gp_mock,
    )

    # Mock os.execvp so SSH interactive sessions don't replace the test process
    execvp_calls: list[tuple[str, list[str]]] = []
    monkeypatch.setattr(
        "os.execvp",
        lambda file, args: execvp_calls.append((file, list(args))),
    )

    return {
        "subprocess": sub_mock,
        "popen": popen_mock,
        "guestfs": gp_mock,
        "execvp_calls": execvp_calls,
    }


class TestSSHConnect:
    """Test SSH connect with various parameter combinations."""

    def test_connect_default_params(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a VM and connect with default SSH params."""
        mocks = _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="ssh-default-vm", ssh_keys=[], enable_console=False
            )
        )
        vm = VMOperation.get(VMInput(identifiers=["ssh-default-vm"]))
        assert isinstance(vm, VMInstanceItem)
        assert vm.ipv4

        result = SSHOperation.connect(SSHInput(name="ssh-default-vm"))
        assert result.item == 0
        assert len(mocks["execvp_calls"]) == 1
        file, args = mocks["execvp_calls"][0]
        assert file == "ssh"
        assert f"root@{vm.ipv4}" in args
        assert "-o" in args
        assert "StrictHostKeyChecking=no" in args
        assert "UserKnownHostsFile=/dev/null" in args

    def test_connect_with_custom_user(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a VM and connect with a custom SSH username."""
        mocks = _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(name="ssh-user-vm", ssh_keys=[], enable_console=False)
        )
        vm = VMOperation.get(VMInput(identifiers=["ssh-user-vm"]))
        assert isinstance(vm, VMInstanceItem)

        result = SSHOperation.connect(
            SSHInput(name="ssh-user-vm", user="ubuntu")
        )
        assert result.item == 0
        assert len(mocks["execvp_calls"]) == 1
        _file, args = mocks["execvp_calls"][0]
        assert f"ubuntu@{vm.ipv4}" in args

    def test_connect_with_explicit_key(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        """Create a VM and connect with an explicit SSH key path."""
        mocks = _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(name="ssh-key-vm", ssh_keys=[], enable_console=False)
        )
        vm = VMOperation.get(VMInput(identifiers=["ssh-key-vm"]))
        assert isinstance(vm, VMInstanceItem)

        key_path = tmp_path / "test_key"
        key_path.write_text(
            "-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----"
        )

        result = SSHOperation.connect(SSHInput(name="ssh-key-vm", key=key_path))
        assert result.item == 0
        assert len(mocks["execvp_calls"]) == 1
        _file, args = mocks["execvp_calls"][0]
        assert "-i" in args
        idx = args.index("-i")
        assert args[idx + 1] == str(key_path)


class TestSSHResolution:
    """Test SSH target resolution by different VM identifiers."""

    def test_connect_by_vm_name(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Connect to a VM using its name as identifier."""
        mocks = _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(name="ssh-name-vm", ssh_keys=[], enable_console=False)
        )
        vm = VMOperation.get(VMInput(identifiers=["ssh-name-vm"]))
        assert isinstance(vm, VMInstanceItem)

        result = SSHOperation.connect(SSHInput(name="ssh-name-vm"))
        assert result.item == 0
        assert len(mocks["execvp_calls"]) == 1
        _file, args = mocks["execvp_calls"][0]
        assert f"root@{vm.ipv4}" in args

    def test_connect_by_vm_id_prefix(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Connect to a VM using its ID prefix as identifier."""
        mocks = _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(name="ssh-id-vm", ssh_keys=[], enable_console=False)
        )
        vm = VMOperation.get(VMInput(identifiers=["ssh-id-vm"]))
        assert isinstance(vm, VMInstanceItem)
        assert len(vm.id) >= 6

        result = SSHOperation.connect(SSHInput(vm_id=vm.id[:6]))
        assert result.item == 0
        assert len(mocks["execvp_calls"]) == 1
        _file, args = mocks["execvp_calls"][0]
        assert f"root@{vm.ipv4}" in args

    def test_connect_nonexistent_vm(self) -> None:
        """Connecting to a nonexistent VM returns error status."""
        result = SSHOperation.connect(SSHInput(name="no-such-vm"))
        assert isinstance(result, OperationResult)
        assert result.status == "error"


class TestSSHEdgeCases:
    """Test SSH edge cases and error handling."""

    def test_connect_invalid_username(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Connect with an invalid username raises SSHError."""
        mocks = _setup_mocks(monkeypatch)

        VMOperation.create(
            VMCreateInput(
                name="ssh-bad-user-vm", ssh_keys=[], enable_console=False
            )
        )
        vm = VMOperation.get(VMInput(identifiers=["ssh-bad-user-vm"]))
        assert isinstance(vm, VMInstanceItem)

        result = SSHOperation.connect(
            SSHInput(name="ssh-bad-user-vm", user="123invalid")
        )
        assert isinstance(result, OperationResult)
        assert result.status == "error"

        # os.execvp should never be called because validation fails before exec
        assert len(mocks["execvp_calls"]) == 0

    def test_connect_empty_identifiers(self) -> None:
        """Connect with no VM identifiers returns error status."""
        result = SSHOperation.connect(SSHInput())
        assert isinstance(result, OperationResult)
        assert result.status == "error"
        assert "identifier" in result.message.lower() or "--name" in result.message
