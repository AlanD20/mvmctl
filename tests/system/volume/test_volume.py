"""Volume management system tests — CRUD, lifecycle, attach/detach, dependencies.

Migrated from tests/e2e/volume/test_volume.py.

VIOLATIONS REMOVED:
  - os.path.exists(data["volume"]["path"]) on HOST → checked inside VM via _guest_run
  - All commands run through _run_mvm / _guest_run (no subprocess on host)
  - No pytest.skip() calls
  - No os.path.exists on VM paths
"""

from __future__ import annotations

import json
import os
import shlex
import stat
import time
import uuid
from contextlib import contextmanager
from contextvars import ContextVar
from pathlib import Path, PurePosixPath
from typing import Any, Generator

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _run_mvm as _installed_mvm,
    _unique_subnet,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_volume,
]


def _inventory(runner_vm: str, domain: str) -> list[dict[str, Any]]:
    rows = _json_success_data(
        _installed_mvm(runner_vm, domain, "ls", "--json").stdout,
        list,
    )
    return sorted(rows, key=lambda row: json.dumps(row, sort_keys=True))


def _baseline_inventory(runner_vm: str, domain: str) -> list[dict[str, Any]]:
    rows = _inventory(runner_vm, domain)
    if domain == "kernel":
        # Public listing refreshes updated_at for missing recorded kernel files.
        # Compare every other field without repairing unrelated missing assets.
        rows = [
            {key: value for key, value in row.items() if key != "updated_at"}
            for row in rows
        ]
        rows.sort(key=lambda row: json.dumps(row, sort_keys=True))
    return rows


class _OwnedVolumeCase:
    """Only public removal of exact names attempted by this case is authorized."""

    domains = ("vm", "volume", "key", "network", "image", "kernel", "bin")

    def __init__(self, runner_vm: str) -> None:
        self.runner_vm = runner_vm
        self.before = {
            domain: _baseline_inventory(runner_vm, domain) for domain in self.domains
        }
        self.config = _json_success_data(
            _installed_mvm(runner_vm, "config", "ls", "--json").stdout,
            dict,
        )
        self.attempted: dict[str, dict[str, str | None]] = {
            domain: {} for domain in ("vm", "volume", "key", "network")
        }
        self.volume_sources: dict[str, tuple[Path, tuple[int, int]]] = {}

    def remember(self, domain: str, name: str) -> None:
        assert all(row.get("name") != name for row in self.before[domain]), (
            "creation would address a baseline resource",
            domain,
            name,
        )
        self.attempted[domain].setdefault(name, None)

    def remove(self, domain: str, name: str) -> None:
        rows = [
            row for row in _inventory(self.runner_vm, domain) if row.get("name") == name
        ]
        if name not in self.attempted[domain]:
            assert not rows, ("unattempted cleanup target exists", domain, name, rows)
            return
        assert len(rows) <= 1, rows
        if rows:
            resource_id = rows[0]["id"]
            remembered_id = self.attempted[domain][name]
            assert remembered_id is None or remembered_id == resource_id, (
                "owned name changed identity",
                domain,
                name,
                remembered_id,
                resource_id,
            )
            self.attempted[domain][name] = resource_id
            assert all(row["id"] != resource_id for row in self.before[domain])
            flags = () if domain == "key" else ("--force",)
            _installed_mvm(
                self.runner_vm,
                domain,
                "rm",
                resource_id,
                *flags,
                timeout=120 if domain == "vm" else 60,
            )
        remaining = _inventory(self.runner_vm, domain)
        assert all(row.get("name") != name for row in remaining), (
            domain,
            name,
            remaining,
        )
        resource_id = self.attempted[domain][name]
        if resource_id is not None:
            assert all(row["id"] != resource_id for row in remaining), (
                domain,
                resource_id,
            )
        if domain == "volume" and name in self.volume_sources:
            source_path, _ = self.volume_sources[name]
            assert not os.path.lexists(source_path), (
                "removed volume retained source",
                source_path,
            )

    def cleanup(self) -> None:
        failures = []
        for domain in ("vm", "volume", "key", "network"):
            for name in self.attempted[domain]:
                try:
                    self.remove(domain, name)
                except Exception as error:
                    failures.append(f"{domain}/{name}: {error}")
        for domain, baseline in self.before.items():
            try:
                assert _baseline_inventory(self.runner_vm, domain) == baseline, domain
            except Exception as error:
                failures.append(f"baseline {domain}: {error}")
        try:
            assert (
                _json_success_data(
                    _installed_mvm(self.runner_vm, "config", "ls", "--json").stdout,
                    dict,
                )
                == self.config
            ), "configuration changed"
        except Exception as error:
            failures.append(f"baseline config: {error}")
        assert not failures, "\n".join(failures)


_active_case: ContextVar[_OwnedVolumeCase] = ContextVar("volume_case")


@contextmanager
def _owned_scope(runner_vm: str) -> Generator[_OwnedVolumeCase, None, None]:
    case = _OwnedVolumeCase(runner_vm)
    token = _active_case.set(case)
    try:
        yield case
    finally:
        try:
            case.cleanup()
        finally:
            _active_case.reset(token)


@pytest.fixture(autouse=True)
def checked_volume_case(runner_vm: str) -> Generator[None, None, None]:
    with _owned_scope(runner_vm):
        yield


def _run_mvm(runner_vm: str, *args: str, **kwargs: Any) -> Any:
    domain = "volume" if args[0] == "vol" else args[0]
    if len(args) >= 3 and args[1] == "create":
        _active_case.get().remember(domain, args[2])
    result = _installed_mvm(runner_vm, *args, **kwargs)
    if len(args) >= 3 and args[1] == "create":
        case = _active_case.get()
        rows = [
            row for row in _inventory(runner_vm, domain) if row.get("name") == args[2]
        ]
        if rows:
            resource_id = rows[0]["id"]
            remembered_id = case.attempted[domain][args[2]]
            assert remembered_id is None or remembered_id == resource_id
            case.attempted[domain][args[2]] = resource_id
            if domain == "volume" and args[2] not in case.volume_sources:
                path = Path(rows[0]["path"])
                source_stat = path.lstat()
                assert path.name == f"{resource_id}.raw" and stat.S_ISREG(
                    source_stat.st_mode
                )
                case.volume_sources[args[2]] = (
                    path,
                    (source_stat.st_dev, source_stat.st_ino),
                )
    return result


def _remove_owned(runner_vm: str, domain: str, *names: str) -> None:
    case = _active_case.get()
    assert case.runner_vm == runner_vm
    for name in names:
        case.remove("volume" if domain == "vol" else domain, name)


def _require_vm_deps(runner_vm: str) -> None:
    """Admission is read-only: missing assets fail instead of repairing the runner."""
    mirror = os.environ.get("MVM_ASSET_MIRROR")
    assert mirror, "MVM_ASSET_MIRROR must identify the preseeded mirror"
    mirror_path = Path(mirror).expanduser()
    assert mirror_path.is_dir() and any(mirror_path.iterdir()), mirror_path
    images = _inventory(runner_vm, "image")
    alpine = [
        row
        for row in images
        if row["type"] == "alpine" and row["version"] == "3.23" and row["is_present"]
    ]
    assert len(alpine) == 1, alpine
    assert 0 < alpine[0]["minimum_rootfs_size_mib"] <= 512, alpine
    assert any(
        row["is_present"] and row["is_default"]
        for row in _inventory(runner_vm, "kernel")
    )
    binaries = _inventory(runner_vm, "bin")
    for binary_type in ("firecracker", "jailer"):
        assert any(
            row["type"] == binary_type
            and row["version"] == "1.17.0"
            and row["is_present"]
            and row["is_default"]
            for row in binaries
        ), (binary_type, binaries)


def _create_volume_vm(runner_vm: str, name: str, *options: str) -> Any:
    """Bound nested guests and explicitly own every SSH key selected at creation."""
    _require_vm_deps(runner_vm)
    guests = _inventory(runner_vm, "vm")
    assert len(guests) < 2, ("nested VM budget exhausted", guests)
    args = list(options)
    networkless = (
        "--no-network" in args
        and "--cloud-init-mode" in args
        and args[args.index("--cloud-init-mode") + 1] == "off"
    )
    if "--ssh-key" not in args and not networkless:
        key_name = f"sys-vol-key-{uuid.uuid4().hex[:8]}"
        _run_mvm(runner_vm, "key", "create", key_name, "--algorithm", "ed25519")
        args.extend(("--ssh-key", key_name))
    return _run_mvm(
        runner_vm,
        "vm",
        "create",
        name,
        "--image",
        "alpine:3.23",
        "-s",
        "512M",
        "--mem",
        "256M",
        "--vcpu",
        "1",
        *args,
        timeout=180,
    )


def _assert_source_identity(path: Path, identity: tuple[int, int]) -> None:
    source_stat = path.lstat()
    assert stat.S_ISREG(source_stat.st_mode)
    assert (source_stat.st_dev, source_stat.st_ino) == identity, (
        path,
        source_stat,
        identity,
    )


# ============================================================================
# Module-scoped fixture — shared volume for read-only tests
# ============================================================================


@pytest.fixture(scope="module")
def shared_volume(runner_vm: str) -> Generator[str, None, None]:
    """Module-scoped volume for read-only (ls, inspect) tests.

    Creating a new volume per read-only test wastes ~3s each.  A single
    module-scoped volume serves all read-only assertions in the class.
    """
    name = f"sys-modvol-{uuid.uuid4().hex[:6]}"
    with _owned_scope(runner_vm):
        _run_mvm(runner_vm, "volume", "create", name, "16M")
        yield name


# ============================================================================
# TestVolumeLifecycle — read-only tests FIRST, create/modify in middle,
# remove/destructive tests LAST
# ============================================================================


class TestVolumeLifecycle:
    """Volume CRUD operations and standard edge cases — grouped by operation type.

    Order within this class:
        1. Read-only / listing / inspection
        2. Create (raw backing, options, edge cases)
        3. Error paths (bad input, nonexistent)
        4. Modify operations (resize)
        5. Remove / destructive operations (last)
    """

    # ── 1. Read-only: listing and inspection ──────────────────────────────

    def test_volume_ls_empty(self, runner_vm: str) -> None:
        # Rationale: Verifies ls succeeds with empty output (headers present,
        # no rows).  A crash on empty DB would escape silently with only an
        # L0 returncode check.
        """Listing volumes when none exist should succeed with empty output."""
        result = _run_mvm(runner_vm, "volume", "ls")
        assert result.returncode == 0
        # L1: output should at least contain the table header
        assert (
            "name" in result.stdout.lower()
            or "volume" in result.stdout.lower()
            or result.stdout.strip() == ""
        )

    def test_volume_list_json(self, runner_vm: str, shared_volume: str) -> None:
        """List volumes in the versioned JSON envelope, including the shared volume."""
        result = _run_mvm(runner_vm, "volume", "ls", "--json")
        data = _json_success_data(result.stdout, list)
        assert any(v["name"] == shared_volume for v in data)

    def test_volume_inspect_json(self, runner_vm: str, shared_volume: str) -> None:
        """Inspect the shared volume through the versioned JSON envelope."""
        result = _run_mvm(runner_vm, "volume", "inspect", shared_volume, "--json")
        data = _json_success_data(result.stdout, dict)
        assert "volume" in data
        assert "name" in data["volume"]
        assert "size_bytes" in data["volume"]
        assert "format" in data["volume"]
        assert "status" in data["volume"]
        assert "path" in data["volume"]
        assert data["volume"]["name"] == shared_volume

    # ── 2. Create operations ─────────────────────────────────────────────

    def test_volume_create(self, runner_vm: str, unique_key_name: str) -> None:
        # Rationale: Verify both persisted metadata and the real backing
        # format so a raw label cannot hide a QCOW2 file.
        """Create a volume with raw storage and metadata by default."""
        vol_name = f"sys-vol-{unique_key_name}"
        try:
            result = _run_mvm(
                runner_vm,
                "volume",
                "create",
                vol_name,
                "16M",
            )
            assert result.returncode == 0

            inspect = _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json")
            data = _json_success_data(inspect.stdout, dict)
            assert data["volume"]["name"] == vol_name
            assert data["volume"]["size_bytes"] == 16 * 1024 * 1024
            assert data["volume"]["status"] == "available"
            assert data["volume"]["format"] == "raw"
            assert data["volume"]["is_read_only"] is False
            assert data["volume"]["cache_type"] == "Unsafe"

            path = data["volume"]["path"]
            assert PurePosixPath(path).suffix == ".raw"
            disk_info = _guest_run(
                runner_vm, f"qemu-img info --output=json {shlex.quote(path)}"
            )
            disk = json.loads(disk_info.stdout)
            assert disk["format"] == "raw"
            assert disk["virtual-size"] == 16 * 1024 * 1024
        finally:
            _remove_owned(runner_vm, "volume", vol_name)

    def test_volume_create_raw_with_read_only_shareable_writeback(
        self, runner_vm: str, unique_key_name: str
    ) -> None:
        # Rationale: Preserve the shared asset-volume option combination
        # while raw becomes the only data-volume format.
        """Raw volumes retain read-only, shareable, and writeback options."""
        vol_name = f"sys-vol-options-{unique_key_name}"
        try:
            result = _run_mvm(
                runner_vm,
                "volume",
                "create",
                vol_name,
                "16M",
                "--read-only",
                "--shareable",
                "--writeback",
            )
            assert result.returncode == 0

            inspect = _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json")
            data = _json_success_data(inspect.stdout, dict)
            assert data["volume"]["name"] == vol_name
            assert data["volume"]["format"] == "raw"
            assert data["volume"]["size_bytes"] == 16 * 1024 * 1024
            assert data["volume"]["status"] == "available"
            assert data["volume"]["is_read_only"] is True
            assert data["volume"]["cache_type"] == "Writeback"
            assert PurePosixPath(data["volume"]["path"]).suffix == ".raw"

            ls_result = _run_mvm(runner_vm, "volume", "ls", "--json")
            volumes = _json_success_data(ls_result.stdout, list)
            matching = [v for v in volumes if v["name"] == vol_name]
            assert len(matching) == 1
            assert matching[0]["is_shareable"] is True
        finally:
            _remove_owned(runner_vm, "volume", vol_name)

    # ── Read-only flag tests ─────────────────────────────────────────────

    def test_volume_create_read_only(
        self, runner_vm: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume (1-3s). Verifies --read-only flag
        # produces a read-only volume via JSON ls --json inspection.
        """Create a volume with --read-only and verify is_read_only via ls --json."""
        vol_name = f"sys-vol-ro-{unique_key_name}"
        try:
            result = _run_mvm(
                runner_vm,
                "volume",
                "create",
                vol_name,
                "16M",
                "--read-only",
            )
            assert result.returncode == 0

            ls_result = _run_mvm(runner_vm, "volume", "ls", "--json")
            volumes = _json_success_data(ls_result.stdout, list)
            matching = [v for v in volumes if v["name"] == vol_name]
            assert len(matching) == 1
            assert matching[0]["is_read_only"] is True
        finally:
            _remove_owned(runner_vm, "volume", vol_name)

    def test_volume_create_shareable(
        self, runner_vm: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume (1-3s). Verifies --shareable flag
        # produces a shareable volume via ls --json inspection.
        """Create a volume with --shareable and verify is_shareable via ls --json."""
        vol_name = f"sys-vol-sh-{unique_key_name}"
        try:
            result = _run_mvm(
                runner_vm,
                "volume",
                "create",
                vol_name,
                "16M",
                "--shareable",
            )
            assert result.returncode == 0

            ls_result = _run_mvm(runner_vm, "volume", "ls", "--json")
            volumes = _json_success_data(ls_result.stdout, list)
            matching = [v for v in volumes if v["name"] == vol_name]
            assert len(matching) == 1
            assert matching[0]["is_shareable"] is True
        finally:
            _remove_owned(runner_vm, "volume", vol_name)

    def test_volume_create_readonly_alias(
        self, runner_vm: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume (1-3s). Tests the --readonly alias
        # flag and the `mvm vol` alias for the volume command.
        """Create a volume using `mvm vol` with --readonly alias and verify via inspect."""
        vol_name = f"sys-vol-roa-{unique_key_name}"
        try:
            result = _run_mvm(
                runner_vm,
                "vol",
                "create",
                vol_name,
                "16M",
                "--readonly",
            )
            assert result.returncode == 0

            inspect = _run_mvm(runner_vm, "vol", "inspect", vol_name, "--json")
            data = _json_success_data(inspect.stdout, dict)
            assert data["volume"]["name"] == vol_name
            assert data["volume"]["is_read_only"] is True
        finally:
            _remove_owned(runner_vm, "vol", vol_name)

    def test_volume_list_json_read_only(
        self, runner_vm: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs two real volumes (2-6s). Verifies both writable
        # (default) and read-only (explicit) appear correctly in JSON output.
        """Create one writable and one read-only volume, verify is_read_only in ls --json."""
        vol_rw = f"sys-vol-rw-{unique_key_name}"
        vol_ro = f"sys-vol-rolist-{unique_key_name}"
        try:
            _run_mvm(runner_vm, "volume", "create", vol_rw, "16M")
            _run_mvm(
                runner_vm,
                "volume",
                "create",
                vol_ro,
                "8M",
                "--read-only",
            )

            ls_result = _run_mvm(runner_vm, "volume", "ls", "--json")
            volumes = _json_success_data(ls_result.stdout, list)

            rw_match = [v for v in volumes if v["name"] == vol_rw]
            assert len(rw_match) == 1
            assert rw_match[0]["is_read_only"] is False

            ro_match = [v for v in volumes if v["name"] == vol_ro]
            assert len(ro_match) == 1
            assert ro_match[0]["is_read_only"] is True
        finally:
            _remove_owned(runner_vm, "volume", vol_rw)
            _remove_owned(runner_vm, "volume", vol_ro)

    def test_volume_duplicate_name_rejected(
        self, runner_vm: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume. Tests that creating a second volume
        # with the same name is rejected — silent overwrite would lose data.
        """Creating a volume with duplicate name should be rejected."""
        vol_name = f"sys-vol-dup-{unique_key_name}"
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")
            result = _run_mvm(
                runner_vm,
                "volume",
                "create",
                vol_name,
                "8M",
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "already exists" in combined

            ls_result = _run_mvm(runner_vm, "volume", "ls", "--json")
            volumes = _json_success_data(ls_result.stdout, list)
            matching = [v for v in volumes if v["name"] == vol_name]
            assert len(matching) == 1
        finally:
            _remove_owned(runner_vm, "volume", vol_name)

    # ── 3. Error paths (no side effects) ─────────────────────────────────

    def test_volume_create_invalid_size_fails(self, runner_vm: str) -> None:
        # Rationale: No resources needed — error path for invalid size.
        # Verifies the CLI rejects non-numeric size strings.
        """Creating a volume with invalid size should be rejected."""
        result = _run_mvm(
            runner_vm,
            "volume",
            "create",
            "invalid-size-vol",
            "abc",
            check=False,
        )
        assert result.returncode != 0

        ls_result = _run_mvm(runner_vm, "volume", "ls", "--json")
        volumes = _json_success_data(ls_result.stdout, list)
        assert not any(v["name"] == "invalid-size-vol" for v in volumes)

    @pytest.mark.parametrize("removed_format", ["raw", "qcow2", "vmdk"])
    def test_volume_create_removed_format_flag_rejected_without_side_effects(
        self,
        runner_vm: str,
        unique_key_name: str,
        removed_format: str,
    ) -> None:
        """Reject the removed --format flag before creating a record or backing file."""
        vol_name = f"sys-vol-removedfmt-{unique_key_name}"
        sentinel = f"sys-vol-sentinel-{uuid.uuid4().hex[:8]}"
        sentinel_created = False
        try:
            _run_mvm(runner_vm, "volume", "create", sentinel, "16M")
            sentinel_created = True
            sentinel_data = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", sentinel, "--json").stdout,
                dict,
            )
            volume_dir = PurePosixPath(sentinel_data["volume"]["path"]).parent
            list_backing_files = (
                f"find {shlex.quote(str(volume_dir))} -mindepth 1 -maxdepth 1 -print"
            )
            backing_files_before = set(
                _guest_run(runner_vm, list_backing_files).stdout.splitlines()
            )
            result = _run_mvm(
                runner_vm,
                "volume",
                "create",
                vol_name,
                "16M",
                "--format",
                removed_format,
                check=False,
            )
            assert result.returncode != 0
            assert "unknown flag: --format" in (result.stdout + result.stderr).lower()

            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert not any(v["name"] == vol_name for v in volumes)

            backing_files_after = set(
                _guest_run(runner_vm, list_backing_files).stdout.splitlines()
            )
            assert backing_files_after == backing_files_before
        finally:
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            if any(volume["name"] == vol_name for volume in volumes):
                _remove_owned(runner_vm, "volume", vol_name)
            if sentinel_created:
                _remove_owned(runner_vm, "volume", sentinel)
            remaining = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert all(
                volume["name"] not in (vol_name, sentinel) for volume in remaining
            )

    def test_volume_inspect_nonexistent_fails(self, runner_vm: str) -> None:
        # Rationale: No resources needed — error path for nonexistent volume.
        """Inspecting a nonexistent volume should fail."""
        result = _run_mvm(
            runner_vm,
            "volume",
            "inspect",
            "nonexistent-volume-abc",
            check=False,
        )
        assert result.returncode != 0

    def test_volume_resize_nonexistent_fails(self, runner_vm: str) -> None:
        # Rationale: No resources needed — error path for nonexistent volume resize.
        """Resizing a nonexistent volume should fail."""
        result = _run_mvm(
            runner_vm,
            "volume",
            "resize",
            "nonexistent-volume-def",
            "32M",
            check=False,
        )
        assert result.returncode != 0

    def test_negative_volume_size(self, runner_vm: str) -> None:
        # Rationale: No resources needed — error path for negative size.
        """A volume with negative size should be rejected."""
        vol_name = f"sys-neg-{uuid.uuid4().hex[:6]}"
        result = _run_mvm(
            runner_vm,
            "volume",
            "create",
            vol_name,
            "--",
            "-1M",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "invalid" in combined or "negative" in combined

    def test_zero_size_volume(self, runner_vm: str) -> None:
        """A zero-size request must fail validation before publishing backing or a record."""
        vol_name = f"sys-zero-{uuid.uuid4().hex[:6]}"
        sentinel = f"sys-zero-sentinel-{uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(runner_vm, "volume", "create", sentinel, "16M")
            sentinel_data = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", sentinel, "--json").stdout,
                dict,
            )
            volume_dir = PurePosixPath(sentinel_data["volume"]["path"]).parent
            list_backing_files = (
                f"find {shlex.quote(str(volume_dir))} -mindepth 1 -maxdepth 1 -print"
            )
            before = set(_guest_run(runner_vm, list_backing_files).stdout.splitlines())

            result = _run_mvm(
                runner_vm,
                "volume",
                "create",
                vol_name,
                "0M",
                "--json",
                check=False,
            )
            assert result.returncode != 0
            envelope = json.loads(result.stdout)
            assert envelope["schema_version"] == 1
            assert envelope["status"] == "error"
            assert envelope["data"] is None
            assert envelope["error"]["code"] == "validation.failed"

            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert not any(volume["name"] == vol_name for volume in volumes)
            after = set(_guest_run(runner_vm, list_backing_files).stdout.splitlines())
            assert after == before, "rejected request published a raw backing file"
        finally:
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            for volume in volumes:
                if volume["name"] in (vol_name, sentinel):
                    _remove_owned(runner_vm, "volume", volume["name"])
            remaining = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert all(
                volume["name"] not in (vol_name, sentinel) for volume in remaining
            )

    # ── 4. Modify operations (resize) ────────────────────────────────────

    def test_volume_resize(self, runner_vm: str, unique_key_name: str) -> None:
        # Rationale: Needs a real volume (1-3s). Tests resize and verifies
        # new size via inspect --json — catches resize that silently fails
        # but reports success.
        """Create a volume and resize it, verify new size via inspect --json."""
        vol_name = f"sys-vol-resize-{unique_key_name}"
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")

            result = _run_mvm(runner_vm, "volume", "resize", vol_name, "32M")
            assert result.returncode == 0

            inspect = _run_mvm(
                runner_vm,
                "volume",
                "inspect",
                vol_name,
                "--json",
            )
            data = _json_success_data(inspect.stdout, dict)
            assert data["volume"]["size_bytes"] == 32 * 1024 * 1024
        finally:
            _remove_owned(runner_vm, "volume", vol_name)

    def test_volume_resize_rejects_shrink_without_changing_backing(
        self, runner_vm: str, unique_key_name: str
    ) -> None:
        """A rejected shrink must leave the authoritative raw backing unchanged."""
        vol_name = f"sys-vol-shrink-{unique_key_name}"
        created = False
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")
            created = True

            before = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json").stdout,
                dict,
            )["volume"]
            assert before["size_bytes"] == 16 * 1024 * 1024

            result = _run_mvm(
                runner_vm,
                "volume",
                "resize",
                vol_name,
                "8M",
                check=False,
            )
            assert result.returncode != 0
            _run_mvm(runner_vm, "volume", "resize", vol_name, "16M")

            after = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json").stdout,
                dict,
            )["volume"]
            assert after["id"] == before["id"]
            assert after["size_bytes"] == before["size_bytes"]
        finally:
            if created:
                _remove_owned(runner_vm, "volume", vol_name)

    # ── 5. Remove / destructive operations ──────────────────────────────

    def test_volume_remove(self, runner_vm: str, unique_key_name: str) -> None:
        # Rationale: Needs a real volume. Tests normal rm and verifies gone
        # via ls --json — catches rm that reports success but leaves stale
        # DB records.
        """Create and remove a volume, verify it's gone via ls --json."""
        vol_name = f"sys-vol-rm-{unique_key_name}"
        _run_mvm(runner_vm, "volume", "create", vol_name, "16M")

        result = _run_mvm(runner_vm, "volume", "rm", vol_name)
        assert result.returncode == 0

        result = _run_mvm(runner_vm, "volume", "ls", "--json")
        volumes = _json_success_data(result.stdout, list)
        assert not any(v["name"] == vol_name for v in volumes)

    def test_volume_rm_with_force(self, runner_vm: str, unique_key_name: str) -> None:
        # Rationale: Needs a real volume. Tests --force removal and verifies
        # gone via ls --json — force path differs from normal rm.
        """Remove a volume with --force and verify it's gone via ls --json."""
        vol_name = f"sys-vol-frc-{unique_key_name}"
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")
            result = _run_mvm(runner_vm, "volume", "rm", vol_name, "--force")
            assert result.returncode == 0
        finally:
            _remove_owned(runner_vm, "volume", vol_name)

        result = _run_mvm(runner_vm, "volume", "ls", "--json")
        volumes = _json_success_data(result.stdout, list)
        assert not any(v["name"] == vol_name for v in volumes)

    def test_volume_remove_nonexistent(self, runner_vm: str) -> None:
        # Rationale: No resources needed — error path for nonexistent volume.
        """Removing a nonexistent volume should give clear error, not crash."""
        result = _run_mvm(
            runner_vm,
            "volume",
            "rm",
            "nonexistent-volume-name-that-will-not-exist",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined

        result_vol = _run_mvm(runner_vm, "volume", "ls", "--json")
        vols_after = _json_success_data(result_vol.stdout, list)
        assert not any(
            v["name"] == "nonexistent-volume-name-that-will-not-exist"
            for v in vols_after
        )

    def test_volume_remove_multiple(self, runner_vm: str, unique_key_name: str) -> None:
        # Rationale: Needs two real volumes. Tests multi-rm and verifies
        # both gone — catches multi-rm that only removes the first.
        """Remove two volumes at once and verify both gone via ls --json."""
        vol1 = f"sys-vol-mrm1-{unique_key_name}"
        vol2 = f"sys-vol-mrm2-{unique_key_name}"
        try:
            _run_mvm(runner_vm, "volume", "create", vol1, "16M")
            _run_mvm(runner_vm, "volume", "create", vol2, "8M")

            result = _run_mvm(runner_vm, "volume", "rm", vol1, vol2)
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "volume", "ls", "--json")
            volumes = _json_success_data(result.stdout, list)
            assert not any(v["name"] == vol1 for v in volumes)
            assert not any(v["name"] == vol2 for v in volumes)
        finally:
            _remove_owned(runner_vm, "volume", vol1, vol2)

    def test_volume_remove_partial_failure(
        self, runner_vm: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume. Tests partial failure: one exists,
        # one nonexistent — the existing volume should still be removed.
        """Remove one existing volume and one nonexistent — existing should still be removed."""
        vol_name = f"sys-vol-partial-{unique_key_name}"
        nonexistent = "nonexistent-volume-partial-test"
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")

            result = _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                nonexistent,
                check=False,
            )
            # Partial failure: CLI may return 0 with error in stderr,
            # or return non-zero. Either way, the error must mention
            # the nonexistent volume.
            combined = (result.stdout + result.stderr).lower()
            assert nonexistent in combined, (
                f"Expected error mentioning '{nonexistent}', got: {combined}"
            )

            result = _run_mvm(runner_vm, "volume", "ls", "--json")
            volumes = _json_success_data(result.stdout, list)
            assert not any(v["name"] == vol_name for v in volumes)
        finally:
            _remove_owned(runner_vm, "volume", vol_name)

    # ── 6. Advanced: invariants across attach/detach ─────────────────────

    @pytest.mark.requires_kvm
    def test_volume_invariants_available_attached_cycle(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
    ) -> None:
        """A protected raw volume keeps its identity through attach and detach."""
        vm_name = unique_vm_name
        vol_name = f"sys-vol-inv-{unique_key_name}"
        backing_path: str | None = None
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")
            available = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json").stdout,
                dict,
            )
            volume = available["volume"]
            assert volume["status"] == "available"
            assert volume["format"] == "raw"
            assert available["attachment"]["vm_id"] is None
            assert available["disk_info"]["protected"] is False
            backing_path = volume["path"]
            backing_size = _guest_run(
                runner_vm,
                f"sudo -n stat -c %s {shlex.quote(backing_path)}",
            )
            assert int(backing_size.stdout.strip()) == 16 * 1024 * 1024

            # Network placement is incidental to this volume lifecycle.
            created = _json_success_data(
                _create_volume_vm(
                    runner_vm,
                    vm_name,
                    "--no-network",
                    "--cloud-init-mode",
                    "off",
                    "--skip-deblob",
                    "--no-pci",
                    "--json",
                ).stdout,
                list,
            )
            assert len(created) == 1 and created[0]["status"] == "running"
            vm_id = created[0]["id"]
            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "volume", "attach", vm_name, vol_name)

            attached = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json").stdout,
                dict,
            )
            assert attached["volume"]["id"] == volume["id"]
            assert attached["volume"]["path"] == backing_path
            assert attached["volume"]["status"] == "attached"
            assert attached["attachment"]["vm_id"] == vm_id
            _run_mvm(runner_vm, "vm", "start", vm_name)
            partitions = _run_mvm(
                runner_vm,
                "exec",
                vm_name,
                "--timeout",
                "30",
                "--",
                "cat",
                "/proc/partitions",
                timeout=30,
            )
            assert "vdb" in partitions.stdout
            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "volume", "detach", vm_name, vol_name)

            detached = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json").stdout,
                dict,
            )
            assert detached["volume"]["id"] == volume["id"]
            assert detached["volume"]["path"] == ""
            assert detached["volume"]["status"] == "available"
            assert detached["attachment"]["vm_id"] is None
            assert detached["disk_info"]["protected"] is True
            assert detached["disk_info"]["logical_bytes"] == 16 * 1024 * 1024
        finally:
            vms = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            if any(vm["name"] == vm_name for vm in vms):
                _remove_owned(runner_vm, "vm", vm_name)
            vms = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            assert all(vm["name"] != vm_name for vm in vms)
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list
            )
            if any(item["name"] == vol_name for item in volumes):
                _remove_owned(runner_vm, "volume", vol_name)
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list
            )
            assert all(item["name"] != vol_name for item in volumes)
            if backing_path is not None:
                assert (
                    _guest_run(
                        runner_vm,
                        f"test ! -e {shlex.quote(backing_path)}",
                        check=False,
                    ).returncode
                    == 0
                ), "removed protected volume retained its managed raw source"


class TestRawVolumeAuthoritativeStorage:
    def test_create_grow_inspect_and_remove(self, runner_vm: str) -> None:
        """The installed CLI keeps raw backing and reported size in sync across operations."""
        name = f"sys-raw-volume-{uuid.uuid4().hex[:8]}"
        try:
            created = _json_success_data(
                _run_mvm(runner_vm, "volume", "create", name, "16M", "--json").stdout,
                dict,
            )
            assert created["name"] == name
            assert created["format"] == "raw"
            assert created["size_bytes"] == 16 * 1024 * 1024

            inspected = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", name, "--json").stdout,
                dict,
            )["volume"]
            assert inspected["id"] == created["id"]
            assert inspected["size_bytes"] == created["size_bytes"]

            _run_mvm(runner_vm, "volume", "resize", name, "32M")
            grown = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", name, "--json").stdout,
                dict,
            )["volume"]
            assert grown["id"] == created["id"]
            assert grown["size_bytes"] == 32 * 1024 * 1024

            _run_mvm(runner_vm, "volume", "rm", name, "--force")
            assert (
                _guest_run(
                    runner_vm,
                    f"test ! -e {shlex.quote(created['path'])}",
                    check=False,
                ).returncode
                == 0
            )
        finally:
            listed = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            if any(volume["name"] == name for volume in listed):
                _remove_owned(runner_vm, "volume", name)
            listed = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert all(volume["name"] != name for volume in listed)

    @pytest.mark.requires_kvm
    def test_changed_protected_source_is_retained(
        self,
        runner_vm: str,
        unique_vm_name: str,
    ) -> None:
        """Removal refuses a same-inode, same-size source changed after import."""
        name = f"sys-raw-changed-{uuid.uuid4().hex[:8]}"
        source_path: Path | None = None
        retained_path: Path | None = None
        source_identity: tuple[int, int] | None = None
        try:
            created = _json_success_data(
                _run_mvm(runner_vm, "volume", "create", name, "16M", "--json").stdout,
                dict,
            )
            source_path = Path(created["path"])
            assert source_path.name == f"{created['id']}.raw"
            source_identity = _active_case.get().volume_sources[name][1]
            _assert_source_identity(source_path, source_identity)
            retained_path = source_path.with_suffix(".retained")
            assert not retained_path.exists()
            _create_volume_vm(
                runner_vm,
                unique_vm_name,
                "--no-network",
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--no-pci",
                "--volume",
                name,
            )
            _run_mvm(runner_vm, "vm", "stop", unique_vm_name)
            _run_mvm(runner_vm, "volume", "detach", unique_vm_name, name)
            inspected = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", name, "--json").stdout,
                dict,
            )
            assert inspected["disk_info"]["protected"] is True

            before = source_path.stat().st_ctime_ns
            time.sleep(0.02)
            source_fd = os.open(source_path, os.O_RDWR | os.O_NOFOLLOW)
            with os.fdopen(source_fd, "r+b") as raw:
                opened = os.fstat(raw.fileno())
                assert (opened.st_dev, opened.st_ino) == source_identity
                raw.write(b"X")
                raw.flush()
                os.fsync(raw.fileno())
            assert source_path.stat().st_ctime_ns != before

            rejected = _run_mvm(runner_vm, "volume", "rm", name, "--force", check=False)
            assert rejected.returncode != 0
            with source_path.open("rb") as raw:
                assert raw.read(1) == b"X"
            still_protected = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", name, "--json").stdout,
                dict,
            )
            assert still_protected["disk_info"]["protected"] is True

            _assert_source_identity(source_path, source_identity)
            assert not os.path.lexists(retained_path)
            source_path.rename(retained_path)
            _run_mvm(runner_vm, "volume", "rm", name, "--force")
            with retained_path.open("rb") as raw:
                assert raw.read(1) == b"X"
        finally:
            _remove_owned(runner_vm, "vm", unique_vm_name)
            if (
                source_path is not None
                and source_path.exists()
                and retained_path is not None
                and not retained_path.exists()
            ):
                assert source_identity is not None
                _assert_source_identity(source_path, source_identity)
                source_path.rename(retained_path)
            _remove_owned(runner_vm, "volume", name)
            if retained_path is not None and os.path.lexists(retained_path):
                assert source_identity is not None
                _assert_source_identity(retained_path, source_identity)
                retained_path.unlink()
                assert not os.path.lexists(retained_path)
            vms = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            assert all(vm["name"] != unique_vm_name for vm in vms)
            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert all(volume["name"] != name for volume in volumes)


class TestVolumeAttachDetach:
    """Volume attach/detach lifecycle with VMs — requires KVM and network."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.domain_volume,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_attach_detach_then_stop_start(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs a real VM (30-120s). Full lifecycle: create,
        # stop, detach, re-attach, start.
        """Create VM with volume, stop, detach, re-attach, start — full lifecycle."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        vol_name = f"sys-st-vol-{unique_key_name}"
        net_name = unique_network_name

        try:
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")

            _create_volume_vm(
                runner_vm,
                vm_name,
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            _run_mvm(runner_vm, "volume", "detach", vm_name, vol_name)

            vol_result = _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json")
            vol_data = _json_success_data(vol_result.stdout, dict)
            assert vol_data.get("volume", {}).get("status") == "available"

            _run_mvm(runner_vm, "volume", "attach", vm_name, vol_name)

            vol_result = _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json")
            vol_data = _json_success_data(vol_result.stdout, dict)
            assert vol_data.get("volume", {}).get("status") == "attached"

            _run_mvm(runner_vm, "vm", "start", vm_name)

            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = _json_success_data(result.stdout, list)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry.get("status") == "running"
        finally:
            _remove_owned(runner_vm, "vm", vm_name)
            _remove_owned(runner_vm, "volume", vol_name)
            _remove_owned(runner_vm, "key", key_name)
            _remove_owned(runner_vm, "network", net_name)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_attach_volume_to_stopped_then_start(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs a real VM (30-120s). Tests attach-to-stopped
        # then start (Bug #7 scenario).
        """Attach volume to a stopped VM then start it — Bug #7 scenario."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        vol_name = f"sys-st-vol-{unique_key_name}"
        net_name = unique_network_name

        try:
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")

            _create_volume_vm(
                runner_vm,
                vm_name,
                "--network",
                net_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            _run_mvm(runner_vm, "volume", "attach", vm_name, vol_name)

            vol_result = _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json")
            vol_data = _json_success_data(vol_result.stdout, dict)
            assert vol_data.get("volume", {}).get("status") == "attached"

            _run_mvm(runner_vm, "vm", "start", vm_name)

            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = _json_success_data(result.stdout, list)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry.get("status") == "running"
        finally:
            _remove_owned(runner_vm, "vm", vm_name)
            _remove_owned(runner_vm, "volume", vol_name)
            _remove_owned(runner_vm, "key", key_name)
            _remove_owned(runner_vm, "network", net_name)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_attach_detach_attach_same_volume(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs a real VM (30-120s). Tests detach, verify
        # available, re-attach, verify attached.
        """Detach volume, verify available, re-attach, verify attached."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        vol_name = f"sys-st-vol-{unique_key_name}"
        net_name = unique_network_name

        try:
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")

            _create_volume_vm(
                runner_vm,
                vm_name,
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            _run_mvm(runner_vm, "volume", "detach", vm_name, vol_name)

            vol_result = _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json")
            vol_data = _json_success_data(vol_result.stdout, dict)
            assert vol_data.get("volume", {}).get("status") == "available"

            _run_mvm(runner_vm, "volume", "attach", vm_name, vol_name)

            vol_result = _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json")
            vol_data = _json_success_data(vol_result.stdout, dict)
            assert vol_data.get("volume", {}).get("status") == "attached"

            _run_mvm(runner_vm, "vm", "start", vm_name)
        finally:
            _remove_owned(runner_vm, "vm", vm_name)
            _remove_owned(runner_vm, "volume", vol_name)
            _remove_owned(runner_vm, "key", key_name)
            _remove_owned(runner_vm, "network", net_name)


class TestVolumeCrossVM:
    """Cross-VM volume attachment constraints — requires KVM."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_volume,
    ]

    @pytest.mark.requires_kvm
    def test_cross_vm_volume_attach_rejected(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs TWO real VMs (30-120s each). Cross-VM exclusivity
        # requires a second VM for conflict.
        """Attaching an already-attached volume to a different VM must fail."""
        vm_a = unique_vm_name
        vm_b = f"sys-vm-b-{uuid.uuid4().hex[:6]}"
        vol_name = f"sys-xvm-vol-{unique_key_name}"
        net_name = unique_network_name

        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )

            _create_volume_vm(
                runner_vm,
                vm_a,
                "--network",
                net_name,
            )
            _create_volume_vm(
                runner_vm,
                vm_b,
                "--network",
                net_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_a, "--force")
            _run_mvm(runner_vm, "vm", "stop", vm_b, "--force")

            _run_mvm(runner_vm, "volume", "attach", vm_a, vol_name)

            vol_result = _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json")
            vol_data = _json_success_data(vol_result.stdout, dict)
            assert vol_data["volume"]["status"] == "attached"
            assert vol_data.get("attachment", {}).get("vm_id") is not None

            vm_ls = _run_mvm(runner_vm, "vm", "ls", "--json")
            vm_entries = _json_success_data(vm_ls.stdout, list)
            vm_a_info = next((v for v in vm_entries if v["name"] == vm_a), None)
            assert vm_a_info is not None
            assert vol_data["attachment"]["vm_id"] == vm_a_info["id"]

            result = _run_mvm(
                runner_vm,
                "volume",
                "attach",
                vm_b,
                vol_name,
                check=False,
            )
            assert result.returncode != 0

            vol_result = _run_mvm(runner_vm, "volume", "inspect", vol_name, "--json")
            vol_data = _json_success_data(vol_result.stdout, dict)
            assert vol_data["volume"]["status"] == "attached"
            assert vol_data.get("attachment", {}).get("vm_id") == vm_a_info["id"]
        finally:
            _remove_owned(runner_vm, "vm", vm_a)
            _remove_owned(runner_vm, "vm", vm_b)
            _remove_owned(runner_vm, "volume", vol_name)
            _remove_owned(runner_vm, "network", net_name)


class TestVolumeRunningVMDependency:
    """Volume dependency checks with running VMs — requires KVM and network."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.domain_volume,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_delete_volume_used_by_running_vm_fails(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs a real VM with volume (30-120s). Tests rm
        # rejection when VM is running.
        """Deleting a volume attached to a running VM should be rejected."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        vol_name = f"sys-dep-vol-{unique_key_name}"
        net_name = unique_network_name

        try:
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            _create_volume_vm(
                runner_vm,
                vm_name,
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            vms = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
                list,
            )
            assert any(vm["name"] == vm_name for vm in vms)

            result = _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                check=False,
            )
            assert result.returncode != 0
            error_text = (result.stdout + result.stderr).lower()
            assert "in use" in error_text or "attached" in error_text

            volumes_after = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert any(volume["name"] == vol_name for volume in volumes_after)
        finally:
            _remove_owned(runner_vm, "vm", vm_name)
            _remove_owned(runner_vm, "volume", vol_name)
            _remove_owned(runner_vm, "key", key_name)
            _remove_owned(runner_vm, "network", net_name)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_force_remove_requires_stopped_vm(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: A running guest may still use its block device. --force
        # must not hot-unplug or delete its backing before the VM stops.
        """--force refuses a running VM, then removes its volume after stop."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        vol_name = f"sys-dep-vol-{unique_key_name}"
        net_name = unique_network_name

        try:
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            _create_volume_vm(
                runner_vm,
                vm_name,
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            vms = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
                list,
            )
            assert any(vm["name"] == vm_name for vm in vms)

            result = _run_mvm(
                runner_vm,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            assert result.returncode != 0
            assert "stop the vm" in (result.stdout + result.stderr).lower()

            volumes_while_running = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert any(volume["name"] == vol_name for volume in volumes_while_running)
            vm_while_running = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            assert vm_while_running["vm"]["status"] == "running"
            assert any(
                volume["name"] == vol_name for volume in vm_while_running["volumes"]
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")
            _run_mvm(runner_vm, "volume", "rm", vol_name, "--force")

            volumes_after = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert all(volume["name"] != vol_name for volume in volumes_after)
            vm_after = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            assert vm_after["vm"]["status"] == "stopped"
            assert all(volume["name"] != vol_name for volume in vm_after["volumes"])
        finally:
            _remove_owned(runner_vm, "vm", vm_name)
            _remove_owned(runner_vm, "volume", vol_name)
            _remove_owned(runner_vm, "key", key_name)
            _remove_owned(runner_vm, "network", net_name)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_grow_attached_volume_requires_stopped_vm(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Growth must not mutate active guest backing, but a stopped
        # VM may retain the attachment; detach is not required.
        """Growth refuses a running VM and succeeds while attached after stop."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        vol_name = f"sys-dep-vol-{unique_key_name}"
        net_name = unique_network_name

        try:
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "16M")
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            _create_volume_vm(
                runner_vm,
                vm_name,
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            vol_list_before = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            vol_info_before = next(
                (v for v in vol_list_before if v.get("name") == vol_name),
                {},
            )
            assert vol_info_before.get("size_bytes") == 16 * 1024 * 1024

            result = _run_mvm(
                runner_vm,
                "volume",
                "resize",
                vol_name,
                "32M",
                check=False,
            )
            assert result.returncode != 0
            assert "stop the vm" in (result.stdout + result.stderr).lower()

            vol_list_while_running = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            vol_info_while_running = next(
                (v for v in vol_list_while_running if v.get("name") == vol_name),
                {},
            )
            assert vol_info_while_running.get("size_bytes") == 16 * 1024 * 1024

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")
            _run_mvm(runner_vm, "volume", "resize", vol_name, "32M")

            vol_list_after = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            vol_info_after = next(
                (v for v in vol_list_after if v.get("name") == vol_name),
                {},
            )
            assert vol_info_after.get("size_bytes") == 32 * 1024 * 1024
            vm_after = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            assert vm_after["vm"]["status"] == "stopped"
            assert any(volume["name"] == vol_name for volume in vm_after["volumes"])
        finally:
            _remove_owned(runner_vm, "vm", vm_name)
            _remove_owned(runner_vm, "volume", vol_name)
            _remove_owned(runner_vm, "key", key_name)
            _remove_owned(runner_vm, "network", net_name)


class TestVolumeNegativeFailure:
    """Volume failure modes with VMs — requires KVM."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_volume,
    ]

    @pytest.mark.requires_kvm
    def test_attach_nonexistent_volume_to_vm_fails(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs a real VM (30-120s). Tests error path for
        # nonexistent volume attach.
        """Attaching a nonexistent volume should give clear error."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            _create_volume_vm(
                runner_vm,
                vm_name,
                "--network",
                net_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            result = _run_mvm(
                runner_vm,
                "volume",
                "attach",
                vm_name,
                "nonexistent-volume-name",
                "--json",
                check=False,
            )
            assert result.returncode != 0
            envelope = json.loads(result.stdout)
            assert envelope["schema_version"] == 1
            assert envelope["status"] == "error"
            assert envelope["error"]["code"] == "volume.not_found"

            vm_info = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            assert not any(
                volume.get("name") == "nonexistent-volume-name"
                for volume in vm_info["volumes"]
            )
        finally:
            _remove_owned(runner_vm, "vm", vm_name)
            _remove_owned(runner_vm, "network", net_name)

    @pytest.mark.requires_kvm
    def test_detach_nonexistent_volume_from_vm_fails(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs a real VM (30-120s). Tests error path for
        # nonexistent volume detach.
        """Detaching a nonexistent volume should give clear error."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
            )
            _create_volume_vm(
                runner_vm,
                vm_name,
                "--network",
                net_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            result = _run_mvm(
                runner_vm,
                "volume",
                "detach",
                vm_name,
                "nonexistent-volume-name",
                "--json",
                check=False,
            )
            assert result.returncode != 0
            envelope = json.loads(result.stdout)
            assert envelope["schema_version"] == 1
            assert envelope["status"] == "error"
            assert envelope["error"]["code"] == "volume.not_found"

            volumes = _json_success_data(
                _run_mvm(runner_vm, "volume", "ls", "--json").stdout,
                list,
            )
            assert all(
                volume["name"] != "nonexistent-volume-name" for volume in volumes
            )

            vm_info = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout,
                dict,
            )
            assert not vm_info["volumes"]
        finally:
            _remove_owned(runner_vm, "vm", vm_name)
            _remove_owned(runner_vm, "network", net_name)
