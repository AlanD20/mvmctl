from pathlib import Path

import pytest


@pytest.fixture(autouse=True)
def _isolate_iptables_rules(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    fake_rules = str(tmp_path / "iptables" / "rules.v4")
    monkeypatch.setattr("fcm.core.host_setup.IPTABLES_RULES_V4", fake_rules, raising=False)
    monkeypatch.setattr("fcm.core.host_state.IPTABLES_RULES_V4", fake_rules, raising=False)
