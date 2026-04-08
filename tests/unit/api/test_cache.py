"""Unit tests for api/cache.py — privilege boundary verification."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

from mvmctl.api import cache as cache_api
from mvmctl.models.vm import VMStatus
from mvmctl.models.network import NetworkConfig

# =============================================================================
# init_all tests
# =============================================================================


@patch("mvmctl.api.host.check_privileges_interactive")
@patch("mvmctl.core.cache_manager.cache_init_all")
def test_init_all_calls_core_functions(mock_init_all, mock_check_privs):
    """Verify init_all delegates to core cache_manager functions."""
    mock_init_all.return_value = {
        "vms": Path("/tmp/cache/vms"),
        "networks": Path("/tmp/cache/networks"),
        "images": Path("/tmp/cache/images"),
        "kernels": Path("/tmp/cache/kernels"),
    }

    result = cache_api.init_all()

    mock_init_all.assert_called_once()
    assert result["vms"] == "/tmp/cache/vms"
    assert result["networks"] == "/tmp/cache/networks"
    assert result["images"] == "/tmp/cache/images"
    assert result["kernels"] == "/tmp/cache/kernels"
    # guestfs is not included (removed)
    assert "guestfs" not in result


# =============================================================================
# prune_vms tests
# =============================================================================


@patch("mvmctl.api.host.check_privileges_interactive")
@patch("mvmctl.core.vm_manager.get_vm_manager")
@patch("mvmctl.api.cache.remove_vm")
def test_prune_vms_privilege_check(mock_remove_vm, mock_get_vm_manager, mock_check_privs):
    """Verify privilege check is called for prune_vms."""
    mock_vm = MagicMock()
    mock_vm.name = "stopped-vm"
    mock_vm.status = VMStatus.STOPPED
    mock_get_vm_manager.return_value.list_all.return_value = [mock_vm]

    result = cache_api.prune_vms(include_stopped=True)

    mock_check_privs.assert_called_once_with("/usr/sbin/ip", "prune VMs")
    mock_remove_vm.assert_called_once_with("stopped-vm")
    assert result == ["stopped-vm"]


@patch("mvmctl.api.host.check_privileges_interactive")
@patch("mvmctl.core.vm_manager.get_vm_manager")
@patch("mvmctl.api.cache.remove_vm")
def test_prune_vms_passes_flags(mock_remove_vm, mock_get_vm_manager, mock_check_privs):
    """Verify flags are passed correctly to prune_vms."""
    mock_vm = MagicMock()
    mock_vm.name = "test-vm"
    mock_vm.status = VMStatus.STOPPED
    mock_get_vm_manager.return_value.list_all.return_value = [mock_vm]

    result = cache_api.prune_vms(include_stopped=True, include_running=False)

    mock_remove_vm.assert_called_once_with("test-vm")
    assert result == ["test-vm"]

    mock_remove_vm.reset_mock()
    result = cache_api.prune_vms(include_stopped=True, dry_run=True)

    mock_remove_vm.assert_not_called()
    assert result == ["test-vm"]


# =============================================================================
# prune_networks tests
# =============================================================================


@patch("mvmctl.api.host.check_privileges_interactive")
@patch("mvmctl.api.cache.list_networks")
@patch("mvmctl.api.cache.remove_network")
def test_prune_networks_privilege_check(mock_remove_network, mock_list_networks, mock_check_privs):
    """Verify privilege check is called for prune_networks."""
    mock_network = MagicMock()
    mock_network.name = "unused-net"
    mock_list_networks.return_value = [mock_network]

    result = cache_api.prune_networks()

    mock_check_privs.assert_called_once_with("/usr/sbin/ip", "prune networks")
    mock_remove_network.assert_called_once_with("unused-net")
    assert result == ["unused-net"]


# =============================================================================
# prune_all tests
# =============================================================================


@patch("mvmctl.api.host.check_privileges_interactive")
@patch("mvmctl.core.vm_manager.get_vm_manager")
@patch("mvmctl.api.cache.list_networks")
@patch("mvmctl.core.metadata.list_image_entries")
@patch("mvmctl.core.metadata.list_kernel_entries")
def test_prune_all_privilege_check(
    mock_list_kernels,
    mock_list_images,
    mock_list_networks,
    mock_get_vm_manager,
    mock_check_privs,
):
    """Verify privilege checks from prune_all and its sub-operations."""
    mock_get_vm_manager.return_value.list_all.return_value = []
    mock_list_networks.return_value = []
    mock_list_images.return_value = {}
    mock_list_kernels.return_value = {}

    result = cache_api.prune_all()

    assert mock_check_privs.call_count == 3
    mock_check_privs.assert_any_call("/usr/sbin/ip", "prune all cache resources")
    mock_check_privs.assert_any_call("/usr/sbin/ip", "prune VMs")
    mock_check_privs.assert_any_call("/usr/sbin/ip", "prune networks")
    assert result["vms"] == []
    assert result["networks"] == []
    assert result["images"] == []
    assert result["kernels"] == []


@patch("mvmctl.api.host.check_privileges_interactive")
@patch("mvmctl.core.vm_manager.get_vm_manager")
@patch("mvmctl.api.cache.list_networks")
@patch("mvmctl.core.metadata.list_image_entries")
@patch("mvmctl.core.metadata.list_kernel_entries")
def test_prune_all_passes_flags(
    mock_list_kernels,
    mock_list_images,
    mock_list_networks,
    mock_get_vm_manager,
    mock_check_privs,
):
    """Verify flags are passed correctly to prune_all."""
    # Setup mocks
    mock_get_vm_manager.return_value.list_all.return_value = []
    mock_list_networks.return_value = []
    mock_list_images.return_value = {}
    mock_list_kernels.return_value = {}

    result = cache_api.prune_all(include_stopped=True, include_running=True)

    # Result should be empty since no VMs/networks/images/kernels to prune
    assert result == {"vms": [], "networks": [], "images": [], "kernels": []}
