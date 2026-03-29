import os
import subprocess
import time
from pathlib import Path

import pytest


@pytest.fixture(autouse=True)
def isolate_config_and_cache(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Ensure tests never write to real config or cache directories."""
    # Use a path under /tmp to satisfy path validation requirements
    import tempfile

    test_base = Path(tempfile.mkdtemp(prefix="mvmctl-test-", dir="/tmp"))
    config_dir = test_base / "config"
    cache_dir = test_base / "cache"
    config_dir.mkdir(parents=True, exist_ok=True)
    cache_dir.mkdir(parents=True, exist_ok=True)
    monkeypatch.setenv("MVM_CONFIG_DIR", str(config_dir))
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))


@pytest.fixture(autouse=True)
def _isolate_iptables_rules(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    fake_rules = str(tmp_path / "iptables" / "rules.v4")
    monkeypatch.setattr("mvmctl.core.host_setup.IPTABLES_RULES_V4", fake_rules, raising=False)
    monkeypatch.setattr("mvmctl.core.host_state.IPTABLES_RULES_V4", fake_rules, raising=False)


@pytest.fixture(autouse=True)
def _mock_sudo_cache(monkeypatch: pytest.MonkeyPatch) -> None:
    """Pre-mark sudo credentials as cached so tests never invoke sudo -n/-v."""
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
def _block_real_sudo_invocations(monkeypatch: pytest.MonkeyPatch) -> None:
    """Fail fast when tests attempt a real sudo invocation.

    Enabled in CI with MVM_TEST_ENFORCE_NO_SUDO=1.
    """
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
