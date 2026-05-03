"""Extended tests for HostOperation — covering error handling and edge cases."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

from mvmctl.api.host_operations import HostOperation
from mvmctl.exceptions import HostError, NetworkError, PrivilegeError
from mvmctl.models.result import NeedsInteraction, OperationResult


def _make_op_result(
    status: str = "success", code: str = "ok", item: object = None
) -> MagicMock:
    r = MagicMock(spec=OperationResult)
    r.status = status
    r.code = code
    r.message = ""
    r.item = item
    r.is_ok = status in ("success", "skipped", "warning")
    r.is_error = status in ("error", "failure")
    return r


def _patch_host_deps(mocker) -> None:
    mocker.patch(
        "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
    )
    mocker.patch("mvmctl.api.host_operations.Database")
    mocker.patch("mvmctl.api.host_operations.HostRepository")
    mocker.patch("mvmctl.api.host_operations.HostController")
    mocker.patch("mvmctl.api.host_operations.AuditLog.log")


def _patch_init_common(mocker) -> dict[str, MagicMock]:
    deps: dict[str, MagicMock] = {}
    mocker.patch(
        "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
    )
    mocker.patch("mvmctl.api.host_operations.Database")
    mocker.patch("mvmctl.api.host_operations.FsUtils.chown_to_real_user")
    mocker.patch(
        "mvmctl.api.host_operations.HostService.check_kvm_access",
        return_value=True,
    )
    mocker.patch(
        "mvmctl.api.host_operations.HostService.check_cloud_localds",
        return_value=True,
    )
    mocker.patch(
        "mvmctl.api.host_operations.NetworkUtils.detect_iptables_backend_conflict",
        return_value=(False, ""),
    )
    mocker.patch(
        "mvmctl.api.host_operations.HostService.check_required_binaries",
        return_value=[],
    )
    mocker.patch(
        "mvmctl.api.host_operations.HostService.validate_sudoers_binaries"
    )
    mocker.patch(
        "mvmctl.api.host_operations.HostService.create_group",
        return_value=False,
    )
    mocker.patch(
        "mvmctl.api.host_operations.HostService.add_user_to_group",
        return_value=False,
    )
    mocker.patch(
        "mvmctl.api.host_operations.HostService._generate_sudoers_content",
        return_value="content",
    )
    mocker.patch("mvmctl.api.host_operations.HostService.write_sudoers")
    mocker.patch(
        "mvmctl.api.host_operations.HostService.enable_ip_forward",
        return_value=None,
    )
    mocker.patch(
        "mvmctl.api.host_operations.HostService.persist_sysctl",
        return_value=None,
    )
    mocker.patch(
        "mvmctl.api.host_operations.SettingsService.resolve",
        return_value="default",
    )
    mocker.patch(
        "mvmctl.api.host_operations.HostService.save_iptables_rules",
        return_value=None,
    )

    mock_repo = MagicMock()
    mock_repo.list_changes.return_value = []
    mocker.patch(
        "mvmctl.api.host_operations.HostRepository", return_value=mock_repo
    )
    deps["repo"] = mock_repo

    mocker.patch(
        "mvmctl.api.host_operations.HostService.ensure_kvm_modules",
        return_value=([], 0),
    )

    mocker.patch("mvmctl.api.host_operations.NetworkRepository")
    mock_net_svc = MagicMock()
    mocker.patch(
        "mvmctl.api.host_operations.NetworkService", return_value=mock_net_svc
    )
    deps["net_svc"] = mock_net_svc

    mock_controller = MagicMock()
    mocker.patch(
        "mvmctl.api.host_operations.HostController",
        return_value=mock_controller,
    )
    deps["controller"] = mock_controller

    mocker.patch("mvmctl.api.host_operations.AuditLog.log")

    # NetworkOperation is imported locally in init() — patch at source
    mock_net_op_restore = MagicMock()
    mock_net_op_restore.is_ok = True
    mock_net_op_restore.item = None
    mocker.patch(
        "mvmctl.api.network_operations.NetworkOperation.restore",
        return_value=mock_net_op_restore,
    )
    deps["net_restore"] = mock_net_op_restore

    mock_default_result = _make_op_result(
        "success", "network.default_created", item=MagicMock()
    )
    mocker.patch(
        "mvmctl.api.network_operations.NetworkOperation.create_default_network",
        return_value=mock_default_result,
    )
    deps["net_default"] = mock_default_result

    return deps


class TestHostOperationInitExtended:
    """Extended tests for HostOperation.init() — every branch."""

    def test_init_needs_root_when_not_root(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=1000)
        result = HostOperation.init(Path("/tmp"))
        assert isinstance(result, NeedsInteraction)
        assert result.code == "privilege.sudo_required"

    def test_init_kvm_missing(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch(
            "mvmctl.api.host_operations.HostService.check_kvm_access",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.Path.exists", return_value=False
        )
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "error"
        assert result.code == "host.kvm.missing"

    def test_init_kvm_unreadable(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch(
            "mvmctl.api.host_operations.HostService.check_kvm_access",
            return_value=False,
        )
        mock_kvm = MagicMock()
        mock_kvm.exists.return_value = True
        mock_kvm.stat.return_value.st_mode = 0o200
        mocker.patch("mvmctl.api.host_operations.Path", return_value=mock_kvm)
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "error"
        assert result.code == "host.kvm.unreadable"

    def test_init_iptables_conflict(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.detect_iptables_backend_conflict",
            return_value=(True, "mixed nft/legacy"),
        )
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "error"
        assert result.code == "host.iptables.conflict"

    def test_init_missing_binaries(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch(
            "mvmctl.api.host_operations.HostService.check_required_binaries",
            return_value=["ip", "brctl"],
        )
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "error"
        assert result.code == "host.binaries.missing"
        assert "ip" in result.message

    def test_init_cloud_localds_warning_logged(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch(
            "mvmctl.api.host_operations.HostService.check_cloud_localds",
            return_value=False,
        )
        mock_logger = mocker.patch("mvmctl.api.host_operations.logger.warning")
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "success"
        mock_logger.assert_called_once()

    def test_init_group_created_and_user_added(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch(
            "mvmctl.api.host_operations.os.environ.get", return_value="testuser"
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.create_group",
            return_value=True,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.add_user_to_group",
            return_value=True,
        )
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "success"
        assert result.metadata["user_added_to_group"] is True

    def test_init_sudoers_stale_and_rewritten(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mock_path_class = mocker.patch("mvmctl.api.host_operations.Path")
        mock_sudoers_path = MagicMock()
        mock_sudoers_path.exists.return_value = True
        mock_sudoers_path.read_text.return_value = "old-content"
        mock_path_class.return_value = mock_sudoers_path
        mocker.patch(
            "mvmctl.api.host_operations.HostService._generate_sudoers_content",
            return_value="new-content",
        )
        mock_write = mocker.patch(
            "mvmctl.api.host_operations.HostService.write_sudoers"
        )
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "success"
        mock_write.assert_called_once()

    def test_init_sudoers_read_error_handled(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mock_path_class = mocker.patch("mvmctl.api.host_operations.Path")
        mock_sudoers_path = MagicMock()
        mock_sudoers_path.exists.return_value = True
        mock_sudoers_path.read_text.side_effect = PermissionError("denied")
        mock_path_class.return_value = mock_sudoers_path
        mock_write = mocker.patch(
            "mvmctl.api.host_operations.HostService.write_sudoers"
        )
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "success"
        mock_write.assert_called_once()

    def test_init_already_configured_skipped(self, mocker):
        deps = _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        result = HostOperation.init(Path("/tmp"))
        # chain_change is always added, so result is "success" not "skipped"
        assert result.status == "success"
        deps["controller"].record_changes.assert_called_once()

    def test_init_restore_existing_network(self, mocker):
        deps = _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mock_net_item = MagicMock()
        mock_net_op_restore = deps["net_restore"]
        mock_net_op_restore.is_ok = True
        mock_net_op_restore.item = mock_net_item
        result = HostOperation.init(Path("/tmp"))
        # restore has item => create_default_network is NOT called
        assert result.status == "success"

    def test_init_default_network_creation_fails_gracefully(self, mocker):
        deps = _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mock_restore_op = deps["net_restore"]
        mock_restore_op.is_ok = True
        mock_restore_op.item = None
        mock_default_op = _make_op_result(
            "error", "network.default_created_failed"
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.create_default_network",
            return_value=mock_default_op,
        )
        mock_logger = mocker.patch("mvmctl.api.host_operations.logger.warning")
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "success"
        mock_logger.assert_called_once()

    def test_init_restore_raises_exception_handled(self, mocker):
        _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.restore",
            side_effect=Exception("network error"),
        )
        mock_logger = mocker.patch("mvmctl.api.host_operations.logger.warning")
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "success"
        mock_logger.assert_called_once()

    def test_init_recording_failures_handled_gracefully(self, mocker):
        deps = _patch_init_common(mocker)
        mocker.patch("mvmctl.api.host_operations.os.getuid", return_value=0)
        mocker.patch(
            "mvmctl.api.host_operations.HostService.create_group",
            return_value=True,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService._generate_sudoers_content",
            return_value="new-content",
        )
        mock_path_class = mocker.patch("mvmctl.api.host_operations.Path")
        mock_sudoers_path = MagicMock()
        mock_sudoers_path.exists.return_value = True
        mock_sudoers_path.read_text.return_value = "old"
        mock_path_class.return_value = mock_sudoers_path

        deps["controller"].record_changes.side_effect = Exception(
            "DB write failed"
        )
        mock_logger = mocker.patch("mvmctl.api.host_operations.logger.warning")
        result = HostOperation.init(Path("/tmp"))
        assert result.status == "success"
        assert mock_logger.call_count >= 1


class TestHostOperationCleanExtended:
    """Extended tests for HostOperation.clean() — every branch."""

    def _setup_clean_mocks(self, mocker) -> None:
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mocker.patch("mvmctl.api.host_operations.NetworkRepository")
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_bridges",
            return_value=[],
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog.log")
        mocker.patch("mvmctl.api.host_operations.NetworkService")

    def test_clean_removes_tap_devices(self, mocker):
        self._setup_clean_mocks(mocker)
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=["mvm-tap0", "mvm-tap1", "other-tap"],
        )
        mock_remove_raw_tap = mocker.patch(
            "mvmctl.api.host_operations.NetworkService.remove_raw_tap"
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"
        assert mock_remove_raw_tap.call_count == 2

    def test_clean_tap_removal_error_handled(self, mocker):
        self._setup_clean_mocks(mocker)
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=["mvm-tap0"],
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkService.remove_raw_tap",
            side_effect=NetworkError("ip error"),
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"

    def test_clean_removes_network_bridges(self, mocker):
        mock_net = MagicMock()
        mock_net.name = "testnet"
        mock_net.bridge = "mvm-testnet"
        mock_net.nat_enabled = True
        mock_net.nat_gateways_list = ["eth0"]
        mock_net.subnet = "10.0.0.0/24"
        mock_net.id = "net-id-1"
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = [mock_net]
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mock_net_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.host_operations.NetworkService",
            return_value=mock_net_svc,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges"
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mocker.patch(
            "mvmctl.api.host_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_bridges",
            return_value=[],
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog.log")
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"
        mock_net_svc.remove_nat.assert_called_once()
        mock_net_svc.remove_bridge.assert_called_once()

    def test_clean_network_bridge_error_handled(self, mocker):
        mock_net = MagicMock()
        mock_net.name = "testnet"
        mock_net.bridge = "mvm-testnet"
        mock_net.nat_enabled = False
        mock_net.subnet = "10.0.0.0/24"
        mock_net.id = "net-id-1"
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = [mock_net]
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mock_net_svc = MagicMock()
        mock_net_svc.remove_bridge.side_effect = NetworkError("bridge in use")
        mocker.patch(
            "mvmctl.api.host_operations.NetworkService",
            return_value=mock_net_svc,
        )
        self._setup_clean_mocks(mocker)
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"

    def test_clean_removes_default_bridge(self, mocker):
        self._setup_clean_mocks(mocker)
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )
        mock_remove_raw_bridge = mocker.patch(
            "mvmctl.api.host_operations.NetworkService.remove_raw_bridge"
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"
        assert mock_remove_raw_bridge.call_count >= 1

    def test_clean_default_bridge_error_handled(self, mocker):
        self._setup_clean_mocks(mocker)
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkService.remove_raw_bridge",
            side_effect=NetworkError("ip error"),
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"

    def test_clean_removes_orphan_bridges(self, mocker):
        self._setup_clean_mocks(mocker)
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_bridges",
            return_value=["mvm-orphan1", "mvm-orphan2"],
        )
        mock_remove_raw_bridge = mocker.patch(
            "mvmctl.api.host_operations.NetworkService.remove_raw_bridge"
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"
        assert mock_remove_raw_bridge.call_count == 2

    def test_clean_removes_default_network(self, mocker):
        mock_net = MagicMock()
        mock_net.name = "default"
        mock_net.bridge = "mvm-default"
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = [mock_net]
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
            return_value=mock_repo,
        )
        self._setup_clean_mocks(mocker)
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mocker.patch("mvmctl.api.host_operations.NetworkService")
        mock_network_op = _make_op_result("success", "ok")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.remove",
            return_value=mock_network_op,
        )
        mocker.patch("mvmctl.api.inputs._network_input.NetworkInput")
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"

    def test_clean_default_network_remove_error_handled(self, mocker):
        mock_net = MagicMock()
        mock_net.name = "default"
        mock_net.bridge = "mvm-default"
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = [mock_net]
        mocker.patch(
            "mvmctl.api.host_operations.NetworkRepository",
            return_value=mock_repo,
        )
        self._setup_clean_mocks(mocker)
        mocker.patch(
            "mvmctl.api.host_operations.NetworkUtils.get_tuntap_devices",
            return_value=[],
        )
        mocker.patch("mvmctl.api.host_operations.NetworkService")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.remove",
            side_effect=NetworkError("remove failed"),
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"

    def test_clean_removes_mvm_chains(self, mocker):
        self._setup_clean_mocks(mocker)
        mock_net_svc = MagicMock()
        mocker.patch(
            "mvmctl.api.host_operations.NetworkService",
            return_value=mock_net_svc,
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"
        mock_net_svc.remove_mvm_chains.assert_called_once()

    def test_clean_mvm_chains_error_handled(self, mocker):
        mock_net_svc = MagicMock()
        mock_net_svc.remove_mvm_chains.side_effect = NetworkError(
            "chains error"
        )
        mocker.patch(
            "mvmctl.api.host_operations.NetworkService",
            return_value=mock_net_svc,
        )
        self._setup_clean_mocks(mocker)
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"

    def test_clean_already_clean(self, mocker):
        self._setup_clean_mocks(mocker)
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "success"
        assert len(result.item) >= 1

    def test_clean_privilege_error_returns_error(self, mocker):
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges",
            side_effect=PrivilegeError("no access"),
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "error"
        assert result.code == "host.clean_failed"

    def test_clean_network_error_returns_error(self, mocker):
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges",
            side_effect=HostError("unexpected"),
        )
        result = HostOperation.clean(Path("/tmp"))
        assert result.status == "error"


class TestHostOperationResetExtended:
    """Extended tests for HostOperation.reset() — every branch."""

    def test_reset_clean_error_propagated(self, mocker):
        _patch_host_deps(mocker)
        mock_clean = _make_op_result("error", "host.clean_failed")
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=mock_clean,
        )
        result = HostOperation.reset(Path("/tmp"))
        assert result.status == "error"

    def test_reset_restore_state_with_changes(self, mocker):
        _patch_host_deps(mocker)
        mock_clean = _make_op_result(
            "success", "ok", item=["Cleaned TAPs", "Cleaned bridges"]
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=mock_clean,
        )
        mock_repo = MagicMock()
        mock_repo.list_changes.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository", return_value=mock_repo
        )
        mock_change_item = MagicMock()
        mock_change_item.setting = "ip_forward"
        mock_service = MagicMock()
        mock_service.restore_state.return_value = [mock_change_item]
        mocker.patch(
            "mvmctl.api.host_operations.HostService", return_value=mock_service
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_sudoers",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_user_from_group",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_group",
            return_value=False,
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog.log")
        result = HostOperation.reset(Path("/tmp"))
        assert result.status == "success"
        assert any("Reverted ip_forward" in s for s in result.item)

    def test_reset_restore_state_raises_host_error(self, mocker):
        _patch_host_deps(mocker)
        mock_clean = _make_op_result("success", "ok")
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=mock_clean,
        )
        mock_repo = MagicMock()
        mock_repo.list_changes.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository", return_value=mock_repo
        )
        mock_service = MagicMock()
        mock_service.restore_state.side_effect = HostError("no state")
        mocker.patch(
            "mvmctl.api.host_operations.HostService", return_value=mock_service
        )
        mock_logger = mocker.patch("mvmctl.api.host_operations.logger.warning")
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_sudoers",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_user_from_group",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_group",
            return_value=False,
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog.log")
        result = HostOperation.reset(Path("/tmp"))
        assert result.status == "success"
        mock_logger.assert_called_once()

    def test_reset_module_changes_notification(self, mocker):
        _patch_host_deps(mocker)
        mock_clean = _make_op_result("success", "ok")
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=mock_clean,
        )
        mock_module_change = MagicMock()
        mock_module_change.setting = "kernel_module_load"
        mock_module_change.applied_value = "kvm_intel"
        mock_repo = MagicMock()

        def list_changes_side_effect(**kwargs: object) -> list:
            include_reverted = kwargs.get("include_reverted", True)
            if not include_reverted:
                return [mock_module_change]
            return []

        mock_repo.list_changes.side_effect = list_changes_side_effect
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository", return_value=mock_repo
        )
        mock_service = MagicMock()
        mock_service.restore_state.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostService", return_value=mock_service
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_sudoers",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_user_from_group",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_group",
            return_value=False,
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog.log")
        result = HostOperation.reset(Path("/tmp"))
        assert result.status == "success"
        assert any("kvm_intel" in s for s in result.item)

    def test_reset_removes_sudoers_and_user_and_group(self, mocker):
        _patch_host_deps(mocker)
        mock_clean = _make_op_result("success", "ok")
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=mock_clean,
        )
        mock_usermod_change = MagicMock()
        mock_usermod_change.mechanism = "usermod"
        mock_usermod_change.applied_value = "testuser:mvm"
        mock_repo = MagicMock()

        def list_changes_side_effect(**kwargs: object) -> list:
            if kwargs.get("include_reverted", True):
                return [mock_usermod_change]
            return [mock_usermod_change]

        mock_repo.list_changes.side_effect = list_changes_side_effect
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository", return_value=mock_repo
        )
        mock_service = MagicMock()
        mock_service.restore_state.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostService", return_value=mock_service
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog.log")
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_sudoers",
            return_value=True,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_user_from_group",
            return_value=True,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_group",
            return_value=True,
        )
        result = HostOperation.reset(Path("/tmp"))
        assert result.status == "success"
        assert any("sudoers" in s.lower() for s in result.item)
        assert any("testuser" in s for s in result.item)
        assert any("mvm" in s for s in result.item)

    def test_reset_remove_sudoers_error_handled(self, mocker):
        _patch_host_deps(mocker)
        mock_clean = _make_op_result("success", "ok")
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=mock_clean,
        )
        mock_repo = MagicMock()
        mock_repo.list_changes.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository", return_value=mock_repo
        )
        mock_service = MagicMock()
        mock_service.restore_state.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostService", return_value=mock_service
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog.log")
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_sudoers",
            side_effect=HostError("cannot remove"),
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_user_from_group",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_group",
            return_value=False,
        )
        result = HostOperation.reset(Path("/tmp"))
        assert result.status == "success"
        assert any("Warning" in s for s in result.item)

    def test_reset_user_group_removal_error_handled(self, mocker):
        _patch_host_deps(mocker)
        mock_clean = _make_op_result("success", "ok")
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=mock_clean,
        )
        mock_usermod_change = MagicMock()
        mock_usermod_change.mechanism = "usermod"
        mock_usermod_change.applied_value = "testuser:mvm"
        mock_repo = MagicMock()

        def list_changes_side_effect(**kwargs: object) -> list:
            include_reverted = kwargs.get("include_reverted", True)
            if include_reverted:
                return [mock_usermod_change]
            return []

        mock_repo.list_changes.side_effect = list_changes_side_effect
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository", return_value=mock_repo
        )
        mock_service = MagicMock()
        mock_service.restore_state.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostService", return_value=mock_service
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog.log")
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_sudoers",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_user_from_group",
            side_effect=HostError("cannot remove user"),
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_group",
            side_effect=HostError("cannot remove group"),
        )
        result = HostOperation.reset(Path("/tmp"))
        assert result.status == "success"

    def test_reset_privilege_error(self, mocker):
        mocker.patch(
            "mvmctl.api.host_operations.HostPrivilegeHelper.check_privileges",
            side_effect=PrivilegeError("no access"),
        )
        result = HostOperation.reset(Path("/tmp"))
        assert result.status == "error"
        assert result.code == "host.reset_failed"

    def test_reset_host_error_from_restore(self, mocker):
        _patch_host_deps(mocker)
        mock_clean = _make_op_result("success", "ok")
        mocker.patch(
            "mvmctl.api.host_operations.HostOperation.clean",
            return_value=mock_clean,
        )
        mock_repo = MagicMock()
        mock_repo.list_changes.return_value = []
        mocker.patch(
            "mvmctl.api.host_operations.HostRepository", return_value=mock_repo
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_sudoers",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_user_from_group",
            return_value=False,
        )
        mocker.patch(
            "mvmctl.api.host_operations.HostService.remove_group",
            return_value=False,
        )
        mocker.patch("mvmctl.api.host_operations.AuditLog.log")
        mock_logger = mocker.patch("mvmctl.api.host_operations.logger.warning")
        mock_service = MagicMock()
        mock_service.restore_state.side_effect = HostError("restore failed")
        mocker.patch(
            "mvmctl.api.host_operations.HostService", return_value=mock_service
        )
        result = HostOperation.reset(Path("/tmp"))
        assert result.status == "success"
        mock_logger.assert_called_once()
