"""Tests for unified download engine."""

from pathlib import Path
from unittest.mock import MagicMock, Mock, patch

import pytest

from mvmctl.core.download_engine import DownloadEngine
from mvmctl.exceptions import ChecksumMismatchError, DownloadError


class TestDownloadEngineInit:
    """Tests for DownloadEngine initialization."""

    def test_uses_default_temp_dir(self, tmp_path, monkeypatch):
        """Engine uses /tmp/mvmctl as default temp dir."""
        monkeypatch.setenv("MVM_TEMP_DIR", str(tmp_path / "mvmctl"))
        engine = DownloadEngine()
        assert engine.temp_dir == tmp_path / "mvmctl"

    def test_uses_mvm_temp_dir_override(self, tmp_path, monkeypatch):
        """Engine respects MVM_TEMP_DIR environment variable."""
        custom_dir = tmp_path / "custom"
        monkeypatch.setenv("MVM_TEMP_DIR", str(custom_dir))
        engine = DownloadEngine()
        assert engine.temp_dir == custom_dir

    def test_creates_temp_dir_if_missing(self, tmp_path):
        """Engine creates temp directory if it doesn't exist."""
        new_dir = tmp_path / "new_temp"
        engine = DownloadEngine(temp_dir=new_dir)
        assert new_dir.exists()
        assert engine.temp_dir == new_dir

    def test_uses_provided_temp_dir(self, tmp_path):
        """Engine uses explicitly provided temp directory."""
        provided = tmp_path / "provided"
        engine = DownloadEngine(temp_dir=provided)
        assert engine.temp_dir == provided


class TestDownloadEngineDownload:
    """Tests for DownloadEngine.download method."""

    def _create_mock_response(self, data: bytes, status: int = 200, headers: dict | None = None):
        """Create a mock response that supports context manager protocol."""
        mock_response = MagicMock()
        mock_response.status = status
        mock_response.headers = headers or {}
        mock_response.read.side_effect = list(self._chunk_data(data)) + [b""]
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        return mock_response

    def _chunk_data(self, data: bytes, chunk_size: int = 1024):
        """Split data into chunks."""
        for i in range(0, len(data), chunk_size):
            yield data[i : i + chunk_size]

    def test_downloads_file_successfully(self, tmp_path):
        """Engine downloads file to destination."""
        engine = DownloadEngine(temp_dir=tmp_path / "temp")
        dest = tmp_path / "output.bin"

        mock_response = self._create_mock_response(b"data", headers={"Content-Length": "4"})

        with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
            result = engine.download("http://example.com/file", dest, progress=False)

        assert result == dest
        assert dest.exists()
        assert dest.read_bytes() == b"data"

    def test_verifies_sha256_checksum(self, tmp_path):
        """Engine verifies SHA256 checksum after download."""
        engine = DownloadEngine(temp_dir=tmp_path / "temp")
        dest = tmp_path / "output.bin"
        data = b"test data"
        wrong_hash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

        mock_response = self._create_mock_response(data)

        with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
            with pytest.raises(ChecksumMismatchError):
                engine.download(
                    "http://example.com/file", dest, expected_sha256=wrong_hash, progress=False
                )

    def test_cleans_up_on_failure(self, tmp_path):
        """Engine cleans up temp files on download failure."""
        from urllib.error import URLError

        engine = DownloadEngine(temp_dir=tmp_path / "temp")
        dest = tmp_path / "output.bin"

        call_count = [0]

        def side_effect(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                # First call is HEAD request (ignored)
                mock_head = MagicMock()
                mock_head.headers = {}
                mock_head.__enter__ = Mock(return_value=mock_head)
                mock_head.__exit__ = Mock(return_value=False)
                return mock_head
            # Second call is actual download (fails)
            raise URLError("Network error")

        with patch(
            "mvmctl.utils.http.urlopen",
            side_effect=side_effect,
        ):
            with pytest.raises(DownloadError):
                engine.download("http://example.com/file", dest, progress=False)

        # Temp files should be cleaned up
        assert not any((tmp_path / "temp").glob("*.tmp"))

    def test_resumes_partial_download(self, tmp_path):
        """Engine resumes partial downloads using HTTP Range."""
        engine = DownloadEngine(temp_dir=tmp_path / "temp")
        dest = tmp_path / "output.bin"
        part_file = engine.temp_dir / f"{dest.name}.part"

        # Create partial file
        part_file.parent.mkdir(parents=True, exist_ok=True)
        part_file.write_bytes(b"partial")

        mock_response = self._create_mock_response(
            b" data", status=206, headers={"Content-Range": "bytes 7-11/12"}
        )

        with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
            result = engine.download("http://example.com/file", dest, resume=True, progress=False)

        assert result == dest
        assert dest.read_bytes() == b"partial data"

    def test_atomic_move_on_success(self, tmp_path):
        """Engine atomically moves file from temp to dest on success."""
        engine = DownloadEngine(temp_dir=tmp_path / "temp")
        dest = tmp_path / "output.bin"

        mock_response = self._create_mock_response(b"data")

        with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
            engine.download("http://example.com/file", dest, progress=False)

        # File should exist at destination
        assert dest.exists()
        assert dest.read_bytes() == b"data"

    def test_shows_progress_when_enabled(self, tmp_path):
        """Engine shows progress bar when progress=True."""
        engine = DownloadEngine(temp_dir=tmp_path / "temp")
        dest = tmp_path / "output.bin"

        mock_response = self._create_mock_response(b"data", headers={"Content-Length": "4"})
        mock_progress = Mock()

        with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
            with patch(
                "mvmctl.core.download_engine.ASCIIProgressBar",
                return_value=mock_progress,
            ):
                engine.download("http://example.com/file", dest, progress=True)

        mock_progress.update.assert_called()
        mock_progress.finish.assert_called_once()

    def test_skips_progress_when_disabled(self, tmp_path):
        """Engine skips progress bar updates when progress=False."""
        engine = DownloadEngine(temp_dir=tmp_path / "temp")
        dest = tmp_path / "output.bin"

        mock_response = self._create_mock_response(b"data")
        mock_progress = Mock()

        with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
            with patch(
                "mvmctl.core.download_engine.ASCIIProgressBar",
                return_value=mock_progress,
            ):
                engine.download("http://example.com/file", dest, progress=False)

        # Progress bar should be created but not updated when progress=False
        mock_progress.update.assert_not_called()
        mock_progress.finish.assert_not_called()


class TestDownloadEngineCleanup:
    """Tests for DownloadEngine.cleanup method."""

    def test_removes_orphaned_tmp_files(self, tmp_path):
        """Cleanup removes orphaned .tmp files."""
        engine = DownloadEngine(temp_dir=tmp_path)
        (tmp_path / "file1.tmp").write_text("orphan")
        (tmp_path / "file2.tmp").write_text("orphan")

        engine.cleanup()

        assert not (tmp_path / "file1.tmp").exists()
        assert not (tmp_path / "file2.tmp").exists()

    def test_removes_orphaned_part_files(self, tmp_path):
        """Cleanup removes orphaned .part files."""
        engine = DownloadEngine(temp_dir=tmp_path)
        (tmp_path / "file1.part").write_text("partial")
        (tmp_path / "file2.part").write_text("partial")

        engine.cleanup()

        assert not (tmp_path / "file1.part").exists()
        assert not (tmp_path / "file2.part").exists()

    def test_handles_cleanup_errors_gracefully(self, tmp_path):
        """Cleanup handles file removal errors gracefully."""
        engine = DownloadEngine(temp_dir=tmp_path)
        (tmp_path / "file.tmp").write_text("data")

        # Should not raise even if files can't be removed
        with patch.object(Path, "unlink", side_effect=OSError("Permission denied")):
            engine.cleanup()


class TestDownloadEngineRespectsEnvVar:
    """Tests for MVM_TEMP_DIR environment variable handling."""

    def test_uses_mvm_temp_dir_when_set(self, tmp_path, monkeypatch):
        """Engine uses MVM_TEMP_DIR when environment variable is set."""
        custom_dir = tmp_path / "custom_temp"
        monkeypatch.setenv("MVM_TEMP_DIR", str(custom_dir))

        engine = DownloadEngine()
        assert engine.temp_dir == custom_dir

    def test_uses_default_when_mvm_temp_dir_unset(self, monkeypatch):
        """Engine uses /tmp/mvmctl when MVM_TEMP_DIR is not set."""
        monkeypatch.delenv("MVM_TEMP_DIR", raising=False)

        engine = DownloadEngine()
        assert engine.temp_dir == Path("/tmp/mvmctl")
