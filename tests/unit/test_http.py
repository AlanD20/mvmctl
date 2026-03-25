"""Tests for HTTP download utilities."""

import hashlib
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.utils.http import download_file
from mvmctl.exceptions import ChecksumMismatchError, MVMError


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
# Resume functionality tests
# ---------------------------------------------------------------------------


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_with_206_partial_content(mock_urlopen: MagicMock, tmp_path: Path):
    """Test successful resume when server returns 206 Partial Content."""
    # First part of file already exists
    dest = tmp_path / "partial_file.bin"
    existing_data = b"Hello, "
    new_data = b"World!"
    full_data = existing_data + new_data
    dest.write_bytes(existing_data)

    # Mock response with 206 status and Content-Range header
    mock_urlopen.return_value = _mock_urlopen_response(
        new_data,
        status=206,
        content_range=f"bytes {len(existing_data)}-{len(full_data) - 1}/{len(full_data)}",
    )

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=hashlib.sha256(full_data).hexdigest(),
        show_progress=False,
        resume=True,
    )

    assert result is True
    assert dest.read_bytes() == full_data

    # Verify Range header was sent
    call_args = mock_urlopen.call_args
    request = call_args[0][0]
    assert request.headers.get("Range") == f"bytes={len(existing_data)}-"


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_fallback_to_200(mock_urlopen: MagicMock, tmp_path: Path):
    """Test fallback to full download when server doesn't support resume (returns 200)."""
    # First part of file already exists
    dest = tmp_path / "partial_file.bin"
    existing_data = b"Old data that should be replaced"
    full_data = b"New complete data"
    dest.write_bytes(existing_data)

    # Mock response with 200 status (server doesn't support resume)
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
        resume=True,
    )

    assert result is True
    # File should be replaced with new data, not appended
    assert dest.read_bytes() == full_data


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_no_existing_file(mock_urlopen: MagicMock, tmp_path: Path):
    """Test resume when no partial file exists (starts fresh download)."""
    dest = tmp_path / "new_file.bin"
    full_data = b"Complete file content"

    # File doesn't exist yet
    assert not dest.exists()

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
        resume=True,
    )

    assert result is True
    assert dest.read_bytes() == full_data

    # Verify no Range header was sent (no partial file)
    call_args = mock_urlopen.call_args
    request = call_args[0][0]
    assert request.headers.get("Range") is None


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_disabled(mock_urlopen: MagicMock, tmp_path: Path):
    """Test that resume=False ignores existing partial file."""
    dest = tmp_path / "partial_file.bin"
    existing_data = b"Old partial data"
    new_data = b"Fresh download"
    dest.write_bytes(existing_data)

    # With resume=False, file should be overwritten
    mock_urlopen.return_value = _mock_urlopen_response(
        new_data,
        status=200,
        content_length=str(len(new_data)),
    )

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=hashlib.sha256(new_data).hexdigest(),
        show_progress=False,
        resume=False,  # Resume disabled
    )

    assert result is True
    # File should be overwritten, not appended
    assert dest.read_bytes() == new_data


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_checksum_verification(mock_urlopen: MagicMock, tmp_path: Path):
    """Test checksum verification works correctly with resumed downloads."""
    dest = tmp_path / "partial_file.bin"
    existing_data = b"First part"
    new_data = b" Second part"
    full_data = existing_data + new_data
    dest.write_bytes(existing_data)

    mock_urlopen.return_value = _mock_urlopen_response(
        new_data,
        status=206,
        content_range=f"bytes {len(existing_data)}-{len(full_data) - 1}/{len(full_data)}",
    )

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=hashlib.sha256(full_data).hexdigest(),
        show_progress=False,
        resume=True,
    )

    assert result is True
    assert dest.read_bytes() == full_data


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_checksum_mismatch(mock_urlopen: MagicMock, tmp_path: Path):
    """Test checksum mismatch is detected with resumed downloads."""
    dest = tmp_path / "partial_file.bin"
    existing_data = b"First part"
    new_data = b" Wrong second part"
    full_data = existing_data + new_data
    dest.write_bytes(existing_data)

    mock_urlopen.return_value = _mock_urlopen_response(
        new_data,
        status=206,
        content_range=f"bytes {len(existing_data)}-{len(full_data) - 1}/{len(full_data)}",
    )

    with pytest.raises(ChecksumMismatchError):
        download_file(
            "https://example.com/file.bin",
            dest,
            expected_sha256="0000000000000000000000000000000000000000000000000000000000000000",
            show_progress=False,
            resume=True,
        )

    # Original file should remain intact on checksum mismatch (atomic rename pattern)
    assert dest.exists()
    assert dest.read_bytes() == existing_data
    # No temp file should remain
    temp_files = list(tmp_path.glob(".*.tmp"))
    assert len(temp_files) == 0


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_empty_existing_file(mock_urlopen: MagicMock, tmp_path: Path):
    """Test resume with empty existing file (starts from beginning)."""
    dest = tmp_path / "empty_file.bin"
    dest.write_bytes(b"")  # Empty file

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
        resume=True,
    )

    assert result is True
    assert dest.read_bytes() == full_data


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_with_content_length(mock_urlopen: MagicMock, tmp_path: Path):
    """Test resume with Content-Length header from 200 response."""
    dest = tmp_path / "partial_file.bin"
    existing_data = b"Partial "
    new_data = b"data"
    full_data = existing_data + new_data
    dest.write_bytes(existing_data)

    # Server doesn't support resume (returns 200), but provides Content-Length
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
        resume=True,
    )

    assert result is True
    # Should get full data (overwritten, not appended due to 200 response)
    assert dest.read_bytes() == full_data


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_progress_counting(mock_urlopen: MagicMock, tmp_path: Path):
    """Test that progress calculation includes already downloaded bytes."""
    dest = tmp_path / "partial_file.bin"
    existing_data = b"Existing content"
    new_data = b" plus new content"
    full_data = existing_data + new_data
    dest.write_bytes(existing_data)

    mock_urlopen.return_value = _mock_urlopen_response(
        new_data,
        status=206,
        content_range=f"bytes {len(existing_data)}-{len(full_data) - 1}/{len(full_data)}",
    )

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=hashlib.sha256(full_data).hexdigest(),
        show_progress=False,
        resume=True,
    )

    assert result is True
    # Verify the file has the complete data
    assert dest.read_bytes() == full_data


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
        resume=False,
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


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_200_with_existing_file(mock_urlopen: MagicMock, tmp_path: Path):
    dest = tmp_path / "existing_file.bin"
    old_data = b"Old content that should be replaced"
    new_data = b"New content from server"
    dest.write_bytes(old_data)

    mock_urlopen.return_value = _mock_urlopen_response(
        new_data,
        status=200,
        content_length=str(len(new_data)),
    )

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=hashlib.sha256(new_data).hexdigest(),
        show_progress=False,
        resume=True,
    )

    assert result is True
    assert dest.read_bytes() == new_data


@patch("mvmctl.utils.http.urlopen")
def test_download_file_resume_partial_file_cleanup(mock_urlopen: MagicMock, tmp_path: Path):
    dest = tmp_path / "target_file.bin"
    partial_path = tmp_path / ".target_file.bin.part"

    existing_data = b"Partial content"
    new_data = b" plus more"
    full_data = existing_data + new_data

    partial_path.write_bytes(existing_data)

    mock_urlopen.return_value = _mock_urlopen_response(
        new_data,
        status=206,
        content_range=f"bytes {len(existing_data)}-{len(full_data) - 1}/{len(full_data)}",
    )

    result = download_file(
        "https://example.com/file.bin",
        dest,
        expected_sha256=hashlib.sha256(full_data).hexdigest(),
        show_progress=False,
        resume=True,
    )

    assert result is True
    assert dest.read_bytes() == full_data
    assert not partial_path.exists()
