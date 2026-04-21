"""Tests for audit logging utility."""

import logging
from unittest.mock import Mock, patch

from mvmctl.utils.audit import _audit_logger, _get_audit_log_path, log_audit


class TestGetAuditLogPath:
    """Tests for _get_audit_log_path function."""

    def test_returns_cache_dir_path(self, tmp_path):
        """Test that audit log path is in cache directory."""
        with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
            result = _get_audit_log_path()
            assert result == tmp_path / "audit.log"


class TestAuditLogger:
    """Tests for _audit_logger function."""

    def test_returns_same_logger_instance(self, tmp_path):
        """Test that logger is a singleton."""
        with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
            # Clear any existing handlers
            logger = logging.getLogger("mvmctl.audit")
            logger.handlers = []

            logger1 = _audit_logger()
            logger2 = _audit_logger()
            assert logger1 is logger2

    def test_creates_file_handler(self, tmp_path):
        """Test that logger creates a file handler."""
        with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
            # Clear any existing handlers
            logger = logging.getLogger("mvmctl.audit")
            logger.handlers = []

            audit_logger = _audit_logger()
            assert len(audit_logger.handlers) > 0
            assert isinstance(audit_logger.handlers[0], logging.FileHandler)

    def test_creates_directory_if_not_exists(self, tmp_path):
        """Test that parent directory is created if it doesn't exist."""
        cache_dir = tmp_path / "nonexistent"
        with patch("mvmctl.utils.audit.get_cache_dir", return_value=cache_dir):
            # Clear any existing handlers
            logger = logging.getLogger("mvmctl.audit")
            logger.handlers = []

            _audit_logger()
            assert cache_dir.exists()

    def test_uses_null_handler_on_os_error(self, tmp_path):
        """Test that NullHandler is used when file creation fails."""
        with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
            # Clear any existing handlers
            logger = logging.getLogger("mvmctl.audit")
            logger.handlers = []

            with patch("logging.FileHandler", side_effect=OSError("Permission denied")):
                audit_logger = _audit_logger()
                # Should have a NullHandler
                assert any(isinstance(h, logging.NullHandler) for h in audit_logger.handlers)

    def test_sets_correct_level_and_propagation(self, tmp_path):
        """Test that logger has correct level and doesn't propagate."""
        with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
            # Clear any existing handlers
            logger = logging.getLogger("mvmctl.audit")
            logger.handlers = []

            audit_logger = _audit_logger()
            assert audit_logger.level == logging.INFO
            assert audit_logger.propagate is False


class TestLogAudit:
    """Tests for log_audit function."""

    @patch("mvmctl.utils.audit._audit_logger")
    @patch("getpass.getuser", return_value="testuser")
    def test_logs_basic_operation(self, mock_getuser, mock_logger, tmp_path):
        """Test basic audit logging."""
        mock_logger_instance = Mock()
        mock_logger.return_value = mock_logger_instance

        with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
            log_audit("vm.create", "created test-vm")

        mock_logger_instance.info.assert_called_once()
        logged_message = mock_logger_instance.info.call_args[0][0]
        assert "user=testuser" in logged_message
        assert "op=vm.create" in logged_message
        assert "detail='created test-vm'" in logged_message

    @patch("mvmctl.utils.audit._audit_logger")
    @patch("getpass.getuser", return_value="testuser")
    def test_logs_without_detail(self, mock_getuser, mock_logger, tmp_path):
        """Test audit logging without detail."""
        mock_logger_instance = Mock()
        mock_logger.return_value = mock_logger_instance

        with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
            log_audit("host.init")

        mock_logger_instance.info.assert_called_once()
        logged_message = mock_logger_instance.info.call_args[0][0]
        assert "user=testuser" in logged_message
        assert "op=host.init" in logged_message
        assert "detail=" not in logged_message

    @patch("mvmctl.utils.audit._audit_logger")
    def test_uses_uid_when_getuser_fails(self, mock_logger, tmp_path):
        """Test that UID is used when getpass.getuser fails."""
        mock_logger_instance = Mock()
        mock_logger.return_value = mock_logger_instance

        with patch("getpass.getuser", side_effect=Exception("No user")):
            with patch("os.getuid", return_value=1234):
                with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
                    log_audit("test.op")

        logged_message = mock_logger_instance.info.call_args[0][0]
        assert "user=1234" in logged_message

    @patch("mvmctl.utils.audit._audit_logger")
    def test_includes_timestamp(self, mock_logger, tmp_path):
        """Test that timestamp is included in log."""
        mock_logger_instance = Mock()
        mock_logger.return_value = mock_logger_instance

        with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
            log_audit("test.op")

        logged_message = mock_logger_instance.info.call_args[0][0]
        # Should have UTC timestamp in ISO format
        assert "[" in logged_message and "]" in logged_message
        assert "Z" in logged_message

    @patch("mvmctl.utils.audit._audit_logger")
    def test_escapes_special_characters_in_detail(self, mock_logger, tmp_path):
        """Test that special characters in detail are handled."""
        mock_logger_instance = Mock()
        mock_logger.return_value = mock_logger_instance

        with patch("mvmctl.utils.audit.get_cache_dir", return_value=tmp_path):
            log_audit("test.op", "detail with 'quotes' and \"double quotes\"")

        logged_message = mock_logger_instance.info.call_args[0][0]
        assert "detail=" in logged_message
