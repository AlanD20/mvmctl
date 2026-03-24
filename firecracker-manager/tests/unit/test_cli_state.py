import json
from pathlib import Path

import pytest

from fcm.core.cli_state import (
    get_assets_state,
    get_cli_state,
    get_cli_state_value,
    get_firecracker_state,
    set_cli_state_value,
    update_assets_state,
    update_firecracker_state,
)


@pytest.fixture()
def state_dir(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    return tmp_path


def test_get_cli_state_empty(state_dir: Path) -> None:
    assert get_cli_state() == {}


def test_set_and_get_flat_value(state_dir: Path) -> None:
    set_cli_state_value("ci_version", "1.12")
    assert get_cli_state_value("ci_version") == "1.12"


def test_set_multiple_flat_values(state_dir: Path) -> None:
    set_cli_state_value("key_a", "val_a")
    set_cli_state_value("key_b", "val_b")
    assert get_cli_state_value("key_a") == "val_a"
    assert get_cli_state_value("key_b") == "val_b"


def test_get_missing_flat_value_returns_default(state_dir: Path) -> None:
    assert get_cli_state_value("missing", default="fallback") == "fallback"


def test_get_missing_flat_value_returns_none(state_dir: Path) -> None:
    assert get_cli_state_value("missing") is None


def test_corrupt_state_returns_empty(state_dir: Path) -> None:
    (state_dir / "cli-state.json").write_text("{invalid json")
    assert get_cli_state() == {}


def test_state_persists_to_file(state_dir: Path) -> None:
    set_cli_state_value("test", "value")
    assert (state_dir / "cli-state.json").exists()


def test_state_file_has_restricted_permissions(state_dir: Path) -> None:
    set_cli_state_value("x", "y")
    mode = (state_dir / "cli-state.json").stat().st_mode & 0o777
    assert mode == 0o600


def test_get_firecracker_state_empty(state_dir: Path) -> None:
    assert get_firecracker_state() == {}


def test_update_firecracker_state_stores_all_fields(state_dir: Path) -> None:
    update_firecracker_state(
        full_version="v1.12.0",
        ci_version="v1.12",
        active_version="v1.12.0",
        active_binary_path="/usr/local/bin/firecracker",
    )
    fc = get_firecracker_state()
    assert fc["full_version"] == "v1.12.0"
    assert fc["ci_version"] == "v1.12"
    assert fc["active_version"] == "v1.12.0"
    assert fc["active_binary_path"] == "/usr/local/bin/firecracker"


def test_update_firecracker_state_merges(state_dir: Path) -> None:
    update_firecracker_state(full_version="v1.10.0", ci_version="v1.10")
    update_firecracker_state(active_binary_path="/bin/fc")
    fc = get_firecracker_state()
    assert fc["full_version"] == "v1.10.0"
    assert fc["active_binary_path"] == "/bin/fc"


def test_update_firecracker_state_overwrites_field(state_dir: Path) -> None:
    update_firecracker_state(full_version="v1.10.0")
    update_firecracker_state(full_version="v1.12.0")
    assert get_firecracker_state()["full_version"] == "v1.12.0"


def test_firecracker_state_persisted_as_nested_key(state_dir: Path) -> None:
    update_firecracker_state(full_version="v1.12.0")
    raw = json.loads((state_dir / "cli-state.json").read_text())
    assert "firecracker" in raw
    assert raw["firecracker"]["full_version"] == "v1.12.0"


def test_firecracker_state_ignores_corrupt_section(state_dir: Path) -> None:
    raw: dict = {"firecracker": "not-a-dict"}
    (state_dir / "cli-state.json").write_text(json.dumps(raw))
    assert get_firecracker_state() == {}


def test_get_assets_state_has_all_expected_keys(state_dir: Path) -> None:
    assets = get_assets_state()
    expected_keys = {
        "kernels_dir",
        "images_dir",
        "bin_dir",
        "networks_dir",
        "vms_dir",
        "keys_dir",
        "kernel_build_dir",
        "image_import_dir",
        "logs_dir",
    }
    assert expected_keys <= assets.keys()


def test_get_assets_state_cache_dirs_under_cache(state_dir: Path) -> None:
    assets = get_assets_state()
    for key in (
        "kernels_dir",
        "images_dir",
        "bin_dir",
        "networks_dir",
        "vms_dir",
        "keys_dir",
        "logs_dir",
    ):
        assert assets[key].startswith(str(state_dir)), f"{key} not under cache dir"


def test_get_assets_state_temp_dirs_under_tmp(state_dir: Path) -> None:
    assets = get_assets_state()
    assert assets["kernel_build_dir"].startswith("/tmp/fcm-kernel-build-")
    assert assets["image_import_dir"].startswith("/tmp/fcm-image-import-")


def test_get_assets_state_temp_dirs_have_3char_suffix(state_dir: Path) -> None:
    assets = get_assets_state()
    suffix_kb = assets["kernel_build_dir"].split("-")[-1]
    suffix_ii = assets["image_import_dir"].split("-")[-1]
    assert len(suffix_kb) == 3
    assert len(suffix_ii) == 3


def test_get_assets_state_temp_dirs_are_stable(state_dir: Path) -> None:
    first = get_assets_state()
    second = get_assets_state()
    assert first["kernel_build_dir"] == second["kernel_build_dir"]
    assert first["image_import_dir"] == second["image_import_dir"]


def test_get_assets_state_persisted_as_nested_key(state_dir: Path) -> None:
    get_assets_state()
    raw = json.loads((state_dir / "cli-state.json").read_text())
    assert "assets" in raw
    assert "kernels_dir" in raw["assets"]


def test_update_assets_state_overrides_field(state_dir: Path) -> None:
    get_assets_state()
    update_assets_state(kernels_dir="/custom/kernels")
    assert get_assets_state()["kernels_dir"] == "/custom/kernels"


def test_update_assets_state_merges(state_dir: Path) -> None:
    get_assets_state()
    update_assets_state(images_dir="/alt/images")
    assets = get_assets_state()
    assert assets["images_dir"] == "/alt/images"
    assert assets["bin_dir"].startswith(str(state_dir))


def test_update_assets_state_persisted_as_nested_key(state_dir: Path) -> None:
    update_assets_state(logs_dir="/var/log/fcm")
    raw = json.loads((state_dir / "cli-state.json").read_text())
    assert raw["assets"]["logs_dir"] == "/var/log/fcm"


def test_firecracker_and_assets_coexist(state_dir: Path) -> None:
    update_firecracker_state(full_version="v1.12.0")
    get_assets_state()
    raw = json.loads((state_dir / "cli-state.json").read_text())
    assert "firecracker" in raw
    assert "assets" in raw


def test_flat_key_and_sections_coexist(state_dir: Path) -> None:
    set_cli_state_value("default_image", "ubuntu-24.04")
    update_firecracker_state(full_version="v1.12.0")
    raw = json.loads((state_dir / "cli-state.json").read_text())
    assert raw["default_image"] == "ubuntu-24.04"
    assert raw["firecracker"]["full_version"] == "v1.12.0"
