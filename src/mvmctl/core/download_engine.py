"""Unified download engine with temp staging, resume, and safe cleanup."""

import hashlib
import os
import shutil
import tempfile
import time
from pathlib import Path
from typing import Optional
from urllib.error import HTTPError, URLError
from urllib.request import Request

from mvmctl.constants import (
    CONST_DOWNLOAD_CHUNK_SIZE,
    CONST_DOWNLOAD_MAX_RETRIES,
    CONST_DOWNLOAD_RETRY_BACKOFF,
    CONST_DOWNLOAD_RETRY_DELAY,
    CONST_HTTP_TIMEOUT_SECONDS,
    HTTP_USER_AGENT,
)
from mvmctl.exceptions import ChecksumMismatchError, DownloadError
from mvmctl.utils import http
from mvmctl.utils.fs import get_temp_dir
from mvmctl.utils.progress import ASCIIProgressBar


class DownloadEngine:
    def __init__(self, temp_dir: Optional[Path] = None) -> None:
        self.temp_dir = temp_dir or get_temp_dir()

    def download(
        self,
        url: str,
        dest: Path,
        expected_sha256: Optional[str] = None,
        progress: bool = True,
        timeout: int = CONST_HTTP_TIMEOUT_SECONDS,
        max_retries: int = CONST_DOWNLOAD_MAX_RETRIES,
    ) -> Path:
        delay = CONST_DOWNLOAD_RETRY_DELAY
        last_exception: Optional[Exception] = None

        for attempt in range(max_retries + 1):
            try:
                return self._download_once(url, dest, expected_sha256, progress, timeout)
            except (URLError, HTTPError, IOError, OSError) as e:
                last_exception = e
                if attempt < max_retries:
                    print(f"Download attempt {attempt + 1}/{max_retries + 1} failed: {e}")
                    print(f"Retrying in {delay:.1f}s...")
                    time.sleep(delay)
                    delay *= CONST_DOWNLOAD_RETRY_BACKOFF
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
        progress: bool = True,
        timeout: int = CONST_HTTP_TIMEOUT_SECONDS,
    ) -> Path:
        dest.parent.mkdir(parents=True, exist_ok=True)
        self.temp_dir.mkdir(parents=True, exist_ok=True)

        total_size = 0
        try:
            req = Request(url, headers={"User-Agent": HTTP_USER_AGENT}, method="HEAD")
            with http.urlopen(req, timeout=30) as response:
                content_length = response.headers.get("Content-Length")
                if content_length:
                    total_size = int(content_length)
        except Exception:
            pass

        progress_bar = ASCIIProgressBar(total=total_size, title=f"Fetching {dest.name}")
        sha256_hash = hashlib.sha256() if expected_sha256 else None
        temp_path: Optional[Path] = None

        try:
            temp_fd, temp_path_str = tempfile.mkstemp(
                dir=self.temp_dir, prefix=f"{dest.stem}-", suffix=".tmp"
            )
            os.close(temp_fd)
            temp_path = Path(temp_path_str)

            req = Request(url, headers={"User-Agent": HTTP_USER_AGENT})
            with http.urlopen(req, timeout=timeout) as response:
                with temp_path.open("wb") as f:
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

            if expected_sha256 and sha256_hash:
                actual = sha256_hash.hexdigest()
                if actual.lower() != expected_sha256.lower():
                    raise ChecksumMismatchError(
                        f"Checksum mismatch! Expected {expected_sha256}, got {actual}"
                    )

            shutil.move(str(temp_path), str(dest))
            temp_path = None
            return dest

        except URLError as e:
            raise DownloadError(f"Download failed: {e}") from e
        except IOError as e:
            raise DownloadError(f"I/O error: {e}") from e
        finally:
            if temp_path is not None:
                temp_path.unlink(missing_ok=True)

    def cleanup(self) -> None:
        if self.temp_dir.exists():
            for f in self.temp_dir.glob("*.tmp"):
                try:
                    f.unlink()
                except OSError:
                    pass
