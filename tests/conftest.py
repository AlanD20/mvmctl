import time
from pathlib import Path

import pytest


@pytest.fixture(autouse=True)
def _isolate_iptables_rules(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    fake_rules = str(tmp_path / "iptables" / "rules.v4")
    monkeypatch.setattr("mvmctl.core.host_setup.IPTABLES_RULES_V4", fake_rules, raising=False)
    monkeypatch.setattr("mvmctl.core.host_state.IPTABLES_RULES_V4", fake_rules, raising=False)


@pytest.fixture(autouse=True)
def _mock_sudo_cache(monkeypatch: pytest.MonkeyPatch) -> None:
    """Pre-mark sudo credentials as cached so tests never invoke sudo -n/-v."""
    import mvmctl.core.network as _net

    monkeypatch.setattr(_net, "_SUDO_CREDENTIALS_VALID", True)
    monkeypatch.setattr(_net, "_SUDO_CACHE_TIMESTAMP", time.monotonic())
    monkeypatch.setattr(_net, "_SUDO_VALIDATION_IN_PROGRESS", False)
