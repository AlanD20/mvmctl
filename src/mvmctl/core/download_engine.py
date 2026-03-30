"""Unified download engine with temp staging, resume, and safe cleanup."""

import hashlib
import os
import shutil
import tempfile
import time
from pathlib import Path
from typing import Optional
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from mvmctl.constants import (
    CONST_DOWNLOAD_CHUNK_SIZE,
    CONST_HTTP_STATUS_OK,
    CONST_HTTP_STATUS_PARTIAL_CONTENT,
    CONST_HTTP_TIMEOUT_SECONDS,
    FALLBACK_TEMP_DIR,
    HTTP_USER_AGENT,
)
from mvmctl.exceptions import ChecksumMismatchError, DownloadError
from mvmctl.utils.progress import ASCIIProgressBar

# Download retry configuration
DEFAULT_MAX_RETRIES = 3
DEFAULT_RETRY_DELAY = 1.0  # seconds
DEFAULT_RETRY_BACKOFF = 2.0  # exponential backoff multiplier


class DownloadEngine:
    """Unified download engine for all asset types.

    Features:
    - Temp staging under FALLBACK_TEMP_DIR (or MVM_TEMP_DIR override)
    - Resumable partial downloads via HTTP Range
    - Safe cleanup on failure via context manager pattern
    - Single-line ASCII progress for all fetches
    """

    def __init__(self, temp_dir: Optional[Path] = None) -> None:
        """Initialize the download engine.

        Args:
            temp_dir: Override temp directory. Defaults to MVM_TEMP_DIR or FALLBACK_TEMP_DIR.
        """
        self.temp_dir = temp_dir or Path(os.environ.get("MVM_TEMP_DIR", FALLBACK_TEMP_DIR))
        self.temp_dir.mkdir(parents=True, exist_ok=True)

    def download(
        self,
        url: str,
        dest: Path,
        expected_sha256: Optional[str] = None,
        resume: bool = True,
        progress: bool = True,
        timeout: int = CONST_HTTP_TIMEOUT_SECONDS,
        max_retries: int = DEFAULT_MAX_RETRIES,
    ) -> Path:
        """Download with temp staging, resume, atomic move, and retry logic.

        Args:
            url: Source URL
            dest: Final destination path
            expected_sha256: Optional SHA256 to verify during download
            resume: Allow resuming partial downloads
            progress: Show ASCII progress bar
            timeout: Download timeout in seconds
            max_retries: Number of retry attempts on transient failures

        Returns:
            Path to downloaded file (dest)

        Raises:
            DownloadError: On failure (with cleanup performed)
            ChecksumMismatchError: If SHA256 verification fails
        """
        delay = DEFAULT_RETRY_DELAY
        last_exception: Optional[Exception] = None

        for attempt in range(max_retries + 1):
            try:
                return self._download_once(url, dest, expected_sha256, resume, progress, timeout)
            except (URLError, HTTPError, IOError, OSError) as e:
                last_exception = e
                if attempt < max_retries:
                    print(f"Download attempt {attempt + 1}/{max_retries + 1} failed: {e}")
                    print(f"Retrying in {delay:.1f}s...")
                    time.sleep(delay)
                    delay *= DEFAULT_RETRY_BACKOFF
                else:
                    raise DownloadError(
                        f"Download failed after {max_retries + 1} attempts: {e}"
                    ) from e

        raise last_exception if last_exception else DownloadError("Download failed")

    def _download_once(
        self,
        url: str,
        dest: Path,
        expected_sha256: Optional[str] = None,
        resume: bool = True,
        progress: bool = True,
        timeout: int = CONST_HTTP_TIMEOUT_SECONDS,
    ) -> Path:
        """Internal download method without retry logic."""
        dest.parent.mkdir(parents=True, exist_ok=True)

        # Create temp file with .part suffix for staging
        part_file = self.temp_dir / f"{dest.name}.part"

        # Check for existing partial download for resume
        resume_byte_pos = 0
        if resume and part_file.exists():
            resume_byte_pos = part_file.stat().st_size

        # Get file size first via HEAD request
        total_size = 0
        try:
            req = Request(
                url,
                headers={"User-Agent": HTTP_USER_AGENT},
                method="HEAD",
            )
            with urlopen(req, timeout=30) as response:
                content_length = response.headers.get("Content-Length")
                if content_length:
                    total_size = int(content_length)
        except Exception:
            pass  # Continue without size info if HEAD fails

        progress_bar = ASCIIProgressBar(total=total_size, title=f"Fetching {dest.name}")
        sha256_hash = hashlib.sha256() if expected_sha256 else None

        temp_path: Optional[Path] = None
        try:
            # Create a temp file in the staging directory
            temp_fd, temp_path_str = tempfile.mkstemp(
                dir=part_file.parent, prefix=f".{dest.name}.", suffix=".tmp"
            )
            os.close(temp_fd)
            temp_path = Path(temp_path_str)

            # Build request with Range header if resuming
            headers = {"User-Agent": HTTP_USER_AGENT}
            if resume_byte_pos > 0:
                headers["Range"] = f"bytes={resume_byte_pos}-"

            req = Request(url, headers=headers)

            with urlopen(req, timeout=timeout) as response:
                is_resume = (
                    resume_byte_pos > 0 and response.status == CONST_HTTP_STATUS_PARTIAL_CONTENT
                )

                if response.status == CONST_HTTP_STATUS_OK and resume_byte_pos > 0 and progress:
                    # Server doesn't support resume, restart
                    resume_byte_pos = 0
                    part_file.unlink(missing_ok=True)

                # Copy existing bytes if resuming
                if is_resume and part_file.exists() and resume_byte_pos > 0:
                    shutil.copy2(part_file, temp_path)
                    if sha256_hash:
                        with temp_path.open("rb") as f:
                            while True:
                                chunk = f.read(CONST_DOWNLOAD_CHUNK_SIZE)
                                if not chunk:
                                    break
                                sha256_hash.update(chunk)

                # Stream download
                with temp_path.open("ab" if is_resume else "wb") as f:
                    while True:
                        chunk = response.read(CONST_DOWNLOAD_CHUNK_SIZE)
                        if not chunk:
                            break
                        f.write(chunk)
                        if sha256_hash:
                            sha256_hash.update(chunk)
                        if progress:
                            progress_bar.update(len(chunk))

            if progress:
                progress_bar.finish()

            # Verify checksum if provided
            if expected_sha256 and sha256_hash:
                actual = sha256_hash.hexdigest()
                if actual.lower() != expected_sha256.lower():
                    raise ChecksumMismatchError(
                        f"Checksum mismatch! Expected {expected_sha256}, got {actual}"
                    )

            # Atomic move from temp to dest
            shutil.move(str(temp_path), str(dest))
            temp_path = None

            # Clean up part file on success
            part_file.unlink(missing_ok=True)

            return dest

        except URLError as e:
            raise DownloadError(f"Download failed: {e}") from e
        except IOError as e:
            raise DownloadError(f"I/O error: {e}") from e
        finally:
            # Guaranteed cleanup of temp files on failure
            if temp_path is not None:
                try:
                    temp_path.unlink()
                except OSError:
                    pass

    def cleanup(self) -> None:
        """Clean up any orphaned temp files in the staging directory."""
        if self.temp_dir.exists():
            for f in self.temp_dir.glob("*.tmp"):
                try:
                    f.unlink()
                except OSError:
                    pass
            for f in self.temp_dir.glob("*.part"):
                try:
                    f.unlink()
                except OSError:
                    pass
