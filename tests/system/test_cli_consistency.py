"""CLI consistency system tests — flag naming and JSON output field consistency.

These tests verify that CLI flags use consistent naming across command groups
and that JSON output field names follow a uniform convention.

Black-box CLI system tests. NO imports from ``mvmctl.*``.
"""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_consistency]


class TestFlagNaming:
    """Verify CLI flags use consistent naming across command groups."""

    def test_force_flag_used_consistently(self, mvm_binary) -> None:
        """``--force`` (not ``--overwrite``) should be used in add/create/export.

        This was Bug 5 — some commands used ``--overwrite`` instead of the
        consistent ``--force`` flag.
        """
        # key add --help: should contain --force
        add_help = _run_mvm(mvm_binary, "key", "add", "--help")
        assert "--force" in add_help.stdout or "-f" in add_help.stdout
        assert "--overwrite" not in add_help.stdout

        # key create --help: should contain --force
        create_help = _run_mvm(mvm_binary, "key", "create", "--help")
        assert "--force" in create_help.stdout or "-f" in create_help.stdout
        assert "--overwrite" not in create_help.stdout

        # key export --help: should contain --force
        export_help = _run_mvm(mvm_binary, "key", "export", "--help")
        assert "--force" in export_help.stdout or "-f" in export_help.stdout
        assert "--overwrite" not in export_help.stdout

    def test_default_flag_used_in_pull_commands(self, mvm_binary) -> None:
        """Pull commands use ``--default`` (was ``--set-default``)."""
        # image pull --help: should contain --default
        result = _run_mvm(mvm_binary, "image", "pull", "--help")
        assert "--default" in result.stdout
        assert "--set-default" not in result.stdout

        # kernel pull --help: should contain --default
        result = _run_mvm(mvm_binary, "kernel", "pull", "--help")
        assert "--default" in result.stdout
        assert "--set-default" not in result.stdout

        # bin pull --help: should contain --default
        result = _run_mvm(mvm_binary, "bin", "pull", "--help")
        assert "--default" in result.stdout
        assert "--set-default" not in result.stdout

    def test_set_default_flag_in_key_create(self, mvm_binary) -> None:
        """``key create`` intentionally kept ``--set-default``."""
        result = _run_mvm(mvm_binary, "key", "create", "--help")
        assert "--set-default" in result.stdout


class TestJsonOutputConsistency:
    """Verify JSON output uses consistent field naming across resources."""

    def test_common_field_names_across_resources(self, mvm_binary) -> None:
        """Field names like ``id``, ``name``, ``created_at`` should be consistent.

        Uses ``check=False`` since some resource lists may be empty.
        """
        # -- vm ls --json
        result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        if result.returncode == 0:
            vms = json.loads(result.stdout)
            if vms:
                item = vms[0]
                assert "id" in item, "vm ls --json missing 'id'"
                assert "name" in item, "vm ls --json missing 'name'"
                assert "status" in item, "vm ls --json missing 'status'"
                assert "created_at" in item, "vm ls --json missing 'created_at'"
                # Verify lowercase convention
                assert "ID" not in item, (
                    "vm ls --json uses 'ID' (should be 'id')"
                )
                assert "Name" not in item, (
                    "vm ls --json uses 'Name' (should be 'name')"
                )

        # -- image ls --json
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

        # -- network ls --json
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

        # -- key ls --json
        result = _run_mvm(mvm_binary, "key", "ls", "--json", check=False)
        if result.returncode == 0:
            keys = json.loads(result.stdout)
            if keys:
                item = keys[0]
                assert "id" in item, "key ls --json missing 'id'"
                assert "name" in item, "key ls --json missing 'name'"

        # -- volume ls --json
        result = _run_mvm(mvm_binary, "volume", "ls", "--json", check=False)
        if result.returncode == 0:
            volumes = json.loads(result.stdout)
            if volumes:
                item = volumes[0]
                assert "id" in item, "volume ls --json missing 'id'"
                assert "name" in item, "volume ls --json missing 'name'"
                assert "status" in item, "volume ls --json missing 'status'"
                assert "size" in item, "volume ls --json missing 'size'"
