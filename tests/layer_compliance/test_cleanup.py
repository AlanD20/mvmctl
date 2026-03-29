"""Tests for pytest temp directory cleanup behavior."""

import os
import shutil
import tempfile
from pathlib import Path
from unittest.mock import MagicMock


def test_pytest_cleanup_hook_exists():
    """Verify pytest_sessionfinish hook is defined in conftest."""
    from tests import conftest

    assert hasattr(conftest, "pytest_sessionfinish")


def test_cleanup_only_targets_pytest_dirs():
    """Verify cleanup only removes pytest-* directories under temp."""
    temp_root = Path(tempfile.gettempdir())
    test_dir = temp_root / "pytest-test-cleanup"

    # Create a fake pytest temp dir
    test_dir.mkdir(parents=True, exist_ok=True)
    (test_dir / "test_file.txt").write_text("test")

    # Simulate cleanup logic
    def safe_cleanup(target: Path) -> bool:
        if not target.exists():
            return False
        if not target.is_dir():
            return False
        if target.is_symlink():
            return False
        if not target.name.startswith("pytest-"):
            return False
        try:
            target.relative_to(temp_root)
        except ValueError:
            return False
        shutil.rmtree(target, ignore_errors=True)
        return True

    # Should succeed
    assert safe_cleanup(test_dir) is True
    assert not test_dir.exists()


def test_cleanup_skips_non_pytest_dirs():
    """Verify cleanup skips directories not matching pytest-* pattern."""
    temp_root = Path(tempfile.gettempdir())
    test_dir = temp_root / "not-pytest-dir"

    test_dir.mkdir(parents=True, exist_ok=True)

    def safe_cleanup(target: Path) -> bool:
        if not target.name.startswith("pytest-"):
            return False
        shutil.rmtree(target, ignore_errors=True)
        return True

    # Should skip
    assert safe_cleanup(test_dir) is False
    assert test_dir.exists()

    # Cleanup
    shutil.rmtree(test_dir, ignore_errors=True)


def test_cleanup_skips_symlinks():
    """Verify cleanup skips symlinked directories."""
    temp_root = Path(tempfile.gettempdir())
    real_dir = temp_root / "pytest-real-dir"
    symlink_dir = temp_root / "pytest-symlink-dir"

    real_dir.mkdir(parents=True, exist_ok=True)
    if symlink_dir.exists() or symlink_dir.is_symlink():
        symlink_dir.unlink()
    symlink_dir.symlink_to(real_dir)

    def safe_cleanup(target: Path) -> bool:
        if not target.exists():
            return False
        if not target.is_dir():
            return False
        if target.is_symlink():
            return False
        if not target.name.startswith("pytest-"):
            return False
        try:
            target.relative_to(temp_root)
        except ValueError:
            return False
        shutil.rmtree(target, ignore_errors=True)
        return True

    # Should skip symlink
    assert safe_cleanup(symlink_dir) is False
    assert symlink_dir.exists()
    assert real_dir.exists()

    # Cleanup
    symlink_dir.unlink()
    shutil.rmtree(real_dir, ignore_errors=True)


def test_cleanup_skips_paths_outside_temp():
    """Verify cleanup skips paths not under temp directory."""
    outside_dir = Path("/tmp") / ".." / "pytest-outside"
    outside_dir = outside_dir.resolve()

    # Don't actually create this - just test the path validation
    def safe_cleanup(target: Path) -> bool:
        temp_root = Path(os.environ.get("TMPDIR", "/tmp"))
        try:
            target.relative_to(temp_root)
        except ValueError:
            return False
        return True

    # Should skip because it's outside /tmp
    assert safe_cleanup(outside_dir) is False


def test_pytest_sessionfinish_with_none_factory():
    """Verify sessionfinish handles None _tmp_path_factory gracefully."""
    from tests.conftest import pytest_sessionfinish

    mock_session = MagicMock()
    mock_session.config._tmp_path_factory = None

    # Should not raise
    pytest_sessionfinish(mock_session, 0)


def test_pytest_sessionfinish_with_none_basetemp():
    """Verify sessionfinish handles None _basetemp gracefully."""
    from tests.conftest import pytest_sessionfinish

    mock_session = MagicMock()
    mock_session.config._tmp_path_factory._basetemp = None

    # Should not raise
    pytest_sessionfinish(mock_session, 0)


def test_pytest_sessionfinish_skips_nonexistent_path():
    """Verify sessionfinish skips non-existent paths."""
    from tests.conftest import pytest_sessionfinish

    mock_session = MagicMock()
    mock_factory = MagicMock()
    mock_factory._basetemp = Path("/nonexistent/path/pytest-999")
    mock_session.config._tmp_path_factory = mock_factory

    # Should not raise and should not attempt removal
    pytest_sessionfinish(mock_session, 0)
