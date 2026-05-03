"""Tests for utils/http.py — HTTP download utilities."""

from __future__ import annotations

import hashlib
from pathlib import Path
from unittest.mock import MagicMock, patch
from urllib.error import URLError

import pytest

from mvmctl.exceptions import ChecksumMismatchError, HttpDownloadError
from mvmctl.utils.http import HttpCache, HttpDownload, _with_retry


def _mock_urlopen_response(
    data: bytes,
    content_length: str | None = None,
    status: int = 200,
):
    """Create a mock urlopen response that yields data in chunks."""
    mock_response = MagicMock()
    mock_response.status = status
    mock_response.headers.get.side_effect = lambda key, default=None: {
        "Content-Length": content_length,
    }.get(key, default)

    chunks = [data[i : i + 8192] for i in range(0, len(data), 8192)]
    # Append empty bytes to signal end of stream for read() loop
    chunks.append(b"")
    mock_response.read.side_effect = chunks

    # Support context manager
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    return mock_response


# ---------------------------------------------------------------------------
# download_file — orchestration entry point
# ---------------------------------------------------------------------------


class TestDownloadFile:
    """Tests for HttpDownload.download_file()."""

    @staticmethod
    def _make_fake_download(full_data: bytes):
        """Create a side_effect function for with_download mock that calls callbacks."""

        def _fake_download(url, dest_path, **kwargs):
            dest_path.write_bytes(full_data)
            # The download_file passes progress_callback which also computes SHA256
            pc = kwargs.get("progress_callback")
            if pc:
                pc(full_data)
            return len(full_data)

        return _fake_download

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_success_with_checksum(self, mock_with_download, tmp_path: Path):
        """Should download a file and verify checksum."""
        dest = tmp_path / "target.bin"
        full_data = b"Complete file content"
        expected_sha256 = hashlib.sha256(full_data).hexdigest()

        mock_with_download.side_effect = self._make_fake_download(full_data)

        result = HttpDownload.download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=expected_sha256,
        )

        assert result is True
        assert dest.exists()
        assert dest.read_bytes() == full_data

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_checksum_mismatch_cleans_up(
        self, mock_with_download, tmp_path: Path
    ):
        """Should clean up on checksum mismatch."""
        dest = tmp_path / "target.bin"
        full_data = b"Complete file content"
        wrong_sha256 = "0" * 64

        mock_with_download.side_effect = self._make_fake_download(full_data)

        with pytest.raises(ChecksumMismatchError):
            HttpDownload.download_file(
                "https://example.com/file.bin",
                dest,
                expected_sha256=wrong_sha256,
            )

        assert not dest.exists()

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_missing_checksum_non_interactive(
        self, mock_with_download, tmp_path: Path
    ):
        """Should raise when no checksum and allow_missing_checksum=False."""
        dest = tmp_path / "target.bin"

        with pytest.raises(HttpDownloadError, match="No checksum"):
            HttpDownload.download_file(
                "https://example.com/file.bin",
                dest,
                expected_sha256=None,
                allow_missing_checksum=False,
            )

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_no_content_length(self, mock_with_download, tmp_path: Path):
        """Should handle missing Content-Length header."""
        dest = tmp_path / "target.bin"
        full_data = b"Complete file content"
        expected_sha256 = hashlib.sha256(full_data).hexdigest()

        def _fake_download(url, dest_path, **kwargs):
            dest_path.write_bytes(full_data)
            pc = kwargs.get("progress_callback")
            if pc:
                pc(full_data)
            return None  # No content-length reported

        mock_with_download.side_effect = _fake_download

        result = HttpDownload.download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=expected_sha256,
        )

        assert result is True
        assert dest.read_bytes() == full_data

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_without_ascii_bar(self, mock_with_download, tmp_path: Path):
        """Should work with progress bar enabled."""
        dest = tmp_path / "target.bin"
        full_data = b"Complete file content for progress test"
        expected_sha256 = hashlib.sha256(full_data).hexdigest()

        mock_with_download.side_effect = self._make_fake_download(full_data)

        result = HttpDownload.download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=expected_sha256,
        )

        assert result is True
        assert dest.read_bytes() == full_data


# ---------------------------------------------------------------------------
# with_download — pure transport entry point
# ---------------------------------------------------------------------------


class TestWithDownload:
    """Tests for HttpDownload.with_download()."""

    @staticmethod
    def _setup_mock_urlopen(
        mock_urlopen, data, status=200, content_length=None
    ):
        """Configure mock for HttpDownload._urlopen context manager pattern."""
        mock_response = _mock_urlopen_response(
            data, status=status, content_length=content_length
        )
        mock_urlopen.return_value = mock_response
        return mock_response

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_atomic_rename(self, mock_urlopen, tmp_path: Path):
        """Should use temp file and atomic rename."""
        dest = tmp_path / "target_file.bin"
        full_data = b"Complete file content"

        self._setup_mock_urlopen(
            mock_urlopen,
            full_data,
            status=200,
            content_length=str(len(full_data)),
        )

        HttpDownload.with_download(
            "https://example.com/file.bin", dest, timeout=10
        )

        assert dest.exists()
        assert dest.read_bytes() == full_data
        # No temp file should remain
        temp_files = list(tmp_path.glob("*.tmp"))
        assert len(temp_files) == 0

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_cleans_up_temp_on_error(self, mock_urlopen, tmp_path: Path):
        """Should clean up temp file on download error."""
        from urllib.error import URLError

        dest = tmp_path / "target_file.bin"
        mock_urlopen.side_effect = URLError("Network error")

        with pytest.raises(HttpDownloadError, match="Download failed"):
            HttpDownload.with_download(
                "https://example.com/file.bin", dest, timeout=10
            )

        assert not dest.exists()
        temp_files = list(tmp_path.glob("*.tmp"))
        assert len(temp_files) == 0

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_with_progress_callback(self, mock_urlopen, tmp_path: Path):
        """Should invoke progress_callback with chunks."""
        dest = tmp_path / "target.bin"
        full_data = b"Progress test data"
        chunks_received = []

        self._setup_mock_urlopen(
            mock_urlopen,
            full_data,
            status=200,
            content_length=str(len(full_data)),
        )

        def progress_cb(chunk: bytes):
            chunks_received.append(chunk)

        HttpDownload.with_download(
            "https://example.com/file.bin",
            dest,
            timeout=10,
            progress_callback=progress_cb,
        )

        assert chunks_received
        assert b"".join(chunks_received) == full_data

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_with_on_start_callback(self, mock_urlopen, tmp_path: Path):
        """Should invoke on_start with content length."""
        dest = tmp_path / "target.bin"
        full_data = b"Start test data"
        captured_size = None

        self._setup_mock_urlopen(
            mock_urlopen,
            full_data,
            status=200,
            content_length=str(len(full_data)),
        )

        def on_start(size):
            nonlocal captured_size
            captured_size = size

        HttpDownload.with_download(
            "https://example.com/file.bin",
            dest,
            timeout=10,
            on_start=on_start,
        )

        assert captured_size == len(full_data)

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_no_content_length(self, mock_urlopen, tmp_path: Path):
        """Should handle missing Content-Length."""
        dest = tmp_path / "target.bin"
        full_data = b"Data without content-length"

        self._setup_mock_urlopen(
            mock_urlopen,
            full_data,
            status=200,
            content_length=None,
        )

        result = HttpDownload.with_download(
            "https://example.com/file.bin", dest, timeout=10
        )

        assert result is None
        assert dest.read_bytes() == full_data


# ---------------------------------------------------------------------------
# _parse_content_length
# ---------------------------------------------------------------------------


class TestParseContentLength:
    """Tests for HttpDownload._parse_content_length()."""

    def test_returns_none_for_no_get_method(self):
        assert HttpDownload._parse_content_length("not an object") is None

    def test_returns_none_for_missing_header(self):
        headers = MagicMock()
        headers.get.return_value = None
        assert HttpDownload._parse_content_length(headers) is None

    def test_returns_none_for_invalid_content_length(self):
        headers = MagicMock()
        headers.get.return_value = "not-a-number"
        assert HttpDownload._parse_content_length(headers) is None

    def test_returns_int_for_valid_content_length(self):
        headers = MagicMock()
        headers.get.return_value = "12345"
        assert HttpDownload._parse_content_length(headers) == 12345


# ---------------------------------------------------------------------------
# _download — low-level fetch
# ---------------------------------------------------------------------------


class TestDownload:
    """Tests for HttpDownload._download()."""

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_success(self, mock_urlopen):
        mock_response = MagicMock()
        mock_response.read.return_value = b"response data"
        mock_response.__enter__ = MagicMock(return_value=mock_response)
        mock_response.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_response

        result = HttpDownload._download("https://example.com/data")
        assert result == b"response data"

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_raises_on_http_error(self, mock_urlopen):
        from urllib.error import HTTPError

        mock_urlopen.side_effect = HTTPError(
            "https://example.com/404", 404, "Not Found", {}, None
        )

        with pytest.raises(HttpDownloadError, match="Failed to fetch"):
            HttpDownload._download("https://example.com/404")

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_raises_on_url_error(self, mock_urlopen):
        from urllib.error import URLError

        mock_urlopen.side_effect = URLError("Connection refused")

        with pytest.raises(HttpDownloadError, match="Failed to fetch"):
            HttpDownload._download("https://example.com/refused")


# ---------------------------------------------------------------------------
# head_size
# ---------------------------------------------------------------------------


class TestHeadSize:
    """Tests for HttpDownload.head_size()."""

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_returns_content_length(self, mock_urlopen):
        mock_response = MagicMock()
        mock_response.headers.get.return_value = "5000"
        mock_response.__enter__ = MagicMock(return_value=mock_response)
        mock_response.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_response

        size = HttpDownload.head_size("https://example.com/file")
        assert size == 5000

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_returns_none_on_error(self, mock_urlopen):
        mock_urlopen.side_effect = Exception("fail")
        size = HttpDownload.head_size("https://example.com/file")
        assert size is None

    def test_cache_hit_returns_cached_size(self, mocker):
        """Should return cached size when cache is valid."""
        mocker.patch.object(
            HttpCache, "_cache_path", return_value=Path("/tmp/test-cache")
        )
        mocker.patch.object(HttpCache, "is_valid", return_value=True)
        mocker.patch.object(HttpCache, "read", return_value=b"5000")

        size = HttpDownload.head_size("https://example.com/file")

        assert size == 5000

    def test_cache_hit_empty_returns_none(self, mocker):
        """Should return None when cached data is empty."""
        mocker.patch.object(
            HttpCache, "_cache_path", return_value=Path("/tmp/test-cache")
        )
        mocker.patch.object(HttpCache, "is_valid", return_value=True)
        mocker.patch.object(HttpCache, "read", return_value=b"")

        size = HttpDownload.head_size("https://example.com/file")

        assert size is None


# ---------------------------------------------------------------------------
# HttpCache
# ---------------------------------------------------------------------------


class TestHttpCache:
    """Tests for HttpCache methods."""

    def test_cache_key_generates_sha256(self):
        key = HttpCache._cache_key("https://example.com/file")
        assert len(key) == 64
        expected = hashlib.sha256(b"https://example.com/file").hexdigest()
        assert key == expected

    def test_cache_path_builds_path(self, mocker, tmp_path):
        from mvmctl.utils.common import CacheUtils

        mocker.patch.object(CacheUtils, "get_temp_dir", return_value=tmp_path)
        url = "https://example.com/file"
        expected_hash = hashlib.sha256(url.encode()).hexdigest()

        path = HttpCache._cache_path(url)

        assert path.parent == tmp_path / "http"
        assert path.name == expected_hash

    def test_is_valid_true_for_fresh_cache(self, tmp_path):
        cache_file = tmp_path / "cache_entry"
        cache_file.write_text("data")

        assert HttpCache.is_valid(cache_file, ttl_seconds=3600) is True

    def test_is_valid_false_for_expired_cache(self, tmp_path, mocker):
        cache_file = tmp_path / "cache_entry"
        cache_file.write_text("data")

        mocker.patch("time.time", return_value=9999999999.0)

        assert HttpCache.is_valid(cache_file, ttl_seconds=1) is False

    def test_is_valid_false_for_missing_path(self, tmp_path):
        cache_file = tmp_path / "nonexistent"

        assert HttpCache.is_valid(cache_file, ttl_seconds=3600) is False

    def test_read_returns_bytes(self, tmp_path):
        cache_file = tmp_path / "cache_entry"
        cache_file.write_bytes(b"test cache data")

        result = HttpCache.read(cache_file)

        assert result == b"test cache data"

    def test_write_atomic(self, tmp_path):
        cache_path = tmp_path / "subdir" / "cache_entry"
        data = b"cache data content"

        HttpCache.write(cache_path, data)

        assert cache_path.exists()
        assert cache_path.read_bytes() == data

    def test_write_creates_parent_dirs(self, tmp_path):
        deep_path = tmp_path / "a" / "b" / "c" / "deep_entry"
        data = b"deep data"

        HttpCache.write(deep_path, data)

        assert deep_path.exists()
        assert deep_path.read_bytes() == data

    def test_write_no_temp_file_left(self, tmp_path):
        cache_path = tmp_path / "write_entry"
        data = b"no temp residue"

        HttpCache.write(cache_path, data)

        temp_files = list(tmp_path.rglob("*.tmp"))
        assert len(temp_files) == 0


# ---------------------------------------------------------------------------
# _with_retry decorator
# ---------------------------------------------------------------------------


class TestWithRetry:
    """Tests for _with_retry decorator."""

    def test_success_on_first_attempt(self):
        """Should succeed without retry on first attempt."""

        @_with_retry(max_retries=2, retry_delay=0.01, backoff=1.0)
        def func():
            return "success"

        result = func()
        assert result == "success"

    def test_retries_then_succeeds(self, mocker):
        """Should retry and then succeed."""
        mocker.patch("time.sleep")
        mock_func = MagicMock()
        mock_func.side_effect = [
            URLError("fail"),
            URLError("fail"),
            "success",
        ]

        @_with_retry(max_retries=2, retry_delay=0.01, backoff=1.0)
        def func():
            return mock_func()

        result = func()
        assert result == "success"
        assert mock_func.call_count == 3

    def test_raises_after_max_retries(self, mocker):
        """Should raise the last exception after exhausting retries."""
        mocker.patch("time.sleep")

        @_with_retry(max_retries=2, retry_delay=0.01, backoff=1.0)
        def func():
            raise URLError("persistent failure")

        with pytest.raises(URLError, match="persistent failure"):
            func()

    def test_non_retryable_exception_propagates(self):
        """Non-retryable exception should propagate immediately."""

        @_with_retry(max_retries=2, retry_delay=0.01, backoff=1.0)
        def func():
            raise ValueError("non-retryable")

        with pytest.raises(ValueError, match="non-retryable"):
            func()

    def test_logs_warning_on_retry(self, mocker):
        """Should log warning on each retry attempt."""
        mocker.patch("time.sleep")
        mock_logger = mocker.patch("mvmctl.utils.http.logger")

        @_with_retry(max_retries=1, retry_delay=0.01, backoff=1.0)
        def func():
            raise URLError("transient")

        with pytest.raises(URLError):
            func()

        mock_logger.warning.assert_called_once()
        mock_logger.error.assert_called_once()

    def test_delay_increases_with_backoff(self, mocker):
        """Should increase delay with backoff factor."""
        mock_sleep = mocker.patch("time.sleep")

        @_with_retry(max_retries=3, retry_delay=1.0, backoff=2.0)
        def func():
            raise URLError("fail")

        with pytest.raises(URLError):
            func()

        # Should sleep for 1.0, 2.0, 4.0 seconds
        assert mock_sleep.call_count == 3
        assert mock_sleep.call_args_list[0][0][0] == 1.0
        assert mock_sleep.call_args_list[1][0][0] == 2.0
        assert mock_sleep.call_args_list[2][0][0] == 4.0


# ---------------------------------------------------------------------------
# read_json_content
# ---------------------------------------------------------------------------


class TestReadJsonContent:
    """Tests for HttpDownload.read_json_content()."""

    @patch("mvmctl.utils.http.HttpDownload._download")
    def test_success_returns_dict(self, mock_download):
        mock_download.return_value = b'{"key": "value", "num": 42}'

        result = HttpDownload.read_json_content("https://example.com/data.json")

        assert result == {"key": "value", "num": 42}

    @patch("mvmctl.utils.http.HttpDownload._download")
    def test_success_returns_list(self, mock_download):
        mock_download.return_value = b'["a", "b", "c"]'

        result = HttpDownload.read_json_content("https://example.com/list.json")

        assert result == ["a", "b", "c"]

    @patch("mvmctl.utils.http.HttpDownload._download")
    def test_json_decode_error_raises(self, mock_download):
        mock_download.return_value = b"not valid json"

        with pytest.raises(HttpDownloadError, match="Failed to parse JSON"):
            HttpDownload.read_json_content("https://example.com/bad.json")

    @patch("mvmctl.utils.http.HttpDownload._download")
    def test_passes_accept_header(self, mock_download):
        mock_download.return_value = b"{}"

        HttpDownload.read_json_content("https://example.com/data.json")

        _, kwargs = mock_download.call_args
        assert "headers" in kwargs
        assert kwargs["headers"] == {"Accept": "application/json"}


# ---------------------------------------------------------------------------
# download_file — additional paths (silent, interactive, etc.)
# ---------------------------------------------------------------------------


class TestDownloadFileExtended:
    """Additional tests for HttpDownload.download_file() uncovered paths."""

    @staticmethod
    def _fake_download(full_data: bytes):
        def _inner(url, dest_path, **kwargs):
            dest_path.write_bytes(full_data)
            pc = kwargs.get("progress_callback")
            if pc:
                pc(full_data)
            return len(full_data)

        return _inner

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_silent_missing_checksum(self, mock_with_download, tmp_path):
        """Should download without checksum when silent_missing_checksum=True."""
        dest = tmp_path / "target.bin"
        full_data = b"silent data"
        mock_with_download.side_effect = self._fake_download(full_data)

        result = HttpDownload.download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=None,
            allow_missing_checksum=True,
            silent_missing_checksum=True,
        )

        assert result is True
        assert dest.read_bytes() == full_data

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_without_checksum_fails_when_disallowed(
        self, mock_with_download, tmp_path
    ):
        """Should raise when no checksum and allow_missing_checksum=False."""
        dest = tmp_path / "target.bin"

        with pytest.raises(HttpDownloadError, match="No checksum"):
            HttpDownload.download_file(
                "https://example.com/file.bin",
                dest,
                expected_sha256=None,
                allow_missing_checksum=False,
            )

        mock_with_download.assert_not_called()

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_missing_checksum_interactive_confirm(
        self, mock_with_download, tmp_path, mocker
    ):
        """Should proceed when user confirms in interactive mode."""
        dest = tmp_path / "target.bin"
        full_data = b"interactive confirm data"
        mock_with_download.side_effect = self._fake_download(full_data)
        mocker.patch("sys.stdin.isatty", return_value=True)
        mocker.patch("typer.confirm", return_value=True)

        result = HttpDownload.download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=None,
            allow_missing_checksum=True,
        )

        assert result is True
        assert dest.read_bytes() == full_data

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_missing_checksum_interactive_cancel(
        self, mock_with_download, tmp_path, mocker
    ):
        """Should raise when user declines in interactive mode."""
        dest = tmp_path / "target.bin"
        mocker.patch("sys.stdin.isatty", return_value=True)
        mocker.patch("typer.confirm", return_value=False)

        with pytest.raises(HttpDownloadError, match="Download cancelled"):
            HttpDownload.download_file(
                "https://example.com/file.bin",
                dest,
                expected_sha256=None,
                allow_missing_checksum=True,
            )

        mock_with_download.assert_not_called()

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_missing_checksum_non_interactive(
        self, mock_with_download, tmp_path, mocker
    ):
        """Should raise when not a tty and allow_missing_checksum=True."""
        dest = tmp_path / "target.bin"
        mocker.patch("sys.stdin.isatty", return_value=False)

        with pytest.raises(HttpDownloadError, match="non-interactive"):
            HttpDownload.download_file(
                "https://example.com/file.bin",
                dest,
                expected_sha256=None,
                allow_missing_checksum=True,
            )

        mock_with_download.assert_not_called()

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_with_expected_sha256_matching(self, mock_with_download, tmp_path):
        """Should verify checksum and return True on match."""
        dest = tmp_path / "target.bin"
        full_data = b"verify me"
        expected_sha256 = hashlib.sha256(full_data).hexdigest()
        mock_with_download.side_effect = self._fake_download(full_data)

        result = HttpDownload.download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=expected_sha256,
        )

        assert result is True
        assert dest.read_bytes() == full_data

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_with_expected_sha256_mismatch(self, mock_with_download, tmp_path):
        """Should raise ChecksumMismatchError and clean up on mismatch."""
        dest = tmp_path / "target.bin"
        full_data = b"mismatch data"
        wrong_hash = "0" * 64
        mock_with_download.side_effect = self._fake_download(full_data)

        with pytest.raises(ChecksumMismatchError):
            HttpDownload.download_file(
                "https://example.com/file.bin",
                dest,
                expected_sha256=wrong_hash,
            )

        assert not dest.exists()


# ---------------------------------------------------------------------------
# HttpCache — extended coverage: write exception cleanup
# ---------------------------------------------------------------------------


class TestHttpCacheExtended:
    """Extended tests for HttpCache uncovered paths."""

    def test_write_raises_cleans_up_temp(self, mocker, tmp_path):
        """Should clean up temp file when os.write fails."""
        mocker.patch(
            "mvmctl.utils.http.os.write", side_effect=OSError("disk full")
        )
        cache_path = tmp_path / "cache_entry"
        with pytest.raises(OSError, match="disk full"):
            HttpCache.write(cache_path, b"some data")
        temp_files = list(tmp_path.rglob("*.tmp"))
        assert len(temp_files) == 0


# ---------------------------------------------------------------------------
# _with_retry — extended: exception chaining
# ---------------------------------------------------------------------------


class TestWithRetryExtended:
    """Extended tests for _with_retry uncovered paths."""

    def test_raises_http_download_error_on_none_exception(self, mocker):
        """Should raise HttpDownloadError when last_exception is None."""
        mocker.patch("time.sleep")

        @_with_retry(
            max_retries=2,
            retry_delay=0.01,
            backoff=1.0,
            retryable_exceptions=(),
        )
        def func():
            raise HttpDownloadError("Generic download failure")

        with pytest.raises(HttpDownloadError, match="Generic download failure"):
            func()


# ---------------------------------------------------------------------------
# _urlopen — direct test
# ---------------------------------------------------------------------------


class TestUrlOpen:
    """Tests for HttpDownload._urlopen()."""

    def test_urlopen_calls_opener(self, mocker):
        """Should delegate to shared opener."""
        mock_opener = mocker.patch("mvmctl.utils.http._http_opener.open")
        req = object()
        HttpDownload._urlopen(req, timeout=42)
        mock_opener.assert_called_once_with(req, timeout=42)


# ---------------------------------------------------------------------------
# _download — use_cache paths
# ---------------------------------------------------------------------------


class TestDownloadCache:
    """Tests for HttpDownload._download() with cache."""

    def test_download_cache_hit(self, mocker):
        """Should return cached response when cache is valid."""
        mocker.patch.object(
            HttpCache, "_cache_path", return_value=Path("/tmp/cached")
        )
        mocker.patch.object(HttpCache, "is_valid", return_value=True)
        mocker.patch.object(HttpCache, "read", return_value=b"cached response")

        result = HttpDownload._download(
            "https://example.com/data", use_cache=True
        )
        assert result == b"cached response"

    def test_download_cache_miss_writes(self, mocker):
        """Should fetch and write to cache on cache miss."""
        mock_cache_path = Path("/tmp/cached")
        mocker.patch.object(
            HttpCache, "_cache_path", return_value=mock_cache_path
        )
        mocker.patch.object(HttpCache, "is_valid", return_value=False)
        mock_write = mocker.patch.object(HttpCache, "write")
        mock_urlopen = mocker.patch("mvmctl.utils.http.HttpDownload._urlopen")
        mock_response = MagicMock()
        mock_response.read.return_value = b"fresh data"
        mock_response.__enter__ = MagicMock(return_value=mock_response)
        mock_response.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_response

        result = HttpDownload._download(
            "https://example.com/data", use_cache=True
        )
        assert result == b"fresh data"
        mock_write.assert_called_once_with(mock_cache_path, b"fresh data")


# ---------------------------------------------------------------------------
# head_size — extended: use_cache=False, no cache write
# ---------------------------------------------------------------------------


class TestHeadSizeExtended:
    """Extended tests for HttpDownload.head_size() uncovered paths."""

    def test_head_size_no_cache_skip(self, mocker):
        """Should skip cache entirely when use_cache=False (covers 231->239, 246->252)."""
        mock_urlopen = mocker.patch("mvmctl.utils.http.HttpDownload._urlopen")
        mock_response = MagicMock()
        mock_response.headers.get.return_value = "3000"
        mock_response.__enter__ = MagicMock(return_value=mock_response)
        mock_response.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_response

        size = HttpDownload.head_size(
            "https://example.com/file", use_cache=False
        )
        assert size == 3000


# ---------------------------------------------------------------------------
# read_raw_content
# ---------------------------------------------------------------------------


class TestReadRawContent:
    """Tests for HttpDownload.read_raw_content()."""

    @patch("mvmctl.utils.http.HttpDownload._download")
    def test_read_raw_content_success(self, mock_download):
        """Should return decoded string."""
        mock_download.return_value = b"raw text content"
        result = HttpDownload.read_raw_content("https://example.com/data.txt")
        assert result == "raw text content"

    @patch("mvmctl.utils.http.HttpDownload._download")
    def test_read_raw_content_with_headers(self, mock_download):
        """Should pass custom headers."""
        mock_download.return_value = b"data"
        HttpDownload.read_raw_content(
            "https://example.com/data.txt",
            headers={"X-Custom": "value"},
        )
        _, kwargs = mock_download.call_args
        headers = kwargs["headers"]
        assert headers["Accept"] == "text/plain"
        assert headers["X-Custom"] == "value"

    @patch("mvmctl.utils.http.HttpDownload._download")
    def test_read_raw_content_with_cache(self, mock_download):
        """Should pass use_cache and cache_ttl."""
        mock_download.return_value = b"cached text"
        HttpDownload.read_raw_content(
            "https://example.com/data.txt",
            use_cache=True,
            cache_ttl_seconds=600,
        )
        _, kwargs = mock_download.call_args
        assert kwargs["use_cache"] is True
        assert kwargs["cache_ttl_seconds"] == 600


# ---------------------------------------------------------------------------
# with_download — extended: OSError errno paths
# ---------------------------------------------------------------------------


class TestWithDownloadExtended:
    """Extended tests for HttpDownload.with_download() uncovered paths."""

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_oserror_errno_122(self, mock_urlopen, tmp_path):
        """Should map ENOSPC (errno 122) to helpful message."""
        mock_urlopen.side_effect = OSError(122, "Disk quota exceeded")
        dest = tmp_path / "target.bin"
        with pytest.raises(HttpDownloadError, match="No storage available"):
            HttpDownload.with_download("https://example.com/file", dest)

    @patch("mvmctl.utils.http.HttpDownload._urlopen")
    def test_oserror_other_errno(self, mock_urlopen, tmp_path):
        """Should map unknown OSError to I/O error."""
        mock_urlopen.side_effect = OSError(5, "Input/output error")
        dest = tmp_path / "target.bin"
        with pytest.raises(HttpDownloadError, match="I/O error"):
            HttpDownload.with_download("https://example.com/file", dest)


# ---------------------------------------------------------------------------
# download_file — extended: progress bar _on_start callback
# ---------------------------------------------------------------------------


class TestDownloadFileProgressBar:
    """Extended tests for HttpDownload.download_file() progress bar path."""

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_download_calls_on_start(self, mock_with_download, tmp_path: Path):
        """Should invoke _on_start and _progress_callback with progress bar."""
        dest = tmp_path / "target.bin"
        full_data = b"progress bar test data"
        expected_sha256 = hashlib.sha256(full_data).hexdigest()

        def _fake_download(url, dest_path, **kwargs):
            dest_path.write_bytes(full_data)
            on_start = kwargs.get("on_start")
            if on_start:
                on_start(len(full_data))
            pc = kwargs.get("progress_callback")
            if pc:
                pc(full_data)
            return len(full_data)

        mock_with_download.side_effect = _fake_download

        result = HttpDownload.download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=expected_sha256,
        )
        assert result is True
        assert dest.read_bytes() == full_data
