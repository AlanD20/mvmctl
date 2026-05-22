"""Tests for CLI key commands."""

from __future__ import annotations

import json
from unittest.mock import patch

from click.testing import CliRunner

from mvmctl.exceptions import MVMKeyError
from mvmctl.main import app
from mvmctl.models import SSHKeyItem
from mvmctl.models.result import BatchResult, OperationResult

runner = CliRunner()


def _make_key(
    name: str = "testkey",
    is_default: bool = False,
    is_present: bool = True,
) -> SSHKeyItem:
    return SSHKeyItem(
        id=f"key-{name}-" + "x" * 55,
        name=name,
        fingerprint="SHA256:abcdef123456",
        algorithm="ssh-ed25519",
        comment="test@host",
        public_key_path=f"/path/to/keys/{name}.pub",
        is_default=is_default,
        is_present=is_present,
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
        private_key_path=f"/path/to/keys/{name}",
    )


class TestKeyLs:
    """Tests for 'key ls' command."""

    @patch("mvmctl.cli.key.KeyOperation")
    def test_ls_empty(self, mock_key_op):
        mock_key_op.list_all.return_value = []
        result = runner.invoke(app, ["key", "ls"])
        assert result.exit_code == 0
        assert "No keys found" in result.output

    @patch("mvmctl.cli.key.KeyOperation")
    def test_ls_with_keys(self, mock_key_op):
        mock_key_op.list_all.return_value = [
            _make_key("key1"),
            _make_key("key2"),
        ]
        result = runner.invoke(app, ["key", "ls", "--long"])
        assert result.exit_code == 0
        assert "key1" in result.output
        assert "key2" in result.output
        assert "Fingerprint" in result.output

    @patch("mvmctl.cli.key.KeyOperation")
    def test_ls_json(self, mock_key_op):
        mock_key_op.list_all.return_value = [_make_key("testkey")]
        result = runner.invoke(app, ["key", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) == 1
        assert data[0]["name"] == "testkey"

    def test_ls_help(self):
        result = runner.invoke(app, ["key", "ls", "--help"])
        assert result.exit_code == 0


class TestKeyAdd:
    """Tests for 'key add' command."""

    @patch("mvmctl.cli.key.KeyOperation")
    def test_add_success(self, mock_key_op, tmp_path):
        mock_key_op.add.return_value = OperationResult(
            status="success", code="key.added", item=_make_key("testkey")
        )
        key_file = tmp_path / "id.pub"
        key_file.write_text("ssh-ed25519 AAAA test@host")
        result = runner.invoke(app, ["key", "add", "testkey", str(key_file)])
        assert result.exit_code == 0
        assert "added" in result.output.lower()

    @patch("mvmctl.cli.key.KeyOperation")
    def test_add_overwrite_flag(self, mock_key_op, tmp_path):
        mock_key_op.add.return_value = OperationResult(
            status="success", code="key.added", item=_make_key("testkey")
        )
        key_file = tmp_path / "id.pub"
        key_file.write_text("ssh-ed25519 AAAA test@host")
        result = runner.invoke(
            app,
            [
                "key",
                "add",
                "testkey",
                str(key_file),
                "--force",
            ],
        )
        assert result.exit_code == 0
        call_kwargs = mock_key_op.add.call_args[1]
        assert call_kwargs["overwrite"] is True

    @patch("mvmctl.cli.key.KeyOperation")
    def test_add_file_not_found(self, mock_key_op):
        result = runner.invoke(
            app, ["key", "add", "testkey", "/nonexistent/key.pub"]
        )
        assert result.exit_code == 1

    def test_add_missing_args(self):
        result = runner.invoke(app, ["key", "add", "testkey"])
        assert result.exit_code != 0

    @patch("mvmctl.cli.key.KeyOperation")
    def test_add_api_error(self, mock_key_op, tmp_path):
        mock_key_op.add.side_effect = MVMKeyError("key already exists")
        key_file = tmp_path / "id.pub"
        key_file.write_text("ssh-ed25519 AAAA test@host")
        result = runner.invoke(app, ["key", "add", "testkey", str(key_file)])
        assert result.exit_code == 1

    def test_add_help(self):
        result = runner.invoke(app, ["key", "add", "--help"])
        assert result.exit_code == 0


class TestKeyCreate:
    """Tests for 'key create' command."""

    @patch("mvmctl.cli.key.KeyOperation")
    def test_create_success(self, mock_key_op):
        mock_key_op.create.return_value = OperationResult(
            status="success", code="key.created", item=_make_key("newkey")
        )
        result = runner.invoke(app, ["key", "create", "newkey"], input="1\n")
        assert result.exit_code == 0
        assert "created" in result.output.lower()

    @patch("mvmctl.cli.key.KeyOperation")
    def test_create_with_options(self, mock_key_op):
        mock_key_op.create.return_value = OperationResult(
            status="success", code="key.created", item=_make_key("newkey")
        )
        result = runner.invoke(
            app,
            [
                "key",
                "create",
                "newkey",
                "--algorithm",
                "ed25519",
                "--comment",
                "my comment",
                "--default",
            ],
        )
        assert result.exit_code == 0

    @patch("mvmctl.cli.key.KeyOperation")
    def test_create_api_error(self, mock_key_op):
        mock_key_op.create.side_effect = MVMKeyError("already exists")
        result = runner.invoke(app, ["key", "create", "existing"], input="1\n")
        assert result.exit_code == 1

    def test_create_help(self):
        result = runner.invoke(app, ["key", "create", "--help"])
        assert result.exit_code == 0


class TestKeyRemove:
    """Tests for 'key rm' command."""

    @patch("mvmctl.cli.key.KeyOperation")
    def test_rm_success(self, mock_key_op):
        mock_key_op.remove.return_value = BatchResult(
            items=[
                OperationResult(
                    status="success", code="key.removed", message="Key removed"
                )
            ]
        )
        result = runner.invoke(app, ["key", "rm", "testkey"])
        assert result.exit_code == 0
        assert "Removed" in result.output

    @patch("mvmctl.cli.key.KeyOperation")
    def test_rm_multiple(self, mock_key_op):
        mock_key_op.remove.return_value = BatchResult(
            items=[
                OperationResult(
                    status="success", code="key.removed", message="Key removed"
                )
            ]
        )
        result = runner.invoke(app, ["key", "rm", "key1", "key2"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.key.KeyOperation")
    def test_rm_no_name(self, mock_key_op):
        result = runner.invoke(app, ["key", "rm"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.key.KeyOperation")
    def test_rm_api_error(self, mock_key_op):
        mock_key_op.remove.side_effect = MVMKeyError("not found")
        result = runner.invoke(app, ["key", "rm", "nonexistent"])
        assert result.exit_code == 1

    def test_rm_help(self):
        result = runner.invoke(app, ["key", "rm", "--help"])
        assert result.exit_code == 0


class TestKeyInspect:
    """Tests for 'key inspect' command."""

    def _key_inspect_dict(self, key):
        return {
            "key": {
                "id": key.id,
                "name": key.name,
                "fingerprint": key.fingerprint,
                "algorithm": key.algorithm,
                "comment": key.comment,
                "is_default": key.is_default,
                "is_present": key.is_present,
            },
            "files": {
                "public_key_path": key.public_key_path,
                "private_key_path": key.private_key_path,
            },
            "timestamps": {
                "created_at": key.created_at,
                "updated_at": key.updated_at,
            },
        }

    @patch("mvmctl.cli.key.KeyOperation")
    def test_inspect_success(self, mock_key_op):
        key = _make_key("testkey")
        mock_key_op.inspect.return_value = self._key_inspect_dict(key)
        result = runner.invoke(app, ["key", "inspect", "testkey"])
        assert result.exit_code == 0
        assert "testkey" in result.output
        assert "ssh-ed25519" in result.output

    @patch("mvmctl.cli.key.KeyOperation")
    def test_inspect_json(self, mock_key_op):
        mock_key_op.inspect.return_value = {
            "name": "testkey",
            "algorithm": "ssh-ed25519",
        }
        result = runner.invoke(app, ["key", "inspect", "testkey", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data["name"] == "testkey"

    @patch("mvmctl.cli.key.KeyOperation")
    def test_inspect_not_found(self, mock_key_op):
        mock_key_op.inspect.side_effect = MVMKeyError("not found")
        result = runner.invoke(app, ["key", "inspect", "nonexistent"])
        assert result.exit_code == 1

    def test_inspect_help(self):
        result = runner.invoke(app, ["key", "inspect", "--help"])
        assert result.exit_code == 0


class TestKeyExport:
    """Tests for 'key export' command."""

    @patch("mvmctl.cli.key.KeyOperation")
    def test_export_success(self, mock_key_op, tmp_path):
        mock_key_op.export.return_value = OperationResult(
            status="success",
            code="key.exported",
            item=(tmp_path / "testkey", tmp_path / "testkey.pub"),
        )
        result = runner.invoke(
            app, ["key", "export", "testkey", "--out", str(tmp_path)]
        )
        assert result.exit_code == 0
        assert "Exported" in result.output

    @patch("mvmctl.cli.key.KeyOperation")
    def test_export_api_error(self, mock_key_op, tmp_path):
        mock_key_op.export.side_effect = MVMKeyError("not found")
        result = runner.invoke(
            app, ["key", "export", "nonexistent", "--out", str(tmp_path)]
        )
        assert result.exit_code == 1

    def test_export_help(self):
        result = runner.invoke(app, ["key", "export", "--help"])
        assert result.exit_code == 0


class TestKeySetDefault:
    """Tests for 'key set-default' command."""

    @patch("mvmctl.cli.key.KeyOperation")
    def test_set_default_success(self, mock_key_op):
        mock_key_op.set_default.return_value = OperationResult(
            status="success",
            code="key.default_set",
            message="Default key(s) set",
        )
        result = runner.invoke(app, ["key", "default", "mykey"])
        assert result.exit_code == 0
        assert "mykey" in result.output

    @patch("mvmctl.cli.key.KeyOperation")
    def test_set_default_clear(self, mock_key_op):
        mock_key_op.clear_defaults.return_value = OperationResult(
            status="success",
            code="key.defaults_cleared",
            message="Defaults cleared",
        )
        result = runner.invoke(app, ["key", "default", "--clear"])
        assert result.exit_code == 0
        assert "Cleared" in result.output

    @patch("mvmctl.cli.key.KeyOperation")
    def test_set_default_no_args(self, mock_key_op):
        result = runner.invoke(app, ["key", "default"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.key.KeyOperation")
    def test_set_default_api_error(self, mock_key_op):
        mock_key_op.set_default.side_effect = MVMKeyError("not found")
        result = runner.invoke(app, ["key", "default", "badkey"])
        assert result.exit_code == 1


class TestKeyHelp:
    """Tests for key command group help."""

    def test_key_help(self):
        result = runner.invoke(app, ["key", "--help"])
        assert result.exit_code == 0
        assert "SSH key management" in result.output

    def test_key_help_command(self):
        result = runner.invoke(app, ["key", "help"])
        assert result.exit_code == 2  # No "help" subcommand registered
