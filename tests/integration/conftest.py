"""Integration test fixtures — isolates cache/config dirs from the real system."""

from pathlib import Path

import pytest


@pytest.fixture(autouse=True)
def isolate_config_and_cache(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Ensure integration tests never touch the real cache or config directories."""
    config_dir = tmp_path / "config"
    cache_dir = tmp_path / "cache"
    config_dir.mkdir(parents=True, exist_ok=True)
    cache_dir.mkdir(parents=True, exist_ok=True)
    monkeypatch.setenv("MVM_CONFIG_DIR", str(config_dir))
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache_dir))
