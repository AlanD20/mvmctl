"""Integration tests for binary operations through the real public API.

Tests exercise the complete binary orchestration flow:
  pull → list → get → set_default → ensure_default → remove

Only HTTP download operations are mocked. ALL orchestration logic in api/
and core/ runs unmocked.
"""

from __future__ import annotations

import hashlib
import io
import tarfile
from pathlib import Path

import pytest

from mvmctl.api import BinaryInput, BinaryOperation, BinaryPullInput
from mvmctl.exceptions import BinaryNotFoundError

# ======================================================================
# Shared helpers
# ======================================================================


class _BinaryTestBase:
    """Base class with shared helpers for binary integration tests."""

    @staticmethod
    def _setup_download_mocks(
        monkeypatch: pytest.MonkeyPatch, version: str
    ) -> None:
        """Mock HttpDownload methods to simulate a successful binary download."""
        tgz_buffer = io.BytesIO()
        with tarfile.open(fileobj=tgz_buffer, mode="w:gz") as tar:
            fc_data = b"fake firecracker binary content"
            fc_info = tarfile.TarInfo(name=f"firecracker-v{version}-x86_64")
            fc_info.size = len(fc_data)
            tar.addfile(fc_info, io.BytesIO(fc_data))

            jl_data = b"fake jailer binary content"
            jl_info = tarfile.TarInfo(name=f"jailer-v{version}-x86_64")
            jl_info.size = len(jl_data)
            tar.addfile(jl_info, io.BytesIO(jl_data))

        tgz_bytes = tgz_buffer.getvalue()
        expected_sha256 = hashlib.sha256(tgz_bytes).hexdigest()

        def mock_read_raw_content(url: str, **kwargs: object) -> str:
            return expected_sha256

        def mock_download_file(url: str, dest: Path, **kwargs: object) -> bool:
            Path(dest).write_bytes(tgz_bytes)
            return True

        monkeypatch.setattr(
            "mvmctl.core.binary._service.HttpDownload.read_raw_content",
            staticmethod(mock_read_raw_content),
        )
        monkeypatch.setattr(
            "mvmctl.core.binary._service.HttpDownload.download_file",
            staticmethod(mock_download_file),
        )


# ======================================================================
# Binary pull tests
# ======================================================================


class TestBinaryPull(_BinaryTestBase):
    """Test binary pull operations."""

    def test_pull_new_version(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Pull a new binary version and verify result contains both binaries."""
        self._setup_download_mocks(monkeypatch, "1.16.0")

        result = BinaryOperation.pull(BinaryPullInput(version="1.16.0"))

        assert len(result.item) == 2
        names = [b.name for b in result.item]
        assert "firecracker" in names
        assert "jailer" in names

        fc = next(b for b in result.item if b.name == "firecracker")
        assert fc.version == "1.16.0"
        assert fc.full_version == "v1.16.0"
        assert fc.resolved_path.exists()
        assert fc.resolved_path.name == "firecracker-v1.16.0"

        jl = next(b for b in result.item if b.name == "jailer")
        assert jl.version == "1.16.0"
        assert jl.resolved_path.exists()
        assert jl.resolved_path.name == "jailer-v1.16.0"

    def test_pull_existing_version_no_override(self) -> None:
        """Pulling an existing version without override returns existing DB entries."""
        # Seed jailer so both firecracker and jailer exist for v1.15.0
        from mvmctl.core._shared import Database
        from mvmctl.core.binary._repository import BinaryRepository
        from mvmctl.models.binary import BinaryItem

        db = Database()
        repo = BinaryRepository(db)
        repo.upsert(
            BinaryItem(
                id="e" * 64,
                name="jailer",
                version="1.15.0",
                full_version="v1.15.0",
                ci_version="v1.15",
                path="jailer",
                is_default=True,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )

        # 1.15.0 firecracker is pre-seeded by _seed_full_test_fixtures
        result = BinaryOperation.pull(
            BinaryPullInput(version="1.15.0", download_override=False)
        )

        assert len(result.item) == 2
        fc = next(b for b in result.item if b.name == "firecracker")
        assert fc.version == "1.15.0"
        assert fc.name == "firecracker"
        assert fc.path == "firecracker"

        jl = next(b for b in result.item if b.name == "jailer")
        assert jl.version == "1.15.0"
        assert jl.path == "jailer"

    def test_pull_invalid_version(self) -> None:
        """Pulling with an invalid version format returns error status."""
        result = BinaryOperation.pull(BinaryPullInput(version="not-a-version"))
        assert result.status == "error"


# ======================================================================
# Binary list and get tests
# ======================================================================


class TestBinaryListAndGet(_BinaryTestBase):
    """Test binary listing and retrieval operations."""

    def test_list_all_returns_seeded_binary(self) -> None:
        """list_all returns the pre-seeded firecracker binary."""
        binaries = BinaryOperation.list_all()

        assert len(binaries) >= 1
        names = [b.name for b in binaries]
        assert "firecracker" in names

        fc = next(b for b in binaries if b.name == "firecracker")
        assert fc.version == "1.15.0"
        assert fc.is_default

    def test_get_by_name_and_version(self) -> None:
        """Get binary by name and version returns the correct binary."""
        result = BinaryOperation.get(
            BinaryInput(identifiers=["firecracker"], version="1.15.0")
        )

        assert len(result) == 1
        assert result[0].name == "firecracker"
        assert result[0].version == "1.15.0"
        assert result[0].is_default

    def test_get_nonexistent_binary(self) -> None:
        """Getting a nonexistent binary raises BinaryNotFoundError."""
        with pytest.raises(BinaryNotFoundError):
            BinaryOperation.get(
                BinaryInput(identifiers=["nonexistent"], version="99.99.99")
            )


# ======================================================================
# Binary default tests
# ======================================================================


class TestBinaryDefault(_BinaryTestBase):
    """Test default binary operations."""

    def test_set_default(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Set a newly pulled binary as default."""
        self._setup_download_mocks(monkeypatch, "1.16.0")
        BinaryOperation.pull(BinaryPullInput(version="1.16.0"))

        # The newly pulled binary should not be default yet
        binaries = BinaryOperation.get(
            BinaryInput(identifiers=["firecracker"], version="1.16.0")
        )
        assert len(binaries) == 1
        assert not binaries[0].is_default

        # Set it as default
        BinaryOperation.set_default(
            BinaryInput(identifiers=["firecracker"], version="1.16.0")
        )

        # Verify it is now default
        binaries = BinaryOperation.get(
            BinaryInput(identifiers=["firecracker"], version="1.16.0")
        )
        assert binaries[0].is_default

        # Verify the old default is no longer default
        old = BinaryOperation.get(
            BinaryInput(identifiers=["firecracker"], version="1.15.0")
        )
        assert not old[0].is_default

    def test_ensure_default_returns_default(self) -> None:
        """ensure_default returns the existing default binary."""
        result = BinaryOperation.ensure_default()

        assert result.item is not None
        assert result.item.name == "firecracker"
        assert result.item.is_default
        assert result.item.version == "1.15.0"


# ======================================================================
# Binary remove tests
# ======================================================================


class TestBinaryRemove(_BinaryTestBase):
    """Test binary removal operations."""

    def test_remove_binary(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Remove a specific binary by name and version."""
        self._setup_download_mocks(monkeypatch, "1.16.0")
        BinaryOperation.pull(BinaryPullInput(version="1.16.0"))

        # Verify it exists
        binaries = BinaryOperation.get(
            BinaryInput(identifiers=["firecracker"], version="1.16.0")
        )
        assert len(binaries) == 1
        path = binaries[0].resolved_path
        assert path.exists()

        # Remove it
        BinaryOperation.remove(
            BinaryInput(identifiers=["firecracker"], version="1.16.0")
        )

        # Verify it's gone from the list
        all_binaries = BinaryOperation.list_all()
        fc_116 = [
            b
            for b in all_binaries
            if b.name == "firecracker" and b.version == "1.16.0"
        ]
        assert len(fc_116) == 0

        # Verify the file is gone
        assert not path.exists()

    def test_remove_nonexistent_binary(self) -> None:
        """Removing a nonexistent binary raises BinaryNotFoundError."""
        with pytest.raises(BinaryNotFoundError):
            BinaryOperation.remove(
                BinaryInput(identifiers=["nonexistent"], version="99.99.99")
            )

    def test_remove_by_version(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """remove_by_version removes both firecracker and jailer for a version."""
        self._setup_download_mocks(monkeypatch, "1.16.0")
        BinaryOperation.pull(BinaryPullInput(version="1.16.0"))

        # Verify both exist before removal
        binaries = BinaryOperation.list_all()
        fc_116 = [
            b
            for b in binaries
            if b.name == "firecracker" and b.version == "1.16.0"
        ]
        jl_116 = [
            b for b in binaries if b.name == "jailer" and b.version == "1.16.0"
        ]
        assert len(fc_116) == 1
        assert len(jl_116) == 1

        # Remove by version
        BinaryOperation.remove_by_version("1.16.0")

        # Verify both are gone
        binaries_after = BinaryOperation.list_all()
        fc_116_after = [
            b
            for b in binaries_after
            if b.name == "firecracker" and b.version == "1.16.0"
        ]
        jl_116_after = [
            b
            for b in binaries_after
            if b.name == "jailer" and b.version == "1.16.0"
        ]
        assert len(fc_116_after) == 0
        assert len(jl_116_after) == 0
