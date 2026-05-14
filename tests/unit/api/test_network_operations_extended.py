"""Tests for NetworkOperation — network management orchestration."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.api.inputs._network_create_input import NetworkCreateInput
from mvmctl.api.inputs._network_input import NetworkInput
from mvmctl.api.network_operations import NetworkOperation
from mvmctl.exceptions import NetworkError
from mvmctl.models import NetworkItem
from mvmctl.models.result import NeedsInteraction, OperationResult


def _make_network(
    name: str = "testnet",
    subnet: str = "10.0.0.0/24",
    bridge: str = "mvm-testnet",
    ipv4_gateway: str = "10.0.0.1",
    nat_enabled: bool = True,
    nat_gateways: str | None = "eth0",
    is_default: bool = False,
    bridge_active: bool = False,
    network_id: str | None = None,
) -> NetworkItem:
    nid = network_id or f"net-{name}-" + "x" * 55
    return NetworkItem(
        id=nid,
        name=name,
        subnet=subnet,
        bridge=bridge,
        ipv4_gateway=ipv4_gateway,
        bridge_active=bridge_active,
        nat_enabled=nat_enabled,
        nat_gateways=nat_gateways,
        is_default=is_default,
        is_present=True,
        created_at="2026-01-01T00:00:00+00:00",
        updated_at="2026-01-01T00:00:00+00:00",
    )


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


class TestNetworkOperationCreate:
    """Tests for NetworkOperation.create()."""

    def test_create_success(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.name = "testnet"
        mock_resolved.subnet = "10.0.0.0/24"
        mock_resolved.bridge = "mvm-testnet"
        mock_resolved.ipv4_gateway = "10.0.0.1"
        mock_resolved.nat_enabled = True
        mock_resolved.nat_gateways = ["eth0"]

        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_create_input.NetworkCreateRequest",
            return_value=mock_request,
        )

        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = mock_item
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )

        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.api.network_operations.HashGenerator.network",
            return_value="hash123",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.compute_bridge_address",
            return_value="10.0.0.1/24",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )
        mocker.patch("mvmctl.api.network_operations.AuditLog")
        mock_service = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )

        result = NetworkOperation.create(
            NetworkCreateInput(name="testnet", subnet="10.0.0.0/24")
        )
        assert result.status == "success"
        assert result.code == "network.created"
        assert result.item is mock_item

    def test_create_infrastructure_failure_cleans_up(self, mocker):
        mock_resolved = MagicMock()
        mock_resolved.name = "testnet"
        mock_resolved.subnet = "10.0.0.0/24"
        mock_resolved.bridge = "mvm-testnet"
        mock_resolved.ipv4_gateway = "10.0.0.1"
        mock_resolved.nat_enabled = True
        mock_resolved.nat_gateways = ["eth0"]

        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_create_input.NetworkCreateRequest",
            return_value=mock_request,
        )

        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )

        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.api.network_operations.HashGenerator.network",
            return_value="hash123",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.compute_bridge_address",
            return_value="10.0.0.1/24",
        )

        mock_service = MagicMock()
        mock_service.ensure_bridge.side_effect = NetworkError("bridge failed")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )

        result = NetworkOperation.create(
            NetworkCreateInput(name="testnet", subnet="10.0.0.0/24")
        )
        assert result.status == "error"
        assert result.code == "network.create_failed"
        mock_repo.delete.assert_called_once_with("hash123")

    def test_create_fetch_updated_item_fails(self, mocker):
        mock_resolved = MagicMock()
        mock_resolved.name = "testnet"
        mock_resolved.subnet = "10.0.0.0/24"
        mock_resolved.bridge = "mvm-testnet"
        mock_resolved.ipv4_gateway = "10.0.0.1"
        mock_resolved.nat_enabled = False
        mock_resolved.nat_gateways = []

        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_create_input.NetworkCreateRequest",
            return_value=mock_request,
        )

        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )

        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.core.config._service.SettingsService.resolve",
            return_value="iptables",
        )
        mocker.patch(
            "mvmctl.api.network_operations.HashGenerator.network",
            return_value="hash123",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.compute_bridge_address",
            return_value="10.0.0.1/24",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        # _run_batch would try real sudo via privileged_cmd; mock it as a no-op
        # since this test only cares about the post-creation DB fetch failing.
        mocker.patch(
            "mvmctl.core.network._service.NetworkUtils._run_batch",
        )
        # ensure_ip_forwarding also calls privileged_cmd → sudo subprocess.
        mocker.patch(
            "mvmctl.core.network._service.NetworkService.ensure_ip_forwarding",
        )

        result = NetworkOperation.create(
            NetworkCreateInput(name="testnet", subnet="10.0.0.0/24")
        )
        assert result.status == "error"
        assert result.code == "network.create_failed"

    def test_create_nat_enabled_ensures_nat(self, mocker):
        mock_resolved = MagicMock()
        mock_resolved.name = "natnet"
        mock_resolved.subnet = "10.10.0.0/24"
        mock_resolved.bridge = "mvm-natnet"
        mock_resolved.ipv4_gateway = "10.10.0.1"
        mock_resolved.nat_enabled = True
        mock_resolved.nat_gateways = ["eth0"]

        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_create_input.NetworkCreateRequest",
            return_value=mock_request,
        )

        mock_item = _make_network(name="natnet", subnet="10.10.0.0/24")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = mock_item
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )

        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.api.network_operations.HashGenerator.network",
            return_value="hash123",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.compute_bridge_address",
            return_value="10.10.0.1/24",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )
        mocker.patch("mvmctl.api.network_operations.AuditLog")

        mock_service = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )

        NetworkOperation.create(
            NetworkCreateInput(name="natnet", subnet="10.10.0.0/24")
        )
        mock_service.ensure_bridge.assert_called_once()
        mock_service.ensure_nat.assert_called_once()


class TestNetworkOperationRemove:
    """Tests for NetworkOperation.remove()."""

    def test_remove_success(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mock_service = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.network_operations.AuditLog")

        result = NetworkOperation.remove(NetworkInput(name=["testnet"]))
        assert result.status == "success"
        assert result.code == "network.removed"
        mock_service.remove.assert_called_once_with(mock_item, force=False)

    def test_remove_with_force(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mock_service = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.network_operations.AuditLog")

        NetworkOperation.remove(NetworkInput(name=["testnet"]), force=True)
        mock_service.remove.assert_called_once_with(mock_item, force=True)

    def test_remove_resolution_failure(self, mocker):
        mock_request = MagicMock()
        mock_request.resolve.side_effect = NetworkError("network not found")
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.core.config._service.SettingsService.resolve",
            return_value="iptables",
        )
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )

        result = NetworkOperation.remove(NetworkInput(name=["nonexistent"]))
        assert result.status == "error"
        assert "not found" in result.message

    def test_remove_in_use_error(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mock_service = MagicMock()
        mock_service.remove.side_effect = NetworkError("network in use by VM")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )

        result = NetworkOperation.remove(NetworkInput(name=["testnet"]))
        assert result.status == "error"
        assert result.code == "network.in_use"


class TestNetworkOperationGet:
    """Tests for NetworkOperation.get()."""

    def test_get_success(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")

        result = NetworkOperation.get(NetworkInput(name=["testnet"]))
        assert result.name == "testnet"

    def test_get_multiple_raises_error(self, mocker):
        mock_resolved = MagicMock()
        mock_resolved.networks = [_make_network("a"), _make_network("b")]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")

        with pytest.raises(NetworkError, match="Expected exactly one"):
            NetworkOperation.get(NetworkInput(name=["ambiguous"]))


class TestNetworkOperationList:
    """Tests for NetworkOperation.list_all()."""

    def test_list_all_with_networks(self, mocker):
        mock_networks = [_make_network("a"), _make_network("b")]
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")

        mock_service = MagicMock()
        mock_service.list_all.return_value = mock_networks
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )

        mock_resolver = MagicMock()
        mock_resolver.enrich.return_value = mock_networks
        mocker.patch(
            "mvmctl.core.network._resolver.NetworkResolver",
            return_value=mock_resolver,
        )

        result = NetworkOperation.list_all()
        assert len(result) == 2
        mock_service.list_all.assert_called_once_with(verify=True)

    def test_list_all_empty(self, mocker):
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_service = MagicMock()
        mock_service.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )

        result = NetworkOperation.list_all()
        assert result == []


class TestNetworkOperationInspect:
    """Tests for NetworkOperation.inspect()."""

    def test_inspect_returns_network_item(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = mock_item
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )

        result = NetworkOperation.inspect(NetworkInput(name=["testnet"]))
        assert isinstance(result, NetworkItem)
        assert result.name == "testnet"

    def test_inspect_returns_json_dict(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = mock_item
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )

        result = NetworkOperation.inspect(
            NetworkInput(name=["testnet"]), is_json=True
        )
        assert isinstance(result, dict)
        assert result["name"] == "testnet"

    def test_inspect_updates_bridge_active(self, mocker):
        mock_item = _make_network(bridge_active=False)
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = mock_item
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )

        NetworkOperation.inspect(NetworkInput(name=["testnet"]))
        mock_repo.update_bridge_active.assert_called_once()

    def test_inspect_updated_item_not_found_raises(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )

        with pytest.raises(NetworkError, match="not found after update"):
            NetworkOperation.inspect(NetworkInput(name=["testnet"]))

    def test_inspect_multiple_raises(self, mocker):
        mock_resolved = MagicMock()
        mock_resolved.networks = [_make_network("a"), _make_network("b")]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")

        with pytest.raises(NetworkError, match="Expected exactly one"):
            NetworkOperation.inspect(NetworkInput(name=["ambiguous"]))


class TestNetworkOperationSetDefault:
    """Tests for NetworkOperation.set_default()."""

    def test_set_default_success(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_controller = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkController",
            return_value=mock_controller,
        )
        mocker.patch("mvmctl.api.network_operations.AuditLog")

        result = NetworkOperation.set_default(NetworkInput(name=["testnet"]))
        assert result.status == "success"
        assert result.code == "network.default_set"
        mock_controller.set_default.assert_called_once()

    def test_set_default_resolution_failure(self, mocker):
        mock_request = MagicMock()
        mock_request.resolve.side_effect = NetworkError("not found")
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")

        result = NetworkOperation.set_default(
            NetworkInput(name=["nonexistent"])
        )
        assert result.status == "error"

    def test_set_default_multiple_networks(self, mocker):
        mock_resolved = MagicMock()
        mock_resolved.networks = [_make_network("a"), _make_network("b")]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")

        result = NetworkOperation.set_default(NetworkInput(name=["ambiguous"]))
        assert result.status == "error"

    def test_set_default_controller_error(self, mocker):
        mock_item = _make_network()
        mock_resolved = MagicMock()
        mock_resolved.networks = [mock_item]
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.inputs._network_input.NetworkRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_controller = MagicMock()
        mock_controller.set_default.side_effect = NetworkError(
            "controller failed"
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkController",
            return_value=mock_controller,
        )

        result = NetworkOperation.set_default(NetworkInput(name=["testnet"]))
        assert result.status == "error"


class TestNetworkOperationCreateDefaultNetwork:
    """Tests for NetworkOperation.create_default_network()."""

    def test_create_default_network_creates_new(self, mocker):
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mock_repo.get_default.return_value = None
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.network_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.detect_outbound_interface",
            return_value="eth0",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.compute_bridge_address",
            return_value="172.16.0.1/24",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )
        mock_net_item = _make_network(name="default", is_default=True)
        mock_create_result = _make_op_result(
            "success", "ok", item=mock_net_item
        )
        mock_create_result.item = mock_net_item
        mock_create_result.status = "success"
        mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.create",
            return_value=mock_create_result,
        )
        mock_controller = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkController",
            return_value=mock_controller,
        )
        mock_service = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.network_operations.AuditLog")

        result = NetworkOperation.create_default_network()
        assert result.status == "success"
        mock_controller.set_default.assert_called_once()

    def test_create_default_network_already_exists(self, mocker):
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_net_item = _make_network(name="default", is_default=True)
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = mock_net_item
        mock_repo.get_default.return_value = mock_net_item
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.network_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.compute_bridge_address",
            return_value="172.16.0.1/24",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )
        mock_service = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )

        result = NetworkOperation.create_default_network()
        assert result.status == "success"

    def test_create_default_network_creates_result_is_needs_interaction(
        self, mocker
    ):
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.network_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.detect_outbound_interface",
            return_value="eth0",
        )
        needs_interaction = NeedsInteraction(
            code="privilege.sudo",
            message="needs sudo",
            input_type="sudo",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.create",
            return_value=needs_interaction,
        )

        result = NetworkOperation.create_default_network()
        assert result.status == "error"
        assert result.code == "network.default_created_failed"

    def test_create_default_network_creates_result_item_none(self, mocker):
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = None
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.network_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.detect_outbound_interface",
            return_value="eth0",
        )
        mock_create = _make_op_result("success", "ok")
        mock_create.item = None
        mocker.patch(
            "mvmctl.api.network_operations.NetworkOperation.create",
            return_value=mock_create,
        )

        result = NetworkOperation.create_default_network()
        assert result.status == "error"

    def test_create_default_network_network_error(self, mocker):
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = _make_network(name="default")
        mock_repo.get_default.side_effect = NetworkError("DB error")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.network_operations.SettingsService.resolve",
            return_value="default",
        )

        result = NetworkOperation.create_default_network()
        assert result.status == "error"

    def test_create_default_network_nat_enabled(self, mocker):
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_net_item = _make_network(
            name="default", is_default=True, nat_enabled=True
        )
        mock_repo = MagicMock()
        mock_repo.get_by_name.return_value = mock_net_item
        mock_repo.get_default.return_value = mock_net_item
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.api.network_operations.SettingsService.resolve",
            return_value="default",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.compute_bridge_address",
            return_value="172.16.0.1/24",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=False,
        )
        mock_service = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )

        result = NetworkOperation.create_default_network()
        assert result.status == "success"
        mock_service.ensure_nat.assert_called_once()


class TestNetworkOperationSync:
    """Tests for NetworkOperation.sync()."""

    def test_sync_all_networks(self, mocker):
        mock_nets = [_make_network("a"), _make_network("b")]
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = mock_nets
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_service = MagicMock()
        mock_service.sync_iptables_rules.return_value = {
            "added": 1,
            "verified": 2,
            "orphaned": 0,
        }
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )

        result = NetworkOperation.sync()
        assert result.status == "success"
        assert result.code == "network.synced"

    def test_sync_specific_network(self, mocker):
        mock_net = _make_network("test")
        mock_repo = MagicMock()
        mock_repo.get.return_value = mock_net
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_service = MagicMock()
        mock_service.sync_iptables_rules.return_value = {
            "added": 1,
            "verified": 2,
            "orphaned": 0,
        }
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )

        result = NetworkOperation.sync(network_id="net-abc")
        assert result.status == "success"
        mock_repo.get.assert_called_once_with("net-abc")

    def test_sync_network_not_found(self, mocker):
        mock_repo = MagicMock()
        mock_repo.get.return_value = None
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.core.config._service.SettingsService.resolve",
            return_value="iptables",
        )

        result = NetworkOperation.sync(network_id="nonexistent")
        assert result.status == "error"

    def test_sync_bridge_reconciliation(self, mocker):
        mock_net = _make_network("test", bridge_active=False)
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = [mock_net]
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mock_service = MagicMock()
        mock_service.sync_iptables_rules.return_value = {
            "added": 0,
            "verified": 0,
            "orphaned": 0,
        }
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.bridge_exists",
            return_value=True,
        )

        result = NetworkOperation.sync()
        assert result.status == "success"
        mock_repo.update_bridge_active.assert_called_once()
        assert result.metadata["bridges_reconciled"] == 1

    def test_sync_network_error(self, mocker):
        mock_repo = MagicMock()
        mock_repo.list_all.side_effect = NetworkError("DB error")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.core.config._service.SettingsService.resolve",
            return_value="iptables",
        )

        result = NetworkOperation.sync()
        assert result.status == "error"


class TestNetworkOperationRestore:
    """Tests for NetworkOperation.restore()."""

    def test_restore_all_networks(self, mocker):
        mock_nets = [_make_network("a"), _make_network("b", nat_enabled=False)]
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = mock_nets
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.compute_bridge_address",
            return_value="172.16.0.1/24",
        )
        mock_service = MagicMock()
        mocker.patch(
            "mvmctl.api.network_operations.NetworkService",
            return_value=mock_service,
        )

        result = NetworkOperation.restore()
        assert result.status == "success"
        assert result.code == "network.restored"

    def test_restore_list_failure(self, mocker):
        mock_repo = MagicMock()
        mock_repo.list_all.side_effect = NetworkError("DB error")
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.core.config._service.SettingsService.resolve",
            return_value="iptables",
        )

        result = NetworkOperation.restore()
        assert result.status == "error"
        assert result.code == "network.restore_failed"

    def test_restore_individual_network_failure_handled(self, mocker):
        mock_net = _make_network("a")
        mock_repo = MagicMock()
        mock_repo.list_all.return_value = [mock_net]
        mocker.patch(
            "mvmctl.api.network_operations.NetworkRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.network_operations.Database")
        mocker.patch(
            "mvmctl.core.config._service.SettingsService.resolve",
            return_value="iptables",
        )
        mocker.patch(
            "mvmctl.api.network_operations.NetworkUtils.compute_bridge_address",
            side_effect=NetworkError("bad address"),
        )

        result = NetworkOperation.restore()
        assert result.status == "success"
        assert any("Failed to restore" in m for m in result.item)


class TestNetworkOperationHelpers:
    """Tests for _network_to_dict helper."""

    def test_network_to_dict_with_leases(self):
        net = _make_network()
        d = NetworkOperation._network_to_dict(net)
        assert d["name"] == "testnet"
        assert d["subnet"] == "10.0.0.0/24"
        assert d["leases"] == []

    def test_network_to_dict_with_nat_gateways(self):
        net = _make_network(nat_gateways="eth0,eth1")
        d = NetworkOperation._network_to_dict(net)
        assert d["nat_gateways"] == ["eth0", "eth1"]

    def test_network_to_dict_without_nat_gateways(self):
        net = _make_network(nat_gateways=None)
        d = NetworkOperation._network_to_dict(net)
        assert d["nat_gateways"] == []
