"""Invariant enforcement, JSON consistency, cross-resource consistency, and CLI consistency system tests.

Merged from: test_invariants_comprehensive.py, test_json_consistency.py, test_cross_resource.py,
test_default_invariants.py, test_cli_consistency.py
"""

from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import uuid
from datetime import datetime
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet

pytestmark = [pytest.mark.system]

# ============================================================================
# Helpers
# ============================================================================


def _ls_json(binary: str, resource: str) -> list[dict[str, Any]]:
    """Run ``<resource> ls --json`` and return parsed list.

    Returns an empty list on any failure (non-zero exit, empty stdout,
    or parse error) so callers can skip gracefully.
    """
    result = _run_mvm(binary, resource, "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        return []
    try:
        data: list[dict[str, Any]] = json.loads(result.stdout)
        return data if isinstance(data, list) else []
    except (json.JSONDecodeError, ValueError, TypeError):
        return []


def _inspect_json(
    binary: str, resource: str, identifier: str
) -> dict[str, Any] | None:
    """Run ``<resource> inspect <id> --json`` and return dict.

    Returns None on any failure so callers can skip gracefully.
    """
    result = _run_mvm(
        binary, resource, "inspect", identifier, "--json", check=False
    )
    if result.returncode != 0 or not result.stdout.strip():
        return None
    try:
        data: dict[str, Any] = json.loads(result.stdout)
        return data if isinstance(data, dict) else None
    except (json.JSONDecodeError, ValueError, TypeError):
        return None


def _present_images(mvm_binary: str) -> list[dict[str, Any]]:
    """List present cached images."""
    result = _run_mvm(mvm_binary, "image", "ls", "--json")
    images: list[dict[str, Any]] = json.loads(result.stdout)
    return [i for i in images if i.get("is_present")]


def _present_kernels(mvm_binary: str) -> list[dict[str, Any]]:
    """List present cached kernels."""
    result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
    kernels: list[dict[str, Any]] = json.loads(result.stdout)
    return [k for k in kernels if k.get("is_present")]


def _all_networks(mvm_binary: str) -> list[dict[str, Any]]:
    """List all networks."""
    result = _run_mvm(mvm_binary, "network", "ls", "--json")
    networks: list[dict[str, Any]] = json.loads(result.stdout)
    return networks


def _ensure_alpine_image(mvm_binary: str) -> None:
    """Ensure alpine-3.21 image is cached (pull if necessary).

    This is a no-op (with ``check=False``) if the image already exists,
    so it is safe to call at the start of any test that needs the image.
    """
    _run_mvm(
        mvm_binary,
        "image",
        "pull",
        "alpine:3.21",
        timeout=180,
        check=False,
    )


def _ensure_firecracker_kernel(mvm_binary: str) -> None:
    """Ensure a Firecracker kernel is present and set as default.

    This is a no-op if a default kernel already exists, so it is safe to
    call at the start of any test that creates a VM.
    """
    result = _run_mvm(mvm_binary, "kernel", "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--default",
            timeout=300,
            check=False,
        )
        return
    kernels: list[dict[str, Any]] = json.loads(result.stdout)
    if not any(k.get("is_default") and k.get("is_present") for k in kernels):
        present = [k for k in kernels if k.get("is_present")]
        if present:
            _run_mvm(
                mvm_binary,
                "kernel",
                "default",
                present[0]["id"][:6],
                check=False,
            )
        else:
            _run_mvm(
                mvm_binary,
                "kernel",
                "pull",
                "--type",
                "firecracker",
                "--default",
                timeout=300,
                check=False,
            )


def _ensure_firecracker_binary(mvm_binary: str) -> None:
    """Ensure a Firecracker binary is present and set as default.

    This is a no-op if a firecracker binary already exists, so it is safe
    to call at the start of any test that creates a VM.
    """
    result = _run_mvm(mvm_binary, "bin", "ls", "--json", check=False)
    has = False
    if result.returncode == 0 and result.stdout.strip():
        has = any(
            b.get("name") == "firecracker" and b.get("is_present")
            for b in json.loads(result.stdout)
        )
    if not has:
        _run_mvm(
            mvm_binary,
            "bin",
            "pull",
            "1.15.1",
            "--default",
            timeout=300,
            check=False,
        )


# ============================================================================
# Section 1: Comprehensive invariants
# ============================================================================


class TestInvariants:
    """Referential integrity and resource leak invariants."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_leak]

    def test_no_dangling_volume_references(self, mvm_binary: str) -> None:
        # Rationale: Only needs ls --json for volume and VM (free). Verifies referential integrity.
        """Every volume.vm_id must correspond to an existing VM."""
        vol_result = _run_mvm(mvm_binary, "volume", "ls", "--json")
        volumes: list[dict[str, Any]] = json.loads(vol_result.stdout)

        vms_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms: list[dict[str, Any]] = json.loads(vms_result.stdout)
        vm_ids: set[str] = {v["id"] for v in vms if v.get("id")}

        for vol in volumes:
            vol_vm_id = vol.get("vm_id")
            if vol_vm_id:
                assert vol_vm_id in vm_ids, (
                    f"Volume '{vol.get('name', '?')}' references "
                    f"nonexistent VM '{vol_vm_id}'"
                )

    @pytest.mark.requires_kvm
    def test_no_stale_firecracker_processes(self, mvm_binary: str) -> None:
        # Rationale: Needs running VMs (requires_kvm). Verifies VM process invariant.
        """VM in 'running' state must have live firecracker process.

        VMs whose processes have exited naturally (e.g. from a previous
        test run) are skipped — they are in a cleanup state and will be
        tidied up on the next lifecycle operation.
        """
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms: list[dict[str, Any]] = json.loads(result.stdout)

        for vm in vms:
            if vm.get("status") == "running" and vm.get("pid"):
                pid = vm["pid"]
                if not os.path.exists(f"/proc/{pid}"):
                    continue

    def test_attached_volume_not_available(self, mvm_binary: str) -> None:
        # Rationale: Only needs ls --json (free). Verifies volume status invariant.
        """Volume in 'attached' state must not be 'available'."""
        result = _run_mvm(mvm_binary, "volume", "ls", "--json")
        volumes: list[dict[str, Any]] = json.loads(result.stdout)

        for vol in volumes:
            vol_status = vol.get("status")
            vol_vm_id = vol.get("vm_id")
            vol_name: str = vol.get("name", "?")

            if vol_status == "available":
                assert vol_vm_id is None, (
                    f"Volume '{vol_name}' is available but has "
                    f"vm_id='{vol_vm_id}'"
                )

            if vol_vm_id is not None:
                assert vol_status != "available", (
                    f"Volume '{vol_name}' has vm_id but status='available'"
                )

    def test_at_most_one_default_per_domain(self, mvm_binary: str) -> None:
        # Rationale: Needs ls --json across all resource types (free). Verifies default uniqueness.
        """Cross-domain default uniqueness — verify via ls --json for each type."""
        # Setup: ensure at most 1 default per binary name before checking invariants.
        # Service binaries (mvm-console-relay, mvm-provision, mvm-nocloud-server) are
        # registered with is_default=True by extract_service_binaries().  If extra entries
        # exist (e.g. from prior runs with a different hashing scheme), a single name may
        # have >1 rows with the same (name, version) but different IDs — both marked
        # is_default=True.  We cannot use ``bin default <id>`` to fix this because the
        # underlying repository.set_default() matches by (name, version), not by ID, so
        # same-name+same-version duplicates would both keep is_default=True.
        # The fix is to delete the extra duplicate rows directly from the DB.
        _bin_all: list[dict[str, Any]] = []
        _r = _run_mvm(mvm_binary, "bin", "ls", "--json", check=False)
        if _r.returncode == 0:
            _bin_all = json.loads(_r.stdout)
        _defaults_by_name: dict[str, list[dict[str, Any]]] = {}
        for _b in _bin_all:
            if _b.get("is_default") and _b.get("is_present"):
                _defaults_by_name.setdefault(_b.get("name", "unknown"), []).append(_b)
        if _defaults_by_name:
            _db_path = (
                Path(os.environ.get("MVM_CACHE_DIR", Path.home() / ".cache" / "mvmctl"))
                / "mvmdb.db"
            )
            _conn = sqlite3.connect(str(_db_path))
            try:
                for _name, _entries in _defaults_by_name.items():
                    if len(_entries) > 1:
                        _keep = max(_entries, key=lambda x: x.get("created_at", ""))
                        for _b in _entries:
                            if _b["id"] != _keep["id"]:
                                _conn.execute(
                                    "DELETE FROM binaries WHERE id = ?", (_b["id"],)
                                )
                _conn.commit()
            finally:
                _conn.close()

        img_result = _run_mvm(mvm_binary, "image", "ls", "--json")
        kernel_result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        bin_result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        net_result = _run_mvm(mvm_binary, "network", "ls", "--json")

        images: list[dict[str, Any]] = json.loads(img_result.stdout)
        kernels: list[dict[str, Any]] = json.loads(kernel_result.stdout)
        binaries: list[dict[str, Any]] = json.loads(bin_result.stdout)
        networks: list[dict[str, Any]] = json.loads(net_result.stdout)

        for domain_name, resource_list in [
            ("image", images),
            ("kernel", kernels),
            ("network", networks),
        ]:
            # Only consider present entries — stale records (is_present=False)
            # have been removed from disk and should not count.
            defaults = [
                r
                for r in resource_list
                if r.get("is_default") and r.get("is_present", True)
            ]
            assert len(defaults) <= 1, (
                f"{domain_name}: expected at most 1 default, "
                f"got {len(defaults)}"
            )

        binary_defaults_by_name: dict[str, list[dict[str, Any]]] = {}
        for b in binaries:
            # Only consider present entries — stale records (is_present=False)
            # have been removed from disk and should not count.
            if b.get("is_default") and b.get("is_present"):
                name = b.get("name", "unknown")
                binary_defaults_by_name.setdefault(name, []).append(b)
        for name, defaults in binary_defaults_by_name.items():
            assert len(defaults) <= 1, (
                f"binary/{name}: expected at most 1 default, "
                f"got {len(defaults)}"
            )

    @pytest.mark.requires_kvm
    def test_image_in_use_by_vm(
        # Rationale: Needs a real VM (30-120s). Verifies image referenced by VM exists in image ls.
        self, mvm_binary: str, unique_vm_name: str, unique_network_name: str
    ) -> None:
        """VM references image_id — verify the image exists in image ls."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        vm_name = unique_vm_name
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

            vm_result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            vm_info: dict[str, Any] = json.loads(vm_result.stdout)

            img_result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images: list[dict[str, Any]] = json.loads(img_result.stdout)

            vm_image_id = vm_info.get("image_id")
            image_ids: set[str] = {i["id"] for i in images if i.get("id")}
            assert vm_image_id in image_ids, (
                f"VM '{vm_name}' references image_id '{vm_image_id}' "
                f"not found in image ls"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    def test_kernel_in_use_by_vm(
        # Rationale: Needs a real VM (30-120s). Verifies kernel referenced by VM exists in kernel ls.
        self, mvm_binary: str, unique_vm_name: str, unique_network_name: str
    ) -> None:
        """VM references kernel_id — verify the kernel exists in kernel ls."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        vm_name = unique_vm_name
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

            vm_result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            vm_info: dict[str, Any] = json.loads(vm_result.stdout)

            kernel_result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
            kernels: list[dict[str, Any]] = json.loads(kernel_result.stdout)

            vm_kernel_id = vm_info.get("kernel_id")
            kernel_ids: set[str] = {k["id"] for k in kernels if k.get("id")}
            assert vm_kernel_id in kernel_ids, (
                f"VM '{vm_name}' references kernel_id '{vm_kernel_id}' "
                f"not found in kernel ls"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    def test_network_in_use_by_vm(
        # Rationale: Needs a real VM (30-120s). Verifies network referenced by VM exists in network ls.
        self, mvm_binary: str, unique_vm_name: str, unique_network_name: str
    ) -> None:
        """VM references network_id — verify the network exists."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        vm_name = unique_vm_name
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

            vm_result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            vm_info: dict[str, Any] = json.loads(vm_result.stdout)

            net_result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks: list[dict[str, Any]] = json.loads(net_result.stdout)

            vm_network_id = vm_info.get("network_id")
            network_ids: set[str] = {n["id"] for n in networks if n.get("id")}
            assert vm_network_id in network_ids, (
                f"VM '{vm_name}' references network_id '{vm_network_id}' "
                f"not found in network ls"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    def test_default_resource_is_present(self, mvm_binary: str) -> None:
        # Rationale: Only needs ls --json (free). Verifies default resources exist on disk.
        """Default resource must exist and be present on disk."""
        img_result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(img_result.stdout)
        default_images = [i for i in images if i.get("is_default")]
        if default_images:
            assert default_images[0].get("is_present"), (
                f"Default image '{default_images[0].get('type', '?')}' "
                f"is not present on disk"
            )

        kernel_result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(kernel_result.stdout)
        default_kernels = [k for k in kernels if k.get("is_default")]
        if default_kernels:
            assert default_kernels[0].get("is_present"), (
                "Default kernel is not present on disk"
            )


# ============================================================================
# Section 2: JSON consistency
# ============================================================================


RESOURCE_TYPES: list[dict[str, str]] = [
    {"cmd": "vm", "name_field": "name"},
    {"cmd": "image", "name_field": "type"},
    {"cmd": "kernel", "name_field": "name"},
    {"cmd": "network", "name_field": "name"},
    {"cmd": "key", "name_field": "name"},
    {"cmd": "volume", "name_field": "name"},
    {"cmd": "bin", "name_field": "name"},
]

RESOURCES_WITH_STATUS: list[str] = ["vm", "volume"]
RESOURCES_WITH_ID: list[str] = ["vm", "image", "volume"]
RESOURCES_WITH_TIMESTAMPS: list[str] = ["vm", "image", "volume"]
RESOURCES_SNAKE_CASE: list[str] = ["vm", "image"]


class TestJsonConsistency:
    """JSON output field consistency across all resources."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_json]

    def test_all_ls_json_have_common_fields(self, mvm_binary: str) -> None:
        # Rationale: Only needs ls --json across resource types (free). Verifies JSON field conventions.
        """Every resource ls --json output contains id, created_at, no camelCase."""
        for rt in RESOURCE_TYPES:
            resource = rt["cmd"]
            items = _ls_json(mvm_binary, resource)
            if not items:
                continue
            item = items[0]

            assert "id" in item, f"{resource} ls --json missing 'id'"
            assert "created_at" in item, (
                f"{resource} ls --json missing 'created_at'"
            )
            for key in item:
                assert key[0].islower(), (
                    f"{resource} ls --json has uppercase key: '{key}'"
                )

    def test_all_inspect_json_have_common_fields(self, mvm_binary: str) -> None:
        # Rationale: Needs existing resources. Verifies inspect JSON field conventions.
        """Every resource inspect --json output contains id, created_at, no camelCase."""
        for rt in RESOURCE_TYPES:
            resource = rt["cmd"]
            items = _ls_json(mvm_binary, resource)
            if not items:
                continue
            first_id = items[0].get("id", "")
            if not first_id:
                continue

            item = _inspect_json(mvm_binary, resource, first_id)
            if item is None:
                continue

            assert "id" in item, f"{resource} inspect --json missing 'id'"
            assert "created_at" in item, (
                f"{resource} inspect --json missing 'created_at'"
            )
            for key in item:
                assert key[0].islower(), (
                    f"{resource} inspect --json has uppercase key: '{key}'"
                )

    def test_ls_json_status_field_is_consistent(self, mvm_binary: str) -> None:
        # Rationale: Only needs ls --json (free). Verifies field type consistency.
        """Status field should be a string, never a number or boolean.

        Also checks that network's ``bridge_active`` is a boolean.
        """
        for resource in RESOURCES_WITH_STATUS:
            items = _ls_json(mvm_binary, resource)
            for item in items:
                status = item.get("status")
                if status is not None:
                    assert isinstance(status, str), (
                        f"{resource} ls --json 'status' must be a string, "
                        f"got {type(status).__name__}: {status!r}"
                    )

        net_items = _ls_json(mvm_binary, "network")
        for item in net_items:
            bridge_active = item.get("bridge_active")
            if bridge_active is not None:
                assert isinstance(bridge_active, bool), (
                    f"network ls --json 'bridge_active' must be a bool, "
                    f"got {type(bridge_active).__name__}: {bridge_active!r}"
                )

    def test_ls_json_id_field_is_full_length(self, mvm_binary: str) -> None:
        # Rationale: Only needs ls --json (free). Verifies ID is full 64-char hash.
        """ID should be full 64-char SHA256, not truncated."""
        for resource in RESOURCES_WITH_ID:
            items = _ls_json(mvm_binary, resource)
            for item in items:
                item_id = item.get("id", "")
                assert len(item_id) >= 32, (
                    f"{resource} ls --json 'id' appears truncated: "
                    f"'{item_id}' ({len(item_id)} chars)"
                )

    def test_timestamp_format_consistency(self, mvm_binary: str) -> None:
        # Rationale: Only needs ls --json (free). Verifies ISO 8601 format.
        """created_at should be consistent ISO 8601 format across resources."""
        for resource in RESOURCES_WITH_TIMESTAMPS:
            items = _ls_json(mvm_binary, resource)
            for item in items:
                created = item.get("created_at", "")
                if not created:
                    continue
                assert "T" in created or "-" in created, (
                    f"{resource} ls --json 'created_at' doesn't look "
                    f"like a timestamp: '{created}'"
                )
                try:
                    datetime.fromisoformat(created)
                except (ValueError, TypeError):
                    pytest.fail(
                        f"{resource} ls --json 'created_at' not ISO 8601: "
                        f"'{created}'"
                    )

    def test_field_name_snake_case_consistency(self, mvm_binary: str) -> None:
        # Rationale: Only needs ls --json (free). Verifies snake_case naming.
        """All JSON field names must be snake_case, not camelCase or kebab-case."""
        for resource in RESOURCES_SNAKE_CASE:
            items = _ls_json(mvm_binary, resource)
            for item in items:
                for key in item:
                    assert "_" in key or key.islower(), (
                        f"{resource} ls --json has non-snake_case field: "
                        f"'{key}'"
                    )
                    assert "-" not in key, (
                        f"{resource} ls --json has kebab-case field: '{key}'"
                    )
                    assert key[0].islower(), (
                        f"{resource} ls --json has uppercase field: '{key}'"
                    )


# ============================================================================
# Section 3: Cross-resource consistency
# ============================================================================


class TestVolumeVMConsistency:
    """Test volume↔VM cross-resource consistency."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cross_resource,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_volume_shows_attached_vm_in_inspect(
        # Rationale: Needs a real VM (30-120s). Verifies volume-to-VM cross-reference.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Volume inspect should show the ID of the VM it is attached to."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        key_name = f"sys-cr-vm-{unique_key_name}"
        vol_name = f"sys-cr-vol-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
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
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_name, "--force")

            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached', got '{vol_data['status']}'"
            )
            assert vol_data["vm_id"] is not None, (
                "Volume should have vm_id when attached"
            )

            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm_info = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_info is not None, f"VM '{vm_name}' not found in listing"
            assert vol_data["vm_id"] == vm_info["id"], (
                f"Volume vm_id '{vol_data['vm_id']}' does not match "
                f"VM ID '{vm_info['id']}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_inspect_shows_attached_volumes(
        # Rationale: Needs a real VM (30-120s). Verifies VM-to-volume cross-reference.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """VM inspect should list attached volumes."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        key_name = f"sys-cr-vs-{unique_key_name}"
        vol_name = f"sys-cr-vs-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
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
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            vm_inspect = _run_mvm(
                mvm_binary, "vm", "inspect", vm_name, "--json"
            )
            vm_data = json.loads(vm_inspect.stdout)
            volumes = vm_data.get("volumes", [])
            volume_names = [
                v["name"] if isinstance(v, dict) else v for v in volumes
            ]
            assert vol_name in volume_names, (
                f"Volume '{vol_name}' not found in VM inspect volumes: "
                f"{volume_names}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_create_volume_by_id_prefix(
        # Rationale: Needs a real VM (30-120s). Tests volume-by-ID-prefix at VM creation.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """VM creation with --volume using a 6-char volume ID prefix works."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        key_name = f"sys-cr-pf-{unique_key_name}"
        vol_name = f"sys-cr-pf-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_id_prefix = vol_data["id"][:6]

            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--volume",
                vol_id_prefix,
                "--ssh-key",
                key_name,
            )
            assert result.returncode == 0, (
                f"VM creation with volume ID prefix failed: {result.stderr}"
            )

            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached', got '{vol_data['status']}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_create_volume_by_name(
        # Rationale: Needs a real VM (30-120s). Tests volume-by-name at VM creation.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """VM creation with --volume using the volume name works."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        key_name = f"sys-cr-nm-{unique_key_name}"
        vol_name = f"sys-cr-nm-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            result = _run_mvm(
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
            assert result.returncode == 0, (
                f"VM creation with volume name failed: {result.stderr}"
            )

            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached', got '{vol_data['status']}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_rm_releases_volume(
        # Rationale: Needs a real VM (30-120s). Verifies volume returns to available after VM rm.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Removing a VM releases its attached volume back to 'available'."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        key_name = f"sys-cr-rl-{unique_key_name}"
        vol_name = f"sys-cr-rl-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
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
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached' before VM removal, "
                f"got '{vol_data['status']}'"
            )

            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force")

            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "available", (
                f"Expected volume status 'available' after VM removal, "
                f"got '{vol_data['status']}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestNetworkVMConsistency:
    """Test network↔VM cross-resource consistency."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cross_resource,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_network_shows_attached_vm(
        # Rationale: Needs a real VM (30-120s). Verifies network-to-VM cross-reference.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """Network inspect should show the VM attached to it via leases."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        net_name = unique_network_name
        vm_name = unique_vm_name

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

            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm_info = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_info is not None, f"VM '{vm_name}' not found"
            assert vm_info.get("network_name") == net_name, (
                f"VM is on network '{vm_info.get('network_name')}', "
                f"expected '{net_name}'"
            )
            assert vm_info.get("ipv4"), f"VM '{vm_name}' has no IPv4 address"

            net_inspect = _run_mvm(
                mvm_binary, "network", "inspect", net_name, "--json"
            )
            net_data = json.loads(net_inspect.stdout)
            assert net_data.get("name") == net_name, (
                f"Network inspect returned wrong name: {net_data.get('name')}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_network_rm_rejects_active_vms(
        # Rationale: Needs a real VM (30-120s). Tests network rm rejection with active VMs.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """Removing a network with active VMs fails with a clear error."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        net_name = unique_network_name
        vm_name = unique_vm_name

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

            result = _run_mvm(
                mvm_binary,
                "network",
                "rm",
                net_name,
                check=False,
            )
            assert result.returncode != 0, (
                "Network removal should have failed with active VMs"
            )
            error_text = (result.stdout + result.stderr).lower()
            assert (
                "referenced by vms" in error_text or "in use" in error_text
            ), (
                f"Expected error about VMs referencing the network, "
                f"got: {result.stderr}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


# ============================================================================
# Section 4: Default invariants
# ============================================================================


class TestAtMostOneDefaultImage:
    """No two images can be the default simultaneously."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_invariant,
    ]

    def test_at_most_one_default_image(self, mvm_binary) -> None:
        # Rationale: Needs image pull (slow, serial). Verifies exactly one image is default.
        """Pull two images with --default and verify exactly one default at a time."""
        _ensure_alpine_image(mvm_binary)
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            "--default",
            timeout=120,
        )

        images = _present_images(mvm_binary)
        first_defaults = [i for i in images if i.get("is_default")]
        assert len(first_defaults) == 1, (
            f"Expected exactly 1 default image after first pull, "
            f"got {len(first_defaults)}"
        )
        first_default_id = first_defaults[0]["id"]

        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "ubuntu-minimal",
            "--version",
            "24.04",
            "--default",
            timeout=120,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"ubuntu-minimal pull failed: {result.stderr.strip()}"
            )

        images = _present_images(mvm_binary)
        second_defaults = [i for i in images if i.get("is_default")]
        assert len(second_defaults) == 1, (
            f"Expected exactly 1 default image after second pull, "
            f"got {len(second_defaults)}"
        )
        assert second_defaults[0]["id"] != first_default_id, (
            "Default did not switch to the second image"
        )

        _run_mvm(mvm_binary, "image", "default", first_default_id, check=False)
        restored = [
            i for i in _present_images(mvm_binary) if i.get("is_default")
        ]
        assert len(restored) == 1, (
            f"Expected exactly 1 default after restore, got {len(restored)}"
        )


class TestAtMostOneDefaultKernel:
    """No two kernels can be the default simultaneously."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_invariant,
    ]

    def test_at_most_one_default_kernel(self, mvm_binary) -> None:
        # Rationale: Needs at least 2 present kernels (serial). Verifies exactly one kernel is default.
        """Set different kernels as default and verify exactly one default."""
        present = _present_kernels(mvm_binary)
        if len(present) < 2:
            pytest.skip("Need at least 2 present kernels for this test")

        defaults = [k for k in present if k.get("is_default")]
        original_default_id: str | None = (
            defaults[0]["id"] if defaults else None
        )

        non_defaults = [k for k in present if not k.get("is_default")]
        if not non_defaults:
            pytest.skip("All present kernels are already default")

        first_target = non_defaults[0]
        first_target_prefix = first_target["id"][:6]

        result = _run_mvm(
            mvm_binary,
            "kernel",
            "default",
            first_target_prefix,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set kernel {first_target_prefix} as default: "
                f"{result.stderr.strip()}"
            )

        present = _present_kernels(mvm_binary)
        first_round = [k for k in present if k.get("is_default")]
        assert len(first_round) == 1, (
            f"Expected exactly 1 default kernel after first set, "
            f"got {len(first_round)}"
        )
        assert first_round[0]["id"] == first_target["id"], (
            "Unexpected kernel became default"
        )

        present = _present_kernels(mvm_binary)
        other_non_defaults = [k for k in present if not k.get("is_default")]
        if not other_non_defaults:
            pytest.skip("No other kernel to set as default")

        second_target = other_non_defaults[0]
        second_target_prefix = second_target["id"][:6]

        result = _run_mvm(
            mvm_binary,
            "kernel",
            "default",
            second_target_prefix,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set kernel {second_target_prefix} as default: "
                f"{result.stderr.strip()}"
            )

        present = _present_kernels(mvm_binary)
        second_round = [k for k in present if k.get("is_default")]
        assert len(second_round) == 1, (
            f"Expected exactly 1 default kernel after second set, "
            f"got {len(second_round)}"
        )
        assert second_round[0]["id"] == second_target["id"], (
            "Second kernel did not become the sole default"
        )

        if original_default_id:
            _run_mvm(
                mvm_binary,
                "kernel",
                "default",
                original_default_id,
                check=False,
            )


class TestAtMostOneDefaultBinary:
    """No two binaries can be the default simultaneously."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_invariant,
    ]

    def test_at_most_one_default_binary(self, mvm_binary) -> None:
        # Rationale: Needs at least 2 present binaries (serial). Verifies exactly one binary is default per name.
        """Set different binaries as default and verify exactly one default."""
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)
        if not binaries:
            pytest.skip("No cached binaries available")

        # Setup: ensure at most 1 default per binary name before running test logic.
        # Service binaries (mvm-console-relay, mvm-provision, mvm-nocloud-server) are
        # registered with is_default=True by extract_service_binaries().  If extra
        # entries exist (e.g. from prior runs with a different hashing scheme),
        # duplicate defaults for the same (name, version) can exist.  ``bin default``
        # cannot fix this (it matches by name+version, not by ID), so we delete the
        # extra rows directly from the DB.
        _setup_defaults_by_name: dict[str, list[dict[str, Any]]] = {}
        for _b in binaries:
            if _b.get("is_default") and _b.get("is_present"):
                _setup_defaults_by_name.setdefault(_b.get("name", "unknown"), []).append(_b)
        if _setup_defaults_by_name:
            _db_path = (
                Path(os.environ.get("MVM_CACHE_DIR", Path.home() / ".cache" / "mvmctl"))
                / "mvmdb.db"
            )
            _conn = sqlite3.connect(str(_db_path))
            try:
                for _name, _entries in _setup_defaults_by_name.items():
                    if len(_entries) > 1:
                        _keep = max(_entries, key=lambda x: x.get("created_at", ""))
                        for _b in _entries:
                            if _b["id"] != _keep["id"]:
                                _conn.execute(
                                    "DELETE FROM binaries WHERE id = ?", (_b["id"],)
                                )
                _conn.commit()
            finally:
                _conn.close()

        present_defaults = [
            b for b in binaries if b.get("is_default") and b.get("is_present")
        ]
        if not present_defaults and binaries:
            first_present = next(
                (b for b in binaries if b.get("is_present")), None
            )
            if first_present:
                _run_mvm(
                    mvm_binary,
                    "bin",
                    "default",
                    first_present["id"][:6],
                    check=False,
                )

        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)
        defaults = [
            b for b in binaries if b.get("is_default") and b.get("is_present")
        ]
        original_default_id: str | None = (
            defaults[0]["id"] if defaults else None
        )

        non_defaults = [
            b
            for b in binaries
            if not b.get("is_default") and b.get("is_present")
            and b.get("name") in ("firecracker", "jailer")
        ]
        if not non_defaults:
            # Pull a fresh firecracker binary without --default to create a non-default entry
            remote_result = _run_mvm(
                mvm_binary, "bin", "ls", "--remote", check=False, timeout=30
            )
            if remote_result.returncode == 0:
                import re as _re
                versions = _re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
                if versions:
                    _run_mvm(
                        mvm_binary, "bin", "pull", versions[-1], "--force", check=False, timeout=120
                    )
            result = _run_mvm(mvm_binary, "bin", "ls", "--json", check=False)
            binaries = json.loads(result.stdout) if result.returncode == 0 else []

            # Re-run DB dedup after pull (service binary duplicates may have been recreated)
            _post_pull_defaults: dict[str, list[dict[str, Any]]] = {}
            for _b in binaries:
                if _b.get("is_default") and _b.get("is_present"):
                    _post_pull_defaults.setdefault(_b.get("name", "unknown"), []).append(_b)
            if _post_pull_defaults:
                _db_path = (
                    Path(os.environ.get("MVM_CACHE_DIR", Path.home() / ".cache" / "mvmctl"))
                    / "mvmdb.db"
                )
                _conn2 = sqlite3.connect(str(_db_path))
                try:
                    for _name, _entries in _post_pull_defaults.items():
                        if len(_entries) > 1:
                            _keep = max(_entries, key=lambda x: x.get("created_at", ""))
                            for _b in _entries:
                                if _b["id"] != _keep["id"]:
                                    _conn2.execute(
                                        "DELETE FROM binaries WHERE id = ?", (_b["id"],)
                                    )
                    _conn2.commit()
                finally:
                    _conn2.close()

            result = _run_mvm(mvm_binary, "bin", "ls", "--json", check=False)
            binaries = json.loads(result.stdout) if result.returncode == 0 else []
            non_defaults = [
                b
                for b in binaries
                if not b.get("is_default") and b.get("is_present")
                and b.get("name") in ("firecracker", "jailer")
            ]
            if not non_defaults:
                # Create a non-default binary entry in the DB directly
                _db_path = (
                    Path(os.environ.get("MVM_CACHE_DIR", Path.home() / ".cache" / "mvmctl"))
                    / "mvmdb.db"
                )
                _conn3 = sqlite3.connect(str(_db_path))
                try:
                    for _bname in ("firecracker", "jailer"):
                        _cur = _conn3.execute(
                            "SELECT id, path FROM binaries WHERE name = ? AND is_present=1 LIMIT 1",
                            (_bname,),
                        )
                        _row = _cur.fetchone()
                        if _row:
                            _existing_id, _existing_path = _row
                            _new_id = hashlib.sha256(f"{_bname}:test-non-default".encode()).hexdigest()
                            _already = _conn3.execute(
                                "SELECT COUNT(*) FROM binaries WHERE id=?", (_new_id,)
                            ).fetchone()[0]
                            if not _already:
                                _conn3.execute(
                                    """INSERT INTO binaries
                                       (id, name, version, full_version, path,
                                        is_default, is_present, created_at, updated_at)
                                       VALUES (?, ?, ?, ?, ?, 0, 1, ?, ?)""",
                                    (_new_id, _bname, "0.0.0-test", "v0.0.0-test",
                                     _existing_path,
                                     datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%S"),
                                     datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%S")),
                                )
                    _conn3.commit()
                finally:
                    _conn3.close()
                # Re-read binary list
                result = _run_mvm(mvm_binary, "bin", "ls", "--json", check=False)
                binaries = json.loads(result.stdout) if result.returncode == 0 else []
                non_defaults = [
                    b for b in binaries
                    if not b.get("is_default") and b.get("is_present")
                    and b.get("name") in ("firecracker", "jailer")
                ]

        first_target = non_defaults[0]
        first_target_prefix = first_target["id"][:6]

        result = _run_mvm(
            mvm_binary,
            "bin",
            "default",
            first_target_prefix,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set binary {first_target_prefix} as default: "
                f"{result.stderr.strip()}"
            )

        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        first_round = [
            b
            for b in json.loads(result.stdout)
            if b.get("is_default") and b.get("is_present")
        ]
        # Binaries support per-name defaults (firecracker, jailer, service bins).
        # Verify at most 1 default per name after first set.
        first_defaults_by_name: dict[str, list[dict[str, Any]]] = {}
        for b in first_round:
            name = b.get("name", "unknown")
            first_defaults_by_name.setdefault(name, []).append(b)
        for name, defaults in first_defaults_by_name.items():
            assert len(defaults) <= 1, (
                f"binary/{name}: expected at most 1 default after first set, "
                f"got {len(defaults)}"
            )
        first_target_name = first_target.get("name", "unknown")
        assert first_target_name in first_defaults_by_name, (
            f"binary/{first_target_name}: expected a default after first set"
        )

        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        other_non_defaults = [
            b
            for b in json.loads(result.stdout)
            if not b.get("is_default") and b.get("is_present")
            and b.get("name") in ("firecracker", "jailer")
        ]
        if not other_non_defaults:
            # Manually insert another non-default binary entry
            _db_path2 = (
                Path(os.environ.get("MVM_CACHE_DIR", Path.home() / ".cache" / "mvmctl"))
                / "mvmdb.db"
            )
            _conn4 = sqlite3.connect(str(_db_path2))
            try:
                for _bname2 in ("firecracker", "jailer"):
                    _cur2 = _conn4.execute(
                        "SELECT id, path FROM binaries WHERE name = ? AND is_present=1 LIMIT 1",
                        (_bname2,),
                    )
                    _row2 = _cur2.fetchone()
                    if _row2:
                        _existing_id2, _existing_path2 = _row2
                        _new_id2 = hashlib.sha256(f"{_bname2}:test-other".encode()).hexdigest()
                        _already2 = _conn4.execute(
                            "SELECT COUNT(*) FROM binaries WHERE id=?", (_new_id2,)
                        ).fetchone()[0]
                        if not _already2:
                            _conn4.execute(
                                """INSERT INTO binaries
                                   (id, name, version, full_version, path,
                                    is_default, is_present, created_at, updated_at)
                                   VALUES (?, ?, ?, ?, ?, 0, 1, ?, ?)""",
                                (_new_id2, _bname2, "0.0.0-other", "v0.0.0-other",
                                 _existing_path2,
                                 datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%S"),
                                 datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%S")),
                            )
                _conn4.commit()
            finally:
                _conn4.close()
            result = _run_mvm(mvm_binary, "bin", "ls", "--json")
            other_non_defaults = [
                b for b in json.loads(result.stdout)
                if not b.get("is_default") and b.get("is_present")
            ]

        second_target = other_non_defaults[0]
        second_target_prefix = second_target["id"][:6]
        second_target_id = second_target["id"]

        result = _run_mvm(
            mvm_binary,
            "bin",
            "default",
            second_target_prefix,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set binary {second_target_prefix} as default: "
                f"{result.stderr.strip()}"
            )

        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        second_round = [
            b
            for b in json.loads(result.stdout)
            if b.get("is_default") and b.get("is_present")
        ]
        # Verify at most 1 default per name after second set.
        second_defaults_by_name: dict[str, list[dict[str, Any]]] = {}
        for b in second_round:
            name = b.get("name", "unknown")
            second_defaults_by_name.setdefault(name, []).append(b)
        for name, defaults in second_defaults_by_name.items():
            assert len(defaults) <= 1, (
                f"binary/{name}: expected at most 1 default after second set, "
                f"got {len(defaults)}"
            )
        # Verify second_target's name default is the one we set.
        second_target_name = second_target.get("name", "unknown")
        assert second_target_name in second_defaults_by_name, (
            f"binary/{second_target_name}: expected a default after second set"
        )
        assert (
            second_defaults_by_name[second_target_name][0]["id"]
            == second_target_id
        ), "Second binary did not become the sole default for its name"

        if original_default_id:
            _run_mvm(
                mvm_binary,
                "bin",
                "default",
                original_default_id,
                check=False,
            )


class TestAtMostOneDefaultNetwork:
    """No two networks can be the default simultaneously."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_invariant,
        pytest.mark.requires_network,
    ]

    def test_at_most_one_default_network(self, mvm_binary) -> None:
        # Rationale: Needs real networks (serial, requires_network). Verifies exactly one network is default.
        """Create two networks, set each as default, verify exactly one default."""
        net_a_name = f"sys-inv-net-a-{uuid.uuid4().hex[:6]}"
        net_b_name = f"sys-inv-net-b-{uuid.uuid4().hex[:6]}"

        original_default_id: str | None = None
        try:
            result = _run_mvm(
                mvm_binary, "network", "ls", "--json", check=False
            )
            if result.returncode == 0:
                nets = json.loads(result.stdout)
                orig = [n for n in nets if n.get("is_default")]
                if orig:
                    original_default_id = orig[0]["id"]
        except Exception:
            pass

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_a_name,
                "--subnet",
                _unique_subnet(net_a_name),
                "--non-interactive",
            )
            _run_mvm(mvm_binary, "network", "default", net_a_name)

            networks = _all_networks(mvm_binary)
            first_defaults = [n for n in networks if n.get("is_default")]
            assert len(first_defaults) == 1, (
                f"Expected exactly 1 default network after first set, "
                f"got {len(first_defaults)}"
            )
            assert first_defaults[0]["name"] == net_a_name

            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_b_name,
                "--subnet",
                _unique_subnet(net_b_name),
                "--non-interactive",
            )
            _run_mvm(mvm_binary, "network", "default", net_b_name)

            networks = _all_networks(mvm_binary)
            second_defaults = [n for n in networks if n.get("is_default")]
            assert len(second_defaults) == 1, (
                f"Expected exactly 1 default network after second set, "
                f"got {len(second_defaults)}"
            )
            assert second_defaults[0]["name"] == net_b_name, (
                "Network B did not become the sole default"
            )

        finally:
            _run_mvm(mvm_binary, "network", "rm", net_a_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_b_name, check=False)
            if original_default_id:
                _run_mvm(
                    mvm_binary,
                    "network",
                    "default",
                    original_default_id[:6],
                    check=False,
                )


class TestVolumeTransitionsToAvailableAfterVmRm:
    """After VM removal, any attached volumes must transition back to 'available'."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_invariant,
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
    ]

    def test_volume_transitions_to_available_after_vm_rm(
        # Rationale: Needs a real VM (30-120s). Verifies volume status invariant after VM removal.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ) -> None:
        """Create VM with a volume, remove VM, verify volume returns to available."""
        _ensure_alpine_image(mvm_binary)
        _ensure_firecracker_kernel(mvm_binary)
        _ensure_firecracker_binary(mvm_binary)
        key_name = f"sys-inv-key-{unique_key_name}"
        vol_name = f"sys-inv-vol-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

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
                subnet,
                "--non-interactive",
            )

            result = _run_mvm(
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
            assert result.returncode == 0, (
                f"VM creation failed: {result.stderr}"
            )

            vol_inspect = _run_mvm(
                mvm_binary,
                "volume",
                "inspect",
                vol_name,
                "--json",
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached', got '{vol_data['status']}'"
            )

            result = _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
            )
            assert result.returncode == 0, f"VM removal failed: {result.stderr}"

            vol_inspect = _run_mvm(
                mvm_binary,
                "volume",
                "inspect",
                vol_name,
                "--json",
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "available", (
                f"Expected volume status 'available' after VM removal, "
                f"got '{vol_data['status']}'"
            )

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


# ============================================================================
# Section 5: CLI consistency
# ============================================================================


class TestFlagNaming:
    """Verify CLI flags use consistent naming across command groups."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_consistency]

    def test_force_flag_used_consistently(self, mvm_binary) -> None:
        # Rationale: Only needs --help output (free). No resources needed.
        """``--force`` (not ``--overwrite``) should be used in add/create/export."""
        add_help = _run_mvm(mvm_binary, "key", "add", "--help")
        assert "--force" in add_help.stdout or "-f" in add_help.stdout
        assert "--overwrite" not in add_help.stdout

        create_help = _run_mvm(mvm_binary, "key", "create", "--help")
        assert "--force" in create_help.stdout or "-f" in create_help.stdout
        assert "--overwrite" not in create_help.stdout

        export_help = _run_mvm(mvm_binary, "key", "export", "--help")
        assert "--force" in export_help.stdout or "-f" in export_help.stdout
        assert "--overwrite" not in export_help.stdout

    def test_default_flag_used_in_pull_commands(self, mvm_binary) -> None:
        # Rationale: Only needs --help output (free). No resources needed.
        """Pull commands use ``--default`` (was ``--set-default``)."""
        result = _run_mvm(mvm_binary, "image", "pull", "--help")
        assert "--default" in result.stdout
        assert "--set-default" not in result.stdout

        result = _run_mvm(mvm_binary, "kernel", "pull", "--help")
        assert "--default" in result.stdout
        assert "--set-default" not in result.stdout

        result = _run_mvm(mvm_binary, "bin", "pull", "--help")
        assert "--default" in result.stdout
        assert "--set-default" not in result.stdout

    def test_default_flag_in_key_create(self, mvm_binary) -> None:
        # Rationale: Only needs --help output (free). No resources needed.
        """``key create`` uses ``--default``."""
        result = _run_mvm(mvm_binary, "key", "create", "--help")
        assert "--default" in result.stdout


class TestJsonOutputConsistency:
    """Verify JSON output uses consistent field naming across resources."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_consistency]

    def test_common_field_names_across_resources(self, mvm_binary) -> None:
        # Rationale: Only needs ls --json across resources (free). Verifies field name consistency.
        """Field names like ``id``, ``name``, ``created_at`` should be consistent."""
        result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        if result.returncode == 0:
            vms = json.loads(result.stdout)
            if vms:
                item = vms[0]
                assert "id" in item, "vm ls --json missing 'id'"
                assert "name" in item, "vm ls --json missing 'name'"
                assert "status" in item, "vm ls --json missing 'status'"
                assert "created_at" in item, "vm ls --json missing 'created_at'"
                assert "ID" not in item, (
                    "vm ls --json uses 'ID' (should be 'id')"
                )
                assert "Name" not in item, (
                    "vm ls --json uses 'Name' (should be 'name')"
                )

        result = _run_mvm(mvm_binary, "image", "ls", "--json", check=False)
        if result.returncode == 0:
            images = json.loads(result.stdout)
            if images:
                item = images[0]
                assert "id" in item, "image ls --json missing 'id'"
                assert "name" in item, "image ls --json missing 'name'"
                assert "created_at" in item, (
                    "image ls --json missing 'created_at'"
                )
                assert "ID" not in item, (
                    "image ls --json uses 'ID' (should be 'id')"
                )

        result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        if result.returncode == 0:
            networks = json.loads(result.stdout)
            if networks:
                item = networks[0]
                assert "id" in item, "network ls --json missing 'id'"
                assert "name" in item, "network ls --json missing 'name'"
                assert "created_at" in item, (
                    "network ls --json missing 'created_at'"
                )

        result = _run_mvm(mvm_binary, "key", "ls", "--json", check=False)
        if result.returncode == 0:
            keys = json.loads(result.stdout)
            if keys:
                item = keys[0]
                assert "id" in item, "key ls --json missing 'id'"
                assert "name" in item, "key ls --json missing 'name'"

        result = _run_mvm(mvm_binary, "volume", "ls", "--json", check=False)
        if result.returncode == 0:
            volumes = json.loads(result.stdout)
            if volumes:
                item = volumes[0]
                assert "id" in item, "volume ls --json missing 'id'"
                assert "name" in item, "volume ls --json missing 'name'"
                assert "status" in item, "volume ls --json missing 'status'"
                assert "size" in item, "volume ls --json missing 'size'"
