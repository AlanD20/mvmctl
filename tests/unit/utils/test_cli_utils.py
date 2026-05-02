"""Tests for utils/cli.py — CLI utilities (handle_errors decorator, CliUtils)."""

from __future__ import annotations

import sqlite3
from unittest.mock import MagicMock

import pytest
import typer
from typer.testing import CliRunner as TyperCliRunner

from mvmctl.exceptions import MVMError, format_exception_debug
from mvmctl.utils.cli import CliUtils, handle_errors

# <leave rest untouched>


# ---------------------------------------------------------------------------
# handle_errors decorator
# ---------------------------------------------------------------------------


class TestHandleErrors:
    """Tests for the handle_errors decorator."""

    def test_successful_command_passes_through(self):
        """Should return normally on success."""
        app = typer.Typer()

        @app.callback()
        def _main() -> None:
            pass

        @app.command(name="test")
        @handle_errors
        def _cmd():
            return "success"

        runner = TyperCliRunner()
        result = runner.invoke(app, ["test"])
        assert result.exit_code == 0

    def test_mvm_error_caught_and_exits(self):
        """Should catch MVMError and exit with code 1."""
        app = typer.Typer()

        @app.callback()
        def _main() -> None:
            pass

        @app.command(name="test")
        @handle_errors
        def _cmd():
            raise MVMError("test error")

        runner = TyperCliRunner()
        result = runner.invoke(app, ["test"])
        assert result.exit_code == 1

    def test_keyboard_interrupt_exit_code_130(self):
        """Should exit with 130 on KeyboardInterrupt."""
        app = typer.Typer()

        @app.callback()
        def _main() -> None:
            pass

        @app.command(name="test")
        @handle_errors
        def _cmd():
            raise KeyboardInterrupt()

        runner = TyperCliRunner()
        result = runner.invoke(app, ["test"])
        assert result.exit_code == 130

    def test_broken_pipe_exit_code_0(self, mocker):
        """Should exit with 0 on BrokenPipeError."""
        mocker.patch("sys.stderr.close")

        @handle_errors
        def _cmd():
            raise BrokenPipeError()

        with pytest.raises(typer.Exit) as exc_info:
            _cmd()
        assert exc_info.value.exit_code == 0

    def test_sqlite_no_table_error(self):
        """Should give helpful message on missing table."""
        app = typer.Typer()

        @app.callback()
        def _main() -> None:
            pass

        @app.command(name="test")
        @handle_errors
        def _cmd():
            raise sqlite3.OperationalError("no such table: vms")

        runner = TyperCliRunner()
        result = runner.invoke(app, ["test"])
        assert result.exit_code == 1
        assert "Run 'mvm init' first" in result.stdout

    def test_unexpected_exception_shows_message(self):
        """Should show unexpected error message."""
        app = typer.Typer()

        @app.callback()
        def _main() -> None:
            pass

        @app.command(name="test")
        @handle_errors
        def _cmd():
            raise ValueError("unexpected value")

        runner = TyperCliRunner()
        result = runner.invoke(app, ["test"])
        assert result.exit_code == 1


# ---------------------------------------------------------------------------
# CliUtils
# ---------------------------------------------------------------------------


class TestCliUtilsCheckNameArg:
    """Tests for CliUtils.check_name_arg()."""

    def test_valid_name_returns_name(self):
        """Should return the name when valid."""
        ctx = MagicMock()
        result = CliUtils.check_name_arg(ctx, "my-vm")
        assert result == "my-vm"

    def test_none_shows_help(self):
        """Should show help and exit when name is None."""
        ctx = MagicMock()
        with pytest.raises(typer.Exit):
            CliUtils.check_name_arg(ctx, None)

    def test_help_shows_help(self):
        """Should show help and exit when name is 'help'."""
        ctx = MagicMock()
        with pytest.raises(typer.Exit):
            CliUtils.check_name_arg(ctx, "help")


# ---------------------------------------------------------------------------
# format_exception_debug
# ---------------------------------------------------------------------------


class TestFormatExceptionDebug:
    """Tests for format_exception_debug()."""

    def test_without_debug_shows_message_only(self):
        exc = MVMError("Test error message")
        result = format_exception_debug(exc, debug=False)
        assert result == "Test error message"

    def test_with_debug_includes_class_and_traceback(self):
        try:
            raise MVMError("Test error message")
        except MVMError as exc:
            result = format_exception_debug(exc, debug=True)
            assert "MVMError: Test error message" in result
            assert "Traceback" in result

    def test_default_no_debug(self):
        exc = MVMError("Test error message")
        result = format_exception_debug(exc)
        assert result == "Test error message"
        assert "Traceback" not in result
        assert "MVMError:" not in result

    def test_with_different_error_types(self):
        exc = ValueError("Invalid value")
        result = format_exception_debug(exc, debug=False)
        assert result == "Invalid value"

        try:
            raise RuntimeError("Runtime problem")
        except RuntimeError as exc:
            result = format_exception_debug(exc, debug=True)
            assert "RuntimeError: Runtime problem" in result
