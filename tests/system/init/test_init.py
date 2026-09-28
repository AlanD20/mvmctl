"""Init wizard system tests."""

from __future__ import annotations

import json
import os
import re
import shlex
from pathlib import Path

import pytest

from tests.system.conftest import _guest_run, _json_success_data, _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_init]


def _init_inventory(runner_vm):
    inventory = {
        domain: _json_success_data(
            _run_mvm(runner_vm, domain, "ls", "--json").stdout, list
        )
        for domain in (
            "vm", "network", "image", "kernel", "bin", "key", "volume",
            "snapshot", "env",
        )
    }
    for policy, expected in (("exec", list), ("traffic", dict)):
        inventory[f"policy.{policy}"] = _json_success_data(
            _run_mvm(runner_vm, "policy", policy, "ls", "--json").stdout,
            expected,
        )
    inventory["config"] = _json_success_data(
        _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
    )
    return inventory


def _retained_rows(domain, rows):
    # HostNetworkSetup readmits registered networks; repository Upsert updates
    # updated_at even when all identity, desired and observed fields are equal.
    # Kernel ListAll also refreshes updated_at when reconciling missing files.
    return sorted(
        ({key: value for key, value in row.items()
          if domain not in ("network", "kernel") or key != "updated_at"} for row in rows),
        key=lambda row: json.dumps(row, sort_keys=True),
    )


class _OwnedInitCase:
    """Preseed-only wizard, with one exact attempted default-network receipt."""

    def __init__(self, runner_vm, before):
        self.runner_vm = runner_vm
        self.before = before
        self.expected_network = None
        self.network_receipt = None

    def networks(self):
        return _json_success_data(
            _run_mvm(self.runner_vm, "network", "ls", "--json").stdout, list
        )

    def capture_network(self):
        baseline_ids = {row["id"] for row in self.before["network"]}
        created = [row for row in self.networks() if row["id"] not in baseline_ids]
        if not created:
            return
        assert self.expected_network is not None, created
        assert len(created) == 1, created
        row = created[0]
        assert re.fullmatch(r"[0-9a-f]{64}", row["id"]), row
        assert {key: row[key] for key in self.expected_network} == self.expected_network
        receipt = {key: row[key] for key in ("id", "name", "subnet", "nat_enabled", "bridge")}
        assert self.network_receipt is None or receipt == self.network_receipt
        self.network_receipt = receipt

    def run_init(self, *, skip_network=False):
        if not skip_network and not self.before["network"]:
            configured = self.before["config"]["defaults.network"]
            self.expected_network = {
                key: value["override"] if value["override"] is not None else value["default"]
                for key, value in configured.items()
                if key in ("name", "subnet", "nat_enabled")
            }
            assert set(self.expected_network) == {"name", "subnet", "nat_enabled"}
        args = ["init", "--non-interactive", "--skip-host", "--binary-version", "1.16.2"]
        if skip_network:
            args.append("--skip-network")
        try:
            return _run_mvm(self.runner_vm, *args, check=False)
        finally:
            # Capture partial publication even when the CLI raises or fails.
            self.capture_network()

    def cleanup_network(self):
        self.capture_network()
        receipt = self.network_receipt
        if receipt is None:
            return
        rows = [row for row in self.networks() if row["id"] == receipt["id"]]
        if rows:
            assert len(rows) == 1
            assert {key: rows[0][key] for key in receipt} == receipt
            _run_mvm(self.runner_vm, "network", "rm", receipt["id"])
        assert not any(
            row["id"] == receipt["id"] or row["name"] == receipt["name"]
            for row in self.networks()
        ), receipt
        if receipt["bridge"]:
            result = _guest_run(
                self.runner_vm, f"ip link show dev {shlex.quote(receipt['bridge'])}",
                check=False,
            )
            assert result.returncode == 1 and "does not exist" in result.stderr, result

    def restore_defaults(self, domain):
        original = self.before[domain]
        current = _json_success_data(
            _run_mvm(self.runner_vm, domain, "ls", "--json").stdout, list
        )
        expected = {row["id"] for row in original if row["is_default"]}
        actual = {row["id"] for row in current if row["is_default"]}
        if actual == expected:
            return
        assert expected <= {row["id"] for row in current}, (domain, expected, current)
        if domain == "key":
            _run_mvm(self.runner_vm, "key", "default", "--clear")
        else:
            # No public clear-default operation exists for these domains.
            # Never delete a baseline row to manufacture the old default set.
            assert expected, (domain, "cannot clear unexpected default safely", actual)
        for resource_id in sorted(expected):
            _run_mvm(self.runner_vm, domain, "default", resource_id)
        restored = _json_success_data(
            _run_mvm(self.runner_vm, domain, "ls", "--json").stdout, list
        )
        assert {row["id"] for row in restored if row["is_default"]} == expected

    def restore_config(self):
        failures = []
        current = _json_success_data(
            _run_mvm(self.runner_vm, "config", "ls", "--json").stdout, dict
        )
        for category, settings in self.before["config"].items():
            for key, original in settings.items():
                try:
                    if current[category][key]["override"] == original["override"]:
                        continue
                    value = original["override"]
                    if value is None:
                        _run_mvm(self.runner_vm, "config", "reset", category, key)
                    else:
                        serialized = (
                            json.dumps(value) if isinstance(value, (bool, dict)) else str(value)
                        )
                        _run_mvm(self.runner_vm, "config", "set", category, key, serialized)
                except Exception as error:
                    failures.append(f"config {category}.{key}: {error}")
        assert not failures, "\n".join(failures)


@pytest.fixture
def owned_init_case(runner_vm):
    """All six selected cases retain the runner's public function baseline."""
    before = _init_inventory(runner_vm)
    host = _json_success_data(
        _run_mvm(runner_vm, "host", "status", "--json").stdout, dict
    )
    for flag in ("kvm_accessible", "ip_forward_ok", "group_exists", "sudoers_exists", "user_in_group"):
        assert host[flag] is True, (flag, host)
    assert not host["missing_binaries"], host
    # This probe exercises bridge-family nftables, not just advertised features.
    assert host["resources"]["nftables_available"] is True, host
    mirror = os.environ.get("MVM_ASSET_MIRROR")
    assert mirror, "An existing populated asset mirror is required; tests never pull"
    mirror_path = Path(mirror).expanduser()
    assert mirror_path.is_dir() and next(mirror_path.iterdir(), None) is not None
    assert any(row["is_default"] and row["is_present"] for row in before["kernel"])
    assert any(
        row["type"] == "alpine" and row["version"] == "3.23" and row["is_present"]
        for row in before["image"]
    ), before["image"]
    for binary_type in ("firecracker", "jailer"):
        assert any(
            row["type"] == binary_type and row["version"] == "1.16.2"
            and row["is_default"] and row["is_present"]
            for row in before["bin"]
        ), before["bin"]
    assert all(row["is_present"] and row["bridge_active"] for row in before["network"])
    case = _OwnedInitCase(runner_vm, before)
    try:
        yield case
    finally:
        failures = []
        try:
            case.cleanup_network()
        except Exception as error:
            failures.append(f"owned network cleanup: {error}")
        # Independent restoration must continue after network-removal failures.
        for domain in ("network", "image", "kernel", "bin", "key"):
            try:
                case.restore_defaults(domain)
            except Exception as error:
                failures.append(f"{domain} defaults: {error}")
        try:
            case.restore_config()
        except Exception as error:
            failures.append(f"config restoration: {error}")
        # One failed inventory cannot suppress the other baseline readbacks.
        for domain, expected in before.items():
            try:
                if domain == "config":
                    actual = _json_success_data(
                        _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
                    )
                    assert actual == expected
                elif domain.startswith("policy."):
                    actual = _json_success_data(
                        _run_mvm(runner_vm, "policy", domain.split(".")[1], "ls", "--json").stdout,
                        type(expected),
                    )
                    assert actual == expected
                else:
                    actual = _json_success_data(
                        _run_mvm(runner_vm, domain, "ls", "--json").stdout, list
                    )
                    assert _retained_rows(domain, actual) == _retained_rows(domain, expected)
            except Exception as error:
                failures.append(f"retained {domain} baseline: {error}")
        assert not failures, "\n".join(failures)


class TestInitWizard:
    """Test the mvm init wizard (non-destructive, no sudo required with --skip-host)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_init,
    ]

    def test_init_non_interactive_skip_host(self, runner_vm, owned_init_case):
        """Run init in non-interactive mode skipping host setup.

        This should succeed without sudo because --skip-host avoids
        the privileged host-init step.
        """
        result = owned_init_case.run_init()

        assert result.returncode == 0
        output = result.stdout + result.stderr
        assert "all set" in output, (
            f"Output missing expected success message:\n{output}"
        )

    def test_init_idempotent(self, runner_vm, owned_init_case):
        """Run init twice — both invocations should succeed.

        Verifies that init is safe to run when everything is already
        set up (idempotent).
        """
        first = owned_init_case.run_init()
        assert first.returncode == 0, (
            f"First init failed (rc={first.returncode})\n"
            f"stdout: {first.stdout}\n"
            f"stderr: {first.stderr}"
        )
        first_output = (first.stdout + first.stderr).strip()
        assert first_output, "First init produced no output"

        second = owned_init_case.run_init()
        assert second.returncode == 0, (
            f"Second init failed (rc={second.returncode})\n"
            f"stdout: {second.stdout}\n"
            f"stderr: {second.stderr}"
        )
        second_output = (second.stdout + second.stderr).strip()
        assert second_output, (
            "Second init (idempotent) produced no output"
        )

    def test_init_abort_on_sudo_needed(self, runner_vm):
        """Run init without --skip-host — should succeed as root (no sudo needed).

        Inside the test VM, commands run as root so sudo is not required.
        This test verifies init handles the privilege path correctly by
        checking that output mentions the host setup steps.
        """
        result = _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            check=False,
        )
        assert result.returncode == 0, (
            f"Expected zero exit (running as root): rc={result.returncode}\n"
            f"stdout: {result.stdout}\n"
            f"stderr: {result.stderr}"
        )
        output = result.stdout + result.stderr
        assert "all set" in output.lower(), (
            f"Output missing expected success message:\n{output}"
        )

    def test_init_without_skip_host(self, runner_vm):
        """Run init without --skip-host non-interactively.

        Inside the test VM, passwordless sudo is always available,
        so init --non-interactive without --skip-host should succeed.
        """
        result = _run_mvm(
            runner_vm,
            "init",
            "--non-interactive",
            check=False,
        )
        assert result.returncode == 0, (
            f"Expected zero exit with passwordless sudo, got rc={result.returncode}\n"
            f"stdout: {result.stdout}\n"
            f"stderr: {result.stderr}"
        )


class TestRootFlags:
    """Test root-level CLI flags (--version, --verbose, --debug)."""

    def test_version_flag(self, runner_vm, owned_init_case):
        """``mvm --version`` should print version and exit."""
        result = _run_mvm(runner_vm, "--version", check=False)
        assert result.returncode == 0
        assert result.stdout.strip(), (
            "Expected version string in output"
        )

    def test_verbose_flag(self, runner_vm, owned_init_case):
        """``mvm --verbose`` should enable verbose logging output."""
        result = _run_mvm(
            runner_vm,
            "--verbose",
            "config",
            "get",
            "defaults.vm",
            "vcpu_count",
            check=False,
        )
        assert result.returncode == 0
        assert "vcpu_count" in result.stdout

    def test_debug_flag(self, runner_vm, owned_init_case):
        """``mvm --debug`` should enable debug-level logging."""
        result = _run_mvm(
            runner_vm,
            "--debug",
            "config",
            "get",
            "defaults.vm",
            "vcpu_count",
            check=False,
        )
        assert result.returncode == 0
        assert "vcpu_count" in result.stdout


class TestInitEdgeCases:
    """Tests for init command edge cases (non-destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier1,
        pytest.mark.domain_init,
    ]

    def test_init_skip_network(self, runner_vm, owned_init_case):
        """``init --non-interactive --skip-network`` should succeed (exit 0).

        Rationale: The --skip-network flag skips network setup during init.
        A regression where --skip-network is silently ignored would cause
        the init wizard to attempt privileged network operations when the
        caller explicitly opted out. L1 verification: checks exit 0 and
        mentions the flag in output.
        """
        result = owned_init_case.run_init(skip_network=True)

        assert result.returncode == 0, (
            f"init --skip-network failed: rc={result.returncode} "
            f"stdout={result.stdout} stderr={result.stderr}"
        )
        output = result.stdout + result.stderr
        assert "all set" in output.lower(), (
            f"Output missing expected success message:\n{output}"
        )
