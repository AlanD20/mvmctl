"""Integration tests for InitOperation API.

Tests exercise the complete init wizard through the public API:
  init_database → setup_host → run (full wizard)

Only external system dependencies (subprocess, grp, pwd) are mocked.
ALL orchestration logic in api/ and core/ runs unmocked.
"""

from __future__ import annotations

import hashlib
import io
import tarfile
from pathlib import Path
from typing import Any

import pytest

from mvmctl.api import InitOperation
from mvmctl.api.init_operations import InitResult
from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.host._repository import HostRepository
from mvmctl.core.host._service import HostService
from mvmctl.models import HostStateChangeItem, HostStateItem
from mvmctl.models.result import OperationResult
from mvmctl.utils.common import CacheUtils

# ======================================================================
# System dependency mocks
# ======================================================================


class _MockPwd:
    pw_name = "testuser"
    pw_gid = 1000


class _MockGrp:
    def getgrnam(self, name: str) -> Any:
        raise KeyError(name)


@pytest.fixture(autouse=True)
def _mock_init_system_deps(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Mock all external system dependencies so the real API can run unmocked."""
    from tests.integration.conftest import SmartSubprocessMock

    sub_mock = SmartSubprocessMock()
    monkeypatch.setattr("subprocess.run", sub_mock)

    # --- Identity mocks ---
    mock_pwd = _MockPwd()
    monkeypatch.setattr("pwd.getpwuid", lambda _uid: mock_pwd)

    mock_grp = _MockGrp()
    monkeypatch.setattr("grp.getgrnam", mock_grp.getgrnam)

    # --- Path redirections ---
    monkeypatch.setattr(
        "mvmctl.api.host_operations.SUDOERS_DROP_IN_PATH",
        str(tmp_path / "sudoers.d" / "mvmctl"),
    )
    monkeypatch.setattr(
        "mvmctl.core.host._service.SYSCTL_CONF",
        tmp_path / "sysctl.d" / "mvmctl.conf",
    )
    # Ensure directories exist so file writes succeed
    (tmp_path / "sudoers.d").mkdir(parents=True, exist_ok=True)
    (tmp_path / "sysctl.d").mkdir(parents=True, exist_ok=True)


# ======================================================================
# Shared helpers
# ======================================================================


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
# init_database tests
# ======================================================================


class TestInitDatabase:
    """Test InitOperation.init_database SQLite schema creation."""

    def test_init_database_creates_tables(self) -> None:
        """init_database creates the expected SQLite tables."""
        InitOperation.init_database()

        db = Database()
        with db.connect() as conn:
            rows = conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table'"
            ).fetchall()
        table_names = {r[0] for r in rows}

        assert "vm_instances" in table_names
        assert "host_state" in table_names
        assert "binaries" in table_names
        assert "images" in table_names
        assert "kernels" in table_names
        assert "networks" in table_names
        assert "db_migrations" in table_names

    def test_init_database_idempotent(self) -> None:
        """Calling init_database twice does not error or duplicate migrations."""
        InitOperation.init_database()

        db = Database()
        with db.connect() as conn:
            before = conn.execute(
                "SELECT COUNT(*) FROM db_migrations"
            ).fetchone()[0]

        InitOperation.init_database()

        with db.connect() as conn:
            after = conn.execute(
                "SELECT COUNT(*) FROM db_migrations"
            ).fetchone()[0]

        assert before == after
        assert before >= 1


# ======================================================================
# setup_host tests
# ======================================================================


class TestInitHost:
    """Test InitOperation.setup_host through the public API."""

    def test_setup_host_returns_changes(self) -> None:
        """setup_host returns a non-empty list of HostStateChangeItem."""
        cache_dir = CacheUtils.get_cache_dir()
        result = InitOperation.setup_host(cache_dir)

        assert isinstance(result, OperationResult)
        assert result.status == "success"
        changes = result.metadata.get("changes", [])
        assert isinstance(changes, list)
        assert len(changes) > 0
        assert all(isinstance(c, HostStateChangeItem) for c in changes)

    def test_setup_host_marks_initialized(self) -> None:
        """After setup_host, HostRepository reports initialized=True."""
        cache_dir = CacheUtils.get_cache_dir()
        InitOperation.setup_host(cache_dir)

        repo = HostRepository()
        state = repo.get_state()

        assert state is not None
        assert isinstance(state, HostStateItem)
        assert state.initialized == True  # noqa: E712
        assert state.id == 1
        assert isinstance(state.initialized_at, str)
        assert len(state.initialized_at) > 0

    def test_setup_host_idempotent(self) -> None:
        """Calling setup_host twice does not error and state remains initialized."""
        cache_dir = CacheUtils.get_cache_dir()

        result1 = InitOperation.setup_host(cache_dir)
        result2 = InitOperation.setup_host(cache_dir)

        assert isinstance(result1, OperationResult)
        assert isinstance(result2, OperationResult)
        assert result1.status == "success"
        assert result2.status in ("success", "skipped")

        repo = HostRepository()
        state = repo.get_state()
        assert state is not None
        assert state.initialized == True  # noqa: E712


# ======================================================================
# run (wizard) tests
# ======================================================================


class TestInitWizard:
    """Test InitOperation.run full wizard sequences."""

    def test_run_skip_host(self) -> None:
        """run(skip_host=True) skips host step but runs local_state, cache, binary."""
        # Pass guestfs_enabled=False to prevent libguestfs detection from
        # blocking the flow — guestfs is installed in the test venv.
        result = InitOperation.run(skip_host=True, guestfs_enabled=False)

        assert isinstance(result, InitResult)

        host_step = next(s for s in result.steps if s.step == "host")
        assert host_step.success is True
        assert "Skipped" in host_step.message

        local_step = next(s for s in result.steps if s.step == "local_state")
        assert local_step.success is True

        cache_step = next(s for s in result.steps if s.step == "cache")
        assert cache_step.success is True

        binary_step = next(s for s in result.steps if s.step == "binary")
        assert binary_step.success is True

    def test_run_non_interactive(self) -> None:
        """run(non_interactive=True) completes successfully with pre-seeded binary."""
        result = InitOperation.run(non_interactive=True, guestfs_enabled=False)

        assert isinstance(result, InitResult)
        assert result.host_ready is True

        host_step = next(s for s in result.steps if s.step == "host")
        assert host_step.success is True

        binary_step = next(s for s in result.steps if s.step == "binary")
        assert binary_step.success is True
        assert "1.15.0" in binary_step.message

    def test_run_download_version(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """run(download_version='1.16.0') fetches the requested binary version."""
        from mvmctl.core.host._service import HostService

        # Clear pre-seeded binaries so the download path is exercised
        db = Database()
        binary_repo = BinaryRepository(db)
        for binary in binary_repo.list_all():
            binary_repo.delete(binary.id)

        bin_dir = CacheUtils.get_bin_dir()
        for child in bin_dir.iterdir():
            if child.is_file():
                child.unlink()

        # Mock validate_sudoers_binaries — the mvm-provision file was
        # deleted above as part of clearing the pre-seeded cache.
        monkeypatch.setattr(
            HostService, "validate_sudoers_binaries", lambda: None
        )

        _setup_download_mocks(monkeypatch, "1.16.0")

        result = InitOperation.run(
            download_version="1.16.0", guestfs_enabled=False
        )

        assert isinstance(result, InitResult)
        assert result.host_ready is True

        binary_step = next(s for s in result.steps if s.step == "binary")
        assert binary_step.success is True
        assert "1.16.0" in binary_step.message

        # Verify the binary was actually persisted to DB
        binaries = binary_repo.list_all()
        fc = [b for b in binaries if b.name == "firecracker"]
        assert len(fc) == 1
        assert fc[0].version == "1.16.0"
        assert fc[0].is_default == True  # noqa: E712


# ======================================================================
# Edge case tests
# ======================================================================


class TestInitEdgeCases:
    """Test init edge cases and state verification."""

    def test_run_when_already_initialized(self) -> None:
        """Running the wizard twice succeeds both times."""
        result1 = InitOperation.run(guestfs_enabled=False)
        assert isinstance(result1, InitResult)
        assert result1.host_ready is True

        result2 = InitOperation.run(guestfs_enabled=False)
        assert isinstance(result2, InitResult)
        assert result2.host_ready is True

        # Second run's host step should still succeed
        host_step = next(s for s in result2.steps if s.step == "host")
        assert host_step.success is True

    def test_setup_host_records_changes_for_rollback(self) -> None:
        """setup_host persists changes; reset_state clears flags but leaves audit."""
        cache_dir = CacheUtils.get_cache_dir()
        result = InitOperation.setup_host(cache_dir)

        assert result.status == "success"
        changes = result.metadata.get("changes", [])
        assert len(changes) > 0

        repo = HostRepository()
        state_before = repo.get_state()
        assert state_before is not None
        assert state_before.initialized == True  # noqa: E712

        # Verify changes were recorded in the DB
        recorded = repo.list_changes(include_reverted=False)
        assert len(recorded) > 0
        assert all(isinstance(c, HostStateChangeItem) for c in recorded)

        # Partial rollback: reset state flags only
        repo.reset_state()
        state_after = repo.get_state()
        assert state_after is not None
        assert state_after.initialized == False  # noqa: E712
        assert state_after.mvm_group_created == False  # noqa: E712
        assert state_after.sudoers_configured == False  # noqa: E712

        # The detailed changes remain in the DB for full rollback
        all_changes = repo.list_changes(include_reverted=True)
        assert len(all_changes) == len(recorded)
