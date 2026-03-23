"""Tests for core/key_manager.py."""

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from fcm.core.key_manager import (
    KeyInfo,
    add_key,
    create_key,
    get_key,
    inspect_key,
    list_keys,
    remove_key,
)
from fcm.exceptions import FCMKeyError

SAMPLE_PUB_KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHtestkeycontent testuser@testhost"


@pytest.fixture()
def keys_dir(tmp_path, monkeypatch):
    """Set up a temporary keys directory."""
    kd = tmp_path / "keys"
    kd.mkdir()
    monkeypatch.setattr("fcm.core.key_manager.get_keys_dir", lambda: kd)
    return kd


# ---------------------------------------------------------------------------
# list_keys
# ---------------------------------------------------------------------------


def test_list_keys_empty(keys_dir):
    assert list_keys() == []


def test_list_keys_with_entries(keys_dir):
    registry = {
        "mykey": {
            "name": "mykey",
            "fingerprint": "SHA256:abc",
            "algorithm": "ssh-ed25519",
            "comment": "me@host",
            "added_at": "2024-01-01T00:00:00",
        }
    }
    (keys_dir / "registry.json").write_text(json.dumps(registry))
    result = list_keys()
    assert len(result) == 1
    assert result[0].name == "mykey"
    assert result[0].fingerprint == "SHA256:abc"


# ---------------------------------------------------------------------------
# get_key
# ---------------------------------------------------------------------------


def test_get_key_found(keys_dir):
    registry = {
        "mykey": {
            "name": "mykey",
            "fingerprint": "SHA256:abc",
            "algorithm": "ssh-ed25519",
            "comment": "me@host",
            "added_at": "2024-01-01T00:00:00",
        }
    }
    (keys_dir / "registry.json").write_text(json.dumps(registry))
    result = get_key("mykey")
    assert result is not None
    assert result.name == "mykey"


def test_get_key_not_found(keys_dir):
    assert get_key("nonexistent") is None


# ---------------------------------------------------------------------------
# add_key
# ---------------------------------------------------------------------------


def test_add_key_success(keys_dir, tmp_path):
    pub_file = tmp_path / "id_ed25519.pub"
    pub_file.write_text(SAMPLE_PUB_KEY)

    info = add_key("testkey", pub_file)

    assert info.name == "testkey"
    assert info.algorithm == "ssh-ed25519"
    assert "testuser@testhost" in info.comment
    assert info.fingerprint.startswith("SHA256:")

    # Verify file stored in cache
    cached = keys_dir / "testkey.pub"
    assert cached.exists()

    # Verify registry
    registry = json.loads((keys_dir / "registry.json").read_text())
    assert "testkey" in registry


def test_add_key_file_not_found(keys_dir, tmp_path):
    with pytest.raises(FCMKeyError, match="not found"):
        add_key("testkey", tmp_path / "nonexistent.pub")


def test_add_key_empty_file(keys_dir, tmp_path):
    pub_file = tmp_path / "empty.pub"
    pub_file.write_text("")

    with pytest.raises(FCMKeyError, match="empty"):
        add_key("testkey", pub_file)


def test_add_key_already_exists(keys_dir, tmp_path):
    pub_file = tmp_path / "id_ed25519.pub"
    pub_file.write_text(SAMPLE_PUB_KEY)

    add_key("testkey", pub_file)

    with pytest.raises(FCMKeyError, match="already exists"):
        add_key("testkey", pub_file)


# ---------------------------------------------------------------------------
# create_key
# ---------------------------------------------------------------------------


def test_create_key_success(keys_dir, tmp_path):
    output_dir = tmp_path / "ssh"
    output_dir.mkdir()

    mock_result = MagicMock()
    mock_result.returncode = 0

    def fake_keygen(*args, **kwargs):
        # Simulate ssh-keygen writing files
        cmd = args[0]
        key_path = None
        for i, arg in enumerate(cmd):
            if arg == "-f" and i + 1 < len(cmd):
                key_path = Path(cmd[i + 1])
                break
        if key_path:
            key_path.write_text("PRIVATE KEY CONTENT")
            pub_path = key_path.with_suffix(".pub")
            pub_path.write_text(SAMPLE_PUB_KEY)
        return mock_result

    with patch("fcm.core.key_manager.subprocess.run", side_effect=fake_keygen):
        info, private_path = create_key("newkey", output_dir=output_dir)

    assert info.name == "newkey"
    assert info.algorithm == "ssh-ed25519"
    assert private_path == output_dir / "newkey"
    assert (keys_dir / "newkey.pub").exists()


def test_create_key_file_exists_no_overwrite(keys_dir, tmp_path):
    output_dir = tmp_path / "ssh"
    output_dir.mkdir()
    (output_dir / "existingkey").write_text("existing private key")

    with pytest.raises(FCMKeyError, match="already exists"):
        create_key("existingkey", output_dir=output_dir)


def test_create_key_name_exists_in_registry(keys_dir, tmp_path):
    registry = {
        "dupkey": {
            "name": "dupkey",
            "fingerprint": "SHA256:abc",
            "algorithm": "ssh-ed25519",
            "comment": "",
            "added_at": "2024-01-01T00:00:00",
        }
    }
    (keys_dir / "registry.json").write_text(json.dumps(registry))

    output_dir = tmp_path / "ssh"
    output_dir.mkdir()

    with pytest.raises(FCMKeyError, match="already exists in cache"):
        create_key("dupkey", output_dir=output_dir)


def test_create_key_ssh_keygen_fails(keys_dir, tmp_path):
    output_dir = tmp_path / "ssh"
    output_dir.mkdir()

    mock_result = MagicMock()
    mock_result.returncode = 1
    mock_result.stderr = "keygen error"

    with patch("fcm.core.key_manager.subprocess.run", return_value=mock_result):
        with pytest.raises(FCMKeyError, match="ssh-keygen failed"):
            create_key("failkey", output_dir=output_dir)


# ---------------------------------------------------------------------------
# remove_key
# ---------------------------------------------------------------------------


def test_remove_key_success(keys_dir, tmp_path):
    # First add a key
    pub_file = tmp_path / "id.pub"
    pub_file.write_text(SAMPLE_PUB_KEY)
    add_key("rmkey", pub_file)

    assert (keys_dir / "rmkey.pub").exists()

    remove_key("rmkey")

    assert not (keys_dir / "rmkey.pub").exists()
    registry = json.loads((keys_dir / "registry.json").read_text())
    assert "rmkey" not in registry


def test_remove_key_not_found(keys_dir):
    with pytest.raises(FCMKeyError, match="not found"):
        remove_key("nonexistent")


# ---------------------------------------------------------------------------
# inspect_key
# ---------------------------------------------------------------------------


def test_inspect_key_success(keys_dir, tmp_path):
    pub_file = tmp_path / "id.pub"
    pub_file.write_text(SAMPLE_PUB_KEY)
    add_key("inspectkey", pub_file)

    info = inspect_key("inspectkey")

    assert info["name"] == "inspectkey"
    assert info["algorithm"] == "ssh-ed25519"
    assert info["fingerprint"].startswith("SHA256:")
    assert "public_key" in info
    assert "ssh-ed25519" in info["public_key"]


def test_inspect_key_not_found(keys_dir):
    with pytest.raises(FCMKeyError, match="not found"):
        inspect_key("nonexistent")


# ---------------------------------------------------------------------------
# S-H8: Registry file permissions (chmod 0o600)
# ---------------------------------------------------------------------------


def test_save_registry_sets_chmod_600(keys_dir, tmp_path):
    """After add_key, registry.json should have mode 0o600."""
    import stat

    pub_file = tmp_path / "id_ed25519.pub"
    pub_file.write_text(SAMPLE_PUB_KEY)

    add_key("chmod-test", pub_file)

    registry_path = keys_dir / "registry.json"
    assert registry_path.exists()
    mode = registry_path.stat().st_mode & 0o777
    assert mode == 0o600, f"Expected 0o600, got {oct(mode)}"
