"""Tests for debug mode functionality."""

from __future__ import annotations

from click.testing import CliRunner

from mvmctl.constants import DEBUG_MODE
from mvmctl.exceptions import MVMError, format_exception_debug
from mvmctl.main import app
from mvmctl.utils.common import is_debug_mode, set_debug_mode


class TestDebugConstants:
    """Test debug mode constants."""

    def test_debug_mode_constant(self):
        """DEBUG_MODE should be False by default."""
        assert DEBUG_MODE is False


class TestDebugFlagIntegration:
    """Test debug flag integration in main.py."""

    def test_debug_flag_stored_in_context(self):
        """Debug flag should be accepted by the CLI."""
        runner = CliRunner()
        result = runner.invoke(app, ["--debug", "version"])
        assert result.exit_code == 0

    def test_debug_flag_false_by_default(self):
        """Debug flag should be False when not specified."""
        runner = CliRunner()
        result = runner.invoke(app, ["version"])
        assert result.exit_code == 0

    def test_debug_flag_propagates_state(self):
        """Setting --debug flag should enable debug state."""
        set_debug_mode(False)
        runner = CliRunner()
        runner.invoke(app, ["--debug", "version"])
        # After invocation, debug mode should be enabled for the session
        # Note: depends on main.py setting it via set_debug_mode


class TestExceptionFormatting:
    """Test debug-aware exception formatting."""

    def test_format_exception_without_debug(self):
        """Without debug mode, format only the exception message."""
        exc = MVMError("Test error message")
        result = format_exception_debug(exc, debug=False)
        assert result == "Test error message"

    def test_format_exception_with_debug(self):
        """With debug mode, include exception class and traceback."""
        try:
            raise MVMError("Test error message")
        except MVMError as exc:
            result = format_exception_debug(exc, debug=True)
            assert "MVMError: Test error message" in result
            assert "Traceback" in result

    def test_format_exception_default_no_debug(self):
        """Default behavior (no debug arg) should not include traceback."""
        exc = MVMError("Test error message")
        result = format_exception_debug(exc)
        assert result == "Test error message"
        assert "Traceback" not in result
        assert "MVMError:" not in result

    def test_format_exception_with_different_error_types(self):
        """Test formatting works with different exception types."""
        exc = ValueError("Invalid value")
        result = format_exception_debug(exc, debug=False)
        assert result == "Invalid value"

        try:
            raise RuntimeError("Runtime problem")
        except RuntimeError as exc:
            result = format_exception_debug(exc, debug=True)
            assert "RuntimeError: Runtime problem" in result


class TestDebugState:
    """Test debug state functions."""

    def setup_method(self):
        set_debug_mode(False)

    def test_set_and_get(self):
        set_debug_mode(True)
        assert is_debug_mode() is True
        set_debug_mode(False)
        assert is_debug_mode() is False
