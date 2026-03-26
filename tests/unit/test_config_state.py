import importlib
import json
from pathlib import Path

import pytest

from mvmctl.core.config_state import (
    get_assets_config,
    get_config,
    get_config_value,
    get_firecracker_config,
    initialize_default_config,
    set_config_value,
    update_assets_config,
    update_firecracker_config,
)
from mvmctl.utils.fs import get_cache_dir, get_config_dir


@pytest.fixture()
def config_dir() -> Path:
    return get_config_dir()


@pytest.fixture()
def cache_dir() -> Path:
    return get_cache_dir()


def test_get_config_empty(config_dir: Path) -> None:
    assert get_config() == {}


def test_set_and_get_flat_value(config_dir: Path) -> None:
    set_config_value("ci_version", "1.12")
    assert get_config_value("ci_version") == "1.12"


def test_set_multiple_flat_values(config_dir: Path) -> None:
    set_config_value("key_a", "val_a")
    set_config_value("key_b", "val_b")
    assert get_config_value("key_a") == "val_a"
    assert get_config_value("key_b") == "val_b"


def test_get_missing_flat_value_returns_default(config_dir: Path) -> None:
    assert get_config_value("missing", default="fallback") == "fallback"


def test_get_missing_flat_value_returns_none(config_dir: Path) -> None:
    assert get_config_value("missing") is None


def test_corrupt_config_returns_empty(config_dir: Path) -> None:
    (config_dir / "config.json").write_text("{invalid json")
    assert get_config() == {}


def test_config_persists_to_file(config_dir: Path) -> None:
    set_config_value("test", "value")
    assert (config_dir / "config.json").exists()


def test_config_file_has_restricted_permissions(config_dir: Path) -> None:
    set_config_value("x", "y")
    mode = (config_dir / "config.json").stat().st_mode & 0o777
    assert mode == 0o600


def test_config_directory_has_restricted_permissions(tmp_path: Path, monkeypatch) -> None:
    """Test that config directory is created with 0o700 permissions (issue #27)."""
    # Use a fresh directory that doesn't exist yet
    fresh_config_dir = tmp_path / "fresh_config"
    monkeypatch.setenv("MVM_CONFIG_DIR", str(fresh_config_dir))
    # Reload module to pick up new env var
    from mvmctl.core import config_state

    importlib.reload(config_state)

    set_config_value("test", "value")
    # Directory should have restrictive permissions
    mode = fresh_config_dir.stat().st_mode & 0o777
    assert mode == 0o700


def test_config_written_as_json(config_dir: Path) -> None:
    set_config_value("test", "value")
    content = (config_dir / "config.json").read_text()
    parsed = json.loads(content)
    assert parsed["test"] == "value"


def test_get_firecracker_config_empty_returns_defaults(config_dir: Path) -> None:
    fc = get_firecracker_config()
    assert "full_version" in fc
    assert "ci_version" in fc


def test_get_firecracker_config_returns_defaults(config_dir: Path) -> None:
    fc = get_firecracker_config()
    assert fc["full_version"].startswith("v1.")
    assert fc["ci_version"].startswith("v1.")


def test_update_firecracker_config_stores_all_fields(config_dir: Path) -> None:
    update_firecracker_config(
        full_version="v1.12.0",
        ci_version="v1.12",
        active_version="v1.12.0",
        active_binary_path="/usr/local/bin/firecracker",
    )
    fc = get_firecracker_config()
    assert fc["full_version"] == "v1.12.0"
    assert fc["ci_version"] == "v1.12"
    assert fc["active_version"] == "v1.12.0"
    assert fc["active_binary_path"] == "/usr/local/bin/firecracker"


def test_update_firecracker_config_merges(config_dir: Path) -> None:
    update_firecracker_config(full_version="v1.10.0", ci_version="v1.10")
    update_firecracker_config(active_binary_path="/bin/fc")
    fc = get_firecracker_config()
    assert fc["full_version"] == "v1.10.0"
    assert fc["active_binary_path"] == "/bin/fc"


def test_update_firecracker_config_overwrites_field(config_dir: Path) -> None:
    update_firecracker_config(full_version="v1.10.0")
    update_firecracker_config(full_version="v1.12.0")
    assert get_firecracker_config()["full_version"] == "v1.12.0"


def test_firecracker_config_persisted_as_nested_key(config_dir: Path) -> None:
    from mvmctl.core.metadata import read_metadata

    update_firecracker_config(full_version="v1.12.0")
    raw = read_metadata(cache_dir=get_cache_dir())
    assert "binaries" in raw
    assert set(raw["binaries"].keys()) == {"firecracker", "jailer"}
    assert raw["binaries"]["firecracker"]["full_version"] == "v1.12.0"
    assert raw["binaries"]["jailer"]["full_version"] == "v1.12.0"
    assert raw["binaries"]["firecracker"]["binary_name"] == "firecracker"
    assert raw["binaries"]["jailer"]["binary_name"] == "jailer"


def test_firecracker_config_ignores_corrupt_section(config_dir: Path) -> None:
    raw: dict = {"firecracker": "not-a-dict"}
    (config_dir / "config.json").write_text(json.dumps(raw))
    fc = get_firecracker_config()
    assert "full_version" in fc


def test_get_assets_config_has_all_expected_keys(cache_dir: Path) -> None:
    assets = get_assets_config()
    expected_keys = {
        "kernels_dir",
        "images_dir",
        "bin_dir",
        "vms_dir",
        "keys_dir",
        "logs_dir",
    }
    assert expected_keys <= assets.keys()


def test_get_assets_config_no_temp_build_dirs(cache_dir: Path) -> None:
    assets = get_assets_config()
    assert "kernel_build_dir" not in assets
    assert "image_import_dir" not in assets


def test_get_assets_config_cache_dirs_under_cache(cache_dir: Path) -> None:
    assets = get_assets_config()
    for key in (
        "kernels_dir",
        "images_dir",
        "bin_dir",
        "vms_dir",
        "keys_dir",
        "logs_dir",
    ):
        assert assets[key].startswith(str(cache_dir)), f"{key} not under cache dir"


def test_get_assets_config_persisted_as_nested_key(config_dir: Path) -> None:
    get_assets_config()
    raw = json.loads((config_dir / "config.json").read_text())
    assert "assets" in raw
    assert "kernels_dir" in raw["assets"]


def test_update_assets_config_overrides_field(cache_dir: Path) -> None:
    get_assets_config()
    update_assets_config(kernels_dir="/custom/kernels")
    assert get_assets_config()["kernels_dir"] == "/custom/kernels"


def test_update_assets_config_merges(cache_dir: Path) -> None:
    get_assets_config()
    update_assets_config(images_dir="/alt/images")
    assets = get_assets_config()
    assert assets["images_dir"] == "/alt/images"
    assert assets["bin_dir"].startswith(str(cache_dir))


def test_update_assets_config_persisted_as_nested_key(config_dir: Path) -> None:
    update_assets_config(logs_dir="/var/log/mvm")
    raw = json.loads((config_dir / "config.json").read_text())
    assert raw["assets"]["logs_dir"] == "/var/log/mvm"


def test_firecracker_and_assets_coexist(config_dir: Path) -> None:
    update_firecracker_config(full_version="v1.12.0")
    get_assets_config()
    raw = json.loads((config_dir / "config.json").read_text())
    assert "assets" in raw


def test_flat_key_and_sections_coexist(config_dir: Path) -> None:
    set_config_value("default_image", "ubuntu-24.04")
    update_firecracker_config(full_version="v1.12.0")
    raw = json.loads((config_dir / "config.json").read_text())
    assert raw["default_image"] == "ubuntu-24.04"


def test_config_dir_env_var_override(config_dir: Path) -> None:
    set_config_value("test_key", "test_value")
    config_path = config_dir / "config.json"
    assert config_path.exists()
    content = json.loads(config_path.read_text())
    assert content["test_key"] == "test_value"


def test_initialize_default_config_creates_file(config_dir: Path) -> None:
    config_path = config_dir / "config.json"
    assert not config_path.exists()
    initialize_default_config()
    assert config_path.exists()


def test_initialize_default_config_writes_defaults(config_dir: Path) -> None:
    result = initialize_default_config()
    assert "assets" in result
    fc = get_firecracker_config()
    assert fc["full_version"] == "v1.15.0"
    assert fc["ci_version"] == "v1.15"


def test_initialize_default_config_idempotent(config_dir: Path) -> None:
    initialize_default_config()
    first_content = (config_dir / "config.json").read_text()
    initialize_default_config()
    second_content = (config_dir / "config.json").read_text()
    assert first_content == second_content


def test_initialize_default_config_preserves_existing(config_dir: Path) -> None:
    update_firecracker_config(full_version="v1.10.0", active_binary_path="/usr/local/bin/fc")
    initialize_default_config()
    fc = get_firecracker_config()
    assert fc["full_version"] == "v1.10.0"
    assert fc["active_binary_path"] == "/usr/local/bin/fc"
    assert fc["ci_version"] == "v1.15"
