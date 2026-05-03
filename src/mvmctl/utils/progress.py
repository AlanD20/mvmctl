"""
ASCII text-based progress bar utilities for downloads.

This module provides a simple ASCII progress bar implementation for TTY and
non-TTY environments. It avoids Rich Progress API for lightweight operation
in CI/script environments.
"""

import logging
import os
import shutil
import sys
import threading
import time

logger = logging.getLogger(__name__)


class ASCIIProgressBar:
    """
    ASCII progress bar that works in both TTY and non-TTY environments.

    Displays progress as: [####      ] 45% (4.2MB/10MB)

    In TTY mode, updates in-place on a single line using carriage return + ANSI clear.
    In non-TTY mode (piped output, CI logs), prints each update on a new line.
    """

    def __init__(
        self, total: int, width: int = 40, title: str = "Downloading"
    ) -> None:
        """
        Initialize the progress bar.

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
        # Use os.isatty(fd) instead of sys.stdout.isatty() — Rich's Console
        # proxy overrides the Python-level isatty() even when the underlying
        # file descriptor IS a TTY, which breaks \r carriage-return updates.
        try:
            self._is_tty = os.isatty(sys.stdout.fileno())
        except (OSError, ValueError):
            self._is_tty = False

    def update(self, n: int) -> None:
        """
        Update progress by n bytes.

        Args:
            n: Number of bytes downloaded since last update

        """
        self.current += n
        self._display()

    def _format_size(self, size_bytes: int) -> str:
        """
        Format bytes to human readable (B, KB, MB, GB).

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

        terminator = "\r\033[K" if self._is_tty else "\n"
        output = f"{terminator}{line}"

        if self._is_tty:
            # Bypass Python-level stdout wrappers (e.g. Rich's Console which
            # intercepts ANSI escapes and \r). os.write(fd, …) goes directly
            # to the kernel and cannot be intercepted.
            os.write(1, output.encode())
        else:
            # Respect sys.stdout redirection (tests, CI, pipes)
            sys.stdout.write(output)
            sys.stdout.flush()

        self._last_line_length = len(line)
        self._last_percent = percent

    def finish(self) -> None:
        """Finish progress display."""
        if self._is_tty:
            os.write(1, b"\r\033[K")
        print(f"{self.title} complete.")


class Spinner:
    """
    Threaded ASCII spinner for indeterminate progress.

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

    def __exit__(
        self, exc_type: object, exc_val: object, exc_tb: object
    ) -> None:
        self.stop()
