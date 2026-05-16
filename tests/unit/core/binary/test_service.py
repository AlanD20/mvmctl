"""Tests for BinaryService with mocked subprocess and HTTP."""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._service import BinaryService
from mvmctl.exceptions import BinaryError
from mvmctl.models import BinaryItem


class TestListRemoteVersions:
    """Tests for BinaryService.list_remote()."""

    def test_list_remote_success(self) -> None:
        """list_remote fetches and returns versions from GitHub."""
        releases = [{"tag_name": "v1.5.0"}, {"tag_name": "v1.4.0"}]
        mock_response = MagicMock()
        mock_response.read.return_value = json.dumps(releases).encode()

        with patch(
            "mvmctl.utils.http.HttpDownload.read_json_content",
            return_value=releases,
        ):
            result = BinaryService.list_remote(limit=5)

        assert result == ["1.5.0", "1.4.0"]

    def test_list_remote_strips_v_prefix(self) -> None:
        """list_remote normalizes version strings by stripping 'v' prefix."""
        releases = [{"tag_name": "v2.0.0"}]
        with patch(
            "mvmctl.utils.http.HttpDownload.read_json_content",
            return_value=releases,
        ):
            result = BinaryService.list_remote(limit=5)
        assert result == ["2.0.0"]

    def test_list_remote_skips_non_string_tags(self) -> None:
        """list_remote skips releases without valid tag_name."""
        releases = [
            {"tag_name": "v1.0.0"},
            {"tag_name": None},
            {"other_key": "v2.0.0"},
        ]
        with patch(
            "mvmctl.utils.http.HttpDownload.read_json_content",
            return_value=releases,
        ):
            result = BinaryService.list_remote(limit=5)
        assert result == ["1.0.0"]

    def test_list_remote_network_error(self) -> None:
        """list_remote raises BinaryError on network failure."""
        from mvmctl.exceptions import HttpDownloadError

        with patch(
            "mvmctl.utils.http.HttpDownload.read_json_content",
            side_effect=HttpDownloadError("connection error"),
        ):
            with pytest.raises(BinaryError, match="GitHub"):
                BinaryService.list_remote(limit=5)

    def test_list_remote_sorts_by_version(self) -> None:
        """list_remote returns versions sorted newest first."""
        releases = [
            {"tag_name": "v1.0.0"},
            {"tag_name": "v2.0.0"},
            {"tag_name": "v1.5.0"},
        ]
        with patch(
            "mvmctl.utils.http.HttpDownload.read_json_content",
            return_value=releases,
        ):
            result = BinaryService.list_remote(limit=5)
        assert result == ["2.0.0", "1.5.0", "1.0.0"]


class TestDownloadFirecracker:
    """Tests for BinaryService.download_firecracker()."""

    def test_download_and_extract(self, tmp_path: Path) -> None:
        """download_firecracker downloads, extracts, and returns BinaryItems."""
        import hashlib
        import io
        import tarfile

        # Create a fake tarball
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w:gz") as tar:
            for name in ("firecracker-v1.5.0-x86_64", "jailer-v1.5.0-x86_64"):
                content = b"#!/bin/sh\necho fake\n"
                info = tarfile.TarInfo(name=f"release-v1.5.0-x86_64/{name}")
                info.size = len(content)
                tar.addfile(info, io.BytesIO(content))
        tarball_data = buf.getvalue()
        expected_sha = hashlib.sha256(tarball_data).hexdigest()

        with (
            patch(
                "mvmctl.utils.http.HttpDownload.read_raw_content",
                return_value=f"{expected_sha}  firecracker-v1.5.0-x86_64.tgz\n",
            ),
            patch(
                "mvmctl.utils.http.HttpDownload.download_file"
            ) as mock_download,
        ):
            # Write tarball so the extraction path works
            tgz_path = tmp_path / "firecracker-v1.5.0-x86_64.tgz"
            tgz_path.write_bytes(tarball_data)

            def _fake_download(url, path, **kwargs):
                path.write_bytes(tarball_data)

            mock_download.side_effect = _fake_download

            from mvmctl.core.binary._service import BinaryService as BS

            results = BS.download_firecracker("1.5.0", tmp_path)

        assert len(results) == 2
        assert results[0].name == "firecracker"
        assert results[1].name == "jailer"
        assert results[0].version == "1.5.0"
        assert (tmp_path / "firecracker-v1.5.0").exists()
        assert (tmp_path / "jailer-v1.5.0").exists()

    def test_checksum_missing_raises(self, tmp_path: Path) -> None:
        """download_firecracker raises BinaryError when SHA256 sidecar unavailable."""
        with patch(
            "mvmctl.utils.http.HttpDownload.read_raw_content",
            side_effect=OSError("404"),
        ):
            with pytest.raises(BinaryError, match="Checksum required"):
                BinaryService.download_firecracker("1.5.0", tmp_path)


class TestListAll:
    """Tests for BinaryService.list_all()."""

    def test_list_all_empty(self, db: Database) -> None:
        """list_all returns empty list when no binaries."""
        repo = BinaryRepository(db)
        service = BinaryService(repo)
        assert service.list_all() == []

    def test_list_all_verify_true(self, db: Database) -> None:
        """list_all with verify=True checks filesystem."""
        from datetime import UTC, datetime

        now = datetime.now(tz=UTC).isoformat()
        repo = BinaryRepository(db)
        binary = BinaryItem(
            id="test-id",
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="v1.15",
            path="firecracker-v1.15.0",
            is_default=False,
            is_present=True,
            created_at=now,
            updated_at=now,
        )
        repo.upsert(binary)
        service = BinaryService(repo)
        # File doesn't exist, so is_present should be updated to 0
        # (but list_all still returns the binary since it's not soft-deleted)
        results = service.list_all(verify=True)
        assert len(results) == 1
        assert results[0].is_present is False or results[0].is_present == 0

    def test_list_all_no_verify(self, db: Database) -> None:
        """list_all with verify=False returns DB records as-is."""
        from datetime import UTC, datetime

        now = datetime.now(tz=UTC).isoformat()
        repo = BinaryRepository(db)
        binary = BinaryItem(
            id="test-id",
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="v1.15",
            path="firecracker-v1.15.0",
            is_default=False,
            is_present=True,
            created_at=now,
            updated_at=now,
        )
        repo.upsert(binary)
        service = BinaryService(repo)
        results = service.list_all(verify=False)
        assert len(results) == 1


class TestGetDefaultFirecracker:
    """Tests for BinaryService.get_default_firecracker()."""

    def test_get_default_firecracker(self, db: Database) -> None:
        """get_default_firecracker returns the default firecracker binary."""
        from datetime import UTC, datetime

        now = datetime.now(tz=UTC).isoformat()
        repo = BinaryRepository(db)
        repo.upsert(
            BinaryItem(
                id="fc-1",
                name="firecracker",
                version="1.15.0",
                full_version="v1.15.0",
                ci_version="v1.15",
                path="firecracker-v1.15.0",
                is_default=True,
                is_present=True,
                created_at=now,
                updated_at=now,
            )
        )
        service = BinaryService(repo)
        default = service.get_default_firecracker()
        assert default is not None
        assert default.name == "firecracker"
        assert default.version == "1.15.0"

    def test_get_default_firecracker_none(self, db: Database) -> None:
        """get_default_firecracker returns None when no default set."""
        repo = BinaryRepository(db)
        service = BinaryService(repo)
        assert service.get_default_firecracker() is None


class TestNormalizeVersion:
    """Tests for BinaryService._normalize_version()."""

    def test_normalize_version_strips_v(self) -> None:
        assert BinaryService._normalize_version("v1.0.0") == "1.0.0"

    def test_normalize_version_no_prefix(self) -> None:
        assert BinaryService._normalize_version("1.0.0") == "1.0.0"

    def test_normalize_version_empty(self) -> None:
        assert BinaryService._normalize_version("") == ""


class TestSemverKey:
    """Tests for VersionResolver.semver_key()."""

    def test_semver_key_normal(self) -> None:
        from mvmctl.core._shared import VersionResolver

        key = VersionResolver.semver_key("1.15.0")
        assert key == (1, 15, 0)

    def test_semver_key_fallback(self) -> None:
        from mvmctl.core._shared import VersionResolver

        key = VersionResolver.semver_key("invalid")
        assert key == (0,)


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied for each test."""
    database = Database()
    database.migrate()
    return database
