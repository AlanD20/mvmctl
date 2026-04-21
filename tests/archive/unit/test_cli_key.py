"""Tests for cli/key.py."""

from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from mvmctl.cli.key import key_app as app
from mvmctl.core.key_manager import KeyInfo
from mvmctl.exceptions import MVMKeyError
from mvmctl.api.inputs import KeyCreateInput

runner = CliRunner()

_FAKE_KEY = KeyInfo(
    name="testkey",
    fingerprint="SHA256:abcdef123456",
    algorithm="ssh-ed25519",
    comment="test@host",
    added_at="2024-01-01T00:00:00+00:00",
    has_private_key=True,
    private_key_path="/home/user/.cache/mvmctl/keys/testkey",
    public_key_path="/home/user/.cache/mvmctl/keys/testkey.pub",
)


# ---------------------------------------------------------------------------
# key ls
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.key.list_keys", return_value=[])
def test_ls_empty(mock_list):
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "No keys found" in result.output


@patch("mvmctl.cli.key.list_keys", return_value=[_FAKE_KEY])
def test_ls_with_keys(mock_list):
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "testkey" in result.output


@patch("mvmctl.cli.key.list_keys", return_value=[_FAKE_KEY])
def test_ls_json(mock_list):
    result = runner.invoke(app, ["ls", "--json"])
    assert result.exit_code == 0
    assert '"testkey"' in result.output
    assert '"fingerprint"' in result.output


# ---------------------------------------------------------------------------
# key add
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.key.add_key", return_value=_FAKE_KEY)
def test_add_success(mock_add, tmp_path):
    key_file = tmp_path / "id.pub"
    key_file.write_text("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHtest test@host")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 0
    assert "added" in result.output.lower()
    mock_add.assert_called_once_with("testkey", str(key_file), overwrite=False)


@patch("mvmctl.cli.key.add_key", side_effect=MVMKeyError("not found"))
def test_add_error(mock_add, tmp_path):
    key_file = tmp_path / "bad.pub"
    key_file.write_text("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHtest test@host")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


def test_add_missing_path():
    result = runner.invoke(app, ["add", "testkey"])
    assert result.exit_code == 1
    assert "Missing argument" in result.output or "PUBLIC_KEY_PATH" in result.output


def test_add_file_not_found():
    result = runner.invoke(app, ["add", "testkey", "/nonexistent/path/id_rsa.pub"])
    assert result.exit_code == 1
    assert "File not found" in result.output or "not found" in result.output.lower()


def test_add_not_a_pub_file(tmp_path):
    key_file = tmp_path / "id_rsa"
    key_file.write_text("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHtest test@host")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 1
    assert "public key" in result.output.lower()


def test_add_not_readable(tmp_path):
    key_file = tmp_path / "test_key.pub"
    key_file.write_text("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHtest test@host")
    key_file.chmod(0o000)
    try:
        result = runner.invoke(app, ["add", "testkey", str(key_file)])
        assert result.exit_code == 1
        assert "Cannot read" in result.output or "permission" in result.output.lower()
    finally:
        key_file.chmod(0o644)


# ---------------------------------------------------------------------------
# key create
# ---------------------------------------------------------------------------


@patch(
    "mvmctl.cli.key.create_key",
    return_value=(_FAKE_KEY, Path("/home/user/.ssh/testkey")),
)
def test_create_success(mock_create):
    result = runner.invoke(app, ["create", "testkey"])
    assert result.exit_code == 0
    assert "created" in result.output.lower()
    mock_create.assert_called_once()
    call_input = mock_create.call_args.kwargs["input"]
    assert call_input.name == "testkey"
    assert call_input.output_dir is None
    assert call_input.comment is None
    assert call_input.overwrite is False


@patch(
    "mvmctl.cli.key.create_key",
    return_value=(_FAKE_KEY, Path("/custom/testkey")),
)
def test_create_with_options(mock_create):
    result = runner.invoke(
        app, ["create", "testkey", "--out", "/custom", "--comment", "my comment"]
    )
    assert result.exit_code == 0
    mock_create.assert_called_once()
    call_input = mock_create.call_args.kwargs["input"]
    assert call_input.name == "testkey"
    assert str(call_input.output_dir) == "/custom"
    assert call_input.comment == "my comment"
    assert call_input.overwrite is False


@patch("mvmctl.cli.key.create_key", side_effect=MVMKeyError("already exists"))
def test_create_error(mock_create):
    result = runner.invoke(app, ["create", "testkey"])
    assert result.exit_code == 1
    assert "already exists" in result.output.lower()


# ---------------------------------------------------------------------------
# key remove
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.key.remove_key")
def test_remove_success(mock_remove):
    """Remove without --force - proceeds immediately."""
    result = runner.invoke(app, ["remove", "testkey"])
    assert result.exit_code == 0
    assert "removed" in result.output.lower()
    mock_remove.assert_called_once_with("testkey")


@patch("mvmctl.cli.key.remove_key", side_effect=MVMKeyError("not found"))
def test_remove_error(mock_remove):
    result = runner.invoke(app, ["remove", "testkey"])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


@patch("mvmctl.cli.key.remove_key")
def test_rm_alias(mock_remove):
    """Rm alias without --force - proceeds immediately."""
    result = runner.invoke(app, ["rm", "testkey"])
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
    "has_private_key": True,
    "public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHtest test@host",
}


@patch("mvmctl.cli.key.inspect_key", return_value=_FAKE_INSPECT)
def test_inspect_success(mock_inspect):
    result = runner.invoke(app, ["inspect", "testkey"])
    assert result.exit_code == 0
    assert "testkey" in result.output
    assert "ssh-ed25519" in result.output


@patch("mvmctl.cli.key.inspect_key", return_value=_FAKE_INSPECT)
def test_inspect_json(mock_inspect):
    result = runner.invoke(app, ["inspect", "testkey", "--json"])
    assert result.exit_code == 0
    assert '"testkey"' in result.output


@patch("mvmctl.cli.key.inspect_key", side_effect=MVMKeyError("not found"))
def test_inspect_error(mock_inspect):
    result = runner.invoke(app, ["inspect", "testkey"])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


# ---------------------------------------------------------------------------
# help subcommand at subcommand level (Phase 4 §5)
# ---------------------------------------------------------------------------


def test_add_help_arg_shows_help():
    """key add help → same as key add --help."""
    result = runner.invoke(app, ["add", "help"])
    assert result.exit_code == 0
    assert "Usage" in result.output


def test_add_no_args_shows_help():
    """key add with no args prints help."""
    result = runner.invoke(app, ["add"])
    assert "Usage" in result.output


def test_create_help_arg_shows_help():
    """key create help → same as key create --help."""
    result = runner.invoke(app, ["create", "help"])
    assert result.exit_code == 0
    assert "Usage" in result.output


def test_remove_help_arg_shows_help():
    """key remove help → same as key remove --help."""
    result = runner.invoke(app, ["remove", "help"])
    assert result.exit_code == 0
    assert "Usage" in result.output


def test_inspect_help_arg_shows_help():
    """key inspect help → same as key inspect --help."""
    result = runner.invoke(app, ["inspect", "help"])
    assert result.exit_code == 0
    assert "Usage" in result.output


# ---------------------------------------------------------------------------
# S-H1: Entity name validation on key commands
# ---------------------------------------------------------------------------


def test_add_rejects_invalid_name():
    result = runner.invoke(app, ["add", "../evil", "/tmp/key.pub"])
    assert result.exit_code != 0
    assert isinstance(result.exception, Exception)
    assert "Invalid key name" in str(result.exception)


def test_create_rejects_invalid_name():
    """Uppercase key name should be rejected."""
    result = runner.invoke(app, ["create", "UPPER"])
    assert result.exit_code == 1


def test_remove_rejects_invalid_name():
    """Key name with semicolon should be rejected."""
    result = runner.invoke(app, ["remove", "bad;name"])
    assert result.exit_code == 1


def test_inspect_rejects_invalid_name():
    """Key name with pipe should be rejected."""
    result = runner.invoke(app, ["inspect", "pipe|name"])
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# P3-13: Private Key column in ls
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.key.list_keys")
def test_key_ls_shows_private_key_column(mock_list):
    """P3-13: key ls shows whether private key is present locally."""
    mock_list.return_value = [
        KeyInfo(
            name="my-key",
            fingerprint="SHA256:abc",
            algorithm="ssh-ed25519",
            comment="test",
            added_at="2026-01-01T00:00:00+00:00",
            has_private_key=True,
        )
    ]
    result = runner.invoke(app, ["ls"])
    assert result.exit_code == 0
    assert "Private Key" in result.output
    assert "yes" in result.output


@patch("mvmctl.cli.key.list_keys")
def test_key_ls_json_includes_private_key_status(mock_list):
    """P3-13: key ls --json includes has_private_key field."""
    mock_list.return_value = [
        KeyInfo(
            name="my-key",
            fingerprint="SHA256:abc",
            algorithm="ssh-ed25519",
            comment="test",
            added_at="2026-01-01T00:00:00+00:00",
            has_private_key=False,
        )
    ]
    result = runner.invoke(app, ["ls", "--json"])
    assert result.exit_code == 0
    assert '"has_private_key"' in result.output
    assert "false" in result.output.lower()


# ---------------------------------------------------------------------------
# Export command tests
# ---------------------------------------------------------------------------


@patch(
    "mvmctl.cli.key.export_key",
    return_value=(Path("/dest/test"), Path("/dest/test.pub")),
)
def test_export_success(mock_export):
    result = runner.invoke(app, ["export", "mykey"])
    assert result.exit_code == 0
    assert "exported" in result.output.lower()
    mock_export.assert_called_once_with("mykey", None, overwrite=True)


@patch(
    "mvmctl.cli.key.export_key",
    return_value=(Path("/custom/test"), Path("/custom/test.pub")),
)
def test_export_with_custom_output(mock_export):
    result = runner.invoke(app, ["export", "mykey", "--out", "/custom"])
    assert result.exit_code == 0
    mock_export.assert_called_once_with("mykey", "/custom", overwrite=True)


@patch("mvmctl.cli.key.export_key", side_effect=MVMKeyError("not found in cache"))
def test_export_error(mock_export):
    result = runner.invoke(app, ["export", "nonexistent"])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


def test_export_help_arg_shows_help():
    result = runner.invoke(app, ["export", "--help"])
    assert result.exit_code == 0
    assert "export" in result.output.lower()


# ---------------------------------------------------------------------------
# State Validation X marks (Phase 4)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.key.list_keys")
@patch("mvmctl.cli.key.get_default_keys")
@patch("mvmctl.cli.key.is_file_missing")
def test_key_ls_shows_x_mark_for_missing_key_file(mock_is_missing, mock_get_defaults, mock_list):
    """Verify X prefix when key file missing."""
    # Mock KeyInfo
    mock_key = KeyInfo(
        name="missing-key",
        fingerprint="SHA256:abc123",
        algorithm="ssh-ed25519",
        comment="test",
        added_at="2026-01-01T00:00:00+00:00",
    )
    mock_list.return_value = [mock_key]
    mock_get_defaults.return_value = []  # Not a default key
    # Mock is_file_missing to return True (file is missing)
    mock_is_missing.return_value = True

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "X " prefix in output
    assert "X " in result.output


@patch("mvmctl.cli.key.list_keys")
@patch("mvmctl.cli.key.get_default_keys")
@patch("mvmctl.cli.key.is_file_missing")
def test_key_ls_no_x_mark_for_existing_key_file(mock_is_missing, mock_get_defaults, mock_list):
    """Verify no X prefix when key file exists."""
    # Mock KeyInfo
    mock_key = KeyInfo(
        name="existing-key",
        fingerprint="SHA256:def456",
        algorithm="ssh-ed25519",
        comment="test",
        added_at="2026-01-01T00:00:00+00:00",
    )
    mock_list.return_value = [mock_key]
    mock_get_defaults.return_value = []  # Not a default key
    # Mock is_file_missing to return False (file exists)
    mock_is_missing.return_value = False

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify no "X " prefix for existing key


# ---------------------------------------------------------------------------
# Default prefix tests (Phase 4)
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.key.list_keys")
@patch("mvmctl.cli.key.get_default_keys")
@patch("mvmctl.cli.key.is_file_missing")
def test_key_ls_shows_default_prefix(mock_is_missing, mock_get_defaults, mock_list):
    """Verify * prefix shown for default key."""
    # Mock list_keys returning key
    mock_key = KeyInfo(
        name="default-key",
        fingerprint="SHA256:default",
        algorithm="ssh-ed25519",
        comment="test",
        added_at="2026-01-01T00:00:00+00:00",
    )
    mock_list.return_value = [mock_key]
    # Mock get_default_keys to return this key as default
    mock_get_defaults.return_value = ["default-key"]
    mock_is_missing.return_value = False

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "*  " prefix (combined marker: default + exists) in output for default key
    assert "*  default-key" in result.output


@patch("mvmctl.cli.key.list_keys")
@patch("mvmctl.cli.key.get_default_keys")
@patch("mvmctl.cli.key.is_file_missing")
def test_key_ls_no_prefix_for_non_default(mock_is_missing, mock_get_defaults, mock_list):
    """Verify no * prefix for non-default key."""
    # Mock list_keys returning key
    mock_key = KeyInfo(
        name="non-default-key",
        fingerprint="SHA256:nondefault",
        algorithm="ssh-ed25519",
        comment="test",
        added_at="2026-01-01T00:00:00+00:00",
    )
    mock_list.return_value = [mock_key]
    # Mock get_default_keys to return empty (no defaults)
    mock_get_defaults.return_value = []
    mock_is_missing.return_value = False

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify no "* " prefix for non-default key
    assert "non-default-key" in result.output


@patch("mvmctl.cli.key.list_keys")
@patch("mvmctl.cli.key.get_default_keys")
@patch("mvmctl.cli.key.is_file_missing")
def test_key_ls_no_def_column(mock_is_missing, mock_get_defaults, mock_list):
    """Verify 'Def' column removed from key ls."""
    mock_key = KeyInfo(
        name="test-key",
        fingerprint="SHA256:test",
        algorithm="ssh-ed25519",
        comment="test",
        added_at="2026-01-01T00:00:00+00:00",
    )
    mock_list.return_value = [mock_key]
    mock_get_defaults.return_value = []
    mock_is_missing.return_value = False

    result = runner.invoke(app, ["ls"])

    assert result.exit_code == 0
    # Verify "Def" column not in output
    assert "Def" not in result.output


def test_help_cmd():
    result = runner.invoke(app, ["help"])
    assert result.exit_code == 0


@patch(
    "mvmctl.cli.key.add_key",
    side_effect=MVMKeyError(
        "File does not appear to be a public key: /tmp/mykey. Did you mean: /tmp/mykey.pub"
    ),
)
def test_add_not_pub_extension_with_pub_sibling(mock_add, tmp_path):
    key_file = tmp_path / "mykey"
    key_file.write_text("private")
    pub_file = tmp_path / "mykey.pub"
    pub_file.write_text("ssh-ed25519 AAAA test")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 1
    assert ".pub" in result.output


@patch(
    "mvmctl.cli.key.add_key",
    side_effect=MVMKeyError(
        "File does not appear to be a public key: /tmp/mykey_nopub. Public keys typically end in .pub"
    ),
)
def test_add_not_pub_extension_no_pub_sibling(mock_add, tmp_path):
    key_file = tmp_path / "mykey_nopub"
    key_file.write_text("private")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 1
    assert "public key" in result.output.lower()


@patch("mvmctl.cli.key.add_key", side_effect=MVMKeyError("already exists"))
def test_add_mvm_key_error_already_exists(mock_add, tmp_path):
    key_file = tmp_path / "mykey.pub"
    key_file.write_text("ssh-ed25519 AAAA test")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 1
    assert "already" in result.output.lower()


@patch("mvmctl.cli.key.add_key", side_effect=MVMKeyError("private key provided"))
def test_add_mvm_key_error_private_key(mock_add, tmp_path):
    key_file = tmp_path / "mykey.pub"
    key_file.write_text("ssh-ed25519 AAAA test")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 1
    assert "private key" in result.output.lower()


@patch("mvmctl.cli.key.add_key", side_effect=MVMKeyError("not found"))
def test_add_mvm_key_error_not_found(mock_add, tmp_path):
    key_file = tmp_path / "mykey.pub"
    key_file.write_text("ssh-ed25519 AAAA test")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 1
    assert "not found" in result.output.lower()


@patch("mvmctl.cli.key.add_key", side_effect=MVMKeyError("something else"))
def test_add_mvm_key_error_generic(mock_add, tmp_path):
    key_file = tmp_path / "mykey.pub"
    key_file.write_text("ssh-ed25519 AAAA test")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 1
    assert "permission" in result.output.lower() or "path" in result.output.lower()


@patch("mvmctl.cli.key.add_key", return_value=_FAKE_KEY)
def test_add_success_with_comment(mock_add, tmp_path):
    key_file = tmp_path / "mykey.pub"
    key_file.write_text("ssh-ed25519 AAAA test")
    result = runner.invoke(app, ["add", "testkey", str(key_file)])
    assert result.exit_code == 0
    assert "Comment" in result.output


@patch("mvmctl.cli.key.create_key", return_value=(_FAKE_KEY, Path("/tmp/testkey")))
def test_create_confirm_no_aborts(mock_create, tmp_path):
    priv = tmp_path / "testkey"
    priv.write_text("existing")
    result = runner.invoke(app, ["create", "testkey", "--out", str(tmp_path)], input="n\n")
    assert result.exit_code == 0


@patch("mvmctl.cli.key.export_key", return_value=(Path("/tmp/k"), Path("/tmp/k.pub")))
def test_export_confirm_no_aborts(mock_export, tmp_path):
    existing = tmp_path / "testkey"
    existing.write_text("key")
    result = runner.invoke(
        app,
        ["export", "testkey", "--out", str(tmp_path)],
        input="n\n",
    )
    assert result.exit_code == 0


@patch("mvmctl.cli.key.inspect_key")
def test_inspect_shows_private_and_public_paths(mock_inspect):
    mock_inspect.return_value = {
        "name": "testkey",
        "algorithm": "ssh-ed25519",
        "fingerprint": "SHA256:abc",
        "comment": "test",
        "added_at": "2024-01-01T00:00:00",
        "public_key": "ssh-ed25519 AAAA",
        "private_key_path": "/home/user/.ssh/testkey",
        "public_key_path": "/home/user/.ssh/testkey.pub",
    }
    result = runner.invoke(app, ["inspect", "testkey"])
    assert result.exit_code == 0
    assert "Private key path" in result.output
    assert "Public key path" in result.output


@patch("mvmctl.cli.key.clear_default_keys")
def test_set_default_clear(mock_clear):
    result = runner.invoke(app, ["set-default", "--clear"])
    assert result.exit_code == 0
    assert "Cleared" in result.output


@patch("mvmctl.cli.key.clear_default_keys", side_effect=MVMKeyError("oops"))
def test_set_default_clear_error(mock_clear):
    result = runner.invoke(app, ["set-default", "--clear"])
    assert result.exit_code == 1


def test_set_default_no_keys():
    result = runner.invoke(app, ["set-default"])
    assert result.exit_code == 1
    assert "at least one" in result.output.lower()


@patch("mvmctl.cli.key.resolve_key_inputs", return_value=["testkey"])
@patch("mvmctl.cli.key.set_default_keys")
def test_set_default_success(mock_set, mock_resolve):
    result = runner.invoke(app, ["set-default", "testkey"])
    assert result.exit_code == 0
    assert "testkey" in result.output


@patch("mvmctl.cli.key.resolve_key_inputs", side_effect=MVMKeyError("key not found"))
def test_set_default_error(mock_resolve):
    result = runner.invoke(app, ["set-default", "badkey"])
    assert result.exit_code == 1
