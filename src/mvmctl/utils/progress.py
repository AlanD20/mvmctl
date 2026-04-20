"""ASCII text-based progress bar utilities for downloads.

This module provides a simple ASCII progress bar implementation for TTY and
non-TTY environments. It avoids Rich Progress API for lightweight operation
in CI/script environments.
"""

import hashlib
import os
import shutil
import sys
import tempfile
from pathlib import Path
from typing import Optional
from urllib.error import URLError
from urllib.request import Request

from mvmctl.constants import (
    CONST_DOWNLOAD_CHUNK_SIZE,
    CONST_DOWNLOAD_MAX_RETRIES,
    CONST_DOWNLOAD_RETRY_BACKOFF,
    CONST_DOWNLOAD_RETRY_DELAY,
    HTTP_USER_AGENT,
)
from mvmctl.exceptions import ChecksumMismatchError, MVMError
from mvmctl.utils import http
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.http import _with_retry


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


@_with_retry(
    max_retries=CONST_DOWNLOAD_MAX_RETRIES,
    retry_delay=CONST_DOWNLOAD_RETRY_DELAY,
    backoff=CONST_DOWNLOAD_RETRY_BACKOFF,
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
    dest.parent.mkdir(parents=True, exist_ok=True)

    sha256_hash = hashlib.sha256() if expected_sha256 else None
    temp_path: Optional[Path] = None

    try:
        temp_fd, temp_str = tempfile.mkstemp(
            dir=CacheUtils.get_temp_dir(), prefix=f"{dest.stem}-", suffix=".tmp"
        )
        os.close(temp_fd)
        temp_path = Path(temp_str)

        progress: Optional[ASCIIProgressBar] = None
        req = Request(url, headers={"User-Agent": HTTP_USER_AGENT})
        with http.urlopen(req, timeout=timeout) as response:
            total_size = (
                int(cl) if (cl := response.headers.get("Content-Length")) else 0
            )
            progress = ASCIIProgressBar(total=total_size, title=title)
            with temp_path.open("wb") as f:
                while True:
                    chunk = response.read(CONST_DOWNLOAD_CHUNK_SIZE)
                    if not chunk:
                        break
                    f.write(chunk)
                    progress.update(len(chunk))
                    if sha256_hash:
                        sha256_hash.update(chunk)

        if progress:
            progress.finish()

        if expected_sha256 and sha256_hash:
            actual_sha256 = sha256_hash.hexdigest()
            if actual_sha256.lower() != expected_sha256.lower():
                temp_path.unlink(missing_ok=True)
                raise ChecksumMismatchError(
                    f"Checksum mismatch! Expected {expected_sha256}, got {actual_sha256}"
                )

        shutil.move(str(temp_path), str(dest))
        temp_path = None
        return True

    except URLError as e:
        raise MVMError(f"Download failed: {e}") from e
    except OSError as e:
        if e.errno == 122:
            raise MVMError(
                "No storage available: insufficient space in /tmp. "
                "Clear temporary files or increase disk space to continue."
            ) from e
        raise MVMError(f"I/O error: {e}") from e
    finally:
        if temp_path is not None:
            temp_path.unlink(missing_ok=True)
