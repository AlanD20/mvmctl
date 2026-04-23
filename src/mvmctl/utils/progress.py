"""ASCII text-based progress bar utilities for downloads.

This module provides a simple ASCII progress bar implementation for TTY and
non-TTY environments. It avoids Rich Progress API for lightweight operation
in CI/script environments.
"""

import logging
import shutil
import sys
import threading
import time
from pathlib import Path
from typing import Optional

logger = logging.getLogger(__name__)


class ASCIIProgressBar:
    """Simple ASCII progress bar that updates on a single line.

    Displays progress as: [####      ] 45% (4.2MB/10MB)

    Uses carriage return + ANSI clear to update the same line.
    Works in both TTY and non-TTY environments.
    """

    def __init__(
        self, total: int, width: int = 40, title: str = "Downloading"
    ) -> None:
        """Initialize the progress bar.

        Args:
            total: Total bytes to download (0 if unknown)
            width: Width of the progress bar in characters
            title: Title to display before the progress bar
        """
        self.total = total
        self.width = width
        self.title = title
        self.current = 0
        self._last_line_length = 0
        self._last_percent = -1

    def update(self, n: int) -> None:
        """Update progress by n bytes.

        Args:
            n: Number of bytes downloaded since last update
        """
        self.current += n
        self._display()

    def _format_size(self, size_bytes: int) -> str:
        """Format bytes to human readable (B, KB, MB, GB).

        Args:
            size_bytes: Size in bytes

        Returns:
            Human-readable size string (e.g., "4.2MB")
        """
        if size_bytes < 1024:
            return f"{size_bytes}B"
        size_kb = size_bytes / 1024
        if size_kb < 1024:
            return f"{size_kb:.1f}KB"
        size_mb = size_kb / 1024
        if size_mb < 1024:
            return f"{size_mb:.1f}MB"
        size_gb = size_mb / 1024
        return f"{size_gb:.1f}GB"

    def _display(self) -> None:
        if self.total == 0:
            percent = 0
        else:
            percent = min(100, int(100 * self.current / self.total))

        if percent == self._last_percent:
            return

        term_width = shutil.get_terminal_size((80, 20)).columns
        filled = int(self.width * percent / 100)
        bar = "#" * filled + " " * (self.width - filled)
        line = f"{self.title} [{bar}] {percent}%"
        if self.total > 0:
            line += f" ({self._format_size(self.current)}/{self._format_size(self.total)})"

        if len(line) > term_width - 1:
            line = line[: term_width - 1]

        sys.stdout.write(f"\r\033[K{line}")
        sys.stdout.flush()
        self._last_line_length = len(line)
        self._last_percent = percent

    def finish(self) -> None:
        """Finish progress display."""
        sys.stdout.write("\r\033[K")
        sys.stdout.flush()
        print(f"{self.title} complete.")


class Spinner:
    """Threaded ASCII spinner for indeterminate progress.

    Displays a rotating character with a message on a single line.
    Runs in a background thread so the main thread can do the actual work.
    """

    _frames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"]

    def __init__(self, message: str = "Processing") -> None:
        self.message = message
        self._stop_event = threading.Event()
        self._thread: threading.Thread | None = None
        self._last_line = ""

    def _run(self) -> None:
        idx = 0
        while not self._stop_event.is_set():
            frame = self._frames[idx % len(self._frames)]
            line = f"{frame} {self.message}..."
            sys.stdout.write(f"\r\033[K{line}")
            sys.stdout.flush()
            self._last_line = line
            idx += 1
            time.sleep(0.1)

    def start(self) -> None:
        """Start the spinner in a background thread."""
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()

    def stop(self, done_message: str | None = None) -> None:
        """Stop the spinner and optionally print a completion message."""
        self._stop_event.set()
        if self._thread is not None:
            self._thread.join(timeout=0.2)
        sys.stdout.write("\r\033[K")
        sys.stdout.flush()
        if done_message:
            print(done_message)

    def __enter__(self) -> "Spinner":
        self.start()
        return self

    def __exit__(self, exc_type: object, exc_val: object, exc_tb: object) -> None:
        self.stop()


def download_with_progress(
    url: str,
    dest: Path,
    title: str = "Downloading",
    expected_sha256: Optional[str] = None,
    timeout: int = 300,
    allow_missing_checksum: bool = False,
    silent_missing_checksum: bool = False,
) -> bool:
    """Download a file with an ASCII progress bar.

    .. deprecated::
        Use :meth:`mvmctl.utils.http.HttpDownload.download_file` instead.
    """
    from mvmctl.utils.http import HttpDownload

    return HttpDownload.download_file(
        url=url,
        dest=dest,
        expected_sha256=expected_sha256,
        timeout=timeout,
        progress_bar=True,
        allow_missing_checksum=allow_missing_checksum,
        silent_missing_checksum=silent_missing_checksum,
        title=title,
    )
