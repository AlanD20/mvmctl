"""Tests for constants.py."""

from unittest.mock import patch

from fcm.constants import (
    BRIDGE_NAME,
    CLI_NAME,
    PROJECT_NAME,
    PROJECT_NAME_UPPER,
    TAP_PREFIX,
    _resolve_project_name,
    cache_dir_name,
    config_filename,
    device_prefix,
    env_var,
)


def test_resolve_project_name_package_found():
    with patch("fcm.constants.importlib.metadata.metadata") as mock_meta:
        mock_meta.return_value = {"Name": "firecracker-manager"}
        assert _resolve_project_name() == "firecracker-manager"


def test_resolve_project_name_package_not_found():
    import importlib.metadata

    with patch(
        "fcm.constants.importlib.metadata.metadata",
        side_effect=importlib.metadata.PackageNotFoundError("not installed"),
    ):
        assert _resolve_project_name() == "firecracker-manager"


def test_project_name_is_string():
    assert isinstance(PROJECT_NAME, str)
    assert len(PROJECT_NAME) > 0


def test_project_name_upper():
    assert PROJECT_NAME_UPPER == PROJECT_NAME.replace("-", "_").upper()


def test_cli_name():
    assert CLI_NAME == "fcm"


def test_env_var():
    assert env_var("CACHE_DIR") == "FCM_CACHE_DIR"
    assert env_var("LOG_LEVEL") == "FCM_LOG_LEVEL"


def test_cache_dir_name():
    assert cache_dir_name() == PROJECT_NAME


def test_device_prefix():
    assert device_prefix() == "fcm"


def test_config_filename():
    assert config_filename() == "fcm.yaml"


def test_bridge_name():
    assert BRIDGE_NAME == "fcm-br0"


def test_tap_prefix():
    assert TAP_PREFIX == "fcm"
