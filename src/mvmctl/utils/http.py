"""HTTP download utilities."""

from __future__ import annotations

import hashlib
import logging
import os
import re
import tempfile
import time
from functools import wraps
from pathlib import Path
from typing import Any, Callable, TypeVar
from urllib.error import HTTPError, URLError
from urllib.request import (
    HTTPHandler,
    HTTPSHandler,
    Request,
    build_opener,
)

from mvmctl.constants import (
    CONST_DOWNLOAD_CHUNK_SIZE,
    CONST_DOWNLOAD_MAX_RETRIES,
    CONST_DOWNLOAD_RETRY_BACKOFF,
    CONST_DOWNLOAD_RETRY_DELAY,
    HTTP_USER_AGENT,
)
from mvmctl.exceptions import ChecksumMismatchError, MVMError
from mvmctl.utils.fs import get_temp_dir

__all__ = ["download_file", "urlopen"]

logger = logging.getLogger(__name__)
_CONTENT_RANGE_PATTERN = re.compile(r"^bytes\s+(\d+)-(\d+)/(\d+|\*)$")

F = TypeVar("F", bound=Callable[..., Any])


# Shared opener with HTTP keep-alive for connection reuse
_http_opener = build_opener(
    HTTPHandler(),
    HTTPSHandler(),
)
_http_opener.addheaders = [("User-Agent", HTTP_USER_AGENT)]


def urlopen(req: Any, timeout: int = 300) -> Any:
    return _http_opener.open(req, timeout=timeout)


def _with_retry(
    max_retries: int = CONST_DOWNLOAD_MAX_RETRIES,
    retry_delay: float = CONST_DOWNLOAD_RETRY_DELAY,
    backoff: float = CONST_DOWNLOAD_RETRY_BACKOFF,
    retryable_exceptions: tuple[type[Exception], ...] = (URLError, HTTPError, IOError),
) -> Callable[[F], F]:
    """Decorator that adds retry logic with exponential backoff."""

    def decorator(func: F) -> F:
        @wraps(func)
        def wrapper(*args: Any, **kwargs: Any) -> Any:
            delay = retry_delay
            last_exception: Exception | None = None

            for attempt in range(max_retries + 1):
                try:
                    return func(*args, **kwargs)
                except retryable_exceptions as e:
                    last_exception = e
                    if attempt < max_retries:
                        logger.warning(
                            "%s failed (attempt %d/%d): %s. Retrying in %.1fs...",
                            func.__name__,
                            attempt + 1,
                            max_retries + 1,
                            e,
                            delay,
                        )
                        time.sleep(delay)
                        delay *= backoff
                    else:
                        logger.error(
                            "%s failed after %d attempts: %s",
                            func.__name__,
                            max_retries + 1,
                            e,
                        )

            raise last_exception if last_exception else MVMError("Download failed")

        return wrapper  # type: ignore[return-value]

    return decorator


def _parse_content_length(response_headers: Any) -> int | None:
    if not hasattr(response_headers, "get"):
        return None
    content_length = response_headers.get("Content-Length")
    if content_length is None:
        return None
    try:
        return int(content_length)
    except (TypeError, ValueError):
        return None


@_with_retry(
    max_retries=CONST_DOWNLOAD_MAX_RETRIES,
    retry_delay=CONST_DOWNLOAD_RETRY_DELAY,
    backoff=CONST_DOWNLOAD_RETRY_BACKOFF,
)
def download_file(
    url: str,
    dest: Path,
    expected_sha256: str | None = None,
    show_progress: bool = True,
    timeout: int = 300,
    allow_missing_checksum: bool = False,
    silent_missing_checksum: bool = False,
) -> bool:
    dest.parent.mkdir(parents=True, exist_ok=True)

    if expected_sha256 is None:
        if silent_missing_checksum:
            pass
        elif not allow_missing_checksum:
            raise MVMError(
                f"No checksum provided for download: {url}. "
                "Checksum verification is mandatory for security. "
                "Provide expected_sha256 or use allow_missing_checksum=True with confirmation."
            )
        else:
            import sys

            from mvmctl.utils.console import print_warning

            print_warning(f"Warning: No checksum available for {url}")
            print_warning("Integrity cannot be verified. This is a potential security risk.")
            if not sys.stdin.isatty():
                raise MVMError(
                    f"No checksum provided for download: {url}. "
                    "Cannot prompt for confirmation in non-interactive mode. "
                    "Provide expected_sha256 or run in an interactive terminal."
                )
            import typer

            if not typer.confirm("Proceed with download anyway?", default=False):
                raise MVMError(f"Download cancelled: {url} (no checksum provided)")

    temp_path: Path | None = None
    try:
        temp_fd, temp_str = tempfile.mkstemp(
            dir=get_temp_dir(), prefix=f"{dest.stem}-", suffix=".tmp"
        )
        os.close(temp_fd)
        temp_path = Path(temp_str)

        req = Request(url, headers={"User-Agent": HTTP_USER_AGENT})

        if show_progress:
            logger.info("Downloading %s", url)

        with urlopen(req, timeout=timeout) as response:
            total_size = _parse_content_length(response.headers)
            sha256_hash = hashlib.sha256() if expected_sha256 else None
            downloaded = 0

            with temp_path.open("wb") as f:
                while True:
                    chunk = response.read(CONST_DOWNLOAD_CHUNK_SIZE)
                    if not chunk:
                        break
                    f.write(chunk)
                    downloaded += len(chunk)
                    if sha256_hash:
                        sha256_hash.update(chunk)
                    if show_progress and total_size:
                        try:
                            percent = (downloaded / total_size) * 100
                            logger.debug("Progress: %.1f%%", percent)
                        except ZeroDivisionError:
                            pass

        if expected_sha256 and sha256_hash:
            actual_sha256 = sha256_hash.hexdigest()
            if actual_sha256.lower() != expected_sha256.lower():
                temp_path.unlink(missing_ok=True)
                raise ChecksumMismatchError(
                    f"Checksum mismatch! Expected {expected_sha256}, got {actual_sha256}"
                )
            logger.info("Checksum verified")

        os.replace(temp_path, dest)
        temp_path = None
        return True

    except URLError as e:
        raise MVMError(f"Download failed: {e}") from e
    except IOError as e:
        raise MVMError(f"I/O error: {e}") from e
    finally:
        if temp_path is not None:
            temp_path.unlink(missing_ok=True)
