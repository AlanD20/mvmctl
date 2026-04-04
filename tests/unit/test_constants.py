"""Tests for constants.py."""

from unittest.mock import patch

from mvmctl.constants import (
    BRIDGE_NAME,
    CLI_NAME,
    DEFAULT_NETWORK_IPV4_GATEWAY,
    DEFAULT_NETWORK_NAME,
    DEFAULT_NETWORK_SUBNET,
    FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S,
    FIRECRACKER_SIGTERM_WAIT_S,
    PRIVILEGED_BINARIES,
    PROJECT_GROUP,
    PROJECT_NAME,
    PROJECT_NAME_UPPER,
    SUDOERS_DROP_IN_PATH,
    TAP_PREFIX,
    _resolve_project_name,
    cache_dir_name,
    config_filename,
    device_prefix,
    env_var,
)


def test_resolve_project_name_package_found():
    with patch("mvmctl.constants.importlib.metadata.metadata") as mock_meta:
        mock_meta.return_value = {"Name": "mvmctl"}
        assert _resolve_project_name() == "mvmctl"


def test_resolve_project_name_package_not_found():
    import importlib.metadata

    with patch(
        "mvmctl.constants.importlib.metadata.metadata",
        side_effect=importlib.metadata.PackageNotFoundError("not installed"),
    ):
        assert _resolve_project_name() == "mvmctl"


def test_project_name_is_string():
    assert isinstance(PROJECT_NAME, str)
    assert len(PROJECT_NAME) > 0


def test_project_name_upper():
    assert PROJECT_NAME_UPPER == PROJECT_NAME.replace("-", "_").upper()


def test_cli_name():
    assert CLI_NAME == "mvm"


def test_env_var():
    assert env_var("CACHE_DIR") == "MVM_CACHE_DIR"
    assert env_var("LOG_LEVEL") == "MVM_LOG_LEVEL"


def test_cache_dir_name():
    assert cache_dir_name() == PROJECT_NAME


def test_device_prefix():
    assert device_prefix() == "mvm"


def test_config_filename():
    assert config_filename() == "mvm.yaml"


def test_bridge_name():
    assert BRIDGE_NAME == "mvm-br0"


def test_tap_prefix():
    assert TAP_PREFIX == "mvm-tap"


def test_project_group():
    assert PROJECT_GROUP == CLI_NAME
    assert PROJECT_GROUP == "mvm"


def test_sudoers_drop_in_path():
    assert SUDOERS_DROP_IN_PATH == "/etc/sudoers.d/mvm"


def test_default_network_name():
    assert DEFAULT_NETWORK_NAME == "default"


def test_default_network_subnet():
    assert DEFAULT_NETWORK_SUBNET == "172.35.0.0/24"


def test_default_network_gateway():
    assert DEFAULT_NETWORK_IPV4_GATEWAY == "172.35.0.1"


def test_firecracker_graceful_shutdown_timeout():
    assert FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S == 5


def test_firecracker_sigterm_wait():
    assert FIRECRACKER_SIGTERM_WAIT_S == 1


def test_privileged_binaries():
    assert isinstance(PRIVILEGED_BINARIES, list)
    assert len(PRIVILEGED_BINARIES) == 7
    assert "/usr/sbin/ip" in PRIVILEGED_BINARIES
    assert "/usr/sbin/iptables" in PRIVILEGED_BINARIES
    assert "/usr/sbin/sysctl" in PRIVILEGED_BINARIES
    assert "/usr/bin/mount" in PRIVILEGED_BINARIES
    assert "/usr/bin/umount" in PRIVILEGED_BINARIES
