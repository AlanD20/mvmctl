"""Tests for cli/key.py."""

from pathlib import Path
from unittest.mock import patch

import pytest
from typer.testing import CliRunner

from fcm.cli.key import app
from fcm.core.key_manager import KeyInfo
from fcm.exceptions import KeyError as FCMKeyError

runner = CliRunner()

_FAKE_KEY = KeyInfo(
    name="testkey",
    fingerprint="SHA256:abcdef123456",
    algorithm="ssh-ed25519",
    comment="test@host",
    added_at="2024-01-01T00:00:00+00:00",
)


# ---------------------------------------------------------------------------
# key ls
# ---------------------------------------------------------------------------


@patch("fcm.cli.key.list_keys", return_value=[])
def test_ls_empty(mock_list):
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "No keys found" in result.output


@patch("fcm.cli.key.list_keys", return_value=[_FAKE_KEY])
def test_ls_with_keys(mock_list):
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "testkey" in result.output


@patch("fcm.cli.key.list_keys", return_value=[_FAKE_KEY])
def test_ls_json(mock_list):
    result = runner.invoke(app, ["ls", "--json"])
    assert result.exit_code == 0
    assert '"testkey"' in result.output
    assert '"fingerprint"' in result.output


# ---------------------------------------------------------------------------
# key add
# ---------------------------------------------------------------------------


@patch("fcm.cli.key.add_key", return_value=_FAKE_KEY)
def test_add_success(mock_add):
    result = runner.invoke(app, ["add", "testkey", "/tmp/id.pub"])
    assert result.exit_code == 0
    assert "added" in result.output.lower()
    mock_add.assert_called_once_with("testkey", "/tmp/id.pub", overwrite=False)


@patch("fcm.cli.key.add_key", side_effect=FCMKeyError("not found"))
def test_add_error(mock_add):
    result = runner.invoke(app, ["add", "testkey", "/tmp/bad.pub"])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


# ---------------------------------------------------------------------------
# key create
# ---------------------------------------------------------------------------


@patch(
    "fcm.cli.key.create_key",
    return_value=(_FAKE_KEY, Path("/home/user/.ssh/testkey")),
)
def test_create_success(mock_create):
    result = runner.invoke(app, ["create", "testkey"])
    assert result.exit_code == 0
    assert "created" in result.output.lower()
    mock_create.assert_called_once_with(
        name="testkey", output_dir=None, comment=None, overwrite=False
    )


@patch(
    "fcm.cli.key.create_key",
    return_value=(_FAKE_KEY, Path("/custom/testkey")),
)
def test_create_with_options(mock_create):
    result = runner.invoke(
        app, ["create", "testkey", "--output", "/custom", "--comment", "my comment"]
    )
    assert result.exit_code == 0
    mock_create.assert_called_once_with(
        name="testkey", output_dir="/custom", comment="my comment", overwrite=False
    )


@patch("fcm.cli.key.create_key", side_effect=FCMKeyError("already exists"))
def test_create_error(mock_create):
    result = runner.invoke(app, ["create", "testkey"])
    assert result.exit_code == 1
    assert "already exists" in result.output.lower()


# ---------------------------------------------------------------------------
# key remove
# ---------------------------------------------------------------------------


@patch("fcm.cli.key.remove_key")
def test_remove_success(mock_remove):
    result = runner.invoke(app, ["remove", "testkey", "--force"])
    assert result.exit_code == 0
    assert "removed" in result.output.lower()
    mock_remove.assert_called_once_with("testkey")


@patch("fcm.cli.key.remove_key", side_effect=FCMKeyError("not found"))
def test_remove_error(mock_remove):
    result = runner.invoke(app, ["remove", "testkey", "--force"])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


@patch("fcm.cli.key.remove_key")
def test_rm_alias(mock_remove):
    result = runner.invoke(app, ["rm", "testkey", "--force"])
    assert result.exit_code == 0
    mock_remove.assert_called_once_with("testkey")


# ---------------------------------------------------------------------------
# key inspect
# ---------------------------------------------------------------------------


_FAKE_INSPECT = {
    "name": "testkey",
    "fingerprint": "SHA256:abcdef123456",
    "algorithm": "ssh-ed25519",
    "comment": "test@host",
    "added_at": "2024-01-01T00:00:00+00:00",
    "public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHtest test@host",
}


@patch("fcm.cli.key.inspect_key", return_value=_FAKE_INSPECT)
def test_inspect_success(mock_inspect):
    result = runner.invoke(app, ["inspect", "testkey"])
    assert result.exit_code == 0
    assert "testkey" in result.output
    assert "ssh-ed25519" in result.output


@patch("fcm.cli.key.inspect_key", return_value=_FAKE_INSPECT)
def test_inspect_json(mock_inspect):
    result = runner.invoke(app, ["inspect", "testkey", "--json"])
    assert result.exit_code == 0
    assert '"testkey"' in result.output


@patch("fcm.cli.key.inspect_key", side_effect=FCMKeyError("not found"))
def test_inspect_error(mock_inspect):
    result = runner.invoke(app, ["inspect", "testkey"])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()
