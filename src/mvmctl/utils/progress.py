"""ASCII text-based progress bar utilities for downloads.

This module provides a simple ASCII progress bar implementation for TTY and
non-TTY environments. It avoids Rich Progress API for lightweight operation
in CI/script environments.
"""

import hashlib
import sys
from pathlib import Path
from typing import Optional
from urllib.error import URLError
from urllib.request import Request

from mvmctl.constants import CONST_DOWNLOAD_CHUNK_SIZE, HTTP_USER_AGENT
from mvmctl.exceptions import ChecksumMismatchError, MVMError
from mvmctl.utils import http
from mvmctl.utils.http import (
    DEFAULT_MAX_RETRIES,
    DEFAULT_RETRY_BACKOFF,
    DEFAULT_RETRY_DELAY,
    _with_retry,
)


class ASCIIProgressBar:
    """Simple ASCII progress bar for TTY and non-TTY environments.

    Displays progress as: [####      ] 45% (4.2MB/10MB)

    In TTY mode: Uses carriage return for smooth animation
    In non-TTY mode: Prints progress every 10% on new lines
    """

    def __init__(self, total: int, width: int = 40, title: str = "Downloading") -> None:
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
        self._is_tty = sys.stdout.isatty()
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
        """Display progress bar.

        In TTY mode: Uses carriage return for animation
        In non-TTY mode: Prints every 10% on new lines
        """
        if self.total == 0:
            percent = 0
        else:
            percent = min(100, int(100 * self.current / self.total))

        filled = int(self.width * percent / 100)
        bar = "#" * filled + " " * (self.width - filled)

        if self._is_tty:
            # TTY: Use carriage return for animation
            line = f"\r{self.title} [{bar}] {percent}%"
            if self.total > 0:
                current_str = self._format_size(self.current)
                total_str = self._format_size(self.total)
                line += f" ({current_str}/{total_str})"
            # Clear previous line and write new one
            sys.stdout.write("\r" + " " * self._last_line_length + "\r")
            sys.stdout.write(line)
            sys.stdout.flush()
            self._last_line_length = len(line)
        else:
            # Non-TTY: Simple line-by-line progress (every 10% or on completion)
            if percent % 10 == 0 and percent != self._last_percent:
                line = f"{self.title} [{bar}] {percent}%"
                if self.total > 0:
                    current_str = self._format_size(self.current)
                    total_str = self._format_size(self.total)
                    line += f" ({current_str}/{total_str})"
                print(line)
                self._last_percent = percent

    def finish(self) -> None:
        """Finish progress display.

        In TTY mode: Moves to new line
        In non-TTY mode: Prints completion message
        """
        if self._is_tty:
            sys.stdout.write("\n")
            sys.stdout.flush()
        print(f"{self.title} complete.")


@_with_retry(
    max_retries=DEFAULT_MAX_RETRIES, retry_delay=DEFAULT_RETRY_DELAY, backoff=DEFAULT_RETRY_BACKOFF
)
def download_with_progress(
    url: str,
    dest: Path,
    title: str = "Downloading",
    expected_sha256: Optional[str] = None,
    timeout: int = 300,
    allow_missing_checksum: bool = False,
    silent_missing_checksum: bool = False,
) -> bool:
    """Download file with ASCII progress bar.

    Args:
        url: URL to download
        dest: Destination path
        title: Progress bar title
        expected_sha256: Optional SHA256 for verification
        timeout: Download timeout in seconds
        allow_missing_checksum: If True, skip verification when sha256 is None
        silent_missing_checksum: If True, skip warnings when checksum is missing (compatibility)

    Returns:
        True if successful

    Raises:
        MVMError: If download fails
        ChecksumMismatchError: If SHA256 verification fails
    """
    dest.parent.mkdir(parents=True, exist_ok=True)

    # Get file size first via HEAD request
    total_size = 0
    try:
        req = Request(
            url,
            headers={"User-Agent": HTTP_USER_AGENT},
            method="HEAD",
        )
        with http.urlopen(req, timeout=30) as response:
            content_length = response.headers.get("Content-Length")
            if content_length:
                total_size = int(content_length)
    except Exception:
        pass  # Continue without size info if HEAD fails

    progress = ASCIIProgressBar(total=total_size, title=title)
    sha256_hash = hashlib.sha256() if expected_sha256 else None

    try:
        req = Request(url, headers={"User-Agent": HTTP_USER_AGENT})
        with http.urlopen(req, timeout=timeout) as response:
            with open(dest, "wb") as f:
                while True:
                    chunk = response.read(CONST_DOWNLOAD_CHUNK_SIZE)
                    if not chunk:
                        break
                    f.write(chunk)
                    progress.update(len(chunk))
                    if sha256_hash:
                        sha256_hash.update(chunk)

        progress.finish()

        # Verify checksum if provided
        if expected_sha256 and sha256_hash:
            actual = sha256_hash.hexdigest()
            if actual.lower() != expected_sha256.lower():
                dest.unlink(missing_ok=True)
                raise ChecksumMismatchError(
                    f"Checksum mismatch! Expected {expected_sha256}, got {actual}"
                )

        return True

    except URLError as e:
        raise MVMError(f"Download failed: {e}") from e
