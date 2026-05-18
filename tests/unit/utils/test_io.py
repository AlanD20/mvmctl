"""Tests for utils/_io.py and utils/cli.py — Logging and CLI display utilities."""

from __future__ import annotations

import logging
import os
from pathlib import Path
from unittest.mock import MagicMock

from rich.table import Table
from rich.tree import Tree

from mvmctl.utils._io import get_logger, log_exception, setup_logging
from mvmctl.utils.cli import MVMCli

# ==================== MVMCli Display Tests ====================


class TestMVMCliDisplayMethods:
    """Tests for MVMCli display methods — error, success, warning, info."""

    def test_error(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_err_console")
        cli.error("something went wrong")
        mock_console.print.assert_called_once_with(
            "[red]\u2717 Error:[/] something went wrong"
        )

    def test_error_unexpected(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_err_console")
        cli.error("unexpected failure", is_unexpected=True)
        mock_console.print.assert_called_once_with(
            "[yellow]\u26a0 Unexpected Error:[/] unexpected failure"
        )

    def test_success(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.success("task completed")
        mock_console.print.assert_called_once_with(
            "[green]\u2713 task completed[/]"
        )

    def test_warning(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_err_console")
        cli.warning("caution advised")
        mock_console.print.assert_called_once_with(
            "[yellow]! caution advised[/]"
        )

    def test_info(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.info("informational message")
        mock_console.print.assert_called_once_with(
            "[dim]  informational message[/]"
        )


class TestMVMCliSectionHeader:
    """Tests for MVMCli.section_header()."""

    def test_prints_header_with_newline_markup(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.section_header("BASIC INFO")
        mock_console.print.assert_called_once_with("\n[bold]BASIC INFO[/]")

    def test_multiple_headers(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.section_header("FIRST")
        cli.section_header("SECOND")
        assert mock_console.print.call_count == 2
        mock_console.print.assert_any_call("\n[bold]FIRST[/]")
        mock_console.print.assert_any_call("\n[bold]SECOND[/]")


class TestMVMCliInspectHeader:
    """Tests for MVMCli.inspect_header()."""

    def test_with_subtitle(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.inspect_header("my-vm", "running")
        assert mock_console.print.call_count == 2
        mock_console.print.assert_any_call("\n[bold]my-vm (running)[/]")
        mock_console.print.assert_any_call("=" * len("my-vm (running)"))

    def test_without_subtitle(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.inspect_header("my-vm")
        assert mock_console.print.call_count == 2
        mock_console.print.assert_any_call("\n[bold]my-vm[/]")
        mock_console.print.assert_any_call("=" * len("my-vm"))


class TestMVMCliKeyValue:
    """Tests for MVMCli.key_value()."""

    def test_default_format(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.key_value("Name", "my-vm")
        mock_console.print.assert_called_once_with("  Name:        my-vm")

    def test_custom_indent(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.key_value("Name", "my-vm", indent=4)
        mock_console.print.assert_called_once_with("    Name:        my-vm")

    def test_custom_key_width(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.key_value("Name", "my-vm", key_width=20)
        mock_console.print.assert_called_once_with(
            "  Name:                my-vm"
        )

    def test_long_value(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.key_value("Description", "a very long description text")
        # "Description:" is exactly 12 chars, so no extra padding beyond key_width=12
        mock_console.print.assert_called_once_with(
            "  Description: a very long description text"
        )


class TestMVMCliTable:
    """Tests for MVMCli.table()."""

    def test_with_title(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.table(
            columns=["Name", "Age"],
            rows=[["Alice", "30"], ["Bob", "25"]],
            title="People",
        )
        mock_console.print.assert_called_once()
        table = mock_console.print.call_args[0][0]
        assert isinstance(table, Table)
        assert table.title == "People"
        assert table.columns[0].header == "Name"
        assert table.columns[1].header == "Age"
        assert len(table.rows) == 2

    def test_without_title(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.table(
            columns=["Name", "Age"],
            rows=[["Alice", "30"]],
        )
        table = mock_console.print.call_args[0][0]
        assert isinstance(table, Table)
        assert table.title is None
        assert table.columns[0].header == "Name"

    def test_column_headers_are_set(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.table(
            columns=["Short", "VeryLongColumnName"],
            rows=[["A", "B"]],
        )
        table = mock_console.print.call_args[0][0]
        assert isinstance(table, Table)
        assert table.columns[0].header == "Short"
        assert table.columns[1].header == "VeryLongColumnName"

    def test_empty_rows(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.table(
            columns=["Name", "Age"],
            rows=[],
        )
        table = mock_console.print.call_args[0][0]
        assert isinstance(table, Table)
        assert len(table.rows) == 0


class TestMVMCliPrintDictTree:
    """Tests for MVMCli.print_dict_tree()."""

    def test_simple_dict(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.print_dict_tree({"name": "test-vm", "state": "running"}, title="VM")
        mock_console.print.assert_called_once()
        tree = mock_console.print.call_args[0][0]
        assert isinstance(tree, Tree)
        assert tree.label == "VM"

    def test_nested_dict(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.print_dict_tree(
            {"metadata": {"created": "2024-01-01", "version": 2}},
        )
        mock_console.print.assert_called_once()
        tree = mock_console.print.call_args[0][0]
        assert isinstance(tree, Tree)

    def test_empty_data_prints_nothing(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.print_dict_tree({})
        mock_console.print.assert_not_called()

    def test_list_data(self, mocker):
        cli = MVMCli()
        mock_console = mocker.patch.object(cli, "_console")
        cli.print_dict_tree([{"id": "1"}, {"id": "2"}])
        mock_console.print.assert_called_once()
        tree = mock_console.print.call_args[0][0]
        assert isinstance(tree, Tree)


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
