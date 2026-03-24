from pathlib import Path
from unittest.mock import patch

import pytest

from fcm.core.user_config import (
    get_config_value,
    get_full_user_config,
    set_config_value,
    _coerce_value,
)


def test_set_and_get_simple(tmp_path: Path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    monkeypatch.setenv("FCM_CONFIG", str(config_file))

    set_config_value("network_interface", "wlo0")
    val = get_config_value("network_interface")
    assert val == "wlo0"


def test_set_and_get_nested(tmp_path: Path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    monkeypatch.setenv("FCM_CONFIG", str(config_file))

    set_config_value("network.bridge_cidr", "192.168.0.0/24")
    val = get_config_value("network.bridge_cidr")
    assert val == "192.168.0.0/24"


def test_get_missing_key(tmp_path: Path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    monkeypatch.setenv("FCM_CONFIG", str(config_file))

    val = get_config_value("nonexistent.key")
    assert val is None


def test_get_full_config_empty(tmp_path: Path, monkeypatch):
    config_file = tmp_path / "missing.yaml"
    monkeypatch.setenv("FCM_CONFIG", str(config_file))

    config = get_full_user_config()
    assert config == {}


def test_get_full_config_with_data(tmp_path: Path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    monkeypatch.setenv("FCM_CONFIG", str(config_file))

    set_config_value("foo", "bar")
    config = get_full_user_config()
    assert config.get("foo") == "bar"


def test_set_overwrites_existing(tmp_path: Path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    monkeypatch.setenv("FCM_CONFIG", str(config_file))

    set_config_value("key", "first")
    set_config_value("key", "second")
    assert get_config_value("key") == "second"


def test_coerce_value_bool_true():
    assert _coerce_value("true") is True
    assert _coerce_value("yes") is True


def test_coerce_value_bool_false():
    assert _coerce_value("false") is False
    assert _coerce_value("no") is False


def test_coerce_value_int():
    assert _coerce_value("42") == 42


def test_coerce_value_float():
    assert _coerce_value("3.14") == pytest.approx(3.14)


def test_coerce_value_string():
    assert _coerce_value("hello") == "hello"


def test_config_persists_to_file(tmp_path: Path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    monkeypatch.setenv("FCM_CONFIG", str(config_file))

    set_config_value("test_key", "test_value")
    assert config_file.exists()
    content = config_file.read_text()
    assert "test_key" in content


def test_corrupt_config_returns_empty(tmp_path: Path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("{invalid: yaml: [}")
    monkeypatch.setenv("FCM_CONFIG", str(config_file))

    config = get_full_user_config()
    assert config == {}


def test_non_dict_config_returns_empty(tmp_path: Path, monkeypatch):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("- item1\n- item2\n")
    monkeypatch.setenv("FCM_CONFIG", str(config_file))

    config = get_full_user_config()
    assert config == {}
