from pathlib import Path
from unittest.mock import patch

from fcm.core.cli_state import (
    get_cli_state,
    get_cli_state_value,
    set_cli_state_value,
)


def test_get_cli_state_empty(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    state = get_cli_state()
    assert state == {}


def test_set_and_get_state_value(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    set_cli_state_value("ci_version", "1.12")
    assert get_cli_state_value("ci_version") == "1.12"


def test_set_multiple_values(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    set_cli_state_value("key_a", "val_a")
    set_cli_state_value("key_b", "val_b")
    assert get_cli_state_value("key_a") == "val_a"
    assert get_cli_state_value("key_b") == "val_b"


def test_get_missing_value_returns_default(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    result = get_cli_state_value("missing_key", default="fallback")
    assert result == "fallback"


def test_get_missing_value_returns_none(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    result = get_cli_state_value("missing_key")
    assert result is None


def test_corrupt_state_returns_empty(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    state_file = tmp_path / "cli-state.json"
    state_file.write_text("{invalid json")
    state = get_cli_state()
    assert state == {}


def test_state_persists_to_file(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    set_cli_state_value("test", "value")
    state_file = tmp_path / "cli-state.json"
    assert state_file.exists()
