"""Tests for LogOperation — log streaming orchestration."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.api.inputs._logs_input import LogInput
from mvmctl.api.logs_operations import LogOperation
from mvmctl.exceptions import VMNotFoundError


class TestLogOperation:
    """Tests for LogOperation.stream()."""

    def _setup_mocks(self, mocker, follow: bool) -> tuple[MagicMock, MagicMock]:
        """Set up common mocks and return (mock_controller, mock_resolved)."""
        mocker.patch("mvmctl.api.logs_operations.Database")
        mocker.patch("mvmctl.api.logs_operations.VMRepository")

        mock_controller = MagicMock()
        mocker.patch(
            "mvmctl.api.logs_operations.LogController",
            return_value=mock_controller,
        )

        mock_resolved = MagicMock()
        mock_resolved.follow = follow
        mock_request = MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.logs_operations.LogRequest",
            return_value=mock_request,
        )
        return mock_controller, mock_resolved

    def test_stream_follow_calls_controller_follow(self, mocker):
        """stream() with follow=True delegates to controller.follow()."""
        mock_controller, mock_resolved = self._setup_mocks(mocker, follow=True)
        mock_controller.follow.return_value = iter(["line1", "line2"])

        result = list(LogOperation.stream(LogInput(identifier="test-vm")))

        assert result == ["line1", "line2"]
        mock_controller.follow.assert_called_once_with(
            mock_resolved.log_type,
            log_filename=mock_resolved.log_filename,
            serial_output_filename=mock_resolved.serial_output_filename,
        )

    def test_stream_show_calls_controller_show(self, mocker):
        """stream() with follow=False delegates to controller.show()."""
        mock_controller, mock_resolved = self._setup_mocks(mocker, follow=False)
        mock_controller.show.return_value = iter(["lineA"])

        result = list(LogOperation.stream(LogInput(identifier="test-vm")))

        assert result == ["lineA"]
        mock_controller.show.assert_called_once_with(
            mock_resolved.log_type,
            mock_resolved.lines,
            log_filename=mock_resolved.log_filename,
            serial_output_filename=mock_resolved.serial_output_filename,
        )

    def test_stream_empty_follow(self, mocker):
        """stream() with follow=True returns empty list on no output."""
        mock_controller, _ = self._setup_mocks(mocker, follow=True)
        mock_controller.follow.return_value = iter([])

        result = list(LogOperation.stream(LogInput(identifier="test-vm")))
        assert result == []

    def test_stream_empty_show(self, mocker):
        """stream() with follow=False returns empty list on no output."""
        mock_controller, _ = self._setup_mocks(mocker, follow=False)
        mock_controller.show.return_value = iter([])

        result = list(LogOperation.stream(LogInput(identifier="test-vm")))
        assert result == []

    def test_stream_propagates_resolve_error(self, mocker):
        """stream() propagates VMNotFoundError from LogRequest.resolve()."""
        mocker.patch("mvmctl.api.logs_operations.Database")
        mock_request = MagicMock()
        mock_request.resolve.side_effect = VMNotFoundError("VM not found")
        mocker.patch(
            "mvmctl.api.logs_operations.LogRequest",
            return_value=mock_request,
        )

        with pytest.raises(VMNotFoundError, match="VM not found"):
            list(LogOperation.stream(LogInput(identifier="nonexistent")))
