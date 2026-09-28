"""Installed-candidate config behavior with exact override restoration."""

from __future__ import annotations

import json
import re
from contextlib import contextmanager
from typing import Generator

import pytest

from tests.system.conftest import _json_success_data, _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_config]


def _override_argument(info: dict) -> str | None:
    """Admit only metadata whose prior override can be restored through config set."""
    assert set(info) == {"type", "default", "override"}, info
    kind = info["type"]
    expected = {"int": int, "bool": bool, "string": str, "nil": type(None)}
    assert isinstance(kind, str) and kind in expected, f"Unsupported config metadata: {info}"
    assert info["default"] is not None or kind == "nil", info
    for value in (info["default"], info["override"]):
        if value is not None:
            assert type(value) is expected[kind], info
    override = info["override"]
    if override is None:
        return None
    if kind == "bool":
        return "true" if override else "false"
    return str(override)


def _config_inventory(runner_vm: str) -> dict:
    inventory = _json_success_data(_run_mvm(runner_vm, "config", "ls", "--json").stdout, dict)
    assert inventory, inventory
    for category, settings in inventory.items():
        assert isinstance(category, str) and category and isinstance(settings, dict), inventory
        for key, info in settings.items():
            assert isinstance(key, str) and key and isinstance(info, dict), settings
            _override_argument(info)
    return inventory


@contextmanager
def _preserve_overrides(
    runner_vm: str, *, category: str | None = None, keys: tuple[str, ...] | None = None,
) -> Generator[dict, None, None]:
    """Restore only the affected scope, then prove the complete prior inventory is unchanged."""
    baseline = _config_inventory(runner_vm)
    assert category is not None or keys is None
    if category is None:
        affected = [(cat, key) for cat, settings in baseline.items() for key in settings]
    else:
        assert category in baseline, category
        selected = tuple(baseline[category]) if keys is None else keys
        assert selected and all(key in baseline[category] for key in selected), selected
        affected = [(category, key) for key in selected]
    # Admit the complete affected restore inventory before the first mutation.
    arguments = {(cat, key): _override_argument(baseline[cat][key]) for cat, key in affected}
    try:
        yield baseline
    finally:
        current = _config_inventory(runner_vm)
        failures = []
        for cat, key in sorted(affected):
            if current[cat][key]["override"] == baseline[cat][key]["override"]:
                continue
            argument = arguments[(cat, key)]
            args = ("reset", cat, key) if argument is None else ("set", cat, key, argument)
            result = _run_mvm(runner_vm, "config", *args, check=False)
            if result.returncode != 0:
                failures.append((cat, key, result.returncode, result.stdout, result.stderr))
        restored = _config_inventory(runner_vm)
        assert not failures, failures
        assert restored == baseline, (baseline, restored)


def _assert_cpu_value(runner_vm: str, value: int) -> None:
    assert _json_success_data(
        _run_mvm(runner_vm, "config", "get", "defaults.vm", "vcpu_count", "--json").stdout, int,
    ) == value


def _assert_reset_scope(current: dict, baseline: dict) -> None:
    assert set(current) == set(baseline), (baseline, current)
    for key, original in baseline.items():
        assert current[key] == {**original, "override": None}, (key, current[key], original)


def _assert_config_error(result) -> None:
    assert result.returncode != 0, (result.stdout, result.stderr)
    envelope = json.loads(result.stdout)
    assert envelope["schema_version"] == 1, envelope
    assert envelope["status"] == "error", envelope
    assert isinstance(envelope["error"], dict) and envelope["error"]["code"], envelope


class TestConfigLifecycle:
    """Test config read, write, and reset operations."""

    def test_config_get_existing(self, runner_vm):
        info = _config_inventory(runner_vm)["defaults.vm"]["vcpu_count"]
        result = _run_mvm(runner_vm, "config", "get", "defaults.vm", "vcpu_count")
        match = re.search(r"vcpu_count\s*[=:]\s*(\d+)", result.stdout)
        assert match and int(match.group(1)) > 0, result.stdout
        expected = info["default"] if info["override"] is None else info["override"]
        _assert_cpu_value(runner_vm, expected)

    def test_config_list(self, runner_vm):
        result = _run_mvm(runner_vm, "config", "ls")
        assert "[defaults.vm]" in result.stdout, result.stdout
        assert {"vcpu_count", "mem_size_mib", "boot_args"} <= set(
            _config_inventory(runner_vm)["defaults.vm"],
        )

    def test_config_set_and_get(self, runner_vm):
        with _preserve_overrides(runner_vm, category="defaults.vm", keys=("vcpu_count",)):
            _run_mvm(runner_vm, "config", "set", "defaults.vm", "vcpu_count", "4")
            assert _config_inventory(runner_vm)["defaults.vm"]["vcpu_count"]["override"] == 4
            _assert_cpu_value(runner_vm, 4)

    def test_config_reset(self, runner_vm):
        with _preserve_overrides(runner_vm, category="defaults.vm", keys=("vcpu_count",)) as baseline:
            _run_mvm(runner_vm, "config", "set", "defaults.vm", "vcpu_count", "4")
            _assert_cpu_value(runner_vm, 4)
            _run_mvm(runner_vm, "config", "reset", "defaults.vm", "vcpu_count")
            info = _config_inventory(runner_vm)["defaults.vm"]["vcpu_count"]
            assert info == {**baseline["defaults.vm"]["vcpu_count"], "override": None}, info
            _assert_cpu_value(runner_vm, info["default"])

    def test_config_reset_all(self, runner_vm):
        with _preserve_overrides(runner_vm) as baseline:
            _run_mvm(runner_vm, "config", "set", "defaults.vm", "vcpu_count", "6")
            _assert_cpu_value(runner_vm, 6)
            _run_mvm(runner_vm, "config", "reset", "--all", "--force")
            current = _config_inventory(runner_vm)
            assert set(current) == set(baseline), current
            for category in baseline:
                _assert_reset_scope(current[category], baseline[category])
            _assert_cpu_value(runner_vm, baseline["defaults.vm"]["vcpu_count"]["default"])


class TestConfigEdgeCases:
    """Category operations and public command guidance."""

    def test_config_get_category_only(self, runner_vm):
        result = _run_mvm(runner_vm, "config", "get", "defaults.vm")
        for key in ("vcpu_count", "mem_size_mib", "boot_args"):
            assert key in result.stdout, result.stdout
        category = _json_success_data(
            _run_mvm(runner_vm, "config", "get", "defaults.vm", "--json").stdout, dict,
        )
        assert category == _config_inventory(runner_vm)["defaults.vm"]

    def test_config_reset_no_args(self, runner_vm):
        baseline = _config_inventory(runner_vm)
        result = _run_mvm(runner_vm, "config", "reset")
        assert "Provide a category" in result.stdout, result.stdout
        assert _config_inventory(runner_vm) == baseline

    def test_config_set_invalid_category(self, runner_vm):
        baseline = _config_inventory(runner_vm)
        result = _run_mvm(
            runner_vm, "config", "set", "nonexistent.cat", "some_key", "some_value",
            "--json", check=False,
        )
        _assert_config_error(result)
        assert _config_inventory(runner_vm) == baseline

    def test_config_reset_category_only(self, runner_vm):
        with _preserve_overrides(runner_vm, category="defaults.vm") as baseline:
            _run_mvm(runner_vm, "config", "set", "defaults.vm", "vcpu_count", "6")
            _assert_cpu_value(runner_vm, 6)
            result = _run_mvm(runner_vm, "config", "reset", "defaults.vm")
            assert "override(s)" in result.stdout, result.stdout
            current = _config_inventory(runner_vm)
            _assert_reset_scope(current["defaults.vm"], baseline["defaults.vm"])
            assert {cat: values for cat, values in current.items() if cat != "defaults.vm"} == {
                cat: values for cat, values in baseline.items() if cat != "defaults.vm"
            }


class TestConfigEdgeCasesExtended:
    """Invalid key and value requests leave the existing configuration intact."""

    def test_config_get_nonexistent_key(self, runner_vm):
        baseline = _config_inventory(runner_vm)
        result = _run_mvm(
            runner_vm, "config", "get", "defaults.vm", "nonexistent_key_xyz",
            "--json", check=False,
        )
        _assert_config_error(result)
        assert _config_inventory(runner_vm) == baseline

    def test_config_set_invalid_value_type(self, runner_vm):
        with _preserve_overrides(runner_vm, category="defaults.vm", keys=("vcpu_count",)) as baseline:
            result = _run_mvm(
                runner_vm, "config", "set", "defaults.vm", "vcpu_count", "not-a-number",
                "--json", check=False,
            )
            _assert_config_error(result)
            assert _config_inventory(runner_vm) == baseline


class TestConfigEdgeCasesResetAllAfterSet:
    """Global reset removes every override and preserves the prior inventory afterward."""

    def test_config_reset_all_with_multiple_overrides(self, runner_vm):
        with _preserve_overrides(runner_vm) as baseline:
            _run_mvm(runner_vm, "config", "set", "defaults.vm", "vcpu_count", "8")
            _run_mvm(runner_vm, "config", "set", "defaults.vm", "mem_size_mib", "2048")
            _assert_cpu_value(runner_vm, 8)
            assert _json_success_data(
                _run_mvm(runner_vm, "config", "get", "defaults.vm", "mem_size_mib", "--json").stdout, int,
            ) == 2048
            _run_mvm(runner_vm, "config", "reset", "--all", "--force")
            current = _config_inventory(runner_vm)
            assert set(current) == set(baseline), current
            for category in baseline:
                _assert_reset_scope(current[category], baseline[category])
