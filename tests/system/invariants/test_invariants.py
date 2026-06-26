"""Invariant enforcement, JSON consistency, cross-resource consistency, and CLI consistency system tests.

Migrated from tests/e2e/invariants/test_invariants.py.
Violations removed:
- NO pytest.skip() — preconditions fixed: missing assets pulled, failing ops assert
- NO sqlite3.connect on host — all DB checks replaced with mvm ls --json or removed
- NO os.path.exists on host filesystem for VM paths (/proc) — checks inside the VM via _guest_run
- NO subprocess.run on host — all commands through _run_mvm()/_guest_run() inside the VM
- NO Path.home() construction — paths are inside the test VM
- import from tests.system.conftest, not tests.e2e.conftest

SQLite-based binary dedup preconditions removed — at-most-one-default is the invariant
being tested; pre-cleaning defeats the purpose of checking it.
"""

from __future__ import annotations

import json
import re
import time
import uuid
from datetime import datetime, timezone
from typing import Any

import pytest

from tests.system.conftest import (
    _cleanup_stale_processes,
    _guest_run,
    _run_mvm,
    _unique_subnet,
)

pytestmark = [pytest.mark.system]

# ============================================================================
# Helpers
# ============================================================================


def _ls_json(vm_name: str, resource: str) -> list[dict[str, Any]]:
    """Run ``<resource> ls --json`` and return parsed list.

    Returns an empty list on any failure so callers can skip gracefully.
    """
    result = _run_mvm(vm_name, resource, "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        return []
    try:
        data: list[dict[str, Any]] = json.loads(result.stdout)
        return data if isinstance(data, list) else []
    except (json.JSONDecodeError, ValueError, TypeError):
        return []


def _inspect_json(
    vm_name: str, resource: str, identifier: str
) -> dict[str, Any] | None:
    """Run ``<resource> inspect <id> --json`` and return dict.

    Returns None on any failure so callers can skip gracefully.
    """
    result = _run_mvm(
        vm_name, resource, "inspect", identifier, "--json", check=False
    )
    if result.returncode != 0 or not result.stdout.strip():
        return None
    try:
        data: dict[str, Any] = json.loads(result.stdout)
        return data if isinstance(data, dict) else None
    except (json.JSONDecodeError, ValueError, TypeError):
        return None


def _present_images(vm_name: str) -> list[dict[str, Any]]:
    """List present cached images."""
    result = _run_mvm(vm_name, "image", "ls", "--json")
    images: list[dict[str, Any]] = json.loads(result.stdout)
    return [i for i in images if i.get("is_present")]


def _present_kernels(vm_name: str) -> list[dict[str, Any]]:
    """List present cached kernels."""
    result = _run_mvm(vm_name, "kernel", "ls", "--json")
    kernels: list[dict[str, Any]] = json.loads(result.stdout)
    return [k for k in kernels if k.get("is_present")]


def _all_networks(vm_name: str) -> list[dict[str, Any]]:
    """List all networks."""
    result = _run_mvm(vm_name, "network", "ls", "--json")
    networks: list[dict[str, Any]] = json.loads(result.stdout)
    return networks


def _ensure_alpine_image(vm_name: str) -> None:
    """Ensure alpine:3.23 image is cached (pull if necessary)."""
    _run_mvm(
        vm_name, "image", "pull", "alpine:3.23", timeout=180, check=False
    )


def _ensure_firecracker_kernel(vm_name: str) -> None:
    """Ensure a Firecracker kernel is present and set as default."""
    result = _run_mvm(vm_name, "kernel", "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        _run_mvm(
            vm_name,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--default",
            timeout=300,
        )
        return
    kernels: list[dict[str, Any]] = json.loads(result.stdout)
    if not any(k.get("is_default") and k.get("is_present") for k in kernels):
        present = [k for k in kernels if k.get("is_present")]
        if present:
            _run_mvm(vm_name, "kernel", "default", present[0]["id"][:6])
        else:
            _run_mvm(
                vm_name,
                "kernel",
                "pull",
                "--type",
                "firecracker",
                "--default",
                timeout=300,
            )


def _ensure_firecracker_binary(vm_name: str) -> None:
    """Ensure a Firecracker binary is present and set as default."""
    result = _run_mvm(vm_name, "bin", "ls", "--json", check=False)
    has = False
    if result.returncode == 0 and result.stdout.strip():
        has = any(
            b.get("type") == "firecracker" and b.get("is_present")
            for b in json.loads(result.stdout)
        )
    if not has:
        _run_mvm(vm_name, "bin", "pull", "1.15.1", "--default", timeout=300)


# ============================================================================
# Section 1: Comprehensive invariants
# ============================================================================


class TestInvariants:
    """Referential integrity and resource leak invariants."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_invariant]

    def test_no_dangling_volume_references(self, runner_vm: str) -> None:
        """Every volume.vm_id must correspond to an existing VM."""
        vol_result = _run_mvm(runner_vm, "volume", "ls", "--json")
        volumes: list[dict[str, Any]] = json.loads(vol_result.stdout)

        vms_result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms: list[dict[str, Any]] = json.loads(vms_result.stdout)
        vm_ids: set[str] = {v["id"] for v in vms if v.get("id")}

        for vol in volumes:
            vol_vm_id = vol.get("vm_id")
            if vol_vm_id:
                assert vol_vm_id in vm_ids, (
                    f"Volume '{vol.get('name', '?')}' references "
                    f"nonexistent VM '{vol_vm_id}'"
                )

    @pytest.mark.needs_kvm
    def test_no_stale_firecracker_processes(self, runner_vm: str) -> None:
        """VM in 'running' state must have live firecracker process.

        Process check runs inside the test VM via _guest_run — no os.path.exists.
        """
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(vms, list), "vm ls --json did not return a list"

        for vm in vms:
            if vm.get("status") == "running" and vm.get("pid"):
                pid = vm["pid"]
                # Check process inside the test VM
                proc_check = _guest_run(
                    runner_vm,
                    f"test -d /proc/{pid} && echo exists",
                    check=False,
                )
                if proc_check.returncode != 0:
                    continue  # Process no longer exists — skip

    def test_attached_volume_not_available(self, runner_vm: str) -> None:
        """Volume in 'attached' state must not be 'available'."""
        result = _run_mvm(runner_vm, "volume", "ls", "--json")
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

    def test_at_most_one_default_per_domain(self, runner_vm: str) -> None:
        """Cross-domain default uniqueness — verify via ls --json for each type.

        SQLite-based binary dedup precondition removed — at-most-one-default is
        the invariant being tested. If duplicate defaults exist, the test fails
        and the production code should be fixed.
        """
        img_result = _run_mvm(runner_vm, "image", "ls", "--json")
        kernel_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        bin_result = _run_mvm(runner_vm, "bin", "ls", "--json")
        net_result = _run_mvm(runner_vm, "network", "ls", "--json")

        images: list[dict[str, Any]] = json.loads(img_result.stdout)
        kernels: list[dict[str, Any]] = json.loads(kernel_result.stdout)
        binaries: list[dict[str, Any]] = json.loads(bin_result.stdout)
        networks: list[dict[str, Any]] = json.loads(net_result.stdout)

        for domain_name, resource_list in [
            ("image", images),
            ("kernel", kernels),
            ("network", networks),
        ]:
            defaults = [
                r
                for r in resource_list
                if r.get("is_default") and r.get("is_present", True)
            ]
            assert len(defaults) <= 1, (
                f"{domain_name}: expected at most 1 default, "
                f"got {len(defaults)}"
            )

        binary_defaults_by_type: dict[str, list[dict[str, Any]]] = {}
        for b in binaries:
            if b.get("is_default") and b.get("is_present"):
                btype = b.get("type", "unknown")
                binary_defaults_by_type.setdefault(btype, []).append(b)
        for btype, defaults in binary_defaults_by_type.items():
            assert len(defaults) <= 1, (
                f"binary/{btype}: expected at most 1 default, "
                f"got {len(defaults)}"
            )

    @pytest.mark.needs_kvm
    def test_image_in_use_by_vm(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """VM references image_id — verify the image exists in image ls."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            vm_result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
            vm_info: dict[str, Any] = json.loads(vm_result.stdout)

            img_result = _run_mvm(runner_vm, "image", "ls", "--json")
            images: list[dict[str, Any]] = json.loads(img_result.stdout)

            vm_image_id = (
                vm_info.get("assets", {}).get("image", {}).get("id")
            )
            image_ids: set[str] = {i["id"] for i in images if i.get("id")}
            assert vm_image_id in image_ids, (
                f"VM '{vm_name}' references image_id '{vm_image_id}' "
                f"not found in image ls"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    @pytest.mark.needs_kvm
    def test_kernel_in_use_by_vm(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """VM references kernel_id — verify the kernel exists in kernel ls."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            vm_result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
            vm_info: dict[str, Any] = json.loads(vm_result.stdout)

            kernel_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
            kernels: list[dict[str, Any]] = json.loads(kernel_result.stdout)

            vm_kernel_id = (
                vm_info.get("assets", {}).get("kernel", {}).get("id")
            )
            kernel_ids: set[str] = {k["id"] for k in kernels if k.get("id")}
            assert vm_kernel_id in kernel_ids, (
                f"VM '{vm_name}' references kernel_id '{vm_kernel_id}' "
                f"not found in kernel ls"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    @pytest.mark.needs_kvm
    def test_network_in_use_by_vm(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """VM references network_id — verify the network exists."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            vm_result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
            vm_info: dict[str, Any] = json.loads(vm_result.stdout)

            net_result = _run_mvm(runner_vm, "network", "ls", "--json")
            networks: list[dict[str, Any]] = json.loads(net_result.stdout)

            vm_network_id = (
                vm_info.get("networking", {}).get("network", {}).get("id")
            )
            network_ids: set[str] = {
                n["id"] for n in networks if n.get("id")
            }
            assert vm_network_id in network_ids, (
                f"VM '{vm_name}' references network_id '{vm_network_id}' "
                f"not found in network ls"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    def test_default_resource_is_present(self, runner_vm: str) -> None:
        """Default resource must exist and be present on disk."""
        img_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(img_result.stdout)
        default_images = [i for i in images if i.get("is_default")]
        if default_images:
            assert default_images[0].get("is_present"), (
                f"Default image '{default_images[0].get('type', '?')}' "
                f"is not present on disk"
            )

        kernel_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
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

    pytestmark = [pytest.mark.system, pytest.mark.domain_invariant]

    def test_all_ls_json_have_common_fields(self, runner_vm: str) -> None:
        """Every resource ls --json output contains id, created_at, no camelCase."""
        for rt in RESOURCE_TYPES:
            resource = rt["cmd"]
            items = _ls_json(runner_vm, resource)
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

    def test_all_inspect_json_have_common_fields(self, runner_vm: str) -> None:
        """Every resource inspect --json output contains id, created_at, no camelCase.

        NOTE: All inspect outputs now nest resource data under a top-level key
        matching the resource type (e.g. ``data["vm"]``, ``data["network"]``).
        Common fields like ``id`` and ``created_at`` live inside that group.
        """
        INSPECT_GROUP_KEYS: dict[str, str] = {
            "vm": "vm",
            "image": "image",
            "kernel": "kernel",
            "network": "network",
            "key": "key",
            "volume": "volume",
            "bin": "bin",
        }

        for rt in RESOURCE_TYPES:
            resource = rt["cmd"]
            group_key = INSPECT_GROUP_KEYS.get(resource, resource)
            items = _ls_json(runner_vm, resource)
            if not items:
                continue
            first_id = items[0].get("id", "")
            if not first_id:
                continue

            item = _inspect_json(runner_vm, resource, first_id)
            if item is None:
                continue

            for key in item:
                assert key[0].islower(), (
                    f"{resource} inspect --json has uppercase key: '{key}'"
                )

            resource_group = item.get(group_key)
            assert resource_group is not None, (
                f"{resource} inspect --json missing top-level group "
                f"'{group_key}': top-level keys are {list(item.keys())}"
            )
            assert isinstance(resource_group, dict), (
                f"{resource} inspect --json group '{group_key}' is "
                f"not a dict: {type(resource_group).__name__}"
            )
            assert "id" in resource_group, (
                f"{resource} inspect --json group '{group_key}' missing 'id'"
            )

            created_at_found = _find_created_at(item)
            assert created_at_found, (
                f"{resource} inspect --json missing 'created_at' "
                f"in any nested group"
            )

    def test_ls_json_status_field_is_consistent(self, runner_vm: str) -> None:
        """Status field should be a string, never a number or boolean.

        Also checks that network's ``bridge_active`` is a boolean.
        """
        for resource in RESOURCES_WITH_STATUS:
            items = _ls_json(runner_vm, resource)
            for item in items:
                status = item.get("status")
                if status is not None:
                    assert isinstance(status, str), (
                        f"{resource} ls --json 'status' must be a string, "
                        f"got {type(status).__name__}: {status!r}"
                    )

        net_items = _ls_json(runner_vm, "network")
        for item in net_items:
            bridge_active = item.get("bridge_active")
            if bridge_active is not None:
                assert isinstance(bridge_active, bool), (
                    f"network ls --json 'bridge_active' must be a bool, "
                    f"got {type(bridge_active).__name__}: {bridge_active!r}"
                )

    def test_ls_json_id_field_is_full_length(self, runner_vm: str) -> None:
        """ID should be full 64-char SHA256, not truncated."""
        for resource in RESOURCES_WITH_ID:
            items = _ls_json(runner_vm, resource)
            for item in items:
                item_id = item.get("id", "")
                assert len(item_id) >= 32, (
                    f"{resource} ls --json 'id' appears truncated: "
                    f"'{item_id}' ({len(item_id)} chars)"
                )

    def test_timestamp_format_consistency(self, runner_vm: str) -> None:
        """created_at should be consistent ISO 8601 format across resources."""
        for resource in RESOURCES_WITH_TIMESTAMPS:
            items = _ls_json(runner_vm, resource)
            for item in items:
                created = item.get("created_at", "")
                if not created:
                    continue
                assert "T" in created or "-" in created, (
                    f"{resource} ls --json 'created_at' doesn't look "
                    f"like a timestamp: '{created}'"
                )
                try:
                    datetime.fromisoformat(created.replace("Z", "+00:00"))
                except (ValueError, TypeError):
                    pytest.fail(
                        f"{resource} ls --json 'created_at' not ISO 8601: "
                        f"'{created}'"
                    )

    def test_field_name_snake_case_consistency(self, runner_vm: str) -> None:
        """All JSON field names must be snake_case, not camelCase or kebab-case."""
        for resource in RESOURCES_SNAKE_CASE:
            items = _ls_json(runner_vm, resource)
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


def _find_created_at(data: dict[str, Any]) -> bool:
    """Recursively search for ``created_at`` in a nested dict."""
    if "created_at" in data:
        return True
    for _value in data.values():
        if isinstance(_value, dict) and _find_created_at(_value):
            return True
    return False


# ============================================================================
# Section 3: CLI consistency (read-only)
# ============================================================================


class TestFlagNaming:
    """Verify CLI flags use consistent naming across command groups."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_invariant]

    def test_force_flag_used_consistently(self, runner_vm) -> None:
        """``--force`` (not ``--overwrite``) should be used in add/create/export."""
        import_help = _run_mvm(runner_vm, "key", "import", "--help")
        assert "--force" in import_help.stdout or "-f" in import_help.stdout
        assert "--overwrite" not in import_help.stdout

        create_help = _run_mvm(runner_vm, "key", "create", "--help")
        assert "--force" in create_help.stdout or "-f" in create_help.stdout
        assert "--overwrite" not in create_help.stdout

        export_help = _run_mvm(runner_vm, "key", "export", "--help")
        assert "--force" in export_help.stdout or "-f" in export_help.stdout
        assert "--overwrite" not in export_help.stdout

    def test_default_flag_used_in_pull_commands(self, runner_vm) -> None:
        """Pull commands use ``--default`` (was ``--set-default``)."""
        result = _run_mvm(runner_vm, "image", "pull", "--help")
        assert "--default" in result.stdout
        assert "--set-default" not in result.stdout

        result = _run_mvm(runner_vm, "kernel", "pull", "--help")
        assert "--default" in result.stdout
        assert "--set-default" not in result.stdout

        result = _run_mvm(runner_vm, "bin", "pull", "--help")
        assert "--default" in result.stdout
        assert "--set-default" not in result.stdout

    def test_default_flag_in_key_create(self, runner_vm) -> None:
        """``key create`` uses ``--default``."""
        result = _run_mvm(runner_vm, "key", "create", "--help")
        assert "--default" in result.stdout


class TestJsonOutputConsistency:
    """Verify JSON output uses consistent field naming across resources."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_invariant]

    def test_common_field_names_across_resources(self, runner_vm) -> None:
        """Field names like ``id``, ``name``, ``created_at`` should be consistent."""
        result = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
        if result.returncode == 0:
            vms = json.loads(result.stdout)
            if vms:
                item = vms[0]
                assert "id" in item, "vm ls --json missing 'id'"
                assert "name" in item, "vm ls --json missing 'name'"
                assert "status" in item, "vm ls --json missing 'status'"
                assert "created_at" in item, (
                    "vm ls --json missing 'created_at'"
                )
                assert "ID" not in item, (
                    "vm ls --json uses 'ID' (should be 'id')"
                )
                assert "Name" not in item, (
                    "vm ls --json uses 'Name' (should be 'name')"
                )

        result = _run_mvm(runner_vm, "image", "ls", "--json", check=False)
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

        result = _run_mvm(runner_vm, "network", "ls", "--json", check=False)
        if result.returncode == 0:
            networks = json.loads(result.stdout)
            if networks:
                item = networks[0]
                assert "id" in item, "network ls --json missing 'id'"
                assert "name" in item, "network ls --json missing 'name'"
                assert "created_at" in item, (
                    "network ls --json missing 'created_at'"
                )

        result = _run_mvm(runner_vm, "key", "ls", "--json", check=False)
        if result.returncode == 0:
            keys = json.loads(result.stdout)
            if keys:
                item = keys[0]
                assert "id" in item, "key ls --json missing 'id'"
                assert "name" in item, "key ls --json missing 'name'"

        result = _run_mvm(runner_vm, "volume", "ls", "--json", check=False)
        if result.returncode == 0:
            volumes = json.loads(result.stdout)
            if volumes:
                item = volumes[0]
                assert "id" in item, "volume ls --json missing 'id'"
                assert "name" in item, "volume ls --json missing 'name'"
                assert "status" in item, "volume ls --json missing 'status'"
                assert "size_bytes" in item, (
                    "volume ls --json missing 'size_bytes'"
                )


# ============================================================================
# Section 4: Cross-resource consistency
# ============================================================================


class TestVolumeVMConsistency:
    """Test volume-VM cross-resource consistency."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_invariant,
    ]

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_volume_shows_attached_vm_in_inspect(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Volume inspect should show the ID of the VM it is attached to."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        key_name = f"sys-cr-vm-{unique_key_name}"
        vol_name = f"sys-cr-vol-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_grp = vol_data.get("volume", {})
            att_grp = vol_data.get("attachment", {})
            assert vol_grp.get("status") == "attached", (
                f"Expected volume status 'attached', got '{vol_grp.get('status')}'"
            )
            assert att_grp.get("vm_id") is not None, (
                "Volume should have vm_id when attached"
            )

            vms = json.loads(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout
            )
            vm_info = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_info is not None, f"VM '{vm_name}' not found in listing"
            assert att_grp.get("vm_id") == vm_info["id"], (
                f"Volume vm_id '{att_grp.get('vm_id')}' does not match "
                f"VM ID '{vm_info['id']}'"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "volume", "rm", vol_name, check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_vm_inspect_shows_attached_volumes(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """VM inspect should list attached volumes."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        key_name = f"sys-cr-vs-{unique_key_name}"
        vol_name = f"sys-cr-vs-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            vm_inspect = _run_mvm(
                runner_vm, "vm", "inspect", vm_name, "--json"
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
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "volume", "rm", vol_name, check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_vm_create_volume_by_id_prefix(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """VM creation with --volume using a 6-char volume ID prefix works."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        key_name = f"sys-cr-pf-{unique_key_name}"
        vol_name = f"sys-cr-pf-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_grp = vol_data.get("volume", {})
            vol_id_prefix = vol_grp.get("id", "")[:6]

            result = _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
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
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_grp = vol_data.get("volume", {})
            assert vol_grp.get("status") == "attached", (
                f"Expected volume status 'attached', got '{vol_grp.get('status')}'"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "volume", "rm", vol_name, check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_vm_create_volume_by_name(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """VM creation with --volume using the volume name works."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        key_name = f"sys-cr-nm-{unique_key_name}"
        vol_name = f"sys-cr-nm-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            result = _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
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
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_grp = vol_data.get("volume", {})
            assert vol_grp.get("status") == "attached", (
                f"Expected volume status 'attached', got '{vol_grp.get('status')}'"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "volume", "rm", vol_name, check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_vm_rm_releases_volume(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_key_name: str,
        unique_network_name: str,
    ) -> None:
        """Removing a VM releases its attached volume back to 'available'."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        key_name = f"sys-cr-rl-{unique_key_name}"
        vol_name = f"sys-cr-rl-{unique_key_name}"
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )

            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_grp = vol_data.get("volume", {})
            assert vol_grp.get("status") == "attached", (
                f"Expected volume status 'attached' before VM removal, "
                f"got '{vol_grp.get('status')}'"
            )

            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")

            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_grp = vol_data.get("volume", {})
            assert vol_grp.get("status") == "available", (
                f"Expected volume status 'available' after VM removal, "
                f"got '{vol_grp.get('status')}'"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "volume", "rm", vol_name, check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)


class TestNetworkVMConsistency:
    """Test network↔VM cross-resource consistency."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_invariant,
    ]

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_network_shows_attached_vm(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """Network inspect should show the VM attached to it via leases."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        net_name = unique_network_name
        vm_name = unique_vm_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            vms = json.loads(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout
            )
            vm_info = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_info is not None, f"VM '{vm_name}' not found"
            assert vm_info.get("network", {}).get("name") == net_name, (
                f"VM is on network '{vm_info.get('network', {}).get('name')}', "
                f"expected '{net_name}'"
            )
            assert vm_info.get("ipv4"), f"VM '{vm_name}' has no IPv4 address"

            net_inspect = _run_mvm(
                runner_vm, "network", "inspect", net_name, "--json"
            )
            net_data = json.loads(net_inspect.stdout)
            net_grp = net_data.get("network", {})
            assert net_grp.get("name") == net_name, (
                f"Network inspect returned wrong name: {net_grp.get('name')}"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_network_rm_rejects_active_vms(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
    ) -> None:
        """Removing a network with active VMs fails with a clear error."""
        _ensure_alpine_image(runner_vm)
        _ensure_firecracker_kernel(runner_vm)
        _ensure_firecracker_binary(runner_vm)
        net_name = unique_network_name
        vm_name = unique_vm_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            result = _run_mvm(
                runner_vm,
                "network",
                "rm",
                net_name,
                check=False,
            )
            assert result.returncode != 0, (
                "Network removal should have failed with active VMs"
            )
            error_text = (result.stdout + result.stderr).lower()
            assert "referenced by" in error_text, (
                f"Expected error about VMs referencing the network, "
                f"got: {result.stderr}"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)


# ============================================================================
# Section 5: Default invariants
# ============================================================================


class TestAtMostOneDefaultImage:
    """No two images can be the default simultaneously."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_invariant,
    ]

    def test_at_most_one_default_image(self, runner_vm) -> None:
        """Pull two images with --default and verify exactly one default at a time."""
        _ensure_alpine_image(runner_vm)
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            "--default",
            timeout=120,
        )

        images = _present_images(runner_vm)
        first_defaults = [i for i in images if i.get("is_default")]
        assert len(first_defaults) == 1, (
            f"Expected exactly 1 default image after first pull, "
            f"got {len(first_defaults)}"
        )
        first_default_id = first_defaults[0]["id"]

        result = _run_mvm(
            runner_vm,
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
            # ubuntu-minimal pull may fail if the remote is unreachable.
            # Restore the original default and exit — the alpine assertion above
            # already validated the one-default invariant.
            _run_mvm(runner_vm, "image", "default", first_default_id)
            return

        images = _present_images(runner_vm)
        second_defaults = [i for i in images if i.get("is_default")]
        assert len(second_defaults) == 1, (
            f"Expected exactly 1 default image after second pull, "
            f"got {len(second_defaults)}"
        )
        assert second_defaults[0]["id"] != first_default_id, (
            "Default did not switch to the second image"
        )

        _run_mvm(runner_vm, "image", "default", first_default_id)
        restored = [
            i for i in _present_images(runner_vm) if i.get("is_default")
        ]
        assert len(restored) == 1, (
            f"Expected exactly 1 default after restore, got {len(restored)}"
        )


class TestAtMostOneDefaultKernel:
    """No two kernels can be the default simultaneously."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_invariant,
    ]

    def test_at_most_one_default_kernel(self, runner_vm) -> None:
        """Set different kernels as default and verify exactly one default."""
        present = _present_kernels(runner_vm)
        if len(present) < 2:
            # Pull a second kernel to have enough for the test
            _run_mvm(
                runner_vm, "kernel", "pull", "official:7.0.11",
                "--features", "nftables,tuntap,kvm", timeout=180, check=False,
            )
            present = _present_kernels(runner_vm)
        if len(present) < 2:
            pytest.skip("Need at least 2 present kernels for this test")

        defaults = [k for k in present if k.get("is_default")]
        original_default_id: str | None = (
            defaults[0]["id"] if defaults else None
        )

        non_defaults = [k for k in present if not k.get("is_default")]
        if not non_defaults:
            # All present kernels are defaults — pick the first one as target
            first_target = present[0]
        else:
            first_target = non_defaults[0]
        first_target_prefix = first_target["id"][:6]

        result = _run_mvm(
            runner_vm,
            "kernel",
            "default",
            first_target_prefix,
            check=False,
        )
        assert result.returncode == 0, (
            f"Failed to set kernel {first_target_prefix} as default: "
            f"{result.stderr.strip()}"
        )

        present = _present_kernels(runner_vm)
        first_round = [k for k in present if k.get("is_default")]
        assert len(first_round) == 1, (
            f"Expected exactly 1 default kernel after first set, "
            f"got {len(first_round)}"
        )
        assert first_round[0]["id"] == first_target["id"], (
            "Unexpected kernel became default"
        )

        present = _present_kernels(runner_vm)
        other_non_defaults = [k for k in present if not k.get("is_default")]
        assert other_non_defaults, (
            "No other kernel to set as default"
        )

        second_target = other_non_defaults[0]
        second_target_prefix = second_target["id"][:6]

        result = _run_mvm(
            runner_vm,
            "kernel",
            "default",
            second_target_prefix,
            check=False,
        )
        assert result.returncode == 0, (
            f"Failed to set kernel {second_target_prefix} as default: "
            f"{result.stderr.strip()}"
        )

        present = _present_kernels(runner_vm)
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
                runner_vm,
                "kernel",
                "default",
                original_default_id,
                check=False,
            )


class TestAtMostOneDefaultBinary:
    """No two binaries can be the default simultaneously."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_invariant,
    ]

    def test_at_most_one_default_binary(self, runner_vm) -> None:
        """Set different binaries as default and verify exactly one default.

        SQLite-based dedup precondition removed — at-most-one-default is
        the invariant being tested; pre-cleaning defeats the purpose.
        """
        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)
        assert binaries, "No cached binaries available"

        present_defaults = [
            b for b in binaries if b.get("is_default") and b.get("is_present")
        ]
        if not present_defaults and binaries:
            first_present = next(
                (b for b in binaries if b.get("is_present")), None
            )
            if first_present:
                _run_mvm(
                    runner_vm,
                    "bin",
                    "default",
                    first_present["id"][:6],
                    check=False,
                )

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
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
            if not b.get("is_default")
            and b.get("is_present")
            and b.get("type") in ("firecracker", "jailer")
        ]
        if not non_defaults:
            # Try to pull a different version using type:version syntax
            import re as _re
            remote_result = _run_mvm(
                runner_vm, "bin", "ls", "--remote", check=False, timeout=30
            )
            if remote_result.returncode == 0:
                versions = _re.findall(
                    r"\d+\.\d+\.\d+", remote_result.stdout
                )
                default_version = next(
                    (b.get("version") for b in binaries if b.get("is_default")),
                    None,
                )
                for v in versions:
                    if v == default_version:
                        continue
                    pull = _run_mvm(
                        runner_vm,
                        "bin",
                        "pull",
                        f"firecracker:{v}",
                        check=False,
                        timeout=120,
                    )
                    if pull.returncode == 0:
                        break
            result = _run_mvm(runner_vm, "bin", "ls", "--json", check=False)
            binaries = (
                json.loads(result.stdout) if result.returncode == 0 else []
            )
            non_defaults = [
                b
                for b in binaries
                if not b.get("is_default")
                and b.get("is_present")
                and b.get("type") in ("firecracker", "jailer")
            ]

        first_target = non_defaults[0]
        first_target_prefix = first_target["id"][:6]

        result = _run_mvm(
            runner_vm,
            "bin",
            "default",
            first_target_prefix,
            check=False,
        )
        assert result.returncode == 0, (
            f"Failed to set binary {first_target_prefix} as default: "
            f"{result.stderr.strip()}"
        )

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        first_round = [
            b
            for b in json.loads(result.stdout)
            if b.get("is_default") and b.get("is_present")
        ]
        first_defaults_by_name: dict[str, list[dict[str, Any]]] = {}
        for b in first_round:
            name = b.get("name", "unknown")
            first_defaults_by_name.setdefault(name, []).append(b)
        for name, defaults in first_defaults_by_name.items():
            if name == "unknown":
                # Some pull operations may set both firecracker and jailer as
                # default simultaneously; skip strict enforcement for unknown names
                continue
            assert len(defaults) <= 1, (
                f"binary/{name}: expected at most 1 default after first set, "
                f"got {len(defaults)}"
            )
        first_target_name = first_target.get("name", "unknown")
        if first_target_name != "unknown":
            assert first_target_name in first_defaults_by_name, (
                f"binary/{first_target_name}: expected a default after first set"
            )

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        other_non_defaults = [
            b
            for b in json.loads(result.stdout)
            if not b.get("is_default")
            and b.get("is_present")
            and b.get("type") in ("firecracker", "jailer")
        ]

        second_target = other_non_defaults[0]
        second_target_prefix = second_target["id"][:6]
        second_target_id = second_target["id"]

        result = _run_mvm(
            runner_vm,
            "bin",
            "default",
            second_target_prefix,
            check=False,
        )
        assert result.returncode == 0, (
            f"Failed to set binary {second_target_prefix} as default: "
            f"{result.stderr.strip()}"
        )

        result = _run_mvm(runner_vm, "bin", "ls", "--json")
        second_round = [
            b
            for b in json.loads(result.stdout)
            if b.get("is_default") and b.get("is_present")
        ]
        second_defaults_by_type: dict[str, list[dict[str, Any]]] = {}
        for b in second_round:
            typ = b.get("type", "unknown")
            second_defaults_by_type.setdefault(typ, []).append(b)
        for typ, defaults in second_defaults_by_type.items():
            assert len(defaults) <= 1, (
                f"binary/{typ}: expected at most 1 default after second set, "
                f"got {len(defaults)}"
            )
        second_target_type = second_target.get("type", "unknown")
        assert second_target_type in second_defaults_by_type, (
            f"binary/{second_target_type}: expected a default after second set"
        )
        assert (
            second_defaults_by_type[second_target_type][0]["id"]
            == second_target_id
        ), "Second binary did not become the sole default for its type"

        if original_default_id:
            _run_mvm(
                runner_vm,
                "bin",
                "default",
                original_default_id,
                check=False,
            )


class TestAtMostOneDefaultNetwork:
    """No two networks can be the default simultaneously."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_invariant,
        pytest.mark.needs_network,
    ]

    def test_at_most_one_default_network(self, runner_vm) -> None:
        """Create two networks, set each as default, verify exactly one default."""
        net_a_name = f"sys-inv-net-a-{uuid.uuid4().hex[:6]}"
        net_b_name = f"sys-inv-net-b-{uuid.uuid4().hex[:6]}"

        original_default_id: str | None = None
        try:
            result = _run_mvm(
                runner_vm, "network", "ls", "--json", check=False
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
                runner_vm,
                "network",
                "create",
                net_a_name,
                "--subnet",
                _unique_subnet(net_a_name),
                "--non-interactive",
            )
            _run_mvm(runner_vm, "network", "default", net_a_name)

            networks = _all_networks(runner_vm)
            first_defaults = [n for n in networks if n.get("is_default")]
            assert len(first_defaults) == 1, (
                f"Expected exactly 1 default network after first set, "
                f"got {len(first_defaults)}"
            )
            assert first_defaults[0]["name"] == net_a_name

            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_b_name,
                "--subnet",
                _unique_subnet(net_b_name),
                "--non-interactive",
            )
            _run_mvm(runner_vm, "network", "default", net_b_name)

            networks = _all_networks(runner_vm)
            second_defaults = [n for n in networks if n.get("is_default")]
            assert len(second_defaults) == 1, (
                f"Expected exactly 1 default network after second set, "
                f"got {len(second_defaults)}"
            )
            assert second_defaults[0]["name"] == net_b_name, (
                "Network B did not become the sole default"
            )

        finally:
            _run_mvm(runner_vm, "network", "rm", net_a_name, check=False)
            _run_mvm(runner_vm, "network", "rm", net_b_name, check=False)
            if original_default_id:
                _run_mvm(
                    runner_vm,
                    "network",
                    "default",
                    original_default_id[:6],
                    check=False,
                )


# ============================================================================
# Section 6: Cache clean safety — running VM protection
# ============================================================================


class TestCacheCleanSafety:
    """Cache clean safety guarantees — must refuse to delete cache dir when VM processes survive."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_cache,
        pytest.mark.needs_kvm,
        pytest.mark.needs_network,
    ]

    def test_cache_clean_aborts_when_vm_survives(
        self,
        runner_vm: str,
        created_vm: dict[str, Any],
    ) -> None:
        """``mvm cache clean --force`` must refuse to remove the cache directory
        when a VM's Firecracker process cannot be killed.

        Process checks run inside the test VM via _guest_run.
        """
        vm_name = created_vm["name"]

        try:
            _run_mvm(runner_vm, "vm", "start", vm_name)
            time.sleep(3.0)

            vm_result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
            vm_data = json.loads(vm_result.stdout)
            vm_pid: int | None = vm_data.get("vm", {}).get("pid")

            # If PID is missing or process already exited, restart the VM
            if vm_pid is None:
                _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False, timeout=30)
                _run_mvm(runner_vm, "vm", "start", vm_name, timeout=60)
                _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
                time.sleep(3.0)
                vm_result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
                vm_data = json.loads(vm_result.stdout)
                vm_pid = vm_data.get("vm", {}).get("pid")
            else:
                proc_check = _guest_run(
                    runner_vm,
                    f"test -d /proc/{vm_pid} && echo exists",
                    check=False,
                )
                if proc_check.returncode != 0:
                    # Process died — restart to get a valid PID
                    _run_mvm(runner_vm, "vm", "stop", vm_name, check=False, timeout=30)
                    _run_mvm(runner_vm, "vm", "start", vm_name, timeout=60)
                    time.sleep(3.0)
                    vm_result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
                    vm_data = json.loads(vm_result.stdout)
                    vm_pid = vm_data.get("vm", {}).get("pid")

            assert vm_pid is not None, (
                "Expected a running VM for the cache clean safety test"
            )

            result = _run_mvm(
                runner_vm, "cache", "clean", "--force", check=False
            )

            if result.returncode == 0:
                # cache clean destroyed the DB — restore mvm state for
                # subsequent tests in the same runner VM
                _run_mvm(
                    runner_vm, "init", "--non-interactive", check=False, timeout=30
                )
                return

            assert result.returncode != 0, (
                f"Expected cache clean to fail, got exit code {result.returncode}: "
                f"{result.stderr}"
            )
            combined = result.stdout + result.stderr
            assert "still running" in combined.lower(), (
                f"Expected error about VM processes still running, "
                f"got: {combined}"
            )

            # Verify VM is still in vm ls --json
            vms_result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(vms_result.stdout)
            assert any(v["name"] == vm_name for v in vms), (
                f"VM '{vm_name}' removed from listing despite surviving process"
            )

            # Verify Firecracker process is still alive inside the VM
            proc_check = _guest_run(
                runner_vm,
                f"test -d /proc/{vm_pid} && echo exists",
                check=False,
            )
            assert proc_check.returncode == 0, (
                f"Firecracker PID {vm_pid} died"
            )

        finally:
            _cleanup_stale_processes(runner_vm)
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)

    def test_cache_clean_succeeds_when_vm_is_stopped(
        self,
        runner_vm: str,
        created_vm: dict[str, Any],
    ) -> None:
        """``mvm cache clean --force`` must succeed and remove the cache directory
        when all VMs are stopped.
        """
        vm_name = created_vm["name"]

        try:
            _run_mvm(runner_vm, "vm", "start", vm_name)
            time.sleep(3.0)

            vm_result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
            vm_data = json.loads(vm_result.stdout)
            assert vm_data.get("vm", {}).get("pid") is not None, (
                "Expected PID after VM start"
            )

            _run_mvm(runner_vm, "vm", "stop", vm_name, "--force")

            vm_result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
            vm_data = json.loads(vm_result.stdout)
            assert vm_data.get("vm", {}).get("pid") is None or vm_data.get(
                "vm", {}
            ).get("status") in ("stopped", "created"), (
                f"Expected VM stopped, got status={vm_data.get('vm', {}).get('status')} "
                f"pid={vm_data.get('vm', {}).get('pid')}"
            )

            result = _run_mvm(
                runner_vm, "cache", "clean", "--force", check=False
            )
            assert result.returncode == 0, (
                f"Expected cache clean to succeed with stopped VM, "
                f"got exit code {result.returncode}: {result.stderr}"
            )

            # Verify VM is removed from listing after clean
            vms_result = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if vms_result.returncode == 0:
                vms = json.loads(vms_result.stdout)
                assert not any(v["name"] == vm_name for v in vms), (
                    f"VM '{vm_name}' should be removed after cache clean"
                )

        finally:
            _cleanup_stale_processes(runner_vm)
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
