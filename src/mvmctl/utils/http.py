"""HTTP download utilities."""

from __future__ import annotations

import hashlib
import json
import logging
import os
import shutil
import tempfile
import time
from collections.abc import Callable
from functools import wraps
from pathlib import Path
from typing import Any, TypeVar
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
from mvmctl.exceptions import ChecksumMismatchError, HttpDownloadError

__all__ = ["HttpDownload"]

logger = logging.getLogger(__name__)

F = TypeVar("F", bound=Callable[..., Any])

DEFAULT_CACHE_TTL_SECONDS: int = 300
DEFAULT_CACHE_DIR = "http"


class HttpCache:
    """File-based HTTP response cache for small remote resources."""

    @staticmethod
    def _cache_key(url: str) -> str:
        return hashlib.sha256(url.encode("utf-8")).hexdigest()

    @staticmethod
    def _cache_path(url: str) -> Path:
        from mvmctl.utils.common import CacheUtils

        cache_dir = CacheUtils.get_temp_dir() / DEFAULT_CACHE_DIR
        cache_dir.mkdir(parents=True, exist_ok=True)
        return cache_dir / HttpCache._cache_key(url)

    @staticmethod
    def is_valid(cache_path: Path, ttl_seconds: int) -> bool:
        if not cache_path.exists():
            return False
        age = time.time() - cache_path.stat().st_mtime
        return age < ttl_seconds

    @staticmethod
    def read(cache_path: Path) -> bytes:
        return cache_path.read_bytes()

    @staticmethod
    def write(cache_path: Path, data: bytes) -> None:
        cache_path.parent.mkdir(parents=True, exist_ok=True)
        fd, temp_path = tempfile.mkstemp(
            dir=cache_path.parent,
            prefix=f"{cache_path.stem}-",
            suffix=".tmp",
        )
        try:
            os.write(fd, data)
            os.close(fd)
            os.replace(temp_path, cache_path)
        except Exception:
            os.close(fd)
            Path(temp_path).unlink(missing_ok=True)
            raise


# Shared opener with HTTP keep-alive for connection reuse
_http_opener = build_opener(
    HTTPHandler(),
    HTTPSHandler(),
)
_http_opener.addheaders = [("User-Agent", HTTP_USER_AGENT)]


def _with_retry(
    max_retries: int = CONST_DOWNLOAD_MAX_RETRIES,
    retry_delay: float = CONST_DOWNLOAD_RETRY_DELAY,
    backoff: float = CONST_DOWNLOAD_RETRY_BACKOFF,
    retryable_exceptions: tuple[type[Exception], ...] = (
        URLError,
        HTTPError,
        IOError,
    ),
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

            raise (
                last_exception
                if last_exception
                else HttpDownloadError("Download failed")
            )

        return wrapper  # type: ignore[return-value]

    return decorator


class HttpDownload:
    """Lightweight HTTP helpers for fetching remote resources."""

    @staticmethod
    def _urlopen(req: Any, timeout: int = 300) -> Any:
        return _http_opener.open(req, timeout=timeout)

    @staticmethod
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

    @staticmethod
    def _download(
        url: str,
        timeout: int = 30,
        headers: dict[str, str] | None = None,
        use_cache: bool = False,
        cache_ttl_seconds: int = DEFAULT_CACHE_TTL_SECONDS,
    ) -> bytes:
        """
        Fetch a URL and return the raw response bytes.

        Args:
            url: The URL to fetch.
            timeout: Request timeout in seconds.
            headers: Optional extra headers to send with the request.
            use_cache: If True, cache the response and serve from cache when valid.
            cache_ttl_seconds: Time-to-live for cached responses in seconds.

        Returns:
            The raw response body as bytes.

        Raises:
            HttpDownloadError: If the download fails.

        """
        cache_file: Path | None = None
        if use_cache:
            cache_file = HttpCache._cache_path(url)
            if HttpCache.is_valid(cache_file, cache_ttl_seconds):
                return HttpCache.read(cache_file)

        default_headers = {"User-Agent": HTTP_USER_AGENT}
        if headers:
            default_headers.update(headers)

        req = Request(url, headers=default_headers)

        try:
            with HttpDownload._urlopen(req, timeout=timeout) as response:
                data: bytes = response.read()
                if use_cache and cache_file is not None:
                    HttpCache.write(cache_file, data)
                return data
        except (URLError, HTTPError, OSError) as exc:
            raise HttpDownloadError(f"Failed to fetch {url}: {exc}") from exc

    @staticmethod
    def head_size(
        url: str,
        timeout: int = 10,
        use_cache: bool = True,
        cache_ttl_seconds: int = DEFAULT_CACHE_TTL_SECONDS,
    ) -> int | None:
        """
        Get remote file size via HEAD request with optional caching.

        Args:
            url: The URL to probe.
            timeout: Request timeout in seconds.
            use_cache: If True, cache the size and serve from cache when valid.
            cache_ttl_seconds: Time-to-live for cached sizes.

        Returns:
            Content-Length in bytes, or None if unavailable.

        """
        if use_cache:
            cache_file = HttpCache._cache_path(url)
            if HttpCache.is_valid(cache_file, cache_ttl_seconds):
                cached = HttpCache.read(cache_file)
                if cached:
                    return int(cached.decode())
                return None

        req = Request(
            url, method="HEAD", headers={"User-Agent": HTTP_USER_AGENT}
        )

        try:
            with HttpDownload._urlopen(req, timeout=timeout) as response:
                size = HttpDownload._parse_content_length(response.headers)
                if use_cache:
                    cache_file = HttpCache._cache_path(url)
                    HttpCache.write(
                        cache_file,
                        str(size).encode() if size is not None else b"",
                    )
                return size
        except Exception:
            return None

    @staticmethod
    def read_raw_content(
        url: str,
        timeout: int = 30,
        headers: dict[str, str] | None = None,
        use_cache: bool = False,
        cache_ttl_seconds: int = DEFAULT_CACHE_TTL_SECONDS,
    ) -> str:
        """
        Download a URL and return its raw content as a string.

        This is a lightweight helper for fetching small text resources
        (like SHA256 sidecar files) where writing to disk is unnecessary.

        Args:
            url: The URL to fetch.
            timeout: Request timeout in seconds.
            headers: Optional extra headers to send with the request.
            use_cache: If True, cache the response and serve from cache when valid.
            cache_ttl_seconds: Time-to-live for cached responses in seconds.

        Returns:
            The decoded response body as a string.

        Raises:
            HttpDownloadError: If the download fails.

        """
        default_headers = {"Accept": "text/plain"}
        if headers:
            default_headers.update(headers)

        data = HttpDownload._download(
            url,
            timeout=timeout,
            headers=default_headers,
            use_cache=use_cache,
            cache_ttl_seconds=cache_ttl_seconds,
        )
        return data.decode()

    @staticmethod
    def read_json_content(
        url: str,
        timeout: int = 30,
        headers: dict[str, str] | None = None,
        use_cache: bool = False,
        cache_ttl_seconds: int = DEFAULT_CACHE_TTL_SECONDS,
    ) -> dict[str, Any] | list[Any]:
        """
        Download a URL and return its JSON content as a parsed object.

        Args:
            url: The URL to fetch.
            timeout: Request timeout in seconds.
            headers: Optional extra headers to send with the request.
            use_cache: If True, cache the response and serve from cache when valid.
            cache_ttl_seconds: Time-to-live for cached responses in seconds.

        Returns:
            The parsed JSON response (dict or list).

        Raises:
            HttpDownloadError: If the download or JSON parsing fails.

        """
        default_headers = {"Accept": "application/json"}
        if headers:
            default_headers.update(headers)

        data = HttpDownload._download(
            url,
            timeout=timeout,
            headers=default_headers,
            use_cache=use_cache,
            cache_ttl_seconds=cache_ttl_seconds,
        )
        try:
            result: dict[str, Any] | list[Any] = json.loads(data.decode())
            return result
        except json.JSONDecodeError as exc:
            raise HttpDownloadError(
                f"Failed to parse JSON from {url}: {exc}"
            ) from exc

    @staticmethod
    @_with_retry(
        max_retries=CONST_DOWNLOAD_MAX_RETRIES,
        retry_delay=CONST_DOWNLOAD_RETRY_DELAY,
        backoff=CONST_DOWNLOAD_RETRY_BACKOFF,
    )
    def with_download(
        url: str,
        dest: Path,
        timeout: int = 300,
        progress_callback: Callable[[bytes], None] | None = None,
        on_start: Callable[[int | None], None] | None = None,
    ) -> int | None:
        """
        Download a remote file to *dest* with an optional progress callback.

        This is the **pure transport** entry point: it handles only HTTP
        mechanics, retries, and atomic placement.  No checksum logic or
        progress-bar rendering lives here.

        The file is downloaded to a temporary sibling of *dest* and then
        atomically promoted with :func:`os.replace`, so readers never see
        a partially-written file.

        Args:
            url: URL to download.
            dest: Final destination path on disk.
            timeout: Request timeout in seconds.
            progress_callback: Optional callable that receives each chunk of
                raw bytes as it is written to disk.
            on_start: Optional callable invoked once before the first chunk
                is read, receiving the reported Content-Length (or None).

        Returns:
            The total Content-Length if the server reported one, else None.

        Raises:
            HttpDownloadError: On network or I/O failure.

        """
        dest.parent.mkdir(parents=True, exist_ok=True)

        temp_path: Path | None = None
        total_size: int | None = None
        try:
            temp_fd, temp_str = tempfile.mkstemp(
                dir=dest.parent,
                prefix=f"{dest.stem}-",
                suffix=".tmp",
            )
            os.close(temp_fd)
            temp_path = Path(temp_str)

            req = Request(url, headers={"User-Agent": HTTP_USER_AGENT})

            with HttpDownload._urlopen(req, timeout=timeout) as response:
                total_size = HttpDownload._parse_content_length(
                    response.headers
                )
                if on_start is not None:
                    on_start(total_size)
                with temp_path.open("wb") as f:
                    while True:
                        chunk = response.read(CONST_DOWNLOAD_CHUNK_SIZE)
                        if not chunk:
                            break
                        f.write(chunk)
                        if progress_callback is not None:
                            progress_callback(chunk)

            os.replace(temp_path, dest)
            temp_path = None
            return total_size

        except URLError as e:
            raise HttpDownloadError(f"Download failed: {e}") from e
        except OSError as e:
            if e.errno == 122:
                raise HttpDownloadError(
                    "No storage available: insufficient space in /tmp. "
                    "Clear temporary files or increase disk space to continue."
                ) from e
            raise HttpDownloadError(f"I/O error: {e}") from e
        finally:
            if temp_path is not None:
                temp_path.unlink(missing_ok=True)

    @staticmethod
    def _resolve_mirror_path(url: str) -> Path | None:
        """
        Check if the URL's file exists in the local asset mirror directory.

        Reads the ``MVM_ASSET_MIRROR`` environment variable to locate the
        mirror directory.  The filename is extracted from the last URL path
        segment, stripping any query-string parameters.

        Returns:
            The full ``Path`` to the mirrored file if it exists, or ``None``
            if the env var is unset or the file is not present in the mirror.
        """
        mirror_dir = os.environ.get("MVM_ASSET_MIRROR")
        if not mirror_dir:
            return None
        # Extract filename from URL (last segment after '/', strip query params)
        filename = url.rsplit("/", 1)[-1].split("?", 1)[0]
        mirror_path = Path(mirror_dir) / filename
        return mirror_path if mirror_path.is_file() else None

    @staticmethod
    def download_file(
        url: str,
        dest: Path,
        expected_sha256: str | None = None,
        timeout: int = 300,
        allow_missing_checksum: bool = False,
        silent_missing_checksum: bool = False,
        progress_callback: Callable[[int, int | None], None] | None = None,
    ) -> bool:
        """
        Download a file with optional SHA256 verification.

        This is the **orchestration** entry point: it delegates the actual
        HTTP transfer to :meth:`with_download` and then handles checksum
        verification and user interaction for missing checksums.

        Args:
            url: URL to download.
            dest: Destination path on disk.
            expected_sha256: Optional SHA256 hex string for verification.
            timeout: Request timeout in seconds.
            allow_missing_checksum: If True, allow download without checksum.
            silent_missing_checksum: If True, skip warnings for missing checksum.
            progress_callback: Optional callback invoked with (current_bytes,
                total_bytes) for each chunk received. Caller decides how to
                render progress — e.g. update a Rich spinner, print text, etc.

        Returns:
            True on success.

        Raises:
            HttpDownloadError: On download or checksum failure.
            ChecksumMismatchError: If SHA256 verification fails.

        """
        dest.parent.mkdir(parents=True, exist_ok=True)

        # --- Local asset mirror check ---
        mirror_path = HttpDownload._resolve_mirror_path(url)
        if mirror_path is not None:
            shutil.copy2(mirror_path, dest)
            logger.info("Using local mirror for %s", url)
            if expected_sha256 is not None:
                # Verify SHA256 of the mirrored file
                actual_hash = hashlib.sha256()
                with dest.open("rb") as f:
                    while True:
                        chunk = f.read(CONST_DOWNLOAD_CHUNK_SIZE)
                        if not chunk:
                            break
                        actual_hash.update(chunk)
                if actual_hash.hexdigest().lower() == expected_sha256.lower():
                    return True
                logger.warning(
                    "Mirror checksum mismatch for %s, falling back to HTTP download",
                    url,
                )
                dest.unlink(missing_ok=True)
                # Fall through to HTTP download below
            else:
                return True

        if expected_sha256 is None:
            if silent_missing_checksum:
                pass
            elif not allow_missing_checksum:
                raise HttpDownloadError(
                    f"No checksum provided for download: {url}. "
                    "Checksum verification is mandatory for security. "
                    "Provide expected_sha256 or use allow_missing_checksum=True with confirmation."
                )
            else:
                import sys

                from mvmctl.utils.cli import mvm_cli

                mvm_cli.warning(f"Warning: No checksum available for {url}")
                mvm_cli.warning(
                    "Integrity cannot be verified. This is a potential security risk."
                )
                if not sys.stdin.isatty():
                    raise HttpDownloadError(
                        f"No checksum provided for download: {url}. "
                        "Cannot prompt for confirmation in non-interactive mode. "
                        "Provide expected_sha256 or run in an interactive terminal."
                    )
                import typer

                if not typer.confirm(
                    "Proceed with download anyway?", default=False
                ):
                    raise HttpDownloadError(
                        f"Download cancelled: {url} (no checksum provided)"
                    )

        sha256_hash = hashlib.sha256() if expected_sha256 else None
        downloaded: list[int] = [0]
        total_size_cell: list[int | None] = [None]

        def _on_start(total_size: int | None) -> None:
            total_size_cell[0] = total_size

        def _chunk_callback(chunk: bytes) -> None:
            downloaded[0] += len(chunk)
            if progress_callback is not None:
                progress_callback(downloaded[0], total_size_cell[0])
            if sha256_hash is not None:
                sha256_hash.update(chunk)

        HttpDownload.with_download(
            url,
            dest,
            timeout=timeout,
            progress_callback=_chunk_callback
            if progress_callback or sha256_hash
            else None,
            on_start=_on_start,
        )

        if expected_sha256 and sha256_hash:
            actual_sha256 = sha256_hash.hexdigest()
            if actual_sha256.lower() != expected_sha256.lower():
                dest.unlink(missing_ok=True)
                raise ChecksumMismatchError(
                    f"Checksum mismatch! Expected {expected_sha256}, got {actual_sha256}"
                )
            logger.info("Checksum verified")

        # ── Auto-populate the local asset mirror ──────────────────────────
        # After a successful download, copy the file into the mirror
        # directory (MVM_ASSET_MIRROR) so subsequent runs use a fast local
        # copy instead of re-downloading from the internet.
        mirror_dir = os.environ.get("MVM_ASSET_MIRROR")
        if mirror_dir:
            filename = url.rsplit("/", 1)[-1].split("?", 1)[0]
            mirror_path = Path(mirror_dir) / filename
            if not mirror_path.exists():
                mirror_path.parent.mkdir(parents=True, exist_ok=True)
                try:
                    shutil.copy2(dest, mirror_path)
                    logger.info("Copied to asset mirror: %s", mirror_path)
                except OSError:
                    logger.warning(
                        "Failed to copy to asset mirror: %s", mirror_path
                    )

        return True
