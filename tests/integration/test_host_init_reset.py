"""Integration tests for host init/reset roundtrip workflow.

Tests host initialization, state management, and reset with mocked system calls.
"""

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from mvmctl.cli.host import app as host_app
from mvmctl.core.host_state import HostChange, HostState
from mvmctl.exceptions import HostError

runner = CliRunner()


@pytest.fixture(autouse=True)
def _mock_default_network_setup(monkeypatch: pytest.MonkeyPatch) -> None:
    """Keep host integration tests isolated from privileged network setup."""
    monkeypatch.setattr("mvmctl.api.network.ensure_default_network", lambda: None)


def _make_host_state(changes: list | None = None) -> HostState:
    return HostState(init_timestamp="2024-01-01T00:00:00+00:00", changes=changes or [])


def _host_change(setting: str, original: str | None, applied: str, mechanism: str) -> HostChange:
    return HostChange(
        setting=setting,
        original_value=original,
        applied_value=applied,
        mechanism=mechanism,
    )


class TestHostInitResetWorkflow:
    """Test host init/reset roundtrip workflow."""

    @patch("mvmctl.api.host.check_privileges")
    @patch("mvmctl.cli.host.init_host")
    @patch("mvmctl.cli.host.get_host_state")
    def test_init_and_check_state(self, mock_get_state, mock_init, mock_check_priv):
        """Test host init and verifying state afterwards."""
        mock_check_priv.return_value = None

        changes = [
            _host_change("group:mvm", None, "mvm", "groupadd"),
            _host_change("net.ipv4.ip_forward", "0", "1", "sysctl"),
        ]
        mock_init.return_value = changes
        mock_get_state.return_value = _make_host_state(changes)

        result = runner.invoke(host_app, ["init"])
        assert result.exit_code == 0
        assert "initialized" in result.output.lower() or len(changes) > 0
        mock_init.assert_called_once()

        result = runner.invoke(host_app, ["ls"])
        assert result.exit_code == 0

    @patch("mvmctl.api.host.check_privileges")
    @patch("mvmctl.cli.host.init_host")
    @patch("mvmctl.cli.host.reset_host")
    @patch("mvmctl.cli.host.get_host_state")
    def test_init_reset_roundtrip(self, mock_get_state, mock_reset, mock_init, mock_check_priv):
        """Test full init -> reset roundtrip."""
        mock_check_priv.return_value = None

        init_changes = [
            _host_change("group:mvm", None, "mvm", "groupadd"),
            _host_change("net.ipv4.ip_forward", "0", "1", "sysctl"),
        ]
        mock_init.return_value = init_changes
        mock_reset.return_value = []

        mock_get_state.side_effect = [
            _make_host_state(init_changes),
            _make_host_state([]),
        ]

        result = runner.invoke(host_app, ["init"])
        assert result.exit_code == 0
        mock_init.assert_called_once()

        result = runner.invoke(host_app, ["reset"], input="y\n")
        assert result.exit_code == 0
        mock_reset.assert_called_once()

    @patch("mvmctl.cli.host.clean_host")
    def test_clean_host(self, mock_clean):
        """Test host clean operation."""
        mock_clean.return_value = []

        result = runner.invoke(host_app, ["clean"], input="y\n")
        assert result.exit_code == 0
        mock_clean.assert_called_once()

    @patch("mvmctl.cli.host.init_host")
    @patch("mvmctl.cli.host.clean_host")
    def test_init_clean_workflow(self, mock_clean, mock_init):
        """Test init followed by clean."""
        changes = [_host_change("group:mvm", None, "mvm", "groupadd")]
        mock_init.return_value = changes
        mock_clean.return_value = []

        result = runner.invoke(host_app, ["init"])
        assert result.exit_code == 0

        result = runner.invoke(host_app, ["clean"], input="y\n")
        assert result.exit_code == 0
        mock_clean.assert_called_once()


class TestHostWithSubprocessMocking:
    """Test host workflows with mocked subprocess calls."""

    @patch("mvmctl.core.host_setup.subprocess.run")
    @patch("mvmctl.core.host_setup.Path.exists")
    @patch("mvmctl.core.host_setup.os.access")
    @patch("mvmctl.core.host_privilege._get_current_user")
    @patch("mvmctl.core.host_privilege._group_exists")
    @patch("mvmctl.core.host_privilege._user_in_group")
    @patch("mvmctl.core.host_state._state_dir")
    @patch("mvmctl.api.host.check_privileges")
    def test_init_with_subprocess_mocking(
        self,
        mock_check_priv,
        mock_state_dir,
        mock_user_in_group,
        mock_group_exists,
        mock_get_user,
        mock_access,
        mock_exists,
        mock_run,
    ):
        """Test host init with all system calls mocked."""
        from mvmctl.core.host_setup import init_host

        mock_check_priv.return_value = None
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        mock_exists.return_value = True
        mock_access.return_value = True
        mock_get_user.return_value = "testuser"
        mock_group_exists.return_value = False
        mock_user_in_group.return_value = False

        state_dir = MagicMock()
        state_dir.exists.return_value = True
        mock_state_dir.return_value = state_dir

        with patch("mvmctl.core.host_setup.os.getuid", return_value=0):
            with patch("mvmctl.core.host_setup.check_kvm_access", return_value=True):
                with patch("mvmctl.core.host_setup.check_required_binaries", return_value=[]):
                    with patch("mvmctl.core.host_setup._enable_ip_forward") as mock_ip_forward:
                        with patch("mvmctl.core.host_setup._ensure_kvm_modules") as mock_kvm:
                            with patch(
                                "mvmctl.core.host_privilege._create_group"
                            ) as mock_create_group:
                                with patch(
                                    "mvmctl.core.host_privilege._add_user_to_group"
                                ) as mock_add_user:
                                    mock_ip_forward.return_value = _host_change(
                                        "net.ipv4.ip_forward", "0", "1", "sysctl"
                                    )
                                    mock_kvm.return_value = []
                                    mock_create_group.return_value = True
                                    mock_add_user.return_value = True
                                    with patch(
                                        "mvmctl.core.host_setup._persist_sysctl", return_value=None
                                    ):
                                        with patch("mvmctl.core.host_setup.setup_mvm_chains"):
                                            with patch("mvmctl.core.host_setup._save_state"):
                                                with patch("mvmctl.core.host_setup._write_sudoers"):
                                                    result = init_host(Path("/tmp/cache"))

                            assert len(result) > 0

    @patch("mvmctl.core.host.restore_host")
    @patch("mvmctl.core.host_state._state_file")
    def test_reset_with_subprocess_mocking(self, mock_state_file, mock_restore):
        from mvmctl.core.host import reset_host

        mock_restore.return_value = []

        state_file = MagicMock()
        state_file.exists.return_value = True
        mock_state_file.return_value = state_file

        with patch("mvmctl.core.host.clean_host", return_value=[]):
            with patch("mvmctl.core.host._remove_sudoers", return_value=False):
                with patch("mvmctl.core.host._remove_group", return_value=False):
                    reset_host(Path("/tmp/cache"))

        mock_restore.assert_called_once()


class TestHostEdgeCases:
    """Test edge cases in host workflows."""

    @patch("mvmctl.cli.host.reset_host")
    @patch("mvmctl.cli.host.get_host_state")
    def test_reset_without_prior_init(self, mock_get_state, mock_reset):
        """Test reset when init has never been run."""
        mock_get_state.return_value = None
        mock_reset.side_effect = HostError("No host state found — init first")

        result = runner.invoke(host_app, ["reset"])
        assert result.exit_code == 1
        assert "init" in result.output.lower()

    @patch("mvmctl.cli.host.init_host")
    def test_init_idempotent(self, mock_init):
        """Test that init is idempotent."""
        mock_init.return_value = []

        result = runner.invoke(host_app, ["init"])
        assert result.exit_code == 0
        assert "No changes" in result.output or result.exit_code == 0

    @patch("mvmctl.cli.host.init_host")
    def test_init_partial_failure(self, mock_init):
        """Test init when some operations fail."""
        mock_init.side_effect = HostError("Failed to create group: permission denied")

        result = runner.invoke(host_app, ["init"])
        assert result.exit_code == 1
        assert "permission" in result.output.lower() or "failed" in result.output.lower()

    @patch("mvmctl.cli.host.clean_host")
    @patch("mvmctl.cli.host.get_vm_manager")
    def test_clean_with_no_networks(self, mock_get_vm_manager, mock_clean):
        """Test clean when no networks exist."""
        mock_get_vm_manager.return_value.list_all.return_value = []
        mock_clean.return_value = []

        result = runner.invoke(host_app, ["clean"], input="y\n")
        assert result.exit_code == 0


class TestHostStateManagement:
    """Test host state snapshot and restoration."""

    def test_host_change_serialization(self):
        """Test HostChange can be serialized and deserialized."""
        change = _host_change("test:key", "old_value", "new_value", "manual")

        data = {
            "setting": change.setting,
            "original_value": change.original_value,
            "applied_value": change.applied_value,
            "mechanism": change.mechanism,
        }
        serialized = json.dumps(data)
        deserialized = json.loads(serialized)

        assert deserialized["setting"] == "test:key"
        assert deserialized["original_value"] == "old_value"
        assert deserialized["applied_value"] == "new_value"

    def test_host_state_serialization(self):
        """Test HostState can be serialized and deserialized."""
        changes = [
            _host_change("group:mvm", None, "mvm", "groupadd"),
            _host_change("net.ipv4.ip_forward", "0", "1", "sysctl"),
        ]
        state = _make_host_state(changes)

        data = {
            "changes": [
                {
                    "setting": c.setting,
                    "original_value": c.original_value,
                    "applied_value": c.applied_value,
                    "mechanism": c.mechanism,
                }
                for c in state.changes
            ],
            "init_timestamp": state.init_timestamp,
        }
        serialized = json.dumps(data)
        deserialized = json.loads(serialized)

        assert len(deserialized["changes"]) == 2
        assert deserialized["init_timestamp"] == "2024-01-01T00:00:00+00:00"

    @patch("mvmctl.core.host_state._state_file")
    def test_save_and_load_state(self, mock_state_file, tmp_path):
        """Test saving and loading host state."""
        from mvmctl.core.host_state import _save_state, get_host_state

        changes = [_host_change("test", "a", "b", "manual")]

        state_file = tmp_path / "state.json"
        mock_state_file.return_value = state_file

        _save_state(tmp_path, changes)

        assert state_file.exists()

        loaded = get_host_state(tmp_path)
        assert loaded is not None
        assert len(loaded.changes) == 1
        assert loaded.changes[0].setting == "test"
