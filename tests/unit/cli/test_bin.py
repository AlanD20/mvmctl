"""Tests for CLI binary commands."""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from mvmctl.exceptions import MVMError
from mvmctl.main import app
from mvmctl.models import BinaryItem

runner = CliRunner()


def _make_binary(
    name: str = "firecracker",
    version: str = "1.15.0",
    is_default: bool = False,
    binary_id: str | None = None,
) -> BinaryItem:
    return BinaryItem(
        id=binary_id or f"bin-{version}-" + "x" * 55,
        name=name,
        version=version,
        full_version=f"v{version}",
        ci_version=f"v{version.split('.')[0]}.{version.split('.')[1]}",
        path=f"bin/{name}-v{version}",
        is_default=is_default,
        is_present=True,
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
    )


class TestBinLs:
    """Tests for 'bin ls' command."""

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_ls_empty(self, mock_bin_op):
        mock_bin_op.list_local.return_value = []
        result = runner.invoke(app, ["bin", "ls"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_ls_with_local(self, mock_bin_op):
        mock_bin_op.list_local.return_value = [
            _make_binary("firecracker", "1.15.0", is_default=True),
            _make_binary("jailer", "1.15.0"),
        ]
        result = runner.invoke(app, ["bin", "ls"])
        assert result.exit_code == 0
        assert "1.15.0" in result.output

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_ls_json(self, mock_bin_op):
        mock_bin_op.list_local.return_value = [
            _make_binary("firecracker", "1.15.0"),
        ]
        result = runner.invoke(app, ["bin", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) >= 1

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_ls_with_remote(self, mock_bin_op):
        mock_bin_op.list_local.return_value = [
            _make_binary("firecracker", "1.15.0")
        ]
        mock_bin_op.list_remote.return_value = ["1.16.0", "1.15.0", "1.14.0"]
        result = runner.invoke(app, ["bin", "ls", "--remote"])
        assert result.exit_code == 0
        assert "1.16.0" in result.output

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_ls_remote_with_limit(self, mock_bin_op):
        mock_bin_op.list_local.return_value = []
        mock_bin_op.list_remote.return_value = ["1.16.0"]
        result = runner.invoke(app, ["bin", "ls", "--remote", "--limit", "5"])
        assert result.exit_code == 0
        mock_bin_op.list_remote.assert_called_once_with(limit=5)

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_ls_remote_error(self, mock_bin_op):
        mock_bin_op.list_local.return_value = []
        mock_bin_op.list_remote.side_effect = MVMError("network fail")
        result = runner.invoke(app, ["bin", "ls", "--remote"])
        assert result.exit_code == 1

    def test_ls_help(self):
        result = runner.invoke(app, ["bin", "ls", "--help"])
        assert result.exit_code == 0


class TestBinFetch:
    """Tests for 'bin fetch' command."""

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_fetch_success(self, mock_bin_op):
        mock_bin_op.get.return_value = None  # Not already downloaded
        mock_bin_op.fetch.return_value = MagicMock(
            result=[_make_binary("firecracker", "1.15.0")],
        )
        result = runner.invoke(app, ["bin", "fetch", "1.15.0"])
        assert result.exit_code == 0
        assert "Downloaded" in result.output

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_fetch_with_set_default(self, mock_bin_op):
        mock_bin_op.get.return_value = None
        mock_bin_op.fetch.return_value = MagicMock(
            result=[_make_binary("firecracker", "1.15.0")],
        )
        result = runner.invoke(app, ["bin", "fetch", "1.15.0", "--set-default"])
        assert result.exit_code == 0
        assert "Default binary set" in result.output

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_fetch_error(self, mock_bin_op):
        mock_bin_op.get.return_value = None
        mock_bin_op.fetch.side_effect = MVMError("download failed")
        result = runner.invoke(app, ["bin", "fetch", "1.5.0"])
        assert result.exit_code == 1

    def test_fetch_help(self):
        result = runner.invoke(app, ["bin", "fetch", "--help"])
        assert result.exit_code == 0


class TestBinRemove:
    """Tests for 'bin rm' command."""

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_rm_by_version(self, mock_bin_op):
        mock_bin_op.remove_by_version.return_value = None
        mock_bin_op.remove.return_value = None
        result = runner.invoke(app, ["bin", "rm", "--version", "1.5.0", "dummyid"])
        assert result.exit_code == 0
        assert "Removed" in result.output

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_rm_by_id(self, mock_bin_op):
        mock_bin_op.remove.return_value = None
        result = runner.invoke(app, ["bin", "rm", "abc123"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_rm_no_target(self, mock_bin_op):
        result = runner.invoke(app, ["bin", "rm"])
        assert result.exit_code == 1
        assert "Provide" in result.output

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_rm_not_found(self, mock_bin_op):
        mock_bin_op.remove.side_effect = MVMError("not found")
        result = runner.invoke(app, ["bin", "rm", "abc123"])
        assert result.exit_code == 1


class TestBinDefault:
    """Tests for 'bin default' command."""

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_set_default_success(self, mock_bin_op):
        mock_bin_op.set_default.return_value = None
        result = runner.invoke(app, ["bin", "default", "abc123"])
        assert result.exit_code == 0
        assert "Default binary set" in result.output

    @patch("mvmctl.cli.bin.BinaryOperation")
    def test_set_default_not_found(self, mock_bin_op):
        mock_bin_op.set_default.side_effect = MVMError("not found")
        result = runner.invoke(app, ["bin", "default", "badid"])
        assert result.exit_code == 1

    def test_default_help(self):
        result = runner.invoke(app, ["bin", "default", "--help"])
        assert result.exit_code == 0


class TestBinHelp:
    """Tests for bin command group help."""

    def test_bin_help(self):
        result = runner.invoke(app, ["bin", "--help"])
        assert result.exit_code == 0
        assert "Binary management" in result.output
