"""Integration tests for host init/reset roundtrip workflow.

Tests host initialization, state management, and reset with mocked system calls.
All tests exercise the real public API (HostOperation) and mock only external
system dependencies (subprocess, os, pathlib, grp, pwd, shutil).
"""

from __future__ import annotations

import os
import subprocess
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock

import pytest

from mvmctl.api import HostOperation
from mvmctl.models.host import HostStateChangeItem, HostStateItem
from mvmctl.models.result import OperationResult


class _MockPwd:
    pw_name = "testuser"
    pw_gid = 1000


class _MockGrp:
    def __init__(self) -> None:
        self._groups: dict[str, Any] = {}

    def getgrnam(self, name: str) -> Any:
        if name not in self._groups:
            raise KeyError(name)
        return self._groups[name]

    def create(self, name: str) -> None:
        self._groups[name] = MagicMock(gr_mem=[], gr_gid=1001)

    def add_user(self, name: str, username: str) -> None:
        if name in self._groups:
            self._groups[name].gr_mem.append(username)

    def delete(self, name: str) -> None:
        self._groups.pop(name, None)


class _MockSubprocessRun:
    """Stateful subprocess.run mock that returns realistic responses."""

    def __call__(
        self, cmd: list[str] | str, **kwargs: Any
    ) -> subprocess.CompletedProcess[str]:
        if not isinstance(cmd, list):
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="", stderr=""
            )

        cmd_str = " ".join(str(c) for c in cmd)

        # sysctl read ip_forward status
        if "sysctl" in cmd and "-n" in cmd:
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="0", stderr=""
            )

        # lsmod — no modules loaded
        if "lsmod" in cmd:
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="", stderr=""
            )

        # modprobe --dry-run kvm_intel succeeds, kvm_amd fails
        if "modprobe" in cmd and "--dry-run" in cmd:
            if "kvm_intel" in cmd_str:
                return subprocess.CompletedProcess(
                    args=cmd, returncode=0, stdout="", stderr=""
                )
            return subprocess.CompletedProcess(
                args=cmd, returncode=1, stdout="", stderr=""
            )

        # iptables-save returns empty ruleset
        if "iptables-save" in cmd:
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="", stderr=""
            )

        # ip -o link show type tuntap / bridge -> empty lists
        if "ip" in cmd and "-o" in cmd:
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="", stderr=""
            )

        # ip link show <specific iface> -> not found (for bridge_exists / tap_exists)
        if "ip" in cmd and "link" in cmd and "show" in cmd and len(cmd) >= 4:
            last = str(cmd[-1])
            if last not in ("type", "tuntap", "bridge", "master"):
                return subprocess.CompletedProcess(
                    args=cmd, returncode=1, stdout="", stderr=""
                )

        # ip route show default
        if "ip" in cmd and "route" in cmd and "default" in cmd:
            return subprocess.CompletedProcess(
                args=cmd,
                returncode=0,
                stdout="default via 192.168.1.1 dev eth0",
                stderr="",
            )

        # iptables check chain / rule -> not found so creation proceeds
        if "iptables" in cmd and ("-C" in cmd or ("-L" in cmd and "-n" in cmd)):
            return subprocess.CompletedProcess(
                args=cmd, returncode=1, stdout="", stderr=""
            )

        # iptables --version (nf_tables backend)
        if "iptables" in cmd and "--version" in cmd:
            return subprocess.CompletedProcess(
                args=cmd,
                returncode=0,
                stdout="",
                stderr="iptables v1.8.7 (nf_tables)",
            )

        # visudo validation
        if "visudo" in cmd:
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="", stderr=""
            )

        # Default: success for groupadd, usermod, sysctl -w, modprobe, ip -batch, etc.
        return subprocess.CompletedProcess(
            args=cmd, returncode=0, stdout="", stderr=""
        )


def _mock_path_exists(self: Path) -> bool:
    """Return True for KVM device and privileged binaries; delegate otherwise."""
    privileged = {
        "/dev/kvm",
        "/usr/sbin/ip",
        "/usr/sbin/iptables",
        "/usr/sbin/iptables-save",
        "/usr/sbin/sysctl",
        "/usr/sbin/modprobe",
    }
    if str(self) in privileged:
        return True
    return os.path.exists(str(self))


@pytest.fixture(autouse=True)
def _mock_host_system_deps(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Mock all external system dependencies so the real API can run unmocked."""
    # --- Identity mocks ---
    monkeypatch.setattr("os.getuid", lambda: 0)
    monkeypatch.setattr("os.getgid", lambda: 0)
    monkeypatch.setattr("os.getegid", lambda: 0)
    monkeypatch.setattr("os.getgroups", lambda: [0, 1000])
    monkeypatch.setattr("os.access", lambda _path, _mode: True)

    # --- User / group mocks ---
    mock_pwd = _MockPwd()
    monkeypatch.setattr("pwd.getpwuid", lambda _uid: mock_pwd)

    mock_grp_state = _MockGrp()
    monkeypatch.setattr("grp.getgrnam", mock_grp_state.getgrnam)

    # --- Binary lookup mock ---
    monkeypatch.setattr(
        "shutil.which",
        lambda cmd: f"/usr/bin/{cmd}" if cmd else None,
    )

    # --- Path.exists mock ---
    monkeypatch.setattr(Path, "exists", _mock_path_exists)

    # --- Subprocess mocks ---
    mock_run = _MockSubprocessRun()
    monkeypatch.setattr("subprocess.run", mock_run)
    monkeypatch.setattr(
        "subprocess.check_output",
        lambda cmd, **kwargs: b"",
    )

    # --- Redirect host-state file paths into tmp_path ---
    # NOTE: These override the actual constants used by the host service.
    # The root conftest patches non-existent modules (host_setup, host_state)
    # which silently no-op. We patch the real modules here.
    monkeypatch.setattr(
        "mvmctl.api.host_operations.SUDOERS_DROP_IN_PATH",
        str(tmp_path / "sudoers.d" / "mvmctl"),
    )
    monkeypatch.setattr(
        "mvmctl.core.host._service.SYSCTL_CONF",
        tmp_path / "sysctl.d" / "mvmctl.conf",
    )
    monkeypatch.setattr(
        "mvmctl.core.host._service.IPTABLES_RULES_V4",
        str(tmp_path / "iptables" / "rules.v4"),
    )

    # Ensure directories exist so file writes succeed
    (tmp_path / "sudoers.d").mkdir(parents=True, exist_ok=True)
    (tmp_path / "sysctl.d").mkdir(parents=True, exist_ok=True)
    (tmp_path / "iptables").mkdir(parents=True, exist_ok=True)

    # Ensure DB schema is present
    from mvmctl.core._shared import Database

    Database().migrate()


class TestHostInitResetWorkflow:
    """Test host init/reset roundtrip workflow through the public API."""

    def test_host_init(self, tmp_path: Path) -> None:
        """HostOperation.init returns HostStateChangeItem list and marks initialized."""
        cache_dir = tmp_path / "cache"
        cache_dir.mkdir(parents=True, exist_ok=True)

        result = HostOperation.init(cache_dir=cache_dir)

        assert isinstance(result, OperationResult)
        assert result.status == "success"
        changes = result.metadata.get("changes", [])
        assert isinstance(changes, list)
        assert all(isinstance(c, HostStateChangeItem) for c in changes)
        # At minimum we expect group, user, sysctl, iptables changes
        assert len(changes) > 0

        state = HostOperation.get_state()
        assert state is not None
        assert isinstance(state, HostStateItem)
        assert state.initialized

    def test_host_get_state(self, tmp_path: Path) -> None:
        """After init, get_state returns HostStateItem with correct fields."""
        cache_dir = tmp_path / "cache"
        cache_dir.mkdir(parents=True, exist_ok=True)

        HostOperation.init(cache_dir=cache_dir)

        state = HostOperation.get_state()
        assert state is not None
        assert isinstance(state, HostStateItem)
        assert state.initialized
        assert state.id == 1
        assert isinstance(state.mvm_group_created, int)
        assert isinstance(state.sudoers_configured, int)
        assert isinstance(state.default_network_created, int)
        assert isinstance(state.initialized_at, str)
        assert isinstance(state.updated_at, str)

    def test_host_check_kvm_access(self) -> None:
        """HostOperation.check_kvm_access returns a boolean."""
        result = HostOperation.check_kvm_access()
        assert isinstance(result, bool)

    def test_host_check_required_binaries(self) -> None:
        """HostOperation.check_required_binaries returns a list of strings."""
        missing = HostOperation.check_required_binaries()
        assert isinstance(missing, list)
        assert all(isinstance(b, str) for b in missing)

    def test_host_clean(self, tmp_path: Path) -> None:
        """After init, clean returns a list of summary strings."""
        cache_dir = tmp_path / "cache"
        cache_dir.mkdir(parents=True, exist_ok=True)

        HostOperation.init(cache_dir=cache_dir)
        result = HostOperation.clean(cache_dir=cache_dir)

        assert isinstance(result, OperationResult)
        assert result.status == "success"
        summary = result.item
        assert isinstance(summary, list)
        assert all(isinstance(s, str) for s in summary)

    def test_host_reset(self, tmp_path: Path) -> None:
        """After init, reset returns a list of summary strings and clears state."""
        cache_dir = tmp_path / "cache"
        cache_dir.mkdir(parents=True, exist_ok=True)

        HostOperation.init(cache_dir=cache_dir)
        result = HostOperation.reset(cache_dir=cache_dir)

        assert isinstance(result, OperationResult)
        assert result.status == "success"
        summary = result.item
        assert isinstance(summary, list)
        assert all(isinstance(s, str) for s in summary)

        state = HostOperation.get_state()
        assert state is not None
        assert not state.initialized
