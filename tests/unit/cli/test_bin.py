"""Tests for CLI binary commands."""

from __future__ import annotations

import json
from unittest.mock import patch

from click.testing import CliRunner

from mvmctl.exceptions import BinaryAlreadyExistsError, MVMError
from mvmctl.main import app
from mvmctl.models import BinaryItem
from mvmctl.models.result import BatchResult, OperationResult

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

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_empty(self, mock_bin_op):
        mock_bin_op.list_all.return_value = []
        result = runner.invoke(app, ["bin", "ls"])
        assert result.exit_code == 0

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_with_local(self, mock_bin_op):
        mock_bin_op.list_all.return_value = [
            _make_binary("firecracker", "1.15.0", is_default=True),
            _make_binary("jailer", "1.15.0"),
        ]
        result = runner.invoke(app, ["bin", "ls"])
        assert result.exit_code == 0
        assert "1.15.0" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_json(self, mock_bin_op):
        mock_bin_op.list_all.return_value = [
            _make_binary("firecracker", "1.15.0"),
        ]
        result = runner.invoke(app, ["bin", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) >= 1

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_with_remote(self, mock_bin_op):
        mock_bin_op.list_all.side_effect = lambda *a, **kw: (
            ["1.16.0", "1.15.0", "1.14.0"]
            if kw.get("remote")
            else [_make_binary("firecracker", "1.15.0")]
        )
        result = runner.invoke(app, ["bin", "ls", "--remote"])
        assert result.exit_code == 0
        assert "1.16.0" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_remote_with_limit(self, mock_bin_op):
        mock_bin_op.list_all.side_effect = lambda *a, **kw: (
            ["1.16.0"] if kw.get("remote") else []
        )
        result = runner.invoke(app, ["bin", "ls", "--remote", "--limit", "5"])
        assert result.exit_code == 0
        mock_bin_op.list_all.assert_called_with(remote=True, limit=5)

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_remote_error(self, mock_bin_op):
        mock_bin_op.list_all.side_effect = lambda *a, **kw: (
            []
            if not kw.get("remote")
            else (_ for _ in ()).throw(MVMError("network fail"))
        )
        result = runner.invoke(app, ["bin", "ls", "--remote"])
        assert result.exit_code == 1

    def test_ls_help(self):
        result = runner.invoke(app, ["bin", "ls", "--help"])
        assert result.exit_code == 0


class TestBinPull:
    """Tests for 'bin pull' command."""

    @patch("mvmctl.api.BinaryOperation")
    def test_pull_success(self, mock_bin_op):
        mock_bin_op.list_all.return_value = ["1.16.0", "1.15.0", "1.14.0"]
        mock_bin_op.get.return_value = None  # Not already downloaded
        mock_bin_op.pull.return_value = OperationResult(
            status="success",
            code="binary.downloaded",
            item=[_make_binary("firecracker", "1.15.0")],
        )
        result = runner.invoke(app, ["bin", "pull", "firecracker", "--version", "1.15.0"])
        assert result.exit_code == 0
        assert "Downloaded" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_pull_with_set_default(self, mock_bin_op):
        mock_bin_op.list_all.return_value = ["1.16.0", "1.15.0", "1.14.0"]
        mock_bin_op.get.return_value = None
        mock_bin_op.pull.return_value = OperationResult(
            status="success",
            code="binary.downloaded",
            item=[_make_binary("firecracker", "1.15.0")],
        )
        result = runner.invoke(app, ["bin", "pull", "firecracker", "--version", "1.15.0", "--default"])
        assert result.exit_code == 0
        assert "Default binary set" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_pull_error(self, mock_bin_op):
        mock_bin_op.list_all.return_value = ["1.16.0", "1.15.0", "1.14.0", "1.5.0"]
        mock_bin_op.get.return_value = None
        mock_bin_op.pull.side_effect = MVMError("download failed")
        result = runner.invoke(app, ["bin", "pull", "firecracker", "--version", "1.5.0"])
        assert result.exit_code == 1

    def test_pull_help(self):
        result = runner.invoke(app, ["bin", "pull", "--help"])
        assert result.exit_code == 0


class TestBinRemove:
    """Tests for 'bin rm' command."""

    @patch("mvmctl.api.BinaryOperation")
    def test_rm_by_version(self, mock_bin_op):
        mock_bin_op.remove_by_version.return_value = OperationResult(
            status="success", code="binary.removed", message="Binary removed"
        )
        mock_bin_op.remove.return_value = BatchResult(
            items=[
                OperationResult(
                    status="success",
                    code="binary.removed",
                    message="Binary removed",
                )
            ]
        )
        result = runner.invoke(
            app, ["bin", "rm", "--version", "1.5.0", "dummyid"]
        )
        assert result.exit_code == 0
        assert "Removed" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_rm_by_id(self, mock_bin_op):
        mock_bin_op.remove.return_value = BatchResult(
            items=[
                OperationResult(
                    status="success",
                    code="binary.removed",
                    message="Binary removed",
                )
            ]
        )
        result = runner.invoke(app, ["bin", "rm", "abc123"])
        assert result.exit_code == 0

    @patch("mvmctl.api.BinaryOperation")
    def test_rm_no_target(self, mock_bin_op):
        result = runner.invoke(app, ["bin", "rm"])
        assert result.exit_code == 1
        assert "Provide" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_rm_not_found(self, mock_bin_op):
        mock_bin_op.remove.side_effect = MVMError("not found")
        result = runner.invoke(app, ["bin", "rm", "abc123"])
        assert result.exit_code == 1


class TestBinDefault:
    """Tests for 'bin default' command."""

    @patch("mvmctl.api.BinaryOperation")
    def test_set_default_success(self, mock_bin_op):
        mock_bin_op.set_default.return_value = OperationResult(
            status="success",
            code="binary.default_set",
            message="Default binary set",
        )
        result = runner.invoke(app, ["bin", "default", "abc123"])
        assert result.exit_code == 0
        assert "Default binary set" in result.output

    @patch("mvmctl.api.BinaryOperation")
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


class TestBinLsExtras:
    """Extended tests for bin ls."""

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_local_with_default_marker(self, mock_bin_op):
        mock_bin_op.list_all.return_value = [
            _make_binary("firecracker", "1.15.0", is_default=True),
            _make_binary("jailer", "1.14.0"),
        ]
        result = runner.invoke(app, ["bin", "ls"])
        assert result.exit_code == 0
        assert "*" in result.output
        assert "firecracker" in result.output
        assert "jailer" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_json_empty(self, mock_bin_op):
        mock_bin_op.list_all.return_value = []
        result = runner.invoke(app, ["bin", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert data == []

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_remote_no_locals(self, mock_bin_op):
        mock_bin_op.list_all.side_effect = lambda *a, **kw: (
            ["1.16.0", "1.15.0"] if kw.get("remote") else []
        )
        result = runner.invoke(app, ["bin", "ls", "--remote"])
        assert result.exit_code == 0
        assert "1.16.0" in result.output
        assert "1.15.0" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_ls_remote_with_cached_marker(self, mock_bin_op):
        mock_bin_op.list_all.side_effect = lambda *a, **kw: (
            ["1.16.0", "1.15.0"]
            if kw.get("remote")
            else [_make_binary("firecracker", "1.15.0")]
        )
        result = runner.invoke(app, ["bin", "ls", "--remote"])
        assert result.exit_code == 0
        assert "✓" in result.output


class TestBinPullExtras:
    """Extended tests for bin pull."""

    @patch("mvmctl.api.BinaryOperation")
    def test_pull_already_exists_decline(self, mock_bin_op):
        mock_bin_op.list_all.return_value = ["1.16.0", "1.15.0", "1.14.0"]
        mock_bin_op.get.return_value = [_make_binary("firecracker", "1.15.0")]
        mock_bin_op.pull.return_value = OperationResult(
            status="error",
            code="binary.pull_failed",
            message="Firecracker v1.15.0 already exists. Use --force to re-download.",
            exception=BinaryAlreadyExistsError("already exists"),
        )
        result = runner.invoke(app, ["bin", "pull", "firecracker", "--version", "1.15.0"], input="n\n")
        assert result.exit_code == 0
        assert "Aborted" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_pull_already_exists_accept(self, mock_bin_op):
        mock_bin_op.list_all.return_value = ["1.16.0", "1.15.0", "1.14.0"]
        mock_bin_op.get.return_value = [_make_binary("firecracker", "1.15.0")]
        mock_bin_op.pull.return_value = OperationResult(
            status="success",
            code="binary.downloaded",
            item=[_make_binary("firecracker", "1.15.0")],
        )
        result = runner.invoke(app, ["bin", "pull", "firecracker", "--version", "1.15.0"], input="y\n")
        assert result.exit_code == 0
        assert "Downloaded" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_pull_force_skip_confirm(self, mock_bin_op):
        mock_bin_op.list_all.return_value = ["1.16.0", "1.15.0", "1.14.0"]
        mock_bin_op.get.return_value = [_make_binary("firecracker", "1.15.0")]
        mock_bin_op.pull.return_value = OperationResult(
            status="success",
            code="binary.downloaded",
            item=[_make_binary("firecracker", "1.15.0")],
        )
        result = runner.invoke(app, ["bin", "pull", "firecracker", "--version", "1.15.0", "--force"])
        assert result.exit_code == 0
        assert "Downloaded" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_pull_skipped_status(self, mock_bin_op):
        mock_bin_op.list_all.return_value = ["1.16.0", "1.15.0", "1.14.0"]
        mock_bin_op.get.return_value = None
        mock_bin_op.pull.return_value = OperationResult(
            status="skipped",
            code="binary.already_present",
            message="Already present",
            item=[_make_binary("firecracker", "1.15.0")],
        )
        result = runner.invoke(app, ["bin", "pull", "firecracker", "--version", "1.15.0"])
        assert result.exit_code == 0
        assert "Already present" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_pull_error_status(self, mock_bin_op):
        mock_bin_op.list_all.return_value = ["1.16.0", "1.15.0", "1.14.0", "1.5.0"]
        mock_bin_op.get.return_value = None
        mock_bin_op.pull.return_value = OperationResult(
            status="error",
            code="binary.fetch_failed",
            message="Network error",
        )
        result = runner.invoke(app, ["bin", "pull", "firecracker", "--version", "1.5.0"])
        assert result.exit_code == 1
        assert "Network error" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_pull_with_v_prefix(self, mock_bin_op):
        mock_bin_op.list_all.return_value = ["1.16.0", "1.15.0", "1.14.0"]
        mock_bin_op.get.return_value = None
        mock_bin_op.pull.return_value = OperationResult(
            status="success",
            code="binary.downloaded",
            item=[_make_binary("firecracker", "1.15.0")],
        )
        result = runner.invoke(app, ["bin", "pull", "firecracker", "--version", "v1.15.0"])
        assert result.exit_code == 0
        assert "Downloaded" in result.output


class TestBinRemoveExtras:
    """Extended tests for bin rm."""

    @patch("mvmctl.api.BinaryOperation")
    def test_rm_batch_mixed(self, mock_bin_op):
        mock_bin_op.remove.return_value = BatchResult(
            items=[
                OperationResult(
                    status="success",
                    code="binary.removed",
                    message="Removed firecracker",
                ),
                OperationResult(
                    status="error",
                    code="binary.remove_failed",
                    message="Failed to remove jailer",
                ),
            ]
        )
        result = runner.invoke(app, ["bin", "rm", "abc123", "def456"])
        assert result.exit_code == 1
        assert "Removed firecracker" in result.output
        assert "Failed to remove jailer" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_rm_force_flag_passed(self, mock_bin_op):
        mock_bin_op.remove.return_value = BatchResult(
            items=[
                OperationResult(status="success", code="binary.removed"),
            ]
        )
        result = runner.invoke(app, ["bin", "rm", "--force", "abc123"])
        assert result.exit_code == 0
        _, kwargs = mock_bin_op.remove.call_args
        assert kwargs.get("force") is True

    @patch("mvmctl.api.BinaryOperation")
    def test_rm_version_error(self, mock_bin_op):
        mock_bin_op.remove_by_version.return_value = OperationResult(
            status="error",
            code="binary.remove_failed",
            message="Version 9.9.9 not found",
        )
        result = runner.invoke(app, ["bin", "rm", "--version", "9.9.9"])
        assert result.exit_code == 1
        assert "not found" in result.output.lower()

    @patch("mvmctl.api.BinaryOperation")
    def test_rm_version_success(self, mock_bin_op):
        mock_bin_op.remove_by_version.return_value = OperationResult(
            status="success",
            code="binary.removed",
            message="Removed binaries for v1.5.0",
        )
        result = runner.invoke(app, ["bin", "rm", "--version", "1.5.0"])
        assert result.exit_code == 0
        assert "Removed" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_rm_by_id_success(self, mock_bin_op):
        mock_bin_op.remove.return_value = BatchResult(
            items=[
                OperationResult(
                    status="success", code="binary.removed", message="Removed"
                ),
            ]
        )
        result = runner.invoke(app, ["bin", "rm", "abc123def"])
        assert result.exit_code == 0


class TestBinDefaultExtras:
    """Extended tests for bin default."""

    @patch("mvmctl.api.BinaryOperation")
    def test_set_default_error_status(self, mock_bin_op):
        mock_bin_op.set_default.return_value = OperationResult(
            status="error",
            code="binary.default_set_failed",
            message="Binary not found",
        )
        result = runner.invoke(app, ["bin", "default", "badid"])
        assert result.exit_code == 1
        assert "Binary not found" in result.output

    @patch("mvmctl.api.BinaryOperation")
    def test_set_default_empty_message(self, mock_bin_op):
        mock_bin_op.set_default.return_value = OperationResult(
            status="success",
            code="binary.default_set",
            message="",
        )
        result = runner.invoke(app, ["bin", "default", "abc123"])
        assert result.exit_code == 0
        assert "Default binary set to" in result.output
