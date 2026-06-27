"""Env test conftest — cleanup pre-existing environments before tests run."""

from __future__ import annotations

import os
import shutil

import pytest
from tests.system.conftest import _run_mvm_host


def _list_all_env_ids() -> list[str]:
    """Parse ``mvm env ls`` table output and return all workflow IDs."""
    result = _run_mvm_host("env", "ls", timeout=30, check=False)
    if result.returncode != 0:
        return []
    output = result.stdout
    if "No saved environments found" in output:
        return []
    ids: list[str] = []
    for line in output.splitlines():
        stripped = line.strip()
        if not stripped:
            continue
        if stripped.startswith("WORKFLOW"):
            continue
        if set(stripped.replace(" ", "")) <= {"-"}:
            continue
        parts = stripped.split()
        if len(parts) >= 2:
            ids.append(parts[0])
    return ids


def _destroy_workflow_state(wfid: str) -> None:
    """Remove workflow state by deleting its directory on disk.

    Uses ``mvm env destroy`` first (which also cleans up resources).
    If that fails (e.g. resources already gone), falls back to deleting
    the workflow state directory directly.
    """
    result = _run_mvm_host("env", "destroy", wfid, timeout=60, check=False)
    if result.returncode == 0:
        return
    # Fallback: delete workflow state directory directly
    cache_base = os.environ.get("MVM_CACHE_DIR", os.path.expanduser("~/.cache/mvmctl"))
    wf_dir = os.path.join(cache_base, "workflows")
    if not os.path.isdir(wf_dir):
        return
    for entry in os.listdir(wf_dir):
        if entry.startswith(wfid):
            shutil.rmtree(os.path.join(wf_dir, entry), ignore_errors=True)
            break


@pytest.fixture(scope="session", autouse=True)
def cleanup_envs():
    """Destroy all environments before env tests run (session scope).

    This fixture runs once before any env test to remove pre-existing
    environments that would interfere with tests expecting clean state.
    """
    for wfid in _list_all_env_ids():
        _destroy_workflow_state(wfid)
