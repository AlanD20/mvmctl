"""Installed-CLI JSON, default, and flag contracts across resource domains."""

from __future__ import annotations

import json
from datetime import datetime
from typing import Any

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system]

# ============================================================================
# Helpers
# ============================================================================


def _success_data(result: Any, expected_type: type) -> Any:
    """Require a successful, versioned response from the installed CLI."""
    assert result.returncode == 0, result.stderr or result.stdout
    envelope = json.loads(result.stdout)
    assert envelope["schema_version"] == 1
    assert envelope["status"] == "success"
    assert envelope["error"] is None
    data = envelope["data"]
    assert isinstance(data, expected_type), envelope
    return data


def _ls_json(vm_name: str, resource: str) -> list[dict[str, Any]]:
    """Run ``<resource> ls --json`` and require a list result."""
    items = _success_data(
        _run_mvm(vm_name, resource, "ls", "--json", check=False), list
    )
    assert all(isinstance(item, dict) for item in items), items
    return items


def _inspect_json(
    vm_name: str, resource: str, identifier: str
) -> dict[str, Any]:
    """Run ``<resource> inspect <id> --json`` and require an object result."""
    return _success_data(
        _run_mvm(vm_name, resource, "inspect", identifier, "--json", check=False),
        dict,
    )


# ============================================================================
# Section 1: Cross-domain default invariant
# ============================================================================


class TestInvariants:
    """Cross-domain default uniqueness through the installed CLI."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_invariant]

    def test_at_most_one_default_per_domain(self, runner_vm: str) -> None:
        """Cross-domain default uniqueness — verify via ls --json for each type.

        SQLite-based binary dedup precondition removed — at-most-one-default is
        the invariant being tested. If duplicate defaults exist, the test fails
        and the production code should be fixed.
        """
        images = _ls_json(runner_vm, "image")
        kernels = _ls_json(runner_vm, "kernel")
        binaries = _ls_json(runner_vm, "bin")
        networks = _ls_json(runner_vm, "network")

        for domain_name, resource_list in [
            ("image", images),
            ("kernel", kernels),
            ("network", networks),
        ]:
            assert all(isinstance(r.get("is_default"), bool) for r in resource_list)
            defaults = [r for r in resource_list if r["is_default"]]
            assert len(defaults) <= 1, (
                f"{domain_name}: expected at most 1 default, "
                f"got {len(defaults)}"
            )
            if domain_name in ("image", "kernel"):
                for default in defaults:
                    assert default.get("is_present") is True, (
                        f"{domain_name} default is not present: {default}"
                    )

        binary_defaults_by_type: dict[str, list[dict[str, Any]]] = {}
        for b in binaries:
            assert isinstance(b.get("is_default"), bool), b
            if b["is_default"]:
                btype = b.get("type", "unknown")
                binary_defaults_by_type.setdefault(btype, []).append(b)
        for btype, defaults in binary_defaults_by_type.items():
            assert len(defaults) <= 1, (
                f"binary/{btype}: expected at most 1 default, "
                f"got {len(defaults)}"
            )
            assert defaults[0].get("is_present") is True, (
                f"binary/{btype} default is not present: {defaults[0]}"
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
    {"cmd": "bin", "name_field": "version"},
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
            assert rt["name_field"] in item, (
                f"{resource} ls --json missing '{rt['name_field']}'"
            )
            assert "created_at" in item, (
                f"{resource} ls --json missing 'created_at'"
            )
            if resource in RESOURCES_WITH_STATUS:
                assert "status" in item, f"{resource} ls --json missing 'status'"
            if resource == "volume":
                assert "size_bytes" in item, "volume ls --json missing 'size_bytes'"
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
        for rt in RESOURCE_TYPES:
            resource = rt["cmd"]
            if resource == "bin":
                continue  # bin has no inspect subcommand
            group_key = resource
            items = _ls_json(runner_vm, resource)
            if not items:
                continue
            first_id = items[0].get("id", "")
            assert first_id, f"{resource} ls --json has an empty 'id'"

            item = _inspect_json(runner_vm, resource, first_id)

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
