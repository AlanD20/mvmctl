import os
import shutil
import subprocess
import warnings
from pathlib import Path

import pytest

from tests.helpers.paths import make_test_paths

# Suppress harmless ResourceWarning from mock objects holding sqlite3.Connection
# references at GC time. The connections are properly closed by Database.connect()
# context manager — this is a CPython GC ordering artifact, not a real leak.
warnings.filterwarnings(
    "ignore",
    message="unclosed database",
    category=ResourceWarning,
)


@pytest.fixture(autouse=True)
def isolate_config_and_cache(
    request, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    """Ensure tests never write to real config or cache directories.

    Skipped for system tests (marked with @pytest.mark.system).
    """
    # Skip isolation for system tests — must yield even when skipping
    # because pytest requires generator fixtures to yield at least once.
    if request.node.get_closest_marker("system"):
        yield
        return

    paths = make_test_paths(tmp_path)
    paths.config.mkdir(parents=True, exist_ok=True)
    paths.cache.mkdir(parents=True, exist_ok=True)
    paths.temp.mkdir(parents=True, exist_ok=True)
    monkeypatch.setenv("MVM_CONFIG_DIR", str(paths.config))
    monkeypatch.setenv("MVM_CACHE_DIR", str(paths.cache))
    monkeypatch.setenv("MVM_TEMP_DIR", str(paths.temp))

    yield

    shutil.rmtree(tmp_path, ignore_errors=True)


@pytest.fixture(autouse=True)
def _isolate_iptables_rules(
    request, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Isolate iptables rules for unit/integration tests.

    Flushes MVM-specific iptables chains before each test to prevent
    test rules from leaking into the host system.

    Skipped for system tests (marked with @pytest.mark.system).
    """
    # Skip isolation for system tests
    if request.node.get_closest_marker("system"):
        return

    # Flush MVM-specific iptables chains (ignore errors if chains don't exist)
    subprocess.run(
        ["iptables", "-t", "nat", "-F", "MVM-POSTROUTING"],
        capture_output=True,
        check=False,
    )
    subprocess.run(
        ["iptables", "-F", "MVM-FORWARD"], capture_output=True, check=False
    )
    subprocess.run(
        ["iptables", "-F", "MVM-NOCLOUD-INPUT"],
        capture_output=True,
        check=False,
    )


@pytest.fixture(autouse=True)
def _mock_sudo_cache() -> None:
    """No-op: sudo caching was removed during refactoring.

    The old _SUDO_CREDENTIALS_VALID / _SUDO_CACHE_TIMESTAMP /
    _SUDO_VALIDATION_IN_PROGRESS constants were removed from
    ``mvmctl.utils._system`` and replaced with group-membership-based
    credential checks via ``require_mvm_group_membership()``.

    Skipped for system tests (marked with @pytest.mark.system) — the
    ``_block_real_sudo_invocations`` fixture handles sudo blocking.
    """


def _is_sudo_command(command: object) -> bool:
    """Return True when command would execute the sudo binary."""
    if isinstance(command, (list, tuple)) and command:
        return Path(str(command[0])).name == "sudo"
    if isinstance(command, str):
        return command.strip().startswith("sudo ")
    return False


@pytest.fixture(autouse=True)
def _block_real_sudo_invocations(
    request, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Fail fast when tests attempt a real sudo invocation.

    Enabled in CI with MVM_TEST_ENFORCE_NO_SUDO=1.

    Also patches ``require_mvm_group_membership`` to a no-op — the ``mvm``
    unix group does not exist on CI runners, so ``grp.getgrnam('mvm')``
    raises before any subprocess call can be checked.

    Skipped for system tests (marked with @pytest.mark.system) and for
    tests that specifically exercise ``require_mvm_group_membership``
    itself (marked with ``@pytest.mark.real_mvm_group_check``).
    """
    # Skip for system tests
    if request.node.get_closest_marker("system"):
        return

    if os.environ.get("MVM_TEST_ENFORCE_NO_SUDO") != "1":
        return

    import mvmctl.utils._system as _sys

    # Patch require_mvm_group_membership — grp.getgrnam('mvm') fails in CI
    # where the mvm group does not exist.
    if not request.node.get_closest_marker("real_mvm_group_check"):
        monkeypatch.setattr(_sys, "require_mvm_group_membership", lambda: None)

    real_run = subprocess.run

    def guarded_run(
        *args: object, **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        command = kwargs.get("args", args[0] if args else None)
        if _is_sudo_command(command):
            raise AssertionError(
                "Real sudo invocation attempted during tests. "
                "Mock subprocess.run (or module-level subprocess.run) in the test."
            )
        return real_run(*args, **kwargs)

    monkeypatch.setattr(subprocess, "run", guarded_run)


@pytest.fixture(autouse=True)
def _mock_privilege_checks(request, monkeypatch: pytest.MonkeyPatch) -> None:
    """Mock privilege checks so tests don't require the mvm group.

    Skipped for system tests (marked with @pytest.mark.system).
    """
    # Skip for system tests
    if request.node.get_closest_marker("system"):
        return

    from unittest.mock import MagicMock

    # Mock check_privileges and check_privileges_interactive
    monkeypatch.setattr(
        "mvmctl.core.host._helper.HostPrivilegeHelper.check_privileges",
        MagicMock(return_value=None),
    )
    # check_privileges_interactive was removed during refactoring;
    # attempt to mock it in case old code still references it
    try:
        monkeypatch.setattr(
            "mvmctl.api.host.check_privileges_interactive",
            MagicMock(return_value=None),
        )
    except ImportError:
        pass


@pytest.fixture(autouse=True)
def _setup_database(request, isolate_config_and_cache) -> None:  # type: ignore[return]
    """Set up SQLite database with migrations for each test.

    Depends on isolate_config_and_cache to ensure MVM_CACHE_DIR is set first.
    Skipped for system tests (marked with @pytest.mark.system).
    """
    if request.node.get_closest_marker("system"):
        return

    from datetime import datetime, timezone

    from mvmctl.core._shared._db import Database

    Database().migrate()

    # Ensure host_state exists with initialized=1 so CLI tests don't
    # fail the is_initialized() check in main.py's root app callback.
    db = Database()
    with db.connect() as conn:
        now = datetime.now(timezone.utc).isoformat()
        conn.execute(
            "INSERT OR IGNORE INTO host_state "
            "(id, initialized, mvm_group_created, sudoers_configured, "
            " default_network_created, initialized_at, updated_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?)",
            (1, 1, 1, 1, 1, now, now),
        )


def pytest_sessionfinish(session: pytest.Session, exitstatus: int) -> None:
    """Clean up this pytest session's temp directory.

    Removes only the temp directory created for the current pytest session.
    Errors are silently ignored to ensure cleanup never fails the test run.
    """
    # Get the session's base temp directory from pytest internals
    tmp_path_factory = getattr(session.config, "_tmp_path_factory", None)
    if tmp_path_factory is None:
        return

    basetemp = getattr(tmp_path_factory, "_basetemp", None)
    if basetemp is None:
        return

    target = Path(basetemp)

    # Safety constraints
    if not target.exists():
        return
    if not target.is_dir():
        return
    if target.is_symlink():
        return

    # Must be under /tmp (or system temp dir)
    temp_root = Path(os.environ.get("TMPDIR", "/tmp"))
    try:
        target.relative_to(temp_root)
    except ValueError:
        return  # Not under temp dir, skip

    # Must look like a pytest temp dir
    if not target.name.startswith("pytest-"):
        return

    # Safe to remove
    shutil.rmtree(target, ignore_errors=True)
