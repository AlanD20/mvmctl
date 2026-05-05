"""Integration tests for the Log API through the real public API.

Tests exercise log streaming operations:
  create VM → write log content → stream → verify

Only subprocess (system-level operations like cp, dd, ip, firecracker)
are mocked. ALL orchestration logic in api/ and core/ runs unmocked.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.api import (
    LogInput,
    LogOperation,
    VMCreateInput,
    VMInput,
    VMOperation,
)
from mvmctl.exceptions import VMNotFoundError
from mvmctl.models import VMInstanceItem
from mvmctl.utils.common import CacheUtils

# ======================================================================
# Helpers
# ======================================================================


def _setup_mocks(monkeypatch: pytest.MonkeyPatch) -> dict[str, object]:
    """Apply subprocess mocks and return references for assertions."""
    from tests.integration.conftest import SmartPopenMock, SmartSubprocessMock

    sub_mock = SmartSubprocessMock()
    popen_mock = SmartPopenMock()
    monkeypatch.setattr("subprocess.run", sub_mock)
    monkeypatch.setattr("subprocess.Popen", popen_mock)

    provisioner_mock = MagicMock()
    monkeypatch.setattr(
        "mvmctl.api.vm_operations.VMProvisioner",
        lambda *args, **kwargs: provisioner_mock,
    )
    return {
        "subprocess": sub_mock,
        "popen": popen_mock,
        "provisioner": provisioner_mock,
    }


def _create_vm(monkeypatch: pytest.MonkeyPatch, name: str) -> VMInstanceItem:
    """Create a VM via the real API with mocked subprocess calls."""
    mocks = _setup_mocks(monkeypatch)
    mocks["provisioner"].resize.return_value = mocks["provisioner"]
    mocks["provisioner"].set_hostname.return_value = mocks["provisioner"]
    mocks["provisioner"].inject_dns.return_value = mocks["provisioner"]
    mocks["provisioner"].setup_ssh.return_value = mocks["provisioner"]
    mocks["provisioner"].disable_cloud_init.return_value = mocks["provisioner"]
    mocks["provisioner"].run.return_value = None

    VMOperation.create(
        VMCreateInput(name=name, ssh_keys=[], enable_console=False)
    )
    return VMOperation.get(VMInput(identifiers=[name]))


# ======================================================================
# Log stream tests
# ======================================================================


class TestLogStream:
    """Test log streaming through the real public API."""

    def test_stream_returns_last_n_lines(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Create a VM, write log lines, and stream the last N lines."""
        vm = _create_vm(monkeypatch, "log-test-vm")
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        log_file = vm_dir / "firecracker.console.log"
        log_file.write_text(
            "\n".join(f"log-line-{i}" for i in range(20)) + "\n"
        )

        lines = list(
            LogOperation.stream(LogInput(identifier="log-test-vm", lines=10))
        )

        assert len(lines) == 10
        assert lines[0] == "log-line-10"
        assert lines[-1] == "log-line-19"

    def test_stream_follow_false(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Stream with follow=False returns log content without blocking."""
        vm = _create_vm(monkeypatch, "log-follow-false-vm")
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        log_file = vm_dir / "firecracker.console.log"
        expected_lines = ["alpha", "beta", "gamma"]
        log_file.write_text("\n".join(expected_lines) + "\n")

        lines = list(
            LogOperation.stream(
                LogInput(
                    identifier="log-follow-false-vm", follow=False, lines=50
                )
            )
        )

        assert len(lines) == 3
        assert lines[0] == "alpha"
        assert lines[1] == "beta"
        assert lines[2] == "gamma"

    def test_stream_known_content(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Write known content to log file and assert it appears in stream."""
        vm = _create_vm(monkeypatch, "log-known-vm")
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        log_file = vm_dir / "firecracker.console.log"
        known = "KNOWN_INTEGRATION_MARKER_42"
        log_file.write_text(f"before\n{known}\nafter\n")

        lines = list(
            LogOperation.stream(LogInput(identifier="log-known-vm", lines=10))
        )

        assert any(known in line for line in lines)
        assert lines == ["before", known, "after"]


# ======================================================================
# Edge cases
# ======================================================================


class TestLogEdgeCases:
    """Test edge cases and error handling in the Log API."""

    def test_stream_nonexistent_vm(self) -> None:
        """Streaming from a nonexistent VM raises VMNotFoundError."""
        with pytest.raises(VMNotFoundError):
            list(
                LogOperation.stream(
                    LogInput(identifier="nonexistent-vm", lines=10)
                )
            )

    def test_stream_zero_lines(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Stream with lines=0 returns an empty list."""
        vm = _create_vm(monkeypatch, "log-zero-vm")
        vm_dir = CacheUtils.get_vm_dir(vm.id)
        log_file = vm_dir / "firecracker.console.log"
        log_file.write_text("some content\nmore content\n")

        lines = list(
            LogOperation.stream(LogInput(identifier="log-zero-vm", lines=0))
        )

        assert lines == []

    def test_stream_empty_log_file(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Stream from VM with empty log file returns empty list."""
        _create_vm(monkeypatch, "log-empty-vm")
        # VM creation creates empty log files; do not write anything

        lines = list(
            LogOperation.stream(LogInput(identifier="log-empty-vm", lines=10))
        )

        assert lines == []
