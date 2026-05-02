"""Tests for utils/http.py — HTTP download utilities."""

from __future__ import annotations

import hashlib
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import ChecksumMismatchError, HttpDownloadError
from mvmctl.utils.http import HttpDownload


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
            progress_bar=False,
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
                progress_bar=False,
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
                progress_bar=False,
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
            progress_bar=False,
        )

        assert result is True
        assert dest.read_bytes() == full_data

    @patch("mvmctl.utils.http.HttpDownload.with_download")
    def test_with_progress_bar(self, mock_with_download, tmp_path: Path):
        """Should work with progress bar enabled."""
        dest = tmp_path / "target.bin"
        full_data = b"Complete file content for progress test"
        expected_sha256 = hashlib.sha256(full_data).hexdigest()

        mock_with_download.side_effect = self._make_fake_download(full_data)

        result = HttpDownload.download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=expected_sha256,
            progress_bar=True,
        )

        assert result is True
        assert dest.read_bytes() == full_data


# ---------------------------------------------------------------------------
# with_download — pure transport entry point
# ---------------------------------------------------------------------------


class TestWithDownload:
    """Tests for HttpDownload.with_download()."""

    @staticmethod
    def _setup_mock_urlopen(mock_urlopen, data, status=200, content_length=None):
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
