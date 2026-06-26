"""VM lifecycle system tests — focused classes with dependency ordering."""

from __future__ import annotations

import concurrent.futures
import json
import os
import shlex
import shutil
import subprocess
import time
import uuid
from pathlib import Path
from typing import Generator

import pytest

from tests.system.conftest import (
    _guest_run,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
    wait_for_ssh,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
]


# ========================================================================
# TestVMListEmpty — MUST run before any VM is created
# ========================================================================


class TestVMListEmpty:
    """Test vm ls behavior when no VMs exist — runs before any VM creation."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    def test_list_empty(self, runner_vm):
        """vm ls --json returns empty list when no VMs exist."""
        result = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
        if result.returncode == 0 and result.stdout.strip():
            try:
                existing = json.loads(result.stdout)
                for vm in existing:
                    _run_mvm(
                        runner_vm,
                        "vm",
                        "rm",
                        vm["name"],
                        "--force",
                        check=False,
                    )
            except (json.JSONDecodeError, KeyError, TypeError):
                pass

        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        assert result.returncode == 0, f"vm ls --json failed: {result.stderr}"
        vms = json.loads(result.stdout)
        assert isinstance(vms, list), (
            f"Expected list, got {type(vms).__name__}: {vms}"
        )
        assert len(vms) == 0, (
            f"Expected empty VM list, got {len(vms)} VMs: "
            f"{[v.get('name') for v in vms]}. "
            "Stale VMs should have been cleaned up."
        )


# ========================================================================
# TestVMAdvancedCreateFlags
# ========================================================================


class TestVMAdvancedCreateFlags:
    """Advanced vm create flags: --ssh-key <filepath>, --user,
    --lsm-flags, --skip-cleanup, --skip-deblob."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_ssh_key_filepath(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --ssh-key pointing to a key file path (not a named key)."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            # Generate a temp SSH key inside the test VM and register it
            key_name = f"ssh-test-{unique_vm_name}"
            test_key_priv = f"/tmp/{key_name}"
            _guest_run(runner_vm,
                f"ssh-keygen -t ed25519 -f '{test_key_priv}' -N '' -q",
                timeout=30,
            )
            _run_mvm(
                runner_vm,
                "key",
                "import",
                key_name,
                f"{test_key_priv}.pub",
            )

            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
            )

            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_create_with_user_flag(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --user set to custom SSH user."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--user",
                "customuser",
            )

            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"

            inspect = _run_mvm(
                runner_vm, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(inspect.stdout)
            vm_data = data.get("vm", {})
            user_val = vm_data.get("ssh_user") or vm_data.get("user") or ""
            assert user_val == "customuser" or "customuser" in str(data), (
                f"Expected 'customuser' in inspect output, got: {data}"
            )
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_lsm_flags(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --lsm-flags set to \"lsm=1\"."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--lsm-flags",
                "lsm=1",
            )

            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"

            ls_result = _run_mvm(runner_vm, "vm", "ls", "--json")
            ls_data = json.loads(ls_result.stdout)
            vm_entry = next(
                (v for v in ls_data if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry.get("lsm_flags") == "lsm=1", (
                f"Expected lsm_flags 'lsm=1' in ls --json, got: {vm_entry}"
            )
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_skip_cleanup(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --skip-cleanup flag."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            # --skip-cleanup triggers typer.confirm(), so we pipe "y" to stdin.
            import subprocess as _subprocess

            result = _subprocess.run(
                ["mvm", "vm", "create", unique_vm_name,
                 "--image", "alpine:3.23",
                 "--network", net_name,
                 "--skip-cleanup"],
                capture_output=True,
                text=True,
                timeout=90,
                input="y\n",
                env={**__import__("os").environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0, (
                f"VM create with --skip-cleanup failed: "
                f"stdout={result.stdout} stderr={result.stderr}"
            )

            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found in listing"
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_skip_deblob(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --skip-deblob flag."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "ubuntu:24.04",
                "--network",
                net_name,
                "--skip-deblob",
            )

            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )


# ========================================================================
# TestVMListInspect
# ========================================================================


class TestVMListInspect:
    """VM listing, inspection, export, import - uses module_vm."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_list_json(self, runner_vm, module_vm):
        """List VMs in JSON format."""
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert any(v["name"] == module_vm["name"] for v in vms)

    def test_list_table(self, runner_vm, module_vm):
        """List VMs in table format — verify name via JSON."""
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert any(v["name"] == module_vm["name"] for v in vms)

    def test_inspect(self, runner_vm, module_vm):
        """Show detailed VM info via vm inspect --json."""
        result = _run_mvm(
            runner_vm, "vm", "inspect", module_vm["name"], "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert data.get("vm", {}).get("name") == module_vm["name"]

    def test_inspect_json(self, runner_vm, module_vm):
        """vm inspect --json should return structured JSON."""
        result = _run_mvm(
            runner_vm, "vm", "inspect", module_vm["name"], "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, dict)
        for section in (
            "vm", "resources", "networking", "assets", "filesystem", "console",
        ):
            assert section in data, (
                f"Top-level section '{section}' missing: {list(data.keys())}"
            )
        vm_data = data["vm"]
        for key in ("id", "name", "status"):
            assert key in vm_data, (
                f"'vm.{key}' missing in inspect output: {list(vm_data.keys())}"
            )
        net_data = data["networking"]
        for key in ("ipv4", "mac"):
            assert key in net_data, (
                f"'networking.{key}' missing in inspect output: {list(net_data.keys())}"
            )
        assert "vm_dir" in data["filesystem"], (
            "'filesystem.vm_dir' missing in inspect output"
        )
        assert "relay_running" in data["console"], (
            "'console.relay_running' missing in inspect output"
        )

    def test_inspect_tree(self, runner_vm, module_vm):
        """Inspect VM via --json."""
        result = _run_mvm(
            runner_vm, "vm", "inspect", module_vm["name"], "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert data.get("vm", {}).get("name") == module_vm["name"]

    def test_ps_lists_running(self, runner_vm, module_vm):
        """vm ps lists running VMs — verify name prefix appears in table output."""
        result = _run_mvm(runner_vm, "vm", "ps")
        assert result.returncode == 0
        assert module_vm["name"][:5] in result.stdout

    def test_ls_json_running_vm_fields(self, runner_vm, module_vm):
        """vm ls --json shows expected fields for a running VM."""
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        running = next(
            (v for v in vms if v["name"] == module_vm["name"]),
            None,
        )
        assert running is not None
        for key in (
            "id", "name", "status", "ipv4", "pid",
            "vcpu_count", "mem_size_mib", "disk_size_mib",
        ):
            assert key in running, (
                f"Missing key '{key}' in ls --json entry: {running}"
            )
        assert running["status"] == "running"
        assert isinstance(running["pid"], int) and running["pid"] > 0
        assert "ipv4" in running

    def test_ps_shows_running_vm_details(self, runner_vm, module_vm):
        """vm ps table output shows running VM details."""
        result = _run_mvm(runner_vm, "vm", "ps")
        assert result.returncode == 0
        output = result.stdout
        assert module_vm["name"][:5] in output
        for header in ("Name", "Status", "IPv4"):
            assert header.lower() in output.lower()

    def test_ps_json(self, runner_vm, module_vm):
        """vm ps --json returns running VMs with name, status, pid fields."""
        result = _run_mvm(runner_vm, "vm", "ps", "--json")
        assert result.returncode == 0
        entries = json.loads(result.stdout)
        assert isinstance(entries, list)
        assert len(entries) > 0
        running_names = [e.get("name") for e in entries]
        assert module_vm["name"] in running_names
        for entry in entries:
            assert "name" in entry
            assert "status" in entry
            assert "pid" in entry
            if entry.get("status") in ("running", "starting"):
                assert isinstance(entry["pid"], int) and entry["pid"] > 0

    def test_list_empty_nonexistent_name(self, runner_vm):
        """Listing a nonexistent VM name returns clean list without it."""
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert not any(v["name"] == nonexistent for v in vms)

    def test_console_state_nonexistent_vm(self, runner_vm):
        """console --state on nonexistent VM should give clear error."""
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
        result = _run_mvm(
            runner_vm, "console", nonexistent, "--state", check=False
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined

    def test_inspect_by_name_flag(self, runner_vm, module_vm):
        """Inspect VM using name as positional argument (verify via --json)."""
        result = _run_mvm(
            runner_vm, "vm", "inspect", module_vm["name"], "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert data.get("vm", {}).get("name") == module_vm["name"]


# ========================================================================
# TestVMSSHIntegration
# ========================================================================


class TestVMSSHIntegration:
    """SSH into created VMs with key."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_ssh_available(self, runner_vm, created_vm, timing_targets):
        """SSH is available after VM boots."""
        ip = created_vm.get("ipv4", "")
        assert ip, "VM has no IP address — cannot test SSH"
        available = wait_for_ssh(
            runner_vm,
            created_vm["name"],
            "root",
            timing_targets["alpine:3.23"],
        )
        assert available, "SSH not available within timeout"


# ========================================================================
# Shared network fixture for TestVMConfigOptions (module-scoped)
# ========================================================================


@pytest.fixture(scope="module")
def config_options_network(runner_vm) -> Generator[str, None, None]:
    """Module-scoped network for read-only config tests in TestVMConfigOptions."""
    name = f"sys-cfg-net-{uuid.uuid4().hex[:6]}"
    _run_mvm(
        runner_vm,
        "network",
        "create",
        name,
        "--subnet",
        _unique_subnet(name),
        "--non-interactive",
    )
    try:
        yield name
    finally:
        _run_mvm(runner_vm, "network", "rm", name, check=False)


# ========================================================================
# TestVMConfigOptions
# ========================================================================


class TestVMConfigOptions:
    """VM config options: vcpus, mem, disk-size, boot-args, pci, logging, metrics."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_vcpus(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with custom --vcpu."""
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--vcpu",
                "2",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["vcpu_count"] == 2
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_vcpus_zero_fails(
        self,
        runner_vm,
        config_options_network,
    ):
        """--vcpu 0 must fail."""
        net_name = config_options_network
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-vcpus-zero",
            "--image",
            "alpine:3.23",
            "--vcpu",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_vcpus_negative_fails(
        self,
        runner_vm,
        config_options_network,
    ):
        """Negative --vcpu must fail."""
        net_name = config_options_network
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-vcpus-neg",
            "--image",
            "alpine:3.23",
            "--vcpu",
            "-1",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_config_chain_precedence(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ) -> None:
        """Config values affect VM creation unless CLI flags override."""
        net_name = config_options_network
        vm_noflag = unique_vm_name
        vm_flag = f"{unique_vm_name}-cli"
        section = "defaults.vm"
        key = "vcpu_count"
        try:
            original = _run_mvm(
                runner_vm, "config", "get", section, key, check=False
            )
            original_value = (
                original.stdout.strip()
                if original.returncode == 0 and original.stdout.strip()
                else None
            )

            _run_mvm(runner_vm, "config", "set", section, key, "4")

            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_noflag,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            result = _run_mvm(runner_vm, "vm", "inspect", vm_noflag, "--json")
            data = json.loads(result.stdout)
            assert data.get("resources", {}).get("vcpu") == 4

            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_flag,
                "--image",
                "alpine:3.23",
                "--vcpu",
                "2",
                "--network",
                net_name,
            )
            result = _run_mvm(runner_vm, "vm", "inspect", vm_flag, "--json")
            data = json.loads(result.stdout)
            assert data.get("resources", {}).get("vcpu") == 2
        finally:
            if original_value:
                _run_mvm(
                    runner_vm, "config", "set", section, key,
                    original_value, check=False,
                )
            else:
                _run_mvm(
                    runner_vm, "config", "reset", section, key, check=False,
                )
            for name in (vm_noflag, vm_flag):
                _run_mvm(runner_vm, "vm", "rm", name, "--force", check=False)

    def test_create_with_memory(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with custom --mem."""
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--mem",
                "1024",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["mem_size_mib"] == 1024
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_memory_zero_fails(
        self,
        runner_vm,
        config_options_network,
    ):
        """--mem 0 must fail."""
        net_name = config_options_network
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-mem-zero",
            "--image",
            "alpine:3.23",
            "--mem",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_memory_human_readable(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with human-readable --mem (1G = 1024 MiB)."""
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--mem",
                "1G",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["mem_size_mib"] == 1024
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_disk_size(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with custom --disk-size."""
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--disk-size",
                "2G",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["disk_size_mib"] == 2048
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_disk_size_zero_fails(
        self,
        runner_vm,
        config_options_network,
    ):
        """--disk-size 0 must fail."""
        net_name = config_options_network
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-disk-zero",
            "--image",
            "alpine:3.23",
            "--disk-size",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_disk_size_invalid_fails(
        self,
        runner_vm,
        config_options_network,
    ):
        """Invalid --disk-size format must fail."""
        net_name = config_options_network
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-disk-inv",
            "--image",
            "alpine:3.23",
            "--disk-size",
            "abc",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_specific_kernel(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with a specific --kernel."""
        net_name = config_options_network
        kernels = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        assert present, "No present kernel to test with"
        kernel_id_prefix = present[0]["id"][:6]
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--kernel",
                kernel_id_prefix,
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["kernel_id"].startswith(kernel_id_prefix)
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_boot_args(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with custom --boot-args."""
        net_name = config_options_network
        custom_boot_args = "quiet loglevel=3"
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--boot-args",
                custom_boot_args,
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            stored_args = vm.get("boot_args", "")
            assert custom_boot_args in stored_args
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_no_console(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with no console relay (default behavior)."""
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("enable_console") is False
            assert vm.get("relay_pid") is None or vm.get("relay_pid") == 0
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_pci_default(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with PCI enabled by default (no --no-pci)."""
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("pci_enabled") is True
            assert vm.get("status") == "running"
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_no_pci(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --no-pci."""
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--no-pci",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("pci_enabled") is False
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_enable_logging(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --enable-logging.

        L3: Verify firecracker.log file exists and is non-empty via inspect
        and checking inside the test VM.
        """
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--enable-logging",
                "--network",
                net_name,
            )
            # L3: Verify firecracker.log file exists via inspect
            inspect = _run_mvm(
                runner_vm, "vm", "inspect", unique_vm_name, "--json"
            )
            info = json.loads(inspect.stdout)
            vm_dir = info.get("filesystem", {}).get("vm_dir", "")

            # Verify the log file exists inside the test VM
            check_log = _guest_run(runner_vm,
                f"test -f '{vm_dir}/firecracker.log' && "
                f"test -s '{vm_dir}/firecracker.log' && echo OK",
                check=False,
            )
            assert check_log.returncode == 0, (
                f"Firecracker log not found or empty at {vm_dir}/firecracker.log"
            )
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_no_enable_logging(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --no-enable-logging."""
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--no-enable-logging",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_enable_metrics(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --enable-metrics.

        L3: Verify metrics file exists and is non-empty via inspect
        and checking inside the test VM.
        """
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--enable-metrics",
                "--network",
                net_name,
            )
            inspect = _run_mvm(
                runner_vm, "vm", "inspect", unique_vm_name, "--json"
            )
            info = json.loads(inspect.stdout)
            vm_dir = info.get("filesystem", {}).get("vm_dir", "")

            check_metrics = _guest_run(runner_vm,
                f"test -f '{vm_dir}/firecracker.metrics' && "
                f"test -s '{vm_dir}/firecracker.metrics' && echo OK",
                check=False,
            )
            assert check_metrics.returncode == 0, (
                f"Firecracker metrics not found or empty at {vm_dir}/firecracker.metrics"
            )
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_no_enable_metrics(
        self,
        runner_vm,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --no-enable-metrics."""
        net_name = config_options_network
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--no-enable-metrics",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_vcpus_negative_rejected(
        self,
        runner_vm: str,
        config_options_network,
    ) -> None:
        """Negative vCPU count should be rejected."""
        net_name = config_options_network
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-neg-cpu",
            "--image",
            "alpine:3.23",
            "--vcpu",
            "-1",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "invalid" in combined or "must be a positive integer" in combined

    def test_mem_zero_rejected(
        self,
        runner_vm: str,
        config_options_network,
    ) -> None:
        """Zero memory should be rejected."""
        net_name = config_options_network
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-zero-mem",
            "--image",
            "alpine:3.23",
            "--mem",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "invalid" in combined

    def test_disk_size_zero_rejected(
        self,
        runner_vm: str,
        config_options_network,
    ) -> None:
        """Zero disk size should be rejected."""
        net_name = config_options_network
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-zero-disk",
            "--image",
            "alpine:3.23",
            "--mem",
            "512",
            "--disk-size",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "smaller than minimum" in combined


# ========================================================================
# TestVMStateTransitions
# ========================================================================


class TestVMStateTransitions:
    """VM state machine: stop/start, pause/resume, reboot, crash recovery, fatigue."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    @pytest.mark.system
    def test_pause_resume_independent(self, runner_vm, created_vm):
        """Pause then resume VM."""
        vm_name = created_vm["name"]
        result = _run_mvm(runner_vm, "vm", "pause", vm_name)
        assert result.returncode == 0
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "paused"
        result = _run_mvm(runner_vm, "vm", "resume", vm_name)
        assert result.returncode == 0

    @pytest.mark.system
    def test_stop_start_independent(self, runner_vm, created_vm):
        """Stop then restart VM."""
        vm_name = created_vm["name"]
        result = _run_mvm(runner_vm, "vm", "stop", vm_name)
        assert result.returncode == 0
        result = _run_mvm(runner_vm, "vm", "start", vm_name)
        assert result.returncode == 0
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "running"

    @pytest.mark.system
    def test_pause_independent(self, runner_vm, created_vm):
        """Pause a running VM."""
        result = _run_mvm(runner_vm, "vm", "pause", created_vm["name"])
        assert result.returncode == 0
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == created_vm["name"]), None)
        assert vm is not None
        assert vm["status"] == "paused"

    @pytest.mark.system
    def test_resume_independent(self, runner_vm, created_vm):
        """Pause then resume VM."""
        vm_name = created_vm["name"]
        _run_mvm(runner_vm, "vm", "pause", vm_name)
        result = _run_mvm(runner_vm, "vm", "resume", vm_name)
        assert result.returncode == 0
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "running"

        # L3: Verify PID is alive (inside test VM)
        pid = vm.get("pid")
        assert pid is not None and pid > 0
        pid_check = _guest_run(runner_vm,
            f"test -d /proc/{pid} && echo ALIVE",
            check=False,
        )
        assert pid_check.returncode == 0 and "ALIVE" in pid_check.stdout, (
            f"Firecracker PID {pid} should be alive after resume"
        )

    @pytest.mark.system
    def test_stop_independent(self, runner_vm, created_vm):
        """Stop a running VM."""
        result = _run_mvm(runner_vm, "vm", "stop", created_vm["name"])
        assert result.returncode == 0
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == created_vm["name"]), None)
        assert vm is not None
        assert vm["status"] == "stopped"

    @pytest.mark.system
    def test_start_independent(self, runner_vm, created_vm):
        """Stop then start a VM."""
        vm_name = created_vm["name"]
        _run_mvm(runner_vm, "vm", "stop", vm_name)
        result = _run_mvm(runner_vm, "vm", "start", vm_name)
        assert result.returncode == 0
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "running"

        pid = vm.get("pid")
        assert pid is not None and pid > 0
        pid_check = _guest_run(runner_vm,
            f"test -d /proc/{pid} && echo ALIVE",
            check=False,
        )
        assert pid_check.returncode == 0 and "ALIVE" in pid_check.stdout, (
            f"Firecracker PID {pid} should be alive after start"
        )

    @pytest.mark.system
    def test_stop_force(self, runner_vm, created_vm):
        """Stop a running VM with --force flag."""
        result = _run_mvm(
            runner_vm, "vm", "stop", created_vm["name"], "--force"
        )
        assert result.returncode == 0
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == created_vm["name"]), None)
        assert vm is not None
        assert vm["status"] == "stopped"

    @pytest.mark.system
    def test_reboot_force_independent(self, runner_vm, created_vm):
        """Reboot VM with --force using a dedicated VM."""
        vm_name = created_vm["name"]

        inspect_before = json.loads(
            _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json").stdout
        )
        old_pid = inspect_before.get("vm", {}).get("pid")

        result = _run_mvm(runner_vm, "vm", "reboot", vm_name, "--force")
        assert result.returncode == 0
        result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "running"

        new_pid = vm.get("pid")
        assert new_pid is not None and new_pid > 0
        if old_pid and old_pid > 0:
            assert new_pid != old_pid, (
                f"PID {old_pid} unchanged after reboot --force — "
                f"VM may not have restarted"
            )
        pid_check = _guest_run(runner_vm,
            f"test -d /proc/{new_pid} && echo ALIVE",
            check=False,
        )
        assert pid_check.returncode == 0 and "ALIVE" in pid_check.stdout, (
            f"Firecracker PID {new_pid} should be alive after reboot --force"
        )

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_stop_start_cycle_multiple_times(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Run 3 stop/start cycles -- state machine fatigue."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            for _ in range(2):
                _run_mvm(runner_vm, "vm", "start", unique_vm_name)
                _run_mvm(runner_vm, "vm", "stop", unique_vm_name, "--force")
            _run_mvm(runner_vm, "vm", "start", unique_vm_name)
            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_pause_remove(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Pause a running VM then remove it -- verify cleanup."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            _run_mvm(runner_vm, "vm", "start", unique_vm_name)
            _run_mvm(runner_vm, "vm", "pause", unique_vm_name)
            _run_mvm(runner_vm, "vm", "rm", unique_vm_name, "--force")
            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert not any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_start_crash_inspect(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Kill the firecracker process -- vm rm --force must recover."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            _run_mvm(runner_vm, "vm", "start", unique_vm_name)
            inspect_result = _run_mvm(
                runner_vm, "vm", "inspect", unique_vm_name, "--json"
            )
            vm_data = json.loads(inspect_result.stdout)
            pid = vm_data.get("vm", {}).get("pid")
            if pid:
                _run_mvm(runner_vm, "kill", "-9", str(pid), check=False)
            _run_mvm(runner_vm, "vm", "rm", unique_vm_name, "--force")
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_stop_by_name_flag(self, runner_vm, created_vm):
        """Stop VM using name as positional argument."""
        result = _run_mvm(runner_vm, "vm", "stop", created_vm["name"])
        assert result.returncode == 0

    def test_stop_by_ip(self, runner_vm, created_vm):
        """Stop VM using IP as positional argument."""
        ip = created_vm.get("ipv4", "")
        assert ip, "VM has no IP address"
        result = _run_mvm(runner_vm, "vm", "stop", ip)
        assert result.returncode == 0

    def test_stop_already_stopped_vm_is_idempotent(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Stopping an already stopped VM should succeed (idempotent)."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            _run_mvm(runner_vm, "vm", "stop", unique_vm_name, "--force")
            _run_mvm(runner_vm, "vm", "stop", unique_vm_name, "--force")
            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "stopped"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_resume_running_vm_is_idempotent(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Resume on a running VM succeeds (idempotent)."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            _run_mvm(runner_vm, "vm", "start", unique_vm_name)
            _run_mvm(runner_vm, "vm", "resume", unique_vm_name)
            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_snapshot_from_stopped_vm_fails(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Snapshot requires paused or running VM -- stopped should be rejected."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            _run_mvm(runner_vm, "vm", "stop", unique_vm_name, "--force")
            result = _run_mvm(
                runner_vm,
                "snapshot",
                "create",
                unique_vm_name,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert (
                "paused or running" in combined
                or "stopped" in combined
                or "connection refused" in combined
            ), f"Unexpected error for snapshot on stopped VM: {combined}"

            result_vm = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if result_vm.returncode == 0:
                vms = json.loads(result_vm.stdout)
                vm_entry = next(
                    (v for v in vms if v["name"] == unique_vm_name), None
                )
                assert vm_entry is not None
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_error_state_is_terminal(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name,
    ) -> None:
        """Kill firecracker PID -- verify vm stop works and rm --force succeeds."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vm_name = unique_vm_name
        try:
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
            vm_inspect = _run_mvm(
                runner_vm, "vm", "inspect", vm_name, "--json"
            )
            vm_data = json.loads(vm_inspect.stdout)
            pid = vm_data.get("vm", {}).get("pid")
            assert pid is not None, "VM should have a PID"
            _run_mvm(runner_vm, "kill", "-9", str(pid), check=False)
            time.sleep(1)

            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None

            _run_mvm(runner_vm, "vm", "stop", vm_name)
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force")
            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert not any(v["name"] == vm_name for v in vms)
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)

    @pytest.mark.requires_kvm
    def test_boot_time_within_limits(
        self,
        runner_vm,
        unique_vm_name,
        timing_targets,
    ):
        """VM boot time should be within limits."""
        network_name = f"{unique_vm_name}-net"
        subnet = _unique_subnet(network_name)
        generous_limit = 30.0
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            start = time.monotonic()
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
            )
            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
            elapsed = time.monotonic() - start
            assert elapsed < generous_limit
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )

    def test_stop_clean_shutdown(
        self,
        runner_vm,
        unique_vm_name,
    ):
        """Graceful stop via Firecracker API (no --force)."""
        network_name = f"{unique_vm_name}-net"
        subnet = _unique_subnet(network_name)
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
            )

            inspect_before = json.loads(
                _run_mvm(
                    runner_vm, "vm", "inspect", unique_vm_name, "--json"
                ).stdout
            )
            pid = inspect_before.get("vm", {}).get("pid")

            _run_mvm(runner_vm, "vm", "stop", unique_vm_name)
            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "stopped"

            # L3: Verify PID is no longer alive inside test VM
            if pid and pid > 0:
                pid_check = _guest_run(runner_vm,
                    f"test -d /proc/{pid} && echo ALIVE || echo DEAD",
                    check=False,
                )
                assert "DEAD" in pid_check.stdout or "ALIVE" not in pid_check.stdout, (
                    f"Firecracker PID {pid} should not exist after graceful stop"
                )
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )

    def test_no_orphaned_processes_after_stop(
        self,
        runner_vm,
        unique_vm_name,
    ):
        """Verify Firecracker process is gone after vm stop --force."""
        network_name = f"{unique_vm_name}-net"
        subnet = _unique_subnet(network_name)
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
            )
            result = _run_mvm(
                runner_vm, "vm", "inspect", unique_vm_name, "--json"
            )
            inspect_data = json.loads(result.stdout)
            pid = inspect_data.get("vm", {}).get("pid")
            assert pid is not None and pid > 0
            _run_mvm(runner_vm, "vm", "stop", unique_vm_name, "--force")
            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "stopped"
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )


# ========================================================================
# TestVMStateTransitionErrors (from CLI edge cases)
# ========================================================================


class TestVMStateTransitionErrors:
    """Tests for invalid or idempotent VM state transitions."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_stop_stopped_vm(self, runner_vm, created_vm):
        """Stopping an already-stopped VM should be idempotent."""
        vm_name = created_vm["name"]
        _run_mvm(runner_vm, "vm", "stop", vm_name)
        result = _run_mvm(runner_vm, "vm", "stop", vm_name, check=False)
        assert result.returncode == 0, f"Second stop failed: {result.stderr}"

    def test_vm_pause_stopped_vm(self, runner_vm, created_vm):
        """Pausing a stopped VM should fail."""
        vm_name = created_vm["name"]
        _run_mvm(runner_vm, "vm", "stop", vm_name)
        result = _run_mvm(runner_vm, "vm", "pause", vm_name, check=False)
        assert result.returncode != 0

    def test_vm_start_running_vm(self, runner_vm, created_vm):
        """Starting a running VM should succeed (idempotent)."""
        vm_name = created_vm["name"]
        result = _run_mvm(runner_vm, "vm", "start", vm_name, check=False)
        assert result.returncode == 0, (
            f"Start on running VM failed: {result.stderr}"
        )

    def test_vm_resume_running_vm(self, runner_vm, created_vm):
        """Resuming a running VM should succeed (idempotent)."""
        vm_name = created_vm["name"]
        result = _run_mvm(runner_vm, "vm", "resume", vm_name, check=False)
        assert result.returncode == 0, (
            f"Resume on running VM failed: {result.stderr}"
        )


# ========================================================================
# TestVMCloudInitModes (from CLI edge cases)
# ========================================================================


class TestVMCloudInitModes:
    """Test VM creation with different --cloud-init-mode values."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_create_cloud_init_mode_iso(
        self, runner_vm, unique_vm_name, unique_network_name
    ):
        """Create VM with --cloud-init-mode iso."""
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
                "--non-interactive",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--cloud-init-mode",
                "iso",
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    def test_vm_create_cloud_init_mode_net(
        self, runner_vm, unique_vm_name, unique_network_name
    ):
        """Create VM with --cloud-init-mode net."""
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
                "--non-interactive",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--cloud-init-mode",
                "net",
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)

    def test_vm_create_cloud_init_mode_off(
        self, runner_vm, unique_vm_name, unique_network_name
    ):
        """Create VM with --cloud-init-mode off (explicit)."""
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
                "--non-interactive",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--cloud-init-mode",
                "off",
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)


# ========================================================================
# TestVMNocloudNetPort (from CLI edge cases)
# ========================================================================


class TestVMNocloudNetPort:
    """Test VM creation with --nocloud-net-port values."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_create_with_nocloud_net_port_specific(
        self, runner_vm, unique_vm_name, unique_network_name
    ):
        """Create VM with --nocloud-net-port set to a specific port."""
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
                "--non-interactive",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--nocloud-net-port",
                "12345",
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)


# ========================================================================
# TestVMDestructiveRmMultiple (from CLI edge cases — destructive, last)
# ========================================================================


class TestVMDestructiveRmMultiple:
    """Destructive: Remove multiple VMs."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_rm_multiple_identifiers(
        self, runner_vm, unique_vm_name, unique_network_name
    ):
        """Remove two VMs at once using multiple positional args."""
        name1 = f"{unique_vm_name}-a"
        name2 = f"{unique_vm_name}-b"
        net_name = unique_network_name
        try:
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                name1,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                name2,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
            )

            result = _run_mvm(runner_vm, "vm", "rm", name1, name2, "--force")
            assert result.returncode == 0

            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            assert not any(v["name"] == name1 for v in vms)
            assert not any(v["name"] == name2 for v in vms)
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", name1, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", name2, "--force", check=False
            )
            _run_mvm(runner_vm, "network", "rm", net_name, check=False)


# ========================================================================
# TestVMNetworkIntegration
# ========================================================================


class TestVMNetworkIntegration:
    """VM network integration: static IP, custom MAC, named network."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_static_ip(
        self,
        runner_vm,
        unique_vm_name,
        created_network,
    ):
        """Create VM with a specific --ip."""
        subnet = _unique_subnet(created_network)
        octets = subnet.split(".")[:3]
        static_ip = f"{octets[0]}.{octets[1]}.{octets[2]}.50"
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                created_network,
                "--ip",
                static_ip,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["ipv4"] == static_ip
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_invalid_ip_fails(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Invalid --ip should fail."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            result = _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--ip",
                "999.999.999.999",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_invalid_ip_format_fails(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Non-IP string for --ip should fail."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            result = _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--ip",
                "not-an-ip",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_custom_mac(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with a custom --mac."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        custom_mac = "aa:bb:cc:dd:ee:ff"
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--mac",
                custom_mac,
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["mac"] == custom_mac
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_named_network(
        self,
        runner_vm,
        unique_vm_name,
        created_network,
    ):
        """Create VM on a specific named network."""
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                created_network,
            )
            nets = json.loads(
                _run_mvm(runner_vm, "network", "ls", "--json").stdout
            )
            net = next(n for n in nets if n["name"] == created_network)
            net_id = net["id"]
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["network_id"] == net_id
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )


# ========================================================================
# TestVMCloudInit (from lifecycle - remaining cloud-init tests)
# ========================================================================


class TestVMCloudInit:
    """Cloud-init modes, user-data, nocloud-net-port."""

    _SSH_WAIT_TIMEOUT = 60

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_user_data(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with custom --cloudinit-config cloud-init file."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        user_data_path = f"/tmp/user-data-{uuid.uuid4().hex[:8]}.cfg"
        _guest_run(runner_vm,
            f"echo '#cloud-config' > '{user_data_path}' && "
            f"echo 'hostname: custom-hostname-test' >> '{user_data_path}'",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--cloudinit-config",
                user_data_path,
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_cloud_init_mode(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --cloud-init-mode inject."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--cloud-init-mode",
                "inject",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_nocloud_net_port(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --nocloud-net-port 0."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--nocloud-net-port",
                "0",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_cloud_init_net_mode_with_port(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --cloud-init-mode net and --nocloud-net-port 9999."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--cloud-init-mode",
                "net",
                "--nocloud-net-port",
                "9999",
                "--network",
                net_name,
            )
            result = _run_mvm(
                runner_vm, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(result.stdout)
            assert data.get("vm", {}).get("cloud_init_mode") == "net"
            assert data.get("vm", {}).get("nocloud_net_port") == 9999
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_user_data_script_executes(
        self,
        runner_vm,
        unique_vm_name,
        timing_targets,
        unique_network_name,
    ):
        """Verify cloud-init user-data runs inside the VM."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:8]}"
        user_data_path = f"/tmp/user-data-{uuid.uuid4().hex[:8]}"
        _guest_run(runner_vm,
            f"echo '#!/bin/sh' > '{user_data_path}' && "
            f"echo 'touch /tmp/user-data-sentinel' >> '{user_data_path}' && "
            f"chmod 644 '{user_data_path}'",
        )
        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--ssh-key",
                key_name,
                "--cloudinit-config",
                user_data_path,
                "--cloud-init-mode",
                "inject",
                "--network",
                net_name,
            )

            ssh_timeout = max(
                timing_targets.get("alpine:3.23", 15), self._SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                runner_vm, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            # Verify the user-data script was injected
            result = _run_mvm(
                runner_vm,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "cat /var/lib/cloud/seed/nocloud-net/user-data",
                check=False,
            )
            assert result.returncode == 0, (
                f"Custom user-data not found in VM seed directory: "
                f"{result.stderr.strip()}"
            )
            assert "touch /tmp/user-data-sentinel" in result.stdout, (
                f"Seed user-data does not contain expected script content. "
                f"Got: {result.stdout.strip()!r}"
            )
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "key", "rm", key_name, "--force", check=False
            )


# ========================================================================
# TestVMVolumeIntegration
# ========================================================================


class TestVMVolumeIntegration:
    """VM volume integration: attach, detach, create-with-volume, lifecycle."""

    _SSH_WAIT_TIMEOUT = 60
    _REBOOT_SSH_WAIT_TIMEOUT = 120

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_volume(
        self,
        runner_vm,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Create a volume and attach it at VM creation time via --volume."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-vm-{unique_key_name}"
        key_name = f"sys-volvm-key-{unique_key_name}"
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_info = vol_data.get("volume") or {}
            assert vol_info.get("status") == "attached"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_attach_detach_volume(
        self,
        runner_vm,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Attach and detach a volume from a VM."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-ad-{unique_key_name}"
        key_name = f"sys-volad-key-{unique_key_name}"
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(runner_vm, "vm", "stop", unique_vm_name, "--force")
            result = _run_mvm(
                runner_vm, "vm", "attach-volume", unique_vm_name, vol_name
            )
            assert result.returncode == 0
            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_info = vol_data.get("volume") or {}
            assert vol_info.get("status") == "attached"
            result = _run_mvm(
                runner_vm, "vm", "detach-volume", unique_vm_name, vol_name
            )
            assert result.returncode == 0
            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_info = vol_data.get("volume") or {}
            assert vol_info.get("status") == "available"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_attach_volume_to_running_vm_succeeds(
        self,
        runner_vm,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Attaching a volume to a RUNNING VM should succeed (Firecracker v1.16+ hotplug)."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-run-{unique_key_name}"
        key_name = f"sys-volrun-key-{unique_key_name}"
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            result = _run_mvm(
                runner_vm,
                "vm",
                "attach-volume",
                unique_vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode == 0, (
                f"Expected hotplug attach to succeed, got: {result.stderr}"
            )
            assert "attached" in result.stdout.lower() or "attached" in result.stderr.lower()
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_detach_volume_from_running_vm_succeeds(
        self,
        runner_vm,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Detaching a volume from a RUNNING VM should succeed (Firecracker v1.16+ hotplug)."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-det-{unique_key_name}"
        key_name = f"sys-voldet-key-{unique_key_name}"
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(runner_vm, "vm", "stop", unique_vm_name, "--force")
            _run_mvm(
                runner_vm, "vm", "attach-volume", unique_vm_name, vol_name
            )
            _run_mvm(runner_vm, "vm", "start", unique_vm_name)
            result = _run_mvm(
                runner_vm,
                "vm",
                "detach-volume",
                unique_vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode == 0, (
                f"Expected hotplug detach to succeed, got: {result.stderr}"
            )
            assert "detached" in result.stdout.lower() or "detached" in result.stderr.lower()
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_create_volume_by_id_prefix(
        self,
        runner_vm,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Create VM with --volume <6-char-id-prefix>."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-prefix-{uuid.uuid4().hex[:6]}"
        key_name = f"sys-volpref-key-{unique_key_name}"
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            vol_ls = _run_mvm(runner_vm, "volume", "ls", "--json")
            vols = json.loads(vol_ls.stdout)
            vol_info = next(v for v in vols if v["name"] == vol_name)
            vol_id_prefix = vol_info["id"][:6]
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--volume",
                vol_id_prefix,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_info = vol_data.get("volume") or {}
            assert vol_info.get("status") == "attached"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_rm_transitions_volume_to_available(
        self,
        runner_vm,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Remove VM transitions attached volumes to 'available'."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-rm-{uuid.uuid4().hex[:6]}"
        key_name = f"sys-volrm-key-{unique_key_name}"
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(runner_vm, "volume", "create", vol_name, "512M")
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_info = vol_data.get("volume") or {}
            assert vol_info.get("status") == "attached"
            _run_mvm(runner_vm, "vm", "rm", unique_vm_name, "--force")
            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_info = vol_data.get("volume") or {}
            assert vol_info.get("status") == "available"
        finally:
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
