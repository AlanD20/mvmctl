"""Tests for CLI kernel commands."""

from __future__ import annotations
from mvmctl.models.result import BatchResult, OperationResult

import json
from unittest.mock import patch

from click.testing import CliRunner

from mvmctl.exceptions import MVMError
from mvmctl.main import app
from mvmctl.models import KernelItem

runner = CliRunner()


def _make_kernel(
    name: str = "vmlinux-6.1.0",
    is_default: bool = False,
    is_present: bool = True,
    kernel_id: str | None = None,
) -> KernelItem:
    return KernelItem(
        id=kernel_id or f"krn-{name}-" + "x" * 55,
        name=name,
        base_name=name,
        version="6.1.0",
        arch="x86_64",
        type="firecracker",
        path=f"kernels/{name}",
        is_default=is_default,
        is_present=is_present,
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
    )


class TestKernelLs:
    """Tests for 'kernel ls' command."""

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_ls_empty(self, mock_krn_op):
        mock_krn_op.list_all.return_value = []
        result = runner.invoke(app, ["kernel", "ls"])
        assert result.exit_code == 0
        assert "No kernels found" in result.output

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_ls_with_kernels(self, mock_krn_op):
        mock_krn_op.list_all.return_value = [
            _make_kernel("vmlinux-6.1.0"),
            _make_kernel("vmlinux-5.15.0"),
        ]
        result = runner.invoke(app, ["kernel", "ls"])
        assert result.exit_code == 0
        assert "vmlinux-6.1.0" in result.output
        assert "vmlinux-5.15.0" in result.output

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_ls_json(self, mock_krn_op):
        mock_krn_op.list_all.return_value = [_make_kernel("vmlinux-6.1.0")]
        result = runner.invoke(app, ["kernel", "ls", "--json"])
        assert result.exit_code == 0
        data = json.loads(result.output)
        assert len(data) == 1

    def test_ls_help(self):
        result = runner.invoke(app, ["kernel", "ls", "--help"])
        assert result.exit_code == 0


class TestKernelFetch:
    """Tests for 'kernel fetch' command."""

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_fetch_success(self, mock_krn_op):
        mock_krn_op.fetch.return_value = OperationResult(status="success", code="kernel.fetched", item=_make_kernel("vmlinux-6.1.0"))
        result = runner.invoke(
            app,
            [
                "kernel",
                "fetch",
                "--type",
                "firecracker",
            ],
        )
        assert result.exit_code == 0
        assert "fetched" in result.output.lower()

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_fetch_with_version_and_arch(self, mock_krn_op):
        mock_krn_op.fetch.return_value = OperationResult(status="success", code="kernel.fetched", item=_make_kernel("vmlinux-6.1.0"))
        result = runner.invoke(
            app,
            [
                "kernel",
                "fetch",
                "--type",
                "firecracker",
                "--version",
                "6.1.0",
                "--arch",
                "x86_64",
            ],
        )
        assert result.exit_code == 0
        call_input = mock_krn_op.fetch.call_args[0][0]
        assert call_input.version == "6.1.0"
        assert call_input.arch == "x86_64"

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_fetch_missing_type(self, mock_krn_op):
        result = runner.invoke(app, ["kernel", "fetch"])
        assert result.exit_code == 2  # Required --type option missing
        assert (
            "Missing option" in result.output or "type" in result.output.lower()
        )

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_fetch_error(self, mock_krn_op):
        mock_krn_op.fetch.side_effect = MVMError("No version available")
        result = runner.invoke(
            app,
            [
                "kernel",
                "fetch",
                "--type",
                "firecracker",
            ],
        )
        assert result.exit_code == 1

    def test_fetch_help(self):
        result = runner.invoke(app, ["kernel", "fetch", "--help"])
        assert result.exit_code == 0


class TestKernelRemove:
    """Tests for 'kernel rm' command."""

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_rm_success(self, mock_krn_op):
        mock_krn_op.remove.return_value = BatchResult(items=[OperationResult(status="success", code="kernel.removed", message="Kernel removed")])
        result = runner.invoke(app, ["kernel", "rm", "abc123"])
        assert result.exit_code == 0
        assert "removed" in result.output.lower()

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_rm_multiple(self, mock_krn_op):
        mock_krn_op.remove.return_value = BatchResult(items=[OperationResult(status="success", code="kernel.removed", message="Kernel removed")])
        result = runner.invoke(app, ["kernel", "rm", "abc123", "def456"])
        assert result.exit_code == 0

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_rm_no_ids(self, mock_krn_op):
        result = runner.invoke(app, ["kernel", "rm"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_rm_not_found(self, mock_krn_op):
        mock_krn_op.remove.side_effect = MVMError("not found")
        result = runner.invoke(app, ["kernel", "rm", "badid"])
        assert result.exit_code == 1

    def test_rm_help(self):
        result = runner.invoke(app, ["kernel", "rm", "--help"])
        assert result.exit_code == 0


class TestKernelSetDefault:
    """Tests for 'kernel set-default' command."""

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_set_default_success(self, mock_krn_op):
        mock_krn_op.set_default.return_value = OperationResult(status='success', code='kernel.default_set', message='Default kernel set')
        result = runner.invoke(app, ["kernel", "set-default", "abc123"])
        assert result.exit_code == 0
        assert "Default kernel set" in result.output

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_set_default_no_args(self, mock_krn_op):
        result = runner.invoke(app, ["kernel", "set-default"])
        assert result.exit_code == 1

    @patch("mvmctl.cli.kernel.KernelOperation")
    def test_set_default_not_found(self, mock_krn_op):
        mock_krn_op.set_default.side_effect = MVMError("not found")
        result = runner.invoke(app, ["kernel", "set-default", "badid"])
        assert result.exit_code == 1

    def test_set_default_help(self):
        result = runner.invoke(app, ["kernel", "set-default", "--help"])
        assert result.exit_code == 0


class TestKernelHelp:
    """Tests for kernel command group help."""

    def test_kernel_help(self):
        result = runner.invoke(app, ["kernel", "--help"])
        assert result.exit_code == 0
        assert "Kernel management" in result.output
