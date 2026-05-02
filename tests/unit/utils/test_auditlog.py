"""Tests for utils/auditlog.py — AuditLog."""

from __future__ import annotations

import logging
from unittest.mock import MagicMock, patch

from mvmctl.utils.auditlog import AuditLog


class TestAuditLogLog:
    """Tests for AuditLog.log()."""

    @patch("mvmctl.utils.auditlog.AuditLog._get_logger")
    def test_logs_basic_operation(self, mock_get_logger):
        """Should log with user and operation."""
        mock_logger = MagicMock()
        mock_get_logger.return_value = mock_logger

        AuditLog.log("vm.create")

        mock_logger.info.assert_called_once()
        msg = mock_logger.info.call_args[0][0]
        assert "op=vm.create" in msg
        assert "user=" in msg

    @patch("mvmctl.utils.auditlog.AuditLog._get_logger")
    def test_logs_with_changes(self, mock_get_logger):
        """Should log with changes dict."""
        mock_logger = MagicMock()
        mock_get_logger.return_value = mock_logger

        AuditLog.log(
            "binary.fetch", changes={"name": "firecracker", "version": "1.15.0"}
        )

        msg = mock_logger.info.call_args[0][0]
        assert "op=binary.fetch" in msg
        assert "changes=name=firecracker,version=1.15.0" in msg

    @patch("mvmctl.utils.auditlog.AuditLog._get_logger")
    def test_logs_with_changes_and_context(self, mock_get_logger):
        """Should log with changes dict and context."""
        mock_logger = MagicMock()
        mock_get_logger.return_value = mock_logger

        AuditLog.log(
            "vm.create", changes={"name": "test-vm"}, context="test context"
        )

        msg = mock_logger.info.call_args[0][0]
        assert "op=vm.create" in msg
        assert "changes=name=test-vm" in msg
        assert "context=" in msg

    @patch("mvmctl.utils.auditlog.AuditLog._get_logger")
    def test_includes_timestamp(self, mock_get_logger):
        """Should include timestamp in log."""
        mock_logger = MagicMock()
        mock_get_logger.return_value = mock_logger

        AuditLog.log("test.op")

        msg = mock_logger.info.call_args[0][0]
        assert "[" in msg
        assert "Z" in msg

    @patch("mvmctl.utils.auditlog.AuditLog._get_logger")
    @patch("mvmctl.utils.auditlog.getpass.getuser", return_value="testuser")
    def test_uses_username(self, mock_getuser, mock_get_logger):
        """Should use getpass.getuser() for user."""
        mock_logger = MagicMock()
        mock_get_logger.return_value = mock_logger

        AuditLog.log("test.op")

        msg = mock_logger.info.call_args[0][0]
        assert "user=testuser" in msg

    @patch("mvmctl.utils.auditlog.AuditLog._get_logger")
    @patch(
        "mvmctl.utils.auditlog.getpass.getuser",
        side_effect=Exception("No user"),
    )
    @patch("mvmctl.utils.auditlog.os.getuid", return_value=1234)
    def test_falls_back_to_uid(
        self, mock_getuid, mock_getuser, mock_get_logger
    ):
        """Should use UID when getpass.getuser fails."""
        mock_logger = MagicMock()
        mock_get_logger.return_value = mock_logger

        AuditLog.log("test.op")

        msg = mock_logger.info.call_args[0][0]
        assert "user=1234" in msg


class TestAuditLogGetLogger:
    """Tests for AuditLog._get_logger()."""

    def test_returns_same_logger_instance(self, tmp_path):
        """Should be a singleton."""
        logger1 = AuditLog._get_logger()
        logger2 = AuditLog._get_logger()
        assert logger1 is logger2

    def test_creates_file_handler(self, tmp_path):
        """Should create a file handler."""
        # Reset singleton for clean test
        AuditLog._logger = None
        logger_obj = AuditLog._get_logger()
        assert len(logger_obj.handlers) > 0
        assert isinstance(logger_obj.handlers[0], logging.FileHandler)

    def test_sets_correct_level_and_propagation(self, tmp_path):
        """Should have correct level and not propagate."""
        AuditLog._logger = None
        logger_obj = AuditLog._get_logger()
        assert logger_obj.level == logging.INFO
        assert logger_obj.propagate is False

    def test_uses_null_handler_on_os_error(self):
        """Should use NullHandler when FileHandler creation fails."""
        AuditLog._logger = None
        with patch(
            "logging.FileHandler", side_effect=OSError("Permission denied")
        ):
            logger_obj = AuditLog._get_logger()
            assert any(
                isinstance(h, logging.NullHandler) for h in logger_obj.handlers
            )
