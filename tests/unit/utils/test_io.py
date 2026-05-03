"""Tests for utils/_io.py — Console output and logging utilities."""

from __future__ import annotations

import logging
import os
from pathlib import Path
from unittest.mock import MagicMock

from mvmctl.utils._io import (
    _PlainConsole,
    _strip_markup,
    get_combined_marker,
    get_logger,
    get_state_marker,
    log_exception,
    print_error,
    print_info,
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
    print_warning,
    setup_logging,
)


class TestStripMarkup:
    """Tests for _strip_markup()."""

    def test_removes_color_tags(self):
        result = _strip_markup("[green]hello[/green]")
        assert result == "hello"

    def test_removes_bold_and_dim(self):
        result = _strip_markup("[bold]bold[/bold] and [dim]dim[/dim]")
        assert result == "bold and dim"

    def test_removes_link_tags(self):
        result = _strip_markup("[link=https://example.com]click[/link]")
        assert result == "click"

    def test_removes_tag_with_attributes(self):
        result = _strip_markup('[style color="red"]red text[/style]')
        assert result == "red text"

    def test_removes_nested_markup(self):
        result = _strip_markup("[bold][green]nested[/green][/bold]")
        assert result == "nested"

    def test_no_markup_unchanged(self):
        result = _strip_markup("plain text with no markup")
        assert result == "plain text with no markup"

    def test_removes_multiple_tags(self):
        result = _strip_markup(
            "[bold][red]ERROR:[/red][/bold] something [blue]failed[/blue]"
        )
        assert result == "ERROR: something failed"


class TestPlainConsole:
    """Tests for _PlainConsole."""

    def test_print_strips_markup(self, capsys):
        console = _PlainConsole()
        console.print("[green]hello[/green]")
        captured = capsys.readouterr()
        assert captured.out.strip() == "hello"

    def test_print_joins_multiple_args(self, capsys):
        console = _PlainConsole()
        console.print("hello", "world")
        captured = capsys.readouterr()
        assert captured.out.strip() == "hello world"

    def test_print_discards_rich_kwargs(self, capsys):
        console = _PlainConsole()
        console.print("plain", markup=True, highlight=False, style="bold")
        captured = capsys.readouterr()
        assert captured.out.strip() == "plain"

    def test_getattr_returns_noop_callable(self):
        console = _PlainConsole()
        noop = console.status
        assert callable(noop)
        noop()
        noop("test", key="value")

    def test_getattr_returns_noop_for_any_attribute(self):
        console = _PlainConsole()
        for attr in ("rule", "progress", "live", "columns"):
            noop = getattr(console, attr)
            assert callable(noop)
            noop()


class TestPrintTable:
    """Tests for print_table()."""

    def test_with_title(self, capsys):
        print_table(
            columns=["Name", "Age"],
            rows=[["Alice", "30"], ["Bob", "25"]],
            title="People",
        )
        captured = capsys.readouterr()
        lines = captured.out.splitlines()
        assert lines[0] == "People"
        assert lines[1] == "------"
        assert "Alice" in lines[4]
        assert "Bob" in lines[5]

    def test_without_title(self, capsys):
        print_table(
            columns=["Name", "Age"],
            rows=[["Alice", "30"]],
        )
        captured = capsys.readouterr()
        assert captured.out.startswith("Name")
        assert "Age" in captured.out

    def test_rows_with_fewer_cells_than_columns(self, capsys):
        print_table(
            columns=["Name", "Age", "City"],
            rows=[["Alice", "30"]],
        )
        captured = capsys.readouterr()
        lines = captured.out.splitlines()
        assert "Alice" in captured.out
        assert "30" in captured.out
        assert "City" in lines[0]

    def test_column_widths_padded_correctly(self, capsys):
        print_table(
            columns=["Short", "VeryLongColumnName"],
            rows=[["A", "B"]],
        )
        captured = capsys.readouterr()
        lines = captured.out.splitlines()
        assert "VeryLongColumnName" in lines[0]


class TestPrintHelpers:
    """Tests for print_error, print_success, print_warning, print_info."""

    def test_print_error(self, capsys):
        print_error("something went wrong")
        captured = capsys.readouterr()
        assert captured.out.strip() == "Error: something went wrong"

    def test_print_success(self, capsys):
        print_success("task completed")
        captured = capsys.readouterr()
        assert captured.out.strip() == "\u2713 task completed"

    def test_print_warning(self, capsys):
        print_warning("caution advised")
        captured = capsys.readouterr()
        assert captured.out.strip() == "! caution advised"

    def test_print_info(self, capsys):
        print_info("informational message")
        captured = capsys.readouterr()
        # print_info uses two leading spaces, strip() removes them
        assert "informational message" in captured.out
        assert captured.out.startswith("  ")


class TestPrintSectionHeader:
    """Tests for print_section_header()."""

    def test_prints_with_newline_prefix(self, capsys):
        print_section_header("BASIC INFO")
        captured = capsys.readouterr()
        assert captured.out == "\nBASIC INFO\n"

    def test_multiple_headers(self, capsys):
        print_section_header("FIRST")
        print_section_header("SECOND")
        captured = capsys.readouterr()
        assert captured.out == "\nFIRST\n\nSECOND\n"


class TestPrintKeyValue:
    """Tests for print_key_value()."""

    def test_default_format(self, capsys):
        print_key_value("Name", "my-vm")
        captured = capsys.readouterr()
        # indent=2, key_width=12: '  ' + 'Name:' padded to 12 + ' ' + 'my-vm'
        # 'Name:' is 5 chars, padded to 12 = 7 spaces padding + 1 separator space = 8
        assert captured.out == "  Name:        my-vm\n"

    def test_custom_indent(self, capsys):
        print_key_value("Name", "my-vm", indent=4)
        captured = capsys.readouterr()
        assert captured.out == "    Name:        my-vm\n"

    def test_custom_key_width(self, capsys):
        print_key_value("Name", "my-vm", key_width=20)
        captured = capsys.readouterr()
        # indent=2, key_width=20: '  ' + 'Name:' padded to 20 + ' ' + 'my-vm'
        # 'Name:' is 5 chars, padded to 20 = 15 spaces padding + 1 separator space = 16
        assert captured.out == "  Name:                my-vm\n"

    def test_custom_indent_and_key_width(self, capsys):
        print_key_value("Key", "value", indent=6, key_width=8)
        captured = capsys.readouterr()
        # 'Key:' is 4 chars, padded to 8 = 4 spaces padding + 1 separator space = 5
        assert captured.out == "      Key:     value\n"

    def test_long_value(self, capsys):
        print_key_value("Description", "a very long description text")
        captured = capsys.readouterr()
        assert "a very long description text" in captured.out


class TestPrintInspectHeader:
    """Tests for print_inspect_header()."""

    def test_with_subtitle(self, capsys):
        print_inspect_header("my-vm", "running")
        captured = capsys.readouterr()
        # Output: '\nmy-vm (running)\n==================\n'
        lines = captured.out.splitlines()
        assert lines[0] == ""
        assert lines[1] == "my-vm (running)"
        assert lines[2] == "=" * len("my-vm (running)")

    def test_without_subtitle(self, capsys):
        print_inspect_header("my-vm")
        captured = capsys.readouterr()
        # Output: '\nmy-vm\n=====\n'
        lines = captured.out.splitlines()
        assert lines[0] == ""
        assert lines[1] == "my-vm"
        assert lines[2] == "=" * len("my-vm")


class TestGetStateMarker:
    """Tests for get_state_marker()."""

    def test_missing_returns_X_with_space(self):
        assert get_state_marker(True) == "X "

    def test_not_missing_returns_two_spaces(self):
        assert get_state_marker(False) == "  "


class TestGetCombinedMarker:
    """Tests for get_combined_marker()."""

    def test_default_and_missing(self):
        assert get_combined_marker(True, True) == "*X "

    def test_missing_only(self):
        assert get_combined_marker(False, True) == " X "

    def test_default_only(self):
        assert get_combined_marker(True, False) == "*  "

    def test_neither(self):
        assert get_combined_marker(False, False) == "   "


# ---------------------------------------------------------------------------
# Logging tests — require root logger isolation
# ---------------------------------------------------------------------------


class TestSetupLogging:
    """Tests for setup_logging() — isolates root logger per test."""

    @staticmethod
    def _reset_root_logger():
        root = logging.getLogger()
        root.handlers.clear()
        root.setLevel(logging.WARNING)

    def teardown_method(self) -> None:
        """Ensure root logger is clean after each test."""
        logging.getLogger().handlers.clear()

    def test_debug_sets_debug_level(self, mocker):
        self._reset_root_logger()
        mocker.patch("logging.handlers.RotatingFileHandler")
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_log_path",
            return_value=Path("/tmp/test-mvmctl.log"),
        )
        setup_logging(debug=True)
        root = logging.getLogger()
        assert root.level == logging.DEBUG
        assert len(root.handlers) == 2
        console_handler = root.handlers[0]
        assert console_handler.level == logging.DEBUG

    def test_verbose_sets_info_level(self, mocker):
        self._reset_root_logger()
        mocker.patch("logging.handlers.RotatingFileHandler")
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_log_path",
            return_value=Path("/tmp/test-mvmctl.log"),
        )
        setup_logging(verbose=True)
        root = logging.getLogger()
        console_handler = root.handlers[0]
        assert console_handler.level == logging.INFO

    def test_env_var_sets_level(self, mocker):
        self._reset_root_logger()
        mocker.patch("logging.handlers.RotatingFileHandler")
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_log_path",
            return_value=Path("/tmp/test-mvmctl.log"),
        )
        mocker.patch.dict(os.environ, {"MVM_LOG_LEVEL": "ERROR"})
        setup_logging()
        console_handler = logging.getLogger().handlers[0]
        assert console_handler.level == logging.ERROR

    def test_default_env_level_is_warning(self, mocker):
        self._reset_root_logger()
        mocker.patch("logging.handlers.RotatingFileHandler")
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_log_path",
            return_value=Path("/tmp/test-mvmctl.log"),
        )
        setup_logging()
        console_handler = logging.getLogger().handlers[0]
        assert console_handler.level == logging.WARNING

    def test_invalid_env_level_falls_back_to_warning(self, mocker):
        self._reset_root_logger()
        mocker.patch("logging.handlers.RotatingFileHandler")
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_log_path",
            return_value=Path("/tmp/test-mvmctl.log"),
        )
        mocker.patch.dict(os.environ, {"MVM_LOG_LEVEL": "BOGUS"})
        setup_logging()
        console_handler = logging.getLogger().handlers[0]
        assert console_handler.level == logging.WARNING

    def test_early_return_when_handlers_exist(self, mocker):
        self._reset_root_logger()
        mocker.patch("logging.handlers.RotatingFileHandler")
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_log_path",
            return_value=Path("/tmp/test-mvmctl.log"),
        )
        setup_logging(debug=True)
        assert len(logging.getLogger().handlers) == 2
        # Second call should NOT add more handlers
        setup_logging(verbose=True)
        assert len(logging.getLogger().handlers) == 2

    def test_file_handler_created(self, mocker):
        self._reset_root_logger()
        mock_rfh = mocker.patch("logging.handlers.RotatingFileHandler")
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_log_path",
            return_value=Path("/tmp/custom-path/mvmctl.log"),
        )
        setup_logging(debug=True)
        assert mock_rfh.call_count > 0
        args, _ = mock_rfh.call_args
        assert str(args[0]) == "/tmp/custom-path/mvmctl.log"

    def test_file_handler_level_is_debug(self, mocker):
        self._reset_root_logger()
        mock_rfh = mocker.patch("logging.handlers.RotatingFileHandler")
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_log_path",
            return_value=Path("/tmp/test-mvmctl.log"),
        )
        setup_logging(debug=True)
        mock_rfh.return_value.setLevel.assert_called_once_with(logging.DEBUG)

    def test_console_and_file_formatter_set(self, mocker):
        self._reset_root_logger()
        mocker.patch("logging.handlers.RotatingFileHandler")
        mocker.patch(
            "mvmctl.utils.common.CacheUtils.get_log_path",
            return_value=Path("/tmp/test-mvmctl.log"),
        )
        setup_logging(debug=True)
        root = logging.getLogger()
        for handler in root.handlers:
            assert handler.formatter is not None


class TestGetLogger:
    """Tests for get_logger()."""

    def test_returns_logger_with_given_name(self):
        logger = get_logger("my.custom.module")
        assert logger.name == "my.custom.module"

    def test_returns_logger_instance(self):
        logger = get_logger("test")
        assert isinstance(logger, logging.Logger)


class TestLogException:
    """Tests for log_exception()."""

    def test_debug_calls_logger_exception(self):
        logger = MagicMock()
        logger.isEnabledFor.return_value = True
        exc = ValueError("bad value")

        log_exception(logger, "test message", exc)

        logger.exception.assert_called_once()
        args, _ = logger.exception.call_args
        assert args[0] == "%s: %s"
        assert args[1] == "test message"
        assert args[2] is exc
        logger.error.assert_not_called()

    def test_not_debug_calls_logger_error(self):
        logger = MagicMock()
        logger.isEnabledFor.return_value = False
        exc = ValueError("bad value")

        log_exception(logger, "test message", exc)

        logger.error.assert_called_once()
        args, _ = logger.error.call_args
        assert args[0] == "%s: %s"
        assert args[1] == "test message"
        assert args[2] is exc
        logger.exception.assert_not_called()

    def test_debug_checks_correct_level(self):
        logger = MagicMock()

        log_exception(logger, "msg", Exception("err"))

        logger.isEnabledFor.assert_called_once_with(logging.DEBUG)
