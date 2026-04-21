"""Tests for HTTP download utilities."""

import hashlib
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import ChecksumMismatchError, MVMError
from mvmctl.utils.http import download_file


def _mock_urlopen_response(
    data: bytes,
    content_length: str | None = None,
    status: int = 200,
    content_range: str | None = None,
):
    """Create a mock urlopen response that yields data in chunks."""
    mock_response = MagicMock()
    mock_response.status = status
    mock_response.headers.get.side_effect = lambda key, default=None: {
        "Content-Length": content_length,
        "Content-Range": content_range,
    }.get(key, default)

    chunks = [data[i : i + 8192] for i in range(0, len(data), 8192)]
    chunks.append(b"")  # EOF sentinel
    mock_response.read.side_effect = chunks

    # Support context manager
    mock_response.__enter__ = MagicMock(return_value=mock_response)
    mock_response.__exit__ = MagicMock(return_value=False)
    return mock_response


# ---------------------------------------------------------------------------
# Atomic rename pattern tests (issue #23)
# ---------------------------------------------------------------------------


@patch("mvmctl.utils.http.urlopen")
def test_download_file_uses_atomic_rename(mock_urlopen: MagicMock, tmp_path: Path):
    """Test that download uses temp file and atomic rename."""
    dest = tmp_path / "target_file.bin"
    full_data = b"Complete file content"

    mock_urlopen.return_value = _mock_urlopen_response(
        full_data,
        status=200,
        content_length=str(len(full_data)),
    )

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=hashlib.sha256(full_data).hexdigest(),
        show_progress=False,
    )

    assert result is True
    assert dest.exists()
    assert dest.read_bytes() == full_data
    # No temp file should remain
    temp_files = list(tmp_path.glob(".*.tmp"))
    assert len(temp_files) == 0


@patch("mvmctl.utils.http.urlopen")
def test_download_file_cleans_up_temp_on_error(mock_urlopen: MagicMock, tmp_path: Path):
    """Test that temp file is cleaned up on download error."""
    from urllib.error import URLError

    dest = tmp_path / "target_file.bin"

    mock_urlopen.side_effect = URLError("Network error")

    with pytest.raises(MVMError, match="Download failed"):
        download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256="abcd1234" * 8,  # Provide checksum to skip interactive check
            show_progress=False,
            allow_missing_checksum=False,
        )

    # Target file should not exist
    assert not dest.exists()
    # No temp file should remain either
    temp_files = list(tmp_path.glob(".*.tmp"))
    assert len(temp_files) == 0


@patch("mvmctl.utils.http.urlopen")
def test_download_file_cleans_up_temp_on_checksum_mismatch(mock_urlopen: MagicMock, tmp_path: Path):
    """Test that temp file is cleaned up on checksum mismatch."""
    dest = tmp_path / "target_file.bin"
    full_data = b"Complete file content"

    mock_urlopen.return_value = _mock_urlopen_response(
        full_data,
        status=200,
        content_length=str(len(full_data)),
    )

    with pytest.raises(ChecksumMismatchError):
        download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256="0000000000000000000000000000000000000000000000000000000000000000",
            show_progress=False,
        )

    # Target file should not exist
    assert not dest.exists()
    # No temp file should remain
    temp_files = list(tmp_path.glob(".*.tmp"))
    assert len(temp_files) == 0


@patch("mvmctl.utils.http.urlopen")
def test_download_file_missing_checksum_non_interactive(mock_urlopen: MagicMock, tmp_path: Path):
    dest = tmp_path / "target_file.bin"
    full_data = b"Complete file content"

    mock_urlopen.return_value = _mock_urlopen_response(
        full_data,
        status=200,
        content_length=str(len(full_data)),
    )

    with pytest.raises(MVMError, match="No checksum provided"):
        download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256=None,
            show_progress=False,
            allow_missing_checksum=False,
        )


@patch("mvmctl.utils.http.urlopen")
def test_download_file_no_content_length(mock_urlopen: MagicMock, tmp_path: Path):
    dest = tmp_path / "target_file.bin"
    full_data = b"Complete file content"

    mock_urlopen.return_value = _mock_urlopen_response(
        full_data,
        status=200,
        content_length=None,
    )

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=hashlib.sha256(full_data).hexdigest(),
        show_progress=False,
    )

    assert result is True
    assert dest.read_bytes() == full_data


class TestParseContentLength:
    """Tests for _parse_content_length."""

    def test_returns_none_for_no_get_method(self):
        from mvmctl.utils.http import _parse_content_length

        assert _parse_content_length("not an object") is None

    def test_returns_none_for_missing_header(self):
        from mvmctl.utils.http import _parse_content_length

        headers = MagicMock()
        headers.get.return_value = None
        assert _parse_content_length(headers) is None

    def test_returns_none_for_invalid_content_length(self):
        from mvmctl.utils.http import _parse_content_length

        headers = MagicMock()
        headers.get.return_value = "not-a-number"
        assert _parse_content_length(headers) is None

    def test_returns_int_for_valid_content_length(self):
        from mvmctl.utils.http import _parse_content_length

        headers = MagicMock()
        headers.get.return_value = "12345"
        assert _parse_content_length(headers) == 12345


class TestRetryDecorator:
    """Tests for _with_retry decorator."""

    def test_retries_on_failure(self):
        from mvmctl.utils.http import _with_retry

        call_count = 0

        @_with_retry(max_retries=2, retry_delay=0.01)
        def flaky_func():
            nonlocal call_count
            call_count += 1
            if call_count < 3:
                raise ConnectionError("fail")
            return "success"

        result = flaky_func()
        assert result == "success"
        assert call_count == 3

    def test_raises_after_max_retries(self):
        from mvmctl.utils.http import _with_retry

        @_with_retry(max_retries=1, retry_delay=0.01)
        def always_fail():
            raise ConnectionError("always fails")

        with pytest.raises(ConnectionError, match="always fails"):
            always_fail()

    def test_no_retry_on_success(self):
        from mvmctl.utils.http import _with_retry

        call_count = 0

        @_with_retry(max_retries=2, retry_delay=0.01)
        def succeed_first():
            nonlocal call_count
            call_count += 1
            return "ok"

        result = succeed_first()
        assert result == "ok"
        assert call_count == 1


@patch("mvmctl.utils.http.urlopen")
def test_download_file_with_progress(mock_urlopen: MagicMock, tmp_path: Path):
    dest = tmp_path / "target_file.bin"
    full_data = b"Complete file content for progress test"

    mock_urlopen.return_value = _mock_urlopen_response(
        full_data,
        status=200,
        content_length=str(len(full_data)),
    )

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=hashlib.sha256(full_data).hexdigest(),
        show_progress=True,
    )

    assert result is True
    assert dest.read_bytes() == full_data
