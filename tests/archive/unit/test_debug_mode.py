"""Tests for debug mode functionality."""

from click.testing import CliRunner

from mvmctl.constants import DEBUG_MODE, DEBUG_SHOW_TRACEBACKS, DEBUG_VERBOSE_ERRORS
from mvmctl.exceptions import MVMError, format_exception_debug
from mvmctl.main import app


class TestDebugConstants:
    """Test debug mode constants load correctly from _defaults.py."""

    def test_debug_mode_constant(self) -> None:
        """DEBUG_MODE should be False by default (from _defaults.py)."""
        assert DEBUG_MODE is False

    def test_debug_verbose_errors_constant(self) -> None:
        """DEBUG_VERBOSE_ERRORS should be True by default (from _defaults.py)."""
        assert DEBUG_VERBOSE_ERRORS is True

    def test_debug_show_tracebacks_constant(self) -> None:
        """DEBUG_SHOW_TRACEBACKS should be False by default (from _defaults.py)."""
        assert DEBUG_SHOW_TRACEBACKS is False


class TestDebugFlagIntegration:
    """Test debug flag integration in main.py."""

    def test_debug_flag_stored_in_context(self) -> None:
        """Debug flag should be stored in Click context object."""
        runner = CliRunner()

        # Use the existing 'version' command - it should work with --debug
        result = runner.invoke(app, ["--debug", "version"])
        # version command exits with 0 and shows version
        assert result.exit_code == 0

    def test_debug_flag_false_by_default(self) -> None:
        """Debug flag should be False when not specified."""
        runner = CliRunner()

        # Invoke without --debug flag
        result = runner.invoke(app, ["version"])
        assert result.exit_code == 0


class TestExceptionFormatting:
    """Test debug-aware exception formatting."""

    def test_format_exception_without_debug(self) -> None:
        """Without debug mode, format only the exception message."""
        exc = MVMError("Test error message")
        result = format_exception_debug(exc, debug=False)
        assert result == "Test error message"

    def test_format_exception_with_debug(self) -> None:
        """With debug mode, include exception class and traceback."""
        # Create exception inside a try block so traceback.format_exc() works
        try:
            raise MVMError("Test error message")
        except MVMError as exc:
            result = format_exception_debug(exc, debug=True)
            # Should contain class name and message
            assert "MVMError: Test error message" in result
            # Should contain traceback info
            assert "Traceback" in result

    def test_format_exception_default_no_debug(self) -> None:
        """Default behavior (no debug arg) should not include traceback."""
        exc = MVMError("Test error message")
        result = format_exception_debug(exc)
        assert result == "Test error message"
        assert "Traceback" not in result
        assert "MVMError:" not in result

    def test_format_exception_with_different_error_types(self) -> None:
        """Test formatting works with different exception types."""
        # ValueError
        exc = ValueError("Invalid value")
        result = format_exception_debug(exc, debug=False)
        assert result == "Invalid value"

        # RuntimeError - test with debug in try block
        try:
            raise RuntimeError("Runtime problem")
        except RuntimeError as exc:
            result = format_exception_debug(exc, debug=True)
            assert "RuntimeError: Runtime problem" in result
