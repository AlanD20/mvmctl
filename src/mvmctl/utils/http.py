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
from urllib.request import Request
from urllib.request import urlopen as urlopen

from mvmctl.constants import CONST_DOWNLOAD_CHUNK_SIZE, HTTP_USER_AGENT
from mvmctl.exceptions import ChecksumMismatchError, MVMError

__all__ = ["download_file", "urlopen"]

logger = logging.getLogger(__name__)
_CONTENT_RANGE_PATTERN = re.compile(r"^bytes\s+(\d+)-(\d+)/(\d+|\*)$")

# Download retry configuration
DEFAULT_MAX_RETRIES = 3
DEFAULT_RETRY_DELAY = 1.0  # seconds
DEFAULT_RETRY_BACKOFF = 2.0  # exponential backoff multiplier

F = TypeVar("F", bound=Callable[..., Any])


def _with_retry(
    max_retries: int = DEFAULT_MAX_RETRIES,
    retry_delay: float = DEFAULT_RETRY_DELAY,
    backoff: float = DEFAULT_RETRY_BACKOFF,
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


def _partial_download_path(dest: Path) -> Path:
    return dest.parent / f".{dest.name}.part"


def _create_temp_download_path(dest: Path) -> Path:
    temp_fd, temp_path = tempfile.mkstemp(dir=dest.parent, prefix=f".{dest.name}.", suffix=".tmp")
    os.close(temp_fd)
    return Path(temp_path)


def _copy_existing_bytes(
    src_path: Path,
    dest_path: Path,
    sha256_hash: Any,
) -> int:
    copied = 0
    with src_path.open("rb") as src_file, dest_path.open("wb") as dest_file:
        while True:
            chunk = src_file.read(CONST_DOWNLOAD_CHUNK_SIZE)
            if not chunk:
                break
            dest_file.write(chunk)
            copied += len(chunk)
            if sha256_hash is not None:
                sha256_hash.update(chunk)
    return copied


def _parse_total_size(
    response_headers: Any,
    *,
    is_resume: bool,
    resume_byte_pos: int,
) -> int | None:
    if not hasattr(response_headers, "get"):
        return None

    header_get = response_headers.get
    content_length = header_get("Content-Length")

    if is_resume:
        content_range = header_get("Content-Range", "")
        if isinstance(content_range, str):
            match = _CONTENT_RANGE_PATTERN.match(content_range.strip())
            if match:
                start = int(match.group(1))
                total = match.group(3)
                if start == resume_byte_pos and total != "*":
                    return int(total)
        if content_length is not None:
            try:
                return resume_byte_pos + int(content_length)
            except (TypeError, ValueError):
                return None
        return None

    if content_length is None:
        return None

    try:
        return int(content_length)
    except (TypeError, ValueError):
        return None


@_with_retry(
    max_retries=DEFAULT_MAX_RETRIES, retry_delay=DEFAULT_RETRY_DELAY, backoff=DEFAULT_RETRY_BACKOFF
)
def download_file(
    url: str,
    dest: Path,
    expected_sha256: str | None = None,
    show_progress: bool = True,
    timeout: int = 300,
    allow_missing_checksum: bool = False,
    resume: bool = False,
    silent_missing_checksum: bool = False,
) -> bool:
    """Download a file with optional progress display and checksum verification.

    Args:
        url: URL to download from
        dest: Destination path
        expected_sha256: Optional SHA-256 checksum to verify
        show_progress: Show progress via logging
        timeout: Request timeout in seconds
        allow_missing_checksum: If True, allow download without checksum verification
            after interactive confirmation. If False (default), raises MVMError
            when no checksum is provided.
        resume: If True, resume partial downloads using HTTP Range requests.
            If the destination file exists, the download will continue from
            where it left off. If the server doesn't support Range requests,
            the download will restart from the beginning.
        silent_missing_checksum: If True, skip all warnings and interactive prompts
            when no checksum is available and proceed silently. Use when the absence
            of a checksum is intentional (e.g. the asset spec deliberately omits one).

    Returns:
        True if successful

    Raises:
        MVMError: On download or I/O failure, or when checksum is required but missing
        ChecksumMismatchError: On checksum mismatch
    """
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
            # Interactive confirmation when checksum is missing
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

    partial_path = _partial_download_path(dest)
    temp_path: Path | None = None
    working_path: Path | None = None
    cleanup_partial = False
    try:
        resume_source: Path | None = None
        resume_byte_pos = 0
        if resume:
            if partial_path.exists():
                resume_source = partial_path
                cleanup_partial = True
            elif dest.exists():
                resume_source = dest

            if resume_source is not None:
                resume_byte_pos = resume_source.stat().st_size
                if show_progress and resume_byte_pos > 0:
                    logger.info("Resuming download from byte %d", resume_byte_pos)

        temp_path = _create_temp_download_path(dest)
        working_path = temp_path

        headers = {"User-Agent": HTTP_USER_AGENT}
        if resume_byte_pos > 0:
            headers["Range"] = f"bytes={resume_byte_pos}-"

        req = Request(url, headers=headers)

        if show_progress:
            logger.info("Downloading %s", url)

        with urlopen(req, timeout=timeout) as response:
            is_resume = resume_byte_pos > 0 and response.status == 206
            total_size = _parse_total_size(
                response.headers,
                is_resume=is_resume,
                resume_byte_pos=resume_byte_pos,
            )

            if response.status == 200 and resume_byte_pos > 0 and show_progress:
                logger.info("Server doesn't support resume, restarting download")

            sha256_hash = hashlib.sha256() if expected_sha256 else None
            downloaded = 0

            if is_resume and resume_source is not None and resume_byte_pos > 0:
                downloaded = _copy_existing_bytes(resume_source, working_path, sha256_hash)
            elif working_path.exists():
                working_path.write_bytes(b"")

            with working_path.open("ab" if is_resume else "wb") as f:
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
                if working_path is not None:
                    working_path.unlink(missing_ok=True)
                raise ChecksumMismatchError(
                    f"Checksum mismatch! Expected {expected_sha256}, got {actual_sha256}"
                )
            logger.info("Checksum verified")

        if working_path is None:
            raise MVMError(f"Download failed: no working file created for {url}")

        os.replace(working_path, dest)
        temp_path = None
        partial_path.unlink(missing_ok=True)
        return True

    except URLError as e:
        raise MVMError(f"Download failed: {e}") from e
    except IOError as e:
        raise MVMError(f"I/O error: {e}") from e
    finally:
        if temp_path is not None:
            try:
                temp_path.unlink()
            except OSError:
                pass
        if cleanup_partial:
            partial_path.unlink(missing_ok=True)
