"""Volume management system tests — CRUD, lifecycle, attach/detach, dependencies."""

from __future__ import annotations

import json
import os
import uuid
from typing import Generator

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet, ensure_vm_deps

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_volume,
]


# ============================================================================
# Module-scoped fixture — shared volume for read-only tests
# ============================================================================


@pytest.fixture(scope="module")
def shared_volume(mvm_binary: str) -> Generator[str, None, None]:
    """Module-scoped volume for read-only (ls, inspect) tests.

    Creating a new volume per read-only test wastes ~3s each.  A single
    module-scoped volume serves all read-only assertions in the class.
    """
    name = f"sys-modvol-{uuid.uuid4().hex[:6]}"
    _run_mvm(mvm_binary, "volume", "create", name, "512M")
    try:
        yield name
    finally:
        _run_mvm(mvm_binary, "volume", "rm", name, "--force", check=False)


# ============================================================================
# TestVolumeLifecycle — read-only tests FIRST, create/modify in middle,
# remove/destructive tests LAST
# ============================================================================


class TestVolumeLifecycle:
    """Volume CRUD operations and standard edge cases — grouped by operation type.

    Order within this class:
        1. Read-only / listing / inspection
        2. Create (various formats, edge cases)
        3. Error paths (bad input, nonexistent)
        4. Modify operations (resize)
        5. Remove / destructive operations (last)
    """

    # ── 1. Read-only: listing and inspection ──────────────────────────────

    def test_volume_ls_empty(self, mvm_binary: str) -> None:
        # Rationale: Verifies ls succeeds with empty output (headers present,
        # no rows).  A crash on empty DB would escape silently with only an
        # L0 returncode check.
        """Listing volumes when none exist should succeed with empty output."""
        result = _run_mvm(mvm_binary, "volume", "ls")
        assert result.returncode == 0
        # L1: output should at least contain the table header
        assert (
            "name" in result.stdout.lower()
            or "volume" in result.stdout.lower()
            or result.stdout.strip() == ""
        )

    def test_volume_list(self, mvm_binary: str, shared_volume: str) -> None:
        # Rationale: Needs a shared volume (~3s for the whole module) to
        # verify ls output contains the volume name — proves DB persistence
        # without creating a volume per test.
        """List volumes — should include the shared volume."""
        result = _run_mvm(mvm_binary, "volume", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert any(v["name"] == shared_volume for v in data)

    def test_volume_list_json(
        self, mvm_binary: str, shared_volume: str
    ) -> None:
        # Rationale: Needs a shared volume. JSON parsing catches structural
        # regressions that plain-text output checks miss.
        """List volumes in JSON format — verify field structure."""
        result = _run_mvm(mvm_binary, "volume", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, list)
        assert any(v["name"] == shared_volume for v in data)

    def test_volume_inspect(self, mvm_binary: str, shared_volume: str) -> None:
        # Rationale: Needs a shared volume. L1 verification that inspect
        # returns the volume name — proves the DB lookup by name works.
        """Inspect a volume — verify name via --json."""
        result = _run_mvm(
            mvm_binary, "volume", "inspect", shared_volume, "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert data.get("volume", {}).get("name") == shared_volume

    def test_volume_inspect_json(
        self, mvm_binary: str, shared_volume: str
    ) -> None:
        # Rationale: Needs a shared volume. L2 verification of all expected
        # fields — catches missing fields that would break tooling.
        """Inspect a volume with --json and verify parsed fields."""
        result = _run_mvm(
            mvm_binary, "volume", "inspect", shared_volume, "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert "volume" in data
        assert "name" in data["volume"]
        assert "size_bytes" in data["volume"]
        assert "format" in data["volume"]
        assert "status" in data["volume"]
        assert "path" in data["volume"]
        assert data["volume"]["name"] == shared_volume

    # ── 2. Create operations ─────────────────────────────────────────────

    def test_volume_create(self, mvm_binary: str, unique_key_name: str) -> None:
        # Rationale: Needs a real volume (1-3s). Verifies size and status via
        # inspect --json. Catches silent create failures where the volume
        # appears in ls but has wrong metadata.
        """Create a volume and verify size/status via inspect --json."""
        vol_name = f"sys-vol-{unique_key_name}"
        try:
            result = _run_mvm(
                mvm_binary,
                "volume",
                "create",
                vol_name,
                "512M",
            )
            assert result.returncode == 0

            inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            data = json.loads(inspect.stdout)
            assert data["volume"]["name"] == vol_name
            assert data["volume"]["size_bytes"] == 512 * 1024 * 1024
            assert data["volume"]["status"] == "available"
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_create_with_format_qcow2(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume (1-3s). Tests --format qcow2 and
        # verifies format field — regression where --format is ignored
        # would silently create raw volumes instead.
        """Create a volume with --format qcow2 and verify format via inspect --json."""
        vol_name = f"sys-vol-qcow2-{unique_key_name}"
        try:
            result = _run_mvm(
                mvm_binary,
                "volume",
                "create",
                vol_name,
                "512M",
                "--format",
                "qcow2",
            )
            assert result.returncode == 0

            inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            data = json.loads(inspect.stdout)
            assert data["volume"]["name"] == vol_name
            assert data["volume"]["format"] == "qcow2"
            assert data["volume"]["size_bytes"] == 512 * 1024 * 1024
            assert data["volume"]["status"] == "available"
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_create_with_format_raw(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume (1-3s). Tests --format raw (the
        # default) explicitly — catches regression where format is
        # misidentified or default changes.
        """Create a volume with --format raw and verify format via inspect --json."""
        vol_name = f"sys-vol-raw-{unique_key_name}"
        try:
            result = _run_mvm(
                mvm_binary,
                "volume",
                "create",
                vol_name,
                "512M",
                "--format",
                "raw",
            )
            assert result.returncode == 0

            inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            data = json.loads(inspect.stdout)
            assert data["volume"]["name"] == vol_name
            assert data["volume"]["format"] == "raw"
            assert data["volume"]["size_bytes"] == 512 * 1024 * 1024
            assert data["volume"]["status"] == "available"
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    # ── Read-only flag tests ─────────────────────────────────────────────

    def test_volume_create_read_only(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume (1-3s). Verifies --read-only flag
        # produces a read-only volume via JSON ls --json inspection.
        """Create a volume with --read-only and verify is_read_only via ls --json."""
        vol_name = f"sys-vol-ro-{unique_key_name}"
        try:
            result = _run_mvm(
                mvm_binary,
                "volume",
                "create",
                vol_name,
                "512M",
                "--read-only",
            )
            assert result.returncode == 0

            ls_result = _run_mvm(mvm_binary, "volume", "ls", "--json")
            volumes = json.loads(ls_result.stdout)
            matching = [v for v in volumes if v["name"] == vol_name]
            assert len(matching) == 1
            assert matching[0]["is_read_only"] is True
        finally:
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )

    def test_volume_create_readonly_alias(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume (1-3s). Tests the --readonly alias
        # flag and the `mvm vol` alias for the volume command.
        """Create a volume using `mvm vol` with --readonly alias and verify via inspect."""
        vol_name = f"sys-vol-roa-{unique_key_name}"
        try:
            result = _run_mvm(
                mvm_binary,
                "vol",
                "create",
                vol_name,
                "512M",
                "--readonly",
            )
            assert result.returncode == 0

            inspect = _run_mvm(mvm_binary, "vol", "inspect", vol_name, "--json")
            data = json.loads(inspect.stdout)
            assert data["volume"]["name"] == vol_name
            assert data["volume"]["is_read_only"] is True
        finally:
            _run_mvm(mvm_binary, "vol", "rm", vol_name, "--force", check=False)

    def test_volume_list_json_read_only(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs two real volumes (2-6s). Verifies both writable
        # (default) and read-only (explicit) appear correctly in JSON output.
        """Create one writable and one read-only volume, verify is_read_only in ls --json."""
        vol_rw = f"sys-vol-rw-{unique_key_name}"
        vol_ro = f"sys-vol-rolist-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_rw, "512M")
            _run_mvm(
                mvm_binary,
                "volume",
                "create",
                vol_ro,
                "256M",
                "--read-only",
            )

            ls_result = _run_mvm(mvm_binary, "volume", "ls", "--json")
            volumes = json.loads(ls_result.stdout)

            rw_match = [v for v in volumes if v["name"] == vol_rw]
            assert len(rw_match) == 1
            assert rw_match[0]["is_read_only"] is False

            ro_match = [v for v in volumes if v["name"] == vol_ro]
            assert len(ro_match) == 1
            assert ro_match[0]["is_read_only"] is True
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_rw, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_ro, "--force", check=False)

    def test_volume_duplicate_name_rejected(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume. Tests that creating a second volume
        # with the same name is rejected — silent overwrite would lose data.
        """Creating a volume with duplicate name should be rejected."""
        vol_name = f"sys-vol-dup-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            result = _run_mvm(
                mvm_binary,
                "volume",
                "create",
                vol_name,
                "256M",
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "already exists" in combined

            ls_result = _run_mvm(mvm_binary, "volume", "ls", "--json")
            volumes = json.loads(ls_result.stdout)
            matching = [v for v in volumes if v["name"] == vol_name]
            assert len(matching) == 1
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    # ── 3. Error paths (no side effects) ─────────────────────────────────

    def test_volume_create_invalid_size_fails(self, mvm_binary: str) -> None:
        # Rationale: No resources needed — error path for invalid size.
        # Verifies the CLI rejects non-numeric size strings.
        """Creating a volume with invalid size should be rejected."""
        result = _run_mvm(
            mvm_binary,
            "volume",
            "create",
            "invalid-size-vol",
            "abc",
            check=False,
        )
        assert result.returncode != 0

        ls_result = _run_mvm(mvm_binary, "volume", "ls", "--json", check=False)
        if ls_result.returncode == 0:
            volumes = json.loads(ls_result.stdout)
            assert not any(v["name"] == "invalid-size-vol" for v in volumes)

    def test_volume_create_invalid_format_fails(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: No resources needed — error path for invalid format.
        """Creating a volume with invalid --format should be rejected."""
        vol_name = f"sys-vol-badfmt-{unique_key_name}"
        result = _run_mvm(
            mvm_binary,
            "volume",
            "create",
            vol_name,
            "512M",
            "--format",
            "vmdk",
            check=False,
        )
        assert result.returncode != 0

        ls_result = _run_mvm(mvm_binary, "volume", "ls", "--json", check=False)
        if ls_result.returncode == 0:
            volumes = json.loads(ls_result.stdout)
            assert not any(v["name"] == vol_name for v in volumes)

    def test_volume_inspect_nonexistent_fails(self, mvm_binary: str) -> None:
        # Rationale: No resources needed — error path for nonexistent volume.
        """Inspecting a nonexistent volume should fail."""
        result = _run_mvm(
            mvm_binary,
            "volume",
            "inspect",
            "nonexistent-volume-abc",
            check=False,
        )
        assert result.returncode != 0

    def test_volume_resize_nonexistent_fails(self, mvm_binary: str) -> None:
        # Rationale: No resources needed — error path for nonexistent volume resize.
        """Resizing a nonexistent volume should fail."""
        result = _run_mvm(
            mvm_binary,
            "volume",
            "resize",
            "nonexistent-volume-def",
            "1G",
            check=False,
        )
        assert result.returncode != 0

    def test_negative_volume_size(self, mvm_binary: str) -> None:
        # Rationale: No resources needed — error path for negative size.
        """A volume with negative size should be rejected."""
        vol_name = f"sys-neg-{uuid.uuid4().hex[:6]}"
        result = _run_mvm(
            mvm_binary,
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

    def test_zero_size_volume(self, mvm_binary: str) -> None:
        # Rationale: No resources needed — error path for zero size.
        """A volume with zero size should be rejected."""
        vol_name = f"sys-zero-{uuid.uuid4().hex[:6]}"
        result = _run_mvm(
            mvm_binary,
            "volume",
            "create",
            vol_name,
            "0M",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "invalid" in combined or "must be" in combined

    # ── 4. Modify operations (resize) ────────────────────────────────────

    def test_volume_resize(self, mvm_binary: str, unique_key_name: str) -> None:
        # Rationale: Needs a real volume (1-3s). Tests resize and verifies
        # new size via inspect --json — catches resize that silently fails
        # but reports success.
        """Create a volume and resize it, verify new size via inspect --json."""
        vol_name = f"sys-vol-resize-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            result = _run_mvm(mvm_binary, "volume", "resize", vol_name, "1G")
            assert result.returncode == 0

            inspect = _run_mvm(
                mvm_binary,
                "volume",
                "inspect",
                vol_name,
                "--json",
            )
            data = json.loads(inspect.stdout)
            assert data["volume"]["size_bytes"] == 1024 * 1024 * 1024
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_resize_shrink_documents_behavior(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Only needs a volume (1-3s). Documents whether shrink
        # is accepted or rejected — volume-level resize behavior is
        # independent of attachment state.
        """Resize a volume down — documents whether shrink is accepted or rejected.

        Rationale: Only needs a volume (cheap, 1-3s). No VM required since
        volume-level resize behavior is independent of attachment state.
        """
        vol_name = f"sys-vol-shrink-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "1G")

            inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            data = json.loads(inspect.stdout)
            assert data["volume"]["size_bytes"] == 1024 * 1024 * 1024
            assert data["volume"]["status"] == "available"

            result = _run_mvm(
                mvm_binary,
                "volume",
                "resize",
                vol_name,
                "512M",
                check=False,
            )

            inspect_after = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            data_after = json.loads(inspect_after.stdout)

            if result.returncode == 0:
                # Backend allowed shrink — verify size matches target
                assert data_after["volume"]["size_bytes"] == 512 * 1024 * 1024
            else:
                # Backend rejected shrink — verify size unchanged
                assert data_after["volume"]["size_bytes"] == 1024 * 1024 * 1024
        finally:
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )

    # ── 5. Remove / destructive operations ──────────────────────────────

    def test_volume_remove(self, mvm_binary: str, unique_key_name: str) -> None:
        # Rationale: Needs a real volume. Tests normal rm and verifies gone
        # via ls --json — catches rm that reports success but leaves stale
        # DB records.
        """Create and remove a volume, verify it's gone via ls --json."""
        vol_name = f"sys-vol-rm-{unique_key_name}"
        _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

        result = _run_mvm(mvm_binary, "volume", "rm", vol_name)
        assert result.returncode == 0

        result = _run_mvm(mvm_binary, "volume", "ls", "--json")
        volumes = json.loads(result.stdout)
        assert not any(v["name"] == vol_name for v in volumes)

    def test_volume_rm_with_force(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume. Tests --force removal and verifies
        # gone via ls --json — force path differs from normal rm.
        """Remove a volume with --force and verify it's gone via ls --json."""
        vol_name = f"sys-vol-frc-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            result = _run_mvm(mvm_binary, "volume", "rm", vol_name, "--force")
            assert result.returncode == 0
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

        result = _run_mvm(mvm_binary, "volume", "ls", "--json")
        volumes = json.loads(result.stdout)
        assert not any(v["name"] == vol_name for v in volumes)

    def test_volume_remove_nonexistent(self, mvm_binary: str) -> None:
        # Rationale: No resources needed — error path for nonexistent volume.
        """Removing a nonexistent volume should give clear error, not crash."""
        result = _run_mvm(
            mvm_binary,
            "volume",
            "rm",
            "nonexistent-volume-name-that-will-not-exist",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined

        result_vol = _run_mvm(mvm_binary, "volume", "ls", "--json", check=False)
        if result_vol.returncode == 0:
            vols_after = json.loads(result_vol.stdout)
            assert not any(
                v["name"] == "nonexistent-volume-name-that-will-not-exist"
                for v in vols_after
            )

    def test_volume_remove_multiple(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs two real volumes. Tests multi-rm and verifies
        # both gone — catches multi-rm that only removes the first.
        """Remove two volumes at once and verify both gone via ls --json."""
        vol1 = f"sys-vol-mrm1-{unique_key_name}"
        vol2 = f"sys-vol-mrm2-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol1, "512M")
            _run_mvm(mvm_binary, "volume", "create", vol2, "256M")

            result = _run_mvm(mvm_binary, "volume", "rm", vol1, vol2)
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "volume", "ls", "--json")
            volumes = json.loads(result.stdout)
            assert not any(v["name"] == vol1 for v in volumes)
            assert not any(v["name"] == vol2 for v in volumes)
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol1, vol2, check=False)

    def test_volume_remove_partial_failure(
        self, mvm_binary: str, unique_key_name: str
    ) -> None:
        # Rationale: Needs a real volume. Tests partial failure: one exists,
        # one nonexistent — the existing volume should still be removed.
        """Remove one existing volume and one nonexistent — existing should still be removed."""
        vol_name = f"sys-vol-partial-{unique_key_name}"
        nonexistent = "nonexistent-volume-partial-test"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            result = _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                nonexistent,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert nonexistent in combined

            result = _run_mvm(mvm_binary, "volume", "ls", "--json")
            volumes = json.loads(result.stdout)
            assert not any(v["name"] == vol_name for v in volumes)
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    # ── 6. Advanced: invariants across attach/detach ─────────────────────

    @pytest.mark.requires_kvm
    def test_volume_invariants_available_attached_cycle(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs a real VM (30-120s) because attachment invariants
        # require a stopped VM to attach/detach the volume. A volume-only
        # test cannot verify vm_id transitions or disk path existence.
        """Verify volume invariants: vm_id and path correctness."""
        vm_name = unique_vm_name
        vol_name = f"sys-vol-inv-{unique_key_name}"
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            # State 1: available — vm_id must be null, path must exist on disk
            inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            data = json.loads(inspect.stdout)
            assert data["volume"]["status"] == "available"
            assert data.get("attachment", {}).get("vm_id") is None
            assert os.path.exists(data["volume"]["path"]), (
                f"Volume path {data['volume']['path']} does not exist on disk"
            )

            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")
            _run_mvm(mvm_binary, "vm", "attach-volume", vm_name, vol_name)

            # State 2: attached — vm_id must be non-null and match VM id
            inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            data = json.loads(inspect.stdout)
            assert data["volume"]["status"] == "attached"
            assert data.get("attachment", {}).get("vm_id") is not None

            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vm_entries = json.loads(vm_ls.stdout)
            vm_info = next(
                (v for v in vm_entries if v["name"] == vm_name), None
            )
            assert vm_info is not None
            assert data["attachment"]["vm_id"] == vm_info["id"]

            _run_mvm(mvm_binary, "vm", "detach-volume", vm_name, vol_name)

            # State 3: available again — vm_id must be null, path still exists
            inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            data = json.loads(inspect.stdout)
            assert data["volume"]["status"] == "available"
            assert data.get("attachment", {}).get("vm_id") is None
            assert os.path.exists(data["volume"]["path"]), (
                f"Volume path {data['volume']['path']} disappeared after detach"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestVolumeAttachDetach:
    """Volume attach/detach lifecycle with VMs — requires KVM and network."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_volume,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_attach_detach_then_stop_start(
        self,
        mvm_binary: str,
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
                mvm_binary,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")

            _run_mvm(mvm_binary, "vm", "detach-volume", vm_name, vol_name)

            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data.get("volume", {}).get("status") == "available"

            _run_mvm(mvm_binary, "vm", "attach-volume", vm_name, vol_name)

            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data.get("volume", {}).get("status") == "attached"

            _run_mvm(mvm_binary, "vm", "start", vm_name)

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry.get("status") == "running"
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_attach_volume_to_stopped_then_start(
        self,
        mvm_binary: str,
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
                mvm_binary,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")

            _run_mvm(mvm_binary, "vm", "attach-volume", vm_name, vol_name)

            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data.get("volume", {}).get("status") == "attached"

            _run_mvm(mvm_binary, "vm", "start", vm_name)

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry.get("status") == "running"
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_attach_detach_attach_same_volume(
        self,
        mvm_binary: str,
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
                mvm_binary,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")

            _run_mvm(mvm_binary, "vm", "detach-volume", vm_name, vol_name)

            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data.get("volume", {}).get("status") == "available"

            _run_mvm(mvm_binary, "vm", "attach-volume", vm_name, vol_name)

            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data.get("volume", {}).get("status") == "attached"

            _run_mvm(mvm_binary, "vm", "start", vm_name)
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestVolumeCrossVM:
    """Cross-VM volume attachment constraints — requires KVM."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_volume,
    ]

    @pytest.mark.requires_kvm
    def test_cross_vm_volume_attach_rejected(
        self,
        mvm_binary: str,
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
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )

            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_a,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_b,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_a, "--force")
            _run_mvm(mvm_binary, "vm", "stop", vm_b, "--force")

            _run_mvm(mvm_binary, "vm", "attach-volume", vm_a, vol_name)

            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data["volume"]["status"] == "attached"
            assert vol_data.get("attachment", {}).get("vm_id") is not None

            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vm_entries = json.loads(vm_ls.stdout)
            vm_a_info = next((v for v in vm_entries if v["name"] == vm_a), None)
            assert vm_a_info is not None
            assert vol_data["attachment"]["vm_id"] == vm_a_info["id"]

            result = _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_b,
                vol_name,
                check=False,
            )
            assert result.returncode != 0

            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data["volume"]["status"] == "attached"
            assert (
                vol_data.get("attachment", {}).get("vm_id") == vm_a_info["id"]
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_a, "--force", check=False)
            _run_mvm(mvm_binary, "vm", "rm", vm_b, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestVolumeRunningVMDependency:
    """Volume dependency checks with running VMs — requires KVM and network."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_volume,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_delete_volume_used_by_running_vm_fails(
        self,
        mvm_binary: str,
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
                mvm_binary,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names

            result = _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                check=False,
            )
            assert result.returncode != 0
            error_text = (result.stdout + result.stderr).lower()
            assert "in use" in error_text or "attached" in error_text

            vol_ls = _run_mvm(mvm_binary, "volume", "ls", "--json", check=False)
            if vol_ls.returncode == 0 and vol_ls.stdout.strip():
                volumes_after = json.loads(vol_ls.stdout)
                vol_names = [v.get("name") for v in volumes_after]
                assert vol_name in vol_names
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_delete_volume_used_by_running_vm_with_force_succeeds(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs a real VM with volume (30-120s). Tests --force
        # allows rm despite running VM.
        """--force allows deleting a volume even when attached to a running VM."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        vol_name = f"sys-dep-vol-{unique_key_name}"
        net_name = unique_network_name

        try:
            _run_mvm(
                mvm_binary,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names

            result = _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            assert result.returncode == 0

            vol_ls = _run_mvm(mvm_binary, "volume", "ls", "--json", check=False)
            if vol_ls.returncode == 0 and vol_ls.stdout.strip():
                volumes_after = json.loads(vol_ls.stdout)
                vol_names = [v.get("name") for v in volumes_after]
                assert vol_name not in vol_names
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_resize_volume_attached_to_running_vm_succeeds(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        # Rationale: Needs a real VM with volume (30-120s). Tests resize
        # while attached to running VM.
        """Resizing a volume attached to a running VM should succeed."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        vol_name = f"sys-dep-vol-{unique_key_name}"
        net_name = unique_network_name

        try:
            _run_mvm(
                mvm_binary,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            vol_ls_before = _run_mvm(
                mvm_binary, "volume", "ls", "--json", check=False
            )
            vol_list_before = (
                []
                if vol_ls_before.returncode != 0
                else json.loads(vol_ls_before.stdout)
            )
            vol_info_before = next(
                (v for v in vol_list_before if v.get("name") == vol_name),
                {},
            )
            original_size = (
                vol_info_before.get("size") if vol_info_before else None
            )

            result = _run_mvm(
                mvm_binary,
                "volume",
                "resize",
                vol_name,
                "1024M",
                check=False,
            )
            assert result.returncode == 0

            vol_ls_after = _run_mvm(
                mvm_binary, "volume", "ls", "--json", check=False
            )
            if vol_ls_after.returncode == 0 and vol_ls_after.stdout.strip():
                vol_list_after = json.loads(vol_ls_after.stdout)
                vol_info_after = next(
                    (v for v in vol_list_after if v.get("name") == vol_name),
                    {},
                )
                new_size = (
                    vol_info_after.get("size") if vol_info_after else None
                )
                assert new_size is not None
                assert original_size is None or new_size != original_size
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestVolumeNegativeFailure:
    """Volume failure modes with VMs — requires KVM."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_volume,
    ]

    @pytest.mark.requires_kvm
    def test_attach_nonexistent_volume_to_vm_fails(
        self,
        mvm_binary: str,
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
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")

            result = _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                "nonexistent-volume-name",
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "not found" in combined

            result_ins = _run_mvm(
                mvm_binary,
                "vm",
                "inspect",
                vm_name,
                "--json",
                check=False,
            )
            if result_ins.returncode == 0:
                vm_info = json.loads(result_ins.stdout)
                attached_vols = vm_info.get("volumes", [])
                assert not any(
                    v.get("name") == "nonexistent-volume-name"
                    for v in attached_vols
                )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    def test_detach_nonexistent_volume_from_vm_fails(
        self,
        mvm_binary: str,
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
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")

            result = _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                "nonexistent-volume-name",
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "not found" in combined

            result_vol = _run_mvm(
                mvm_binary, "volume", "ls", "--json", check=False
            )
            if result_vol.returncode == 0:
                vols = json.loads(result_vol.stdout)
                assert not any(
                    v["name"] == "nonexistent-volume-name" for v in vols
                )

            result_ins = _run_mvm(
                mvm_binary,
                "vm",
                "inspect",
                vm_name,
                "--json",
                check=False,
            )
            if result_ins.returncode == 0:
                vm_info = json.loads(result_ins.stdout)
                attached_vols = vm_info.get("volumes", [])
                assert len(attached_vols) == 0
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
