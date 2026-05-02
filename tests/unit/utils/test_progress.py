"""Tests for utils/progress.py — ASCII progress bar and spinner."""

from __future__ import annotations

import sys
from io import StringIO

from mvmctl.utils.progress import ASCIIProgressBar, Spinner


class TestASCIIProgressBar:
    """Tests for ASCIIProgressBar class."""

    def test_progress_bar_tty_display(self, mocker):
        """Verify carriage return animation in TTY mode."""
        mocker.patch("sys.stdout.isatty", return_value=True)

        old_stdout = sys.stdout
        sys.stdout = StringIO()

        try:
            bar = ASCIIProgressBar(total=100, title="Testing")
            bar.update(50)
            output = sys.stdout.getvalue()

            assert "\r" in output or "[" in output
            assert "50" in output or "50%" in output

            bar.finish()
        finally:
            sys.stdout = old_stdout

    def test_progress_bar_non_tty_display(self, mocker):
        """Verify line-by-line output in non-TTY mode."""
        mocker.patch("sys.stdout.isatty", return_value=False)

        old_stdout = sys.stdout
        sys.stdout = StringIO()

        try:
            bar = ASCIIProgressBar(total=100, title="Testing")
            bar.update(10)
            bar.update(10)
            bar.update(10)
            output = sys.stdout.getvalue()

            lines = [
                line for line in output.strip().split("\n") if line.strip()
            ]
            assert len(lines) >= 1

            bar.finish()
        finally:
            sys.stdout = old_stdout

    def test_progress_bar_completion(self, mocker):
        """Verify finish() outputs completion message."""
        mocker.patch("sys.stdout.isatty", return_value=True)

        old_stdout = sys.stdout
        sys.stdout = StringIO()

        try:
            bar = ASCIIProgressBar(total=100, title="Testing")
            bar.update(100)
            bar.finish()
            output = sys.stdout.getvalue()

            assert "complete" in output.lower() or "100" in output
        finally:
            sys.stdout = old_stdout

    def test_progress_bar_zero_total(self, mocker):
        """Handle unknown content-length (total=0)."""
        mocker.patch("sys.stdout.isatty", return_value=True)

        old_stdout = sys.stdout
        sys.stdout = StringIO()

        try:
            bar = ASCIIProgressBar(total=0, title="Testing")
            bar.update(0)
            bar.finish()
            output = sys.stdout.getvalue()

            assert "0" in output or "complete" in output.lower()
        finally:
            sys.stdout = old_stdout

    def test_format_size_bytes(self, mocker):
        """Verify _format_size for bytes."""
        mocker.patch("sys.stdout.isatty", return_value=True)
        bar = ASCIIProgressBar(total=100, title="Test")

        assert bar._format_size(512) == "512B"
        assert bar._format_size(1023) == "1023B"

    def test_format_size_kb(self, mocker):
        """Verify _format_size for KB."""
        mocker.patch("sys.stdout.isatty", return_value=True)
        bar = ASCIIProgressBar(total=100, title="Test")

        result = bar._format_size(1024)
        assert result == "1.0KB"

    def test_format_size_mb(self, mocker):
        """Verify _format_size for MB."""
        mocker.patch("sys.stdout.isatty", return_value=True)
        bar = ASCIIProgressBar(total=100, title="Test")

        result = bar._format_size(1024 * 1024)
        assert result == "1.0MB"

    def test_format_size_gb(self, mocker):
        """Verify _format_size for GB."""
        mocker.patch("sys.stdout.isatty", return_value=True)
        bar = ASCIIProgressBar(total=100, title="Test")

        result = bar._format_size(1024 * 1024 * 1024)
        assert result == "1.0GB"


class TestSpinner:
    """Tests for Spinner class."""

    def test_spinner_start_stop(self):
        spinner = Spinner(message="Testing")
        spinner.start()
        spinner.stop(done_message="Done")
        assert spinner._stop_event.is_set()

    def test_spinner_context_manager(self):
        with Spinner(message="Testing") as spinner:
            assert spinner._thread is not None
            assert spinner._thread.is_alive()
        assert spinner._stop_event.is_set()
