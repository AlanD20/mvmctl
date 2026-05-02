"""Tests for utils/cli.py — CLI utilities (handle_errors decorator, CliUtils)."""

from __future__ import annotations

import sqlite3
from unittest.mock import MagicMock

import click
import pytest
import typer
from typer.testing import CliRunner as TyperCliRunner

from mvmctl.exceptions import MVMError, PrivilegeError, format_exception_debug
from mvmctl.utils.cli import CliUtils, _print_error, handle_errors

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


# ---------------------------------------------------------------------------
# Additional handle_errors tests for uncovered branches
# ---------------------------------------------------------------------------


class TestHandleErrorsAdditional:
    """Additional tests for uncovered branches in handle_errors decorator."""

    def test_abort_exit_code_130(self):
        """click.exceptions.Abort should convert to Exit(code=130)."""

        @handle_errors
        def _cmd():
            raise click.exceptions.Abort()

        with pytest.raises(typer.Exit) as exc_info:
            _cmd()
        assert exc_info.value.exit_code == 130

    def test_broken_pipe_stderr_close_also_fails(self, mocker):
        """Should handle BrokenPipeError when stderr.close() also fails."""

        mocker.patch("sys.stderr.close", side_effect=BrokenPipeError())

        @handle_errors
        def _cmd():
            raise BrokenPipeError()

        with pytest.raises(typer.Exit) as exc_info:
            _cmd()
        assert exc_info.value.exit_code == 0

    def test_privilege_error_without_details(self, capsys):
        """PrivilegeError without details should print error and exit."""

        @handle_errors
        def _cmd():
            raise PrivilegeError("permission denied")

        with pytest.raises(typer.Exit) as exc_info:
            _cmd()
        assert exc_info.value.exit_code == 1
        captured = capsys.readouterr()
        assert "permission denied" in captured.out

    def test_privilege_error_with_details(self, capsys):
        """PrivilegeError with details should print details and suggestions."""

        @handle_errors
        def _cmd():
            raise PrivilegeError(
                "permission denied",
                details={
                    "message": "User not in mvm group",
                    "suggestions": [
                        "Run: sudo usermod -aG mvm $USER",
                        "Log out and back in",
                    ],
                },
            )

        with pytest.raises(typer.Exit) as exc_info:
            _cmd()
        assert exc_info.value.exit_code == 1
        captured = capsys.readouterr()
        assert "User not in mvm group" in captured.out
        assert "sudo usermod" in captured.out

    def test_privilege_error_with_details_no_message(self, capsys):
        """PrivilegeError with details but no message key should skip message."""

        @handle_errors
        def _cmd():
            raise PrivilegeError(
                "permission denied",
                details={
                    "suggestions": ["Fix it"],
                },
            )

        with pytest.raises(typer.Exit) as exc_info:
            _cmd()
        assert exc_info.value.exit_code == 1
        captured = capsys.readouterr()
        assert "Fix it" in captured.out

    def test_sqlite_operational_error_other(self, capsys):
        """Non-table OperationalError should print database error."""

        @handle_errors
        def _cmd():
            raise sqlite3.OperationalError("database is locked")

        with pytest.raises(typer.Exit) as exc_info:
            _cmd()
        assert exc_info.value.exit_code == 1

    def test_unexpected_exception_calls_log_exception(self, mocker):
        """Unexpected exception should call log_exception."""

        mock_log_exception = mocker.patch("mvmctl.utils.cli.log_exception")

        @handle_errors
        def _cmd():
            raise RuntimeError("unexpected runtime error")

        with pytest.raises(typer.Exit):
            _cmd()

        mock_log_exception.assert_called_once()
        args = mock_log_exception.call_args
        assert "unexpected runtime error" in str(args)


class TestPrintError:
    """Tests for _print_error()."""

    def test_print_error_normal(self, mocker):
        """Normal error should use red color and 'Error' title."""
        mock_console = mocker.patch("mvmctl.utils.cli._err_console")
        _print_error("test error")
        mock_console.print.assert_called_once()
        call_arg = mock_console.print.call_args[0][0]
        assert "Error" in call_arg
        assert "test error" in call_arg

    def test_print_error_unexpected(self, mocker):
        """Unexpected error should use yellow and 'Unexpected Error'."""
        mock_console = mocker.patch("mvmctl.utils.cli._err_console")
        _print_error("unexpected", is_unexpected=True)
        mock_console.print.assert_called_once()
        call_arg = mock_console.print.call_args[0][0]
        assert "Unexpected Error" in call_arg
        assert "unexpected" in call_arg
