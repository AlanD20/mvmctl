"""Unit tests for api/cache.py — privilege boundary verification."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import patch

from mvmctl.api import cache as cache_api

# =============================================================================
# init_all tests
# =============================================================================


@patch("mvmctl.api.cache.check_privileges")
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


@patch("mvmctl.api.cache.check_privileges")
@patch("mvmctl.core.cache_manager.cache_init_all")
def test_init_all_privilege_check(mock_init_all, mock_check_privs):
    """Verify privilege check is called for init_all."""
    mock_init_all.return_value = {"vms": Path("/tmp/cache/vms")}

    cache_api.init_all()

    mock_check_privs.assert_called_once_with("/usr/sbin/ip")


# =============================================================================
# prune_vms tests
# =============================================================================


@patch("mvmctl.api.cache.check_privileges")
@patch("mvmctl.core.cache_manager.cache_prune_vms")
def test_prune_vms_privilege_check(mock_prune_vms, mock_check_privs):
    """Verify privilege check is called for prune_vms."""
    mock_prune_vms.return_value = ["vm1", "vm2"]

    result = cache_api.prune_vms()

    mock_check_privs.assert_called_once_with("/usr/sbin/ip")
    assert result == ["vm1", "vm2"]


@patch("mvmctl.api.cache.check_privileges")
@patch("mvmctl.core.cache_manager.cache_prune_vms")
def test_prune_vms_passes_flags(mock_prune_vms, mock_check_privs):
    """Verify flags are passed correctly to core prune_vms."""
    mock_prune_vms.return_value = ["vm1"]

    # Test with include_stopped=True
    result = cache_api.prune_vms(include_stopped=True, include_running=False)

    mock_prune_vms.assert_called_once_with(
        include_stopped=True, include_running=False, dry_run=False
    )
    assert result == ["vm1"]

    # Reset and test with include_running=True
    mock_prune_vms.reset_mock()
    mock_prune_vms.return_value = ["running-vm"]

    result = cache_api.prune_vms(include_stopped=False, include_running=True)

    mock_prune_vms.assert_called_once_with(
        include_stopped=False, include_running=True, dry_run=False
    )
    assert result == ["running-vm"]


# =============================================================================
# prune_networks tests
# =============================================================================


@patch("mvmctl.api.cache.check_privileges")
@patch("mvmctl.core.cache_manager.cache_prune_networks")
def test_prune_networks_privilege_check(mock_prune_networks, mock_check_privs):
    """Verify privilege check is called for prune_networks."""
    mock_prune_networks.return_value = ["unused-net"]

    result = cache_api.prune_networks()

    mock_check_privs.assert_called_once_with("/usr/sbin/ip")
    mock_prune_networks.assert_called_once()
    assert result == ["unused-net"]


# =============================================================================
# prune_images tests
# =============================================================================


@patch("mvmctl.api.cache.check_privileges")
@patch("mvmctl.core.cache_manager.cache_prune_images")
def test_prune_images_privilege_check(mock_prune_images, mock_check_privs):
    """Verify privilege check is called for prune_images."""
    mock_prune_images.return_value = ["abc123"]

    result = cache_api.prune_images()

    mock_check_privs.assert_called_once_with("/usr/sbin/ip")
    mock_prune_images.assert_called_once()
    assert result == ["abc123"]


# =============================================================================
# prune_kernels tests
# =============================================================================


@patch("mvmctl.api.cache.check_privileges")
@patch("mvmctl.core.cache_manager.cache_prune_kernels")
def test_prune_kernels_privilege_check(mock_prune_kernels, mock_check_privs):
    """Verify privilege check is called for prune_kernels."""
    mock_prune_kernels.return_value = ["def456"]

    result = cache_api.prune_kernels()

    mock_check_privs.assert_called_once_with("/usr/sbin/ip")
    mock_prune_kernels.assert_called_once()
    assert result == ["def456"]


# =============================================================================
# prune_all tests
# =============================================================================


@patch("mvmctl.api.cache.check_privileges")
@patch("mvmctl.core.cache_manager.cache_prune_all")
def test_prune_all_privilege_check(mock_prune_all, mock_check_privs):
    """Verify privilege check is called for prune_all."""
    mock_prune_all.return_value = {
        "vms": ["vm1"],
        "networks": ["net1"],
        "images": ["img1"],
        "kernels": ["kern1"],
    }

    result = cache_api.prune_all()

    mock_check_privs.assert_called_once_with("/usr/sbin/ip")
    assert result["vms"] == ["vm1"]
    assert result["networks"] == ["net1"]
    assert result["images"] == ["img1"]
    assert result["kernels"] == ["kern1"]
    # guestfs is not included (removed)
    assert "guestfs" not in result


@patch("mvmctl.api.cache.check_privileges")
@patch("mvmctl.core.cache_manager.cache_prune_all")
def test_prune_all_passes_flags(mock_prune_all, mock_check_privs):
    """Verify flags are passed correctly to core prune_all."""
    mock_prune_all.return_value = {
        "vms": [],
        "networks": [],
        "images": [],
        "kernels": [],
    }

    # Test with include_stopped=True
    result = cache_api.prune_all(include_stopped=True, include_running=False)

    mock_prune_all.assert_called_once_with(
        include_stopped=True, include_running=False, dry_run=False
    )

    # Reset and test with include_running=True
    mock_prune_all.reset_mock()
    mock_prune_all.return_value = {
        "vms": ["running-vm"],
        "networks": [],
        "images": [],
        "kernels": [],
    }

    result = cache_api.prune_all(include_stopped=False, include_running=True)

    mock_prune_all.assert_called_once_with(
        include_stopped=False, include_running=True, dry_run=False
    )
    assert result["vms"] == ["running-vm"]
