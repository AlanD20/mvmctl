import os
import shutil
import subprocess
import time
from pathlib import Path

import pytest


@pytest.fixture(autouse=True)
def isolate_config_and_cache(request, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Ensure tests never write to real config or cache directories.

    Skipped for system tests (marked with @pytest.mark.system).
    """
    # Skip isolation for system tests
    if request.node.get_closest_marker("system"):
        return

    # Use tmp_path which pytest automatically cleans up after each test
    config_dir = tmp_path / "config"
    cache_dir = tmp_path / "cache"
    config_dir.mkdir(parents=True, exist_ok=True)
    cache_dir.mkdir(parents=True, exist_ok=True)
    monkeypatch.setenv("MVM_CONFIG_DIR", str(config_dir))
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))


@pytest.fixture(autouse=True)
def _isolate_iptables_rules(request, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Isolate iptables rules for unit/integration tests.

    Skipped for system tests (marked with @pytest.mark.system).
    """
    # Skip isolation for system tests
    if request.node.get_closest_marker("system"):
        return

    fake_rules = str(tmp_path / "iptables" / "rules.v4")
    monkeypatch.setattr("mvmctl.core.host_setup.IPTABLES_RULES_V4", fake_rules, raising=False)
    monkeypatch.setattr("mvmctl.core.host_state.IPTABLES_RULES_V4", fake_rules, raising=False)


@pytest.fixture(autouse=True)
def _mock_sudo_cache(request, monkeypatch: pytest.MonkeyPatch) -> None:
    """Pre-mark sudo credentials as cached so tests never invoke sudo -n/-v.

    Skipped for system tests (marked with @pytest.mark.system).
    """
    # Skip for system tests
    if request.node.get_closest_marker("system"):
        return

    import mvmctl.utils.process as _proc

    monkeypatch.setattr(_proc, "_SUDO_CREDENTIALS_VALID", True)
    monkeypatch.setattr(_proc, "_SUDO_CACHE_TIMESTAMP", time.monotonic())
    monkeypatch.setattr(_proc, "_SUDO_VALIDATION_IN_PROGRESS", False)


def _is_sudo_command(command: object) -> bool:
    """Return True when command would execute the sudo binary."""
    if isinstance(command, (list, tuple)) and command:
        return Path(str(command[0])).name == "sudo"
    if isinstance(command, str):
        return command.strip().startswith("sudo ")
    return False


@pytest.fixture(autouse=True)
def _block_real_sudo_invocations(request, monkeypatch: pytest.MonkeyPatch) -> None:
    """Fail fast when tests attempt a real sudo invocation.

    Enabled in CI with MVM_TEST_ENFORCE_NO_SUDO=1.

    Skipped for system tests (marked with @pytest.mark.system).
    """
    # Skip for system tests
    if request.node.get_closest_marker("system"):
        return

    if os.environ.get("MVM_TEST_ENFORCE_NO_SUDO") != "1":
        return

    real_run = subprocess.run

    def guarded_run(*args: object, **kwargs: object) -> subprocess.CompletedProcess[str]:
        command = kwargs.get("args", args[0] if args else None)
        if _is_sudo_command(command):
            raise AssertionError(
                "Real sudo invocation attempted during tests. "
                "Mock subprocess.run (or module-level subprocess.run) in the test."
            )
        return real_run(*args, **kwargs)

    monkeypatch.setattr(subprocess, "run", guarded_run)


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
