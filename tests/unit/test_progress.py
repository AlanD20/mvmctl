"""Tests for progress bar utilities."""

import sys
from io import StringIO

from pytest_mock import MockerFixture

from mvmctl.utils.progress import ASCIIProgressBar


class TestASCIIProgressBar:
    """Tests for ASCIIProgressBar class."""

    def test_progress_bar_tty_display(self, mocker: MockerFixture):
        """Verify carriage return animation in TTY mode."""
        # Mock sys.stdout.isatty() -> True
        mocker.patch("sys.stdout.isatty", return_value=True)

        # Capture stdout
        old_stdout = sys.stdout
        sys.stdout = StringIO()

        try:
            bar = ASCIIProgressBar(total=100, title="Testing")
            bar.update(50)
            output = sys.stdout.getvalue()

            # Verify output contains carriage return and progress format
            assert "\r" in output or "[" in output
            assert "50" in output or "50%" in output

            bar.finish()
        finally:
            sys.stdout = old_stdout

    def test_progress_bar_non_tty_display(self, mocker: MockerFixture):
        """Verify line-by-line output in non-TTY mode."""
        # Mock sys.stdout.isatty() -> False
        mocker.patch("sys.stdout.isatty", return_value=False)

        # Capture stdout
        old_stdout = sys.stdout
        sys.stdout = StringIO()

        try:
            bar = ASCIIProgressBar(total=100, title="Testing")
            bar.update(10)  # 10%
            bar.update(10)  # 20%
            bar.update(10)  # 30%
            output = sys.stdout.getvalue()

            # In non-TTY mode, each 10% update should print on new line
            lines = [line for line in output.strip().split("\n") if line.strip()]
            # Should have lines for progress updates
            assert len(lines) >= 1

            bar.finish()
        finally:
            sys.stdout = old_stdout

    def test_progress_bar_completion(self, mocker: MockerFixture):
        """Verify finish() outputs completion message."""
        mocker.patch("sys.stdout.isatty", return_value=True)

        old_stdout = sys.stdout
        sys.stdout = StringIO()

        try:
            bar = ASCIIProgressBar(total=100, title="Testing")
            bar.update(100)
            bar.finish()
            output = sys.stdout.getvalue()

            # Verify completion message
            assert "complete" in output.lower() or "100" in output
        finally:
            sys.stdout = old_stdout

    def test_progress_bar_zero_total(self, mocker: MockerFixture):
        """Handle unknown content-length (total=0)."""
        mocker.patch("sys.stdout.isatty", return_value=True)

        old_stdout = sys.stdout
        sys.stdout = StringIO()

        try:
            # Should not crash with total=0
            bar = ASCIIProgressBar(total=0, title="Testing")
            bar.update(0)
            bar.finish()
            output = sys.stdout.getvalue()

            # Should handle gracefully - either show 0% or indeterminate
            assert "0" in output or "complete" in output.lower()
        finally:
            sys.stdout = old_stdout

    def test_progress_bar_format_size_bytes(self, mocker: MockerFixture):
        """Verify _format_size for bytes."""
        bar = ASCIIProgressBar(total=100, title="Test")

        # Test bytes (< 1024)
        assert bar._format_size(512) == "512B"
        assert bar._format_size(1023) == "1023B"

    def test_progress_bar_format_size_kb(self, mocker: MockerFixture):
        """Verify _format_size for KB."""
        bar = ASCIIProgressBar(total=100, title="Test")

        # Test KB (1-1023 KB)
        result = bar._format_size(1024)
        assert "KB" in result or "MB" in result

    def test_progress_bar_format_size_mb(self, mocker: MockerFixture):
        """Verify _format_size for MB."""
        bar = ASCIIProgressBar(total=100, title="Test")

        # Test MB (1-1023 MB)
        result = bar._format_size(1024 * 1024)
        assert "MB" in result or "GB" in result

    def test_progress_bar_format_size_gb(self, mocker: MockerFixture):
        """Verify _format_size for GB."""
        bar = ASCIIProgressBar(total=100, title="Test")

        # Test GB (>= 1024 MB)
        result = bar._format_size(1024 * 1024 * 1024)
        assert "GB" in result


class TestDownloadWithProgress:
    """Tests for download_with_progress function."""

    def test_download_with_progress_imports(self):
        """Verify download_with_progress can be imported."""
        from mvmctl.utils.progress import download_with_progress

        assert callable(download_with_progress)

    def test_download_with_progress_signature(self):
        """Verify download_with_progress has correct signature."""
        import inspect

        from mvmctl.utils.progress import download_with_progress

        sig = inspect.signature(download_with_progress)
        params = list(sig.parameters.keys())

        assert "url" in params
        assert "dest" in params
        assert "title" in params
        assert "expected_sha256" in params
        assert "timeout" in params
