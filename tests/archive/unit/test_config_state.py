import json
from pathlib import Path

import pytest
from pytest_mock import MockerFixture

from mvmctl.core.config_state import (
    get_assets_config,
    get_defaults_config,
    get_firecracker_config,
    initialize_default_config,
    set_defaults_value,
)
from mvmctl.exceptions import AssetNotFoundError
from mvmctl.utils.fs import get_cache_dir, get_config_dir


def _rand_suffix(n: int = 3) -> str:
    import random
    import string

    return "".join(random.choices(string.ascii_lowercase + string.digits, k=n))


@pytest.fixture()
def config_dir() -> Path:
    return get_config_dir()


@pytest.fixture()
def cache_dir() -> Path:
    return get_cache_dir()


def _seed_binary(
    cache_dir: Path,
    version: str,
    fc_path: str,
    jl_path: str | None = None,
    ci_version: str | None = None,
) -> None:
    import hashlib
    from datetime import datetime, timezone

    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.db.models import Binary

    db = MVMDatabase()
    db.migrate()

    now = datetime.now(timezone.utc).isoformat()
    norm_version = version.removeprefix("v")
    computed_ci = ci_version or (
        "v" + ".".join(norm_version.split(".")[:2]) if "." in norm_version else f"v{norm_version}"
    )

    fc_id = hashlib.sha256(f"firecracker:{norm_version}".encode()).hexdigest()
    fc_binary = Binary(
        id=fc_id,
        name="firecracker",
        version=norm_version,
        full_version=f"v{norm_version}",
        ci_version=computed_ci,
        path=fc_path,
        is_default=True,
        created_at=now,
        updated_at=now,
    )
    db.upsert_binary(fc_binary)
    db.set_default_binary("firecracker", norm_version, fc_path)

    if jl_path is not None:
        jl_id = hashlib.sha256(f"jailer:{norm_version}".encode()).hexdigest()
        jl_binary = Binary(
            id=jl_id,
            name="jailer",
            version=norm_version,
            full_version=f"v{norm_version}",
            ci_version=computed_ci,
            path=jl_path,
            is_default=True,
            created_at=now,
            updated_at=now,
        )
        db.upsert_binary(jl_binary)
        db.set_default_binary("jailer", norm_version, jl_path)


def test_get_firecracker_config_empty_raises_asset_not_found(config_dir: Path) -> None:
    with pytest.raises(AssetNotFoundError, match="No active binary for 'firecracker'"):
        get_firecracker_config(binary_record=None)


def test_get_firecracker_config_no_default_error_mentions_fetch(config_dir: Path) -> None:
    with pytest.raises(AssetNotFoundError, match="mvm bin fetch"):
        get_firecracker_config(binary_record=None)


def test_get_firecracker_config_with_binary_record(config_dir: Path, cache_dir: Path) -> None:
    bin_dir = cache_dir / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    fc_path = str(bin_dir / "firecracker-v1.12.0")
    (bin_dir / "firecracker-v1.12.0").write_bytes(b"\x7fELF")
    (bin_dir / "jailer-v1.12.0").write_bytes(b"\x7fELF")

    _seed_binary(cache_dir, "v1.12.0", fc_path, ci_version="v1.12")

    from mvmctl.core.mvm_db import MVMDatabase

    db = MVMDatabase()
    binary_record = db.get_default_binary("firecracker")
    fc = get_firecracker_config(binary_record=binary_record)
    assert fc["full_version"] == "v1.12.0"
    assert fc["ci_version"] == "v1.12"
    assert fc["active_version"] == "v1.12.0"
    assert fc["default_version"] == "v1.12.0"


def test_get_firecracker_config_does_not_own_config_json(config_dir: Path, cache_dir: Path) -> None:
    bin_dir = cache_dir / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    fc_path = str(bin_dir / "firecracker-v1.12.0")
    (bin_dir / "firecracker-v1.12.0").write_bytes(b"\x7fELF")

    _seed_binary(cache_dir, "v1.12.0", fc_path)

    from mvmctl.core.mvm_db import MVMDatabase

    db = MVMDatabase()
    binary_record = db.get_default_binary("firecracker")
    get_firecracker_config(binary_record=binary_record)
    if (config_dir / "config.json").exists():
        raw = json.loads((config_dir / "config.json").read_text())
        assert "firecracker" not in raw


def test_get_firecracker_config_with_explicit_record(config_dir: Path, cache_dir: Path) -> None:
    bin_dir = cache_dir / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    (bin_dir / "firecracker-v1.10.0").write_bytes(b"\x7fELF")
    (bin_dir / "firecracker-v1.12.0").write_bytes(b"\x7fELF")

    _seed_binary(cache_dir, "v1.10.0", str(bin_dir / "firecracker-v1.10.0"))
    _seed_binary(cache_dir, "v1.12.0", str(bin_dir / "firecracker-v1.12.0"))

    from mvmctl.core.mvm_db import MVMDatabase

    db = MVMDatabase()
    binary_record = db.get_default_binary("firecracker")
    fc = get_firecracker_config(binary_record=binary_record)
    assert fc["full_version"] == "v1.12.0"


def test_firecracker_config_raises_when_no_binary_record(config_dir: Path) -> None:
    raw: dict = {"firecracker": "not-a-dict"}
    (config_dir / "config.json").write_text(json.dumps(raw))
    with pytest.raises(AssetNotFoundError, match="No active binary for 'firecracker'"):
        get_firecracker_config(binary_record=None)


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


def test_get_assets_config_cache_dirs_under_cache(cache_dir: Path, config_dir: Path) -> None:
    assets = get_assets_config()
    # keys_dir is expected to be under the config dir (user-managed keys),
    # while the other asset dirs live under the cache dir.
    for key in (
        "kernels_dir",
        "images_dir",
        "bin_dir",
        "vms_dir",
        "logs_dir",
    ):
        assert assets[key].startswith(str(cache_dir)), f"{key} not under cache dir"
    # keys_dir is now config-backed (not cache-backed) per key management semantics
    assert assets["keys_dir"].startswith(str(config_dir))


def test_get_assets_config_persisted_as_nested_key(config_dir: Path) -> None:
    get_assets_config()
    raw = json.loads((config_dir / "config.json").read_text())
    assert "assets" in raw
    assert "kernels_dir" in raw["assets"]


def test_firecracker_and_assets_coexist(config_dir: Path, cache_dir: Path) -> None:
    bin_dir = cache_dir / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    fc_path = str(bin_dir / "firecracker-v1.12.0")
    (bin_dir / "firecracker-v1.12.0").write_bytes(b"\x7fELF")

    _seed_binary(cache_dir, "v1.12.0", fc_path)
    get_assets_config()
    if (config_dir / "config.json").exists():
        raw = json.loads((config_dir / "config.json").read_text())
        assert "assets" in raw
        assert "firecracker" not in raw


def test_initialize_default_config_creates_file(config_dir: Path) -> None:
    config_path = config_dir / "config.json"
    assert not config_path.exists()
    initialize_default_config()
    assert config_path.exists()


def test_initialize_default_config_writes_defaults(config_dir: Path) -> None:
    result = initialize_default_config()
    assert "assets" in result
    assert "firecracker" not in result
    raw = json.loads((config_dir / "config.json").read_text())
    assert "firecracker" not in raw


def test_initialize_default_config_idempotent(config_dir: Path) -> None:
    initialize_default_config()
    first_content = (config_dir / "config.json").read_text()
    initialize_default_config()
    second_content = (config_dir / "config.json").read_text()
    assert first_content == second_content


def test_initialize_default_config_preserves_binary_state_in_db(
    config_dir: Path, cache_dir: Path
) -> None:
    from mvmctl.core.mvm_db import MVMDatabase

    bin_dir = cache_dir / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    fc_path = str(bin_dir / "firecracker-v1.10.0")
    (bin_dir / "firecracker-v1.10.0").write_bytes(b"\x7fELF")

    _seed_binary(cache_dir, "v1.10.0", fc_path)

    initialize_default_config()
    db = MVMDatabase()
    db_entry = db.get_default_binary("firecracker")
    assert db_entry is not None
    assert db_entry.full_version == "v1.10.0"
    fc = get_firecracker_config(binary_record=db_entry)
    assert fc["full_version"] == "v1.10.0"
    raw = json.loads((config_dir / "config.json").read_text())
    assert "firecracker" not in raw


def test_rand_suffix_returns_correct_length() -> None:
    result = _rand_suffix(5)
    assert len(result) == 5
    assert all(c in "abcdefghijklmnopqrstuvwxyz0123456789" for c in result)


def test_rand_suffix_default_length() -> None:
    result = _rand_suffix()
    assert len(result) == 3


def test_initialize_default_config_removes_defaults_key(config_dir: Path) -> None:
    raw = {"defaults": {"image": "ubuntu"}, "assets": {"kernels_dir": "/k"}}
    (config_dir / "config.json").write_text(json.dumps(raw))
    result = initialize_default_config()
    assert "defaults" not in result


def test_initialize_default_config_removes_default_image(config_dir: Path) -> None:
    raw = {"default_image": "ubuntu-24.04"}
    (config_dir / "config.json").write_text(json.dumps(raw))
    result = initialize_default_config()
    assert "default_image" not in result


def test_initialize_default_config_removes_legacy_firecracker_key(config_dir: Path) -> None:
    raw = {"firecracker": {"full_version": "v1.10.0", "ci_version": "v1.10"}}
    (config_dir / "config.json").write_text(json.dumps(raw))
    result = initialize_default_config()
    assert "firecracker" not in result
    on_disk = json.loads((config_dir / "config.json").read_text())
    assert "firecracker" not in on_disk


def test_get_defaults_config_returns_none_when_no_defaults(cache_dir: Path) -> None:
    defaults = get_defaults_config()
    assert defaults["image"] is None
    assert defaults["kernel"] is None


def test_get_defaults_config_with_explicit_values(cache_dir: Path) -> None:
    defaults = get_defaults_config(
        default_image_slug="ubuntu-24.04",
        default_kernel_path="/path/to/vmlinux",
    )
    assert defaults["image"] == "ubuntu-24.04"
    assert defaults["kernel"] == "/path/to/vmlinux"


def test_set_defaults_value_image_non_string_raises(cache_dir: Path) -> None:
    with pytest.raises(ValueError, match="Default image must be a string"):
        set_defaults_value("image", 123)


def test_set_defaults_value_kernel_non_string_raises(cache_dir: Path) -> None:
    with pytest.raises(ValueError, match="Default kernel must be a string"):
        set_defaults_value("kernel", 456)


def test_set_defaults_value_generic_key(config_dir: Path) -> None:
    set_defaults_value("custom_key", "custom_value")
    raw = json.loads((config_dir / "config.json").read_text())
    assert raw["custom_key"] == "custom_value"


def test_get_defaults_config_image_from_explicit_value(cache_dir: Path) -> None:
    defaults = get_defaults_config(default_image_slug="ubuntu-24.04")
    assert defaults["image"] == "ubuntu-24.04"


def test_get_defaults_config_kernel_from_explicit_value(cache_dir: Path) -> None:
    kernel_path = str(cache_dir / "kernels" / "vmlinux-5.10")
    defaults = get_defaults_config(default_kernel_path=kernel_path)
    assert defaults["kernel"] == kernel_path


def test_set_defaults_value_image_persists_to_json(cache_dir: Path) -> None:
    set_defaults_value("image", "ubuntu-24.04")
    import json, os

    config_file = os.environ.get("MVM_CONFIG_DIR", "")
    if config_file:
        cfg = json.loads((Path(config_file) / "config.json").read_text())
        assert cfg["image"] == "ubuntu-24.04"


def test_set_defaults_value_kernel_persists_to_json(cache_dir: Path) -> None:
    set_defaults_value("kernel", "/path/to/vmlinux")
    import json, os

    config_file = os.environ.get("MVM_CONFIG_DIR", "")
    if config_file:
        cfg = json.loads((Path(config_file) / "config.json").read_text())
        assert cfg["kernel"] == "/path/to/vmlinux"
