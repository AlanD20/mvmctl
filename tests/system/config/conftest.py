"""Config domain system tests — setup."""

from __future__ import annotations

import os
from pathlib import Path

import pytest


@pytest.fixture(scope="session", autouse=True)
def prepare_system_env(mvm_binary, check_system_prerequisites) -> None:
    """Verify prerequisites for config tests."""
    if os.environ.get("PYTEST_XDIST_WORKER"):
        return
    if not (Path.home() / ".cache" / "mvmctl" / "mvmdb.db").exists():
        pytest.skip("mvmctl not initialized (run 'mvm host init')")
