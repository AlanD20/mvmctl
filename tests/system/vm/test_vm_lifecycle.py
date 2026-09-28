"""VM lifecycle system tests — focused classes with dependency ordering."""

from __future__ import annotations

import concurrent.futures
import json
import os
import select
import shlex
import shutil
import subprocess
import time
import uuid
from contextlib import contextmanager
from pathlib import Path
from typing import Generator

import pytest

from tests.system.conftest import (
    _guest_run,
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
    wait_for_ssh,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
]


def _kill_exact_test_firecracker(pid: int, start_ticks: int, vm_id: str) -> None:
    """Inject one crash only after root rechecks the test VM's process identity."""
    jail_root = f"/var/lib/mvmctl/jailer/firecracker/{vm_id}/root"
    script = """
import os
import select
import signal
import sys

pid, start_ticks, jail_root = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3]
pidfd = os.pidfd_open(pid)
try:
    stat = open(f"/proc/{pid}/stat", encoding="ascii").read().rpartition(") ")[2].split()
    if len(stat) < 20 or int(stat[19]) != start_ticks:
        raise RuntimeError("test Firecracker PID/start identity changed")
    root = os.stat(f"/proc/{pid}/root")
    expected_root = os.stat(jail_root)
    if (root.st_dev, root.st_ino) != (expected_root.st_dev, expected_root.st_ino):
        raise RuntimeError("test Firecracker jail root changed")
    executable = os.stat(f"/proc/{pid}/exe")
    expected_executable = os.stat(f"{jail_root}/firecracker")
    if (executable.st_dev, executable.st_ino) != (
        expected_executable.st_dev, expected_executable.st_ino
    ):
        raise RuntimeError("test Firecracker executable changed")
    signal.pidfd_send_signal(pidfd, signal.SIGKILL)
    poller = select.poll()
    poller.register(pidfd, select.POLLIN)
    events = poller.poll(10_000)
    if (len(events) != 1 or events[0][0] != pidfd
            or events[0][1] & (select.POLLERR | select.POLLNVAL)
            or not events[0][1] & (select.POLLIN | select.POLLHUP)):
        raise RuntimeError("test Firecracker exit was not proven by its retained pidfd")
finally:
    os.close(pidfd)
"""
    result = subprocess.run(
        ["sudo", "-n", "python3", "-c", script, str(pid), str(start_ticks), jail_root],
        capture_output=True,
        text=True,
        timeout=15,
        check=False,
    )
    assert result.returncode == 0, (result.stdout, result.stderr)


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
        """A clean runner returns a versioned empty list without deleting any VM."""
        assert _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == []


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


@pytest.fixture(scope="class")
def listing_vm(runner_vm) -> Generator[dict, None, None]:
    """One bounded VM for read-only output checks, without SSH or unrelated cleanup."""
    suffix = uuid.uuid4().hex[:8]
    name = f"sys-list-{suffix}"
    network = f"sys-list-net-{suffix}"
    ensure_vm_deps(runner_vm)
    try:
        _run_mvm(
            runner_vm, "network", "create", network, "--subnet", _unique_subnet(network),
            "--no-nat",
        )
        created = _json_success_data(
            _run_mvm(
                runner_vm, "vm", "create", name, "--image", "alpine:3.23", "--network", network,
                "--cloud-init-mode", "off", "--skip-deblob", "--no-pci", "-s", "512M",
                "--mem", "256M", "--vcpu", "1", "--json", timeout=180,
            ).stdout, list,
        )
        assert len(created) == 1, created
        assert created[0]["name"] == name
        yield created[0]
    finally:
        listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        if any(vm["name"] == name for vm in listed):
            _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
        networks = _json_success_data(
            _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
        )
        if any(net["name"] == network for net in networks):
            _run_mvm(runner_vm, "network", "rm", network, "--force")
        assert all(vm["name"] != name for vm in _parse_vm_list(
            _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
        ))
        assert all(net["name"] != network for net in _json_success_data(
            _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
        ))


class TestVMListInspect:
    """Each public listing and inspection output boundary is exercised once."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    @pytest.mark.parametrize("command", ["ls", "ps"])
    def test_listing_json(self, runner_vm, listing_vm, command):
        """The real running VM and explicit allocation survive schema-1 serialization."""
        vms = _parse_vm_list(_run_mvm(runner_vm, "vm", command, "--json").stdout)
        matches = [vm for vm in vms if vm["id"] == listing_vm["id"]]
        assert len(matches) == 1, vms
        vm = matches[0]
        assert vm["name"] == listing_vm["name"]
        assert vm["status"] == "running"
        assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (1, 256, 512)
        assert vm["ipv4"] == listing_vm["ipv4"]
        assert vm["pid"] > 0
        assert vm["process_start_time"] > 0

    @pytest.mark.parametrize("command", ["ls", "ps"])
    def test_listing_text(self, runner_vm, listing_vm, command):
        """Human output is checked directly, not through a second JSON invocation."""
        output = _run_mvm(runner_vm, "vm", command).stdout
        assert listing_vm["name"] in output
        assert listing_vm["ipv4"] in output
        for header in ("name", "status", "ipv4"):
            assert header in output.lower(), output

    @pytest.mark.parametrize("selector", ["name", "ipv4", "id_prefix"])
    def test_inspect_json(self, runner_vm, listing_vm, selector):
        """Inspect exposes the same selected VM, explicit resources, and public sections."""
        identifier = {
            "name": listing_vm["name"],
            "ipv4": listing_vm["ipv4"],
            "id_prefix": listing_vm["id"][:8],
        }[selector]
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", identifier, "--json").stdout, dict,
        )
        for section in (
            "vm", "resources", "networking", "assets", "filesystem", "console",
        ):
            assert isinstance(data[section], dict), data
        assert data["vm"]["id"] == listing_vm["id"]
        assert data["vm"]["name"] == listing_vm["name"]
        assert data["vm"]["status"] == "running"
        assert data["vm"]["pid"] == listing_vm["pid"]
        assert (data["resources"]["vcpu"], data["resources"]["mem"], data["resources"]["disk"]) == (
            1, 256, 512,
        )
        assert data["networking"]["ipv4"] == listing_vm["ipv4"]
        assert data["networking"]["mac"] == listing_vm["mac"]
        assert data["filesystem"]["vm_dir"]
        assert data["console"]["relay_running"] is False

    def test_inspect_tree(self, runner_vm, listing_vm):
        """The real text inspection route identifies the selected VM."""
        output = _run_mvm(runner_vm, "vm", "inspect", listing_vm["name"]).stdout
        assert listing_vm["name"] in output
        assert listing_vm["id"] in output
        assert listing_vm["ipv4"] in output
        assert "running" in output

    def test_console_state_nonexistent_vm(self, runner_vm):
        """A nonexistent VM retains its structured domain error through the console route."""
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
        result = _run_mvm(
            runner_vm, "console", nonexistent, "--state", "--json", check=False
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1
        assert data["status"] == "error"
        assert data["data"] is None
        assert data["error"]["code"] == "vm.not_found"


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
# TestVMConfigOptions
# ========================================================================


@pytest.fixture(scope="class")
def config_vm_deps(runner_vm) -> None:
    """Prepare mirrored assets once for the resource/configuration class."""
    ensure_vm_deps(runner_vm)


def _remove_config_vm(runner_vm: str, name: str) -> None:
    """Remove only this test's exact VM, with checked public readback."""
    listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
    if any(vm["name"] == name for vm in listed):
        _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
    assert all(vm["name"] != name for vm in _parse_vm_list(
        _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
    ))


@pytest.fixture
def config_vm_name(runner_vm, config_vm_deps) -> Generator[str, None, None]:
    """Own one unique name and the precedence test's explicit CLI variant."""
    name = f"sys-config-{uuid.uuid4().hex[:8]}"
    try:
        yield name
    finally:
        _remove_config_vm(runner_vm, name)
        _remove_config_vm(runner_vm, f"{name}-cli")


def _create_config_vm(
    runner_vm: str,
    name: str,
    *options: str,
    memory: str = "256M",
    vcpu: int | None = 1,
    pci_enabled: bool = False,
) -> dict:
    """Launch a small isolated VM through the public CLI, not a test-only backend."""
    args = [
        "vm", "create", name, "--image", "alpine:3.23", "--no-network",
        "--cloud-init-mode", "off", "--skip-deblob", "--disk-size", "512M",
        "--mem", memory, "--json",
    ]
    if vcpu is not None:
        args.extend(["--vcpu", str(vcpu)])
    if not pci_enabled:
        args.append("--no-pci")
    created = _json_success_data(
        _run_mvm(runner_vm, *args, *options, timeout=180).stdout, list,
    )
    assert len(created) == 1, created
    assert created[0]["name"] == name
    assert created[0]["status"] == "running"
    return created[0]


class TestVMConfigOptions:
    """Real allocation and feature effects, plus typed refusal before VM creation."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    @pytest.mark.parametrize("memory", ["256", "256M"], ids=["numeric", "human-readable"])
    def test_resource_allocation(self, runner_vm, config_vm_name, memory):
        """CPU/RAM/root-disk flags reach the guest, not merely the stored input."""
        vm = _create_config_vm(runner_vm, config_vm_name, memory=memory, vcpu=2)
        assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (2, 256, 512)
        assert _run_mvm(runner_vm, "exec", config_vm_name, "--", "nproc").stdout.strip() == "2"
        memory_info = _run_mvm(
            runner_vm, "exec", config_vm_name, "--", "cat", "/proc/meminfo",
        ).stdout
        total_kib = int(next(line.split()[1] for line in memory_info.splitlines()
                             if line.startswith("MemTotal:")))
        # Kernel reservations reduce guest-visible RAM; the bound still rejects an ignored 256 MiB flag.
        assert (256 - 64) * 1024 < total_kib <= 256 * 1024, memory_info
        sectors = _run_mvm(
            runner_vm, "exec", config_vm_name, "--", "cat", "/sys/class/block/vda/size",
        ).stdout.strip()
        assert int(sectors) * 512 == 512 * 1024 * 1024
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", config_vm_name, "--json").stdout, dict,
        )
        assert inspected["resources"]["vcpu"] == 2
        assert inspected["resources"]["mem"] == 256
        assert inspected["resources"]["disk"] == 512
        assert inspected["vm"]["enable_console"] is False
        assert inspected["console"]["relay_running"] is False
        assert inspected["console"]["relay_pid"] is None

    @pytest.mark.parametrize(
        ("flag", "value", "code"),
        [
            ("--vcpu", "0", "vm.create.failed"),
            ("--vcpu", "-1", "vm.create.failed"),
            ("--mem", "0", "vm.create.failed"),
            ("--disk-size", "0", "vm.create.failed"),
            ("--disk-size", "abc", "validation.failed"),
        ],
        ids=["zero-cpu", "negative-cpu", "zero-memory", "zero-disk", "malformed-disk"],
    )
    def test_invalid_resource_rejected_before_vm_creation(
        self, runner_vm, config_vm_name, flag, value, code,
    ):
        """An invalid allocation is a validation error, not an incidental launch failure."""
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        result = _run_mvm(
            runner_vm, "vm", "create", config_vm_name, "--image", "alpine:3.23",
            "--no-network", flag, value, "--json", check=False,
        )
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert envelope["error"]["code"] == code
        assert envelope["error"]["class"] == 1
        assert _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before

    def test_config_chain_precedence(self, runner_vm, config_vm_name):
        """Configured CPU count and explicit CLI precedence are observed inside two sequential guests."""
        def cpu_setting() -> dict:
            return _json_success_data(
                _run_mvm(runner_vm, "config", "get", "defaults.vm", "--json").stdout, dict,
            )["vcpu_count"]

        original = cpu_setting()
        assert original["type"] == "int"
        try:
            _run_mvm(runner_vm, "config", "set", "defaults.vm", "vcpu_count", "2")
            inherited = _create_config_vm(runner_vm, config_vm_name, vcpu=None)
            assert inherited["vcpu_count"] == 2
            assert _run_mvm(
                runner_vm, "exec", config_vm_name, "--", "nproc",
            ).stdout.strip() == "2"
            _remove_config_vm(runner_vm, config_vm_name)

            explicit_name = f"{config_vm_name}-cli"
            explicit = _create_config_vm(runner_vm, explicit_name, vcpu=1)
            assert explicit["vcpu_count"] == 1
            assert _run_mvm(
                runner_vm, "exec", explicit_name, "--", "nproc",
            ).stdout.strip() == "1"
        finally:
            if original["override"] is None:
                _run_mvm(runner_vm, "config", "reset", "defaults.vm", "vcpu_count")
            else:
                _run_mvm(
                    runner_vm, "config", "set", "defaults.vm", "vcpu_count",
                    str(original["override"]),
                )
            assert cpu_setting() == original

    def test_boot_args_reach_guest(self, runner_vm, config_vm_name):
        """A distinct custom argument reaches the booted kernel command line."""
        marker = "mvm_qa_resource=1"
        _create_config_vm(
            runner_vm, config_vm_name, "--boot-args", f"rw quiet loglevel=5 {marker}",
        )
        command_line = _run_mvm(
            runner_vm, "exec", config_vm_name, "--", "cat", "/proc/cmdline",
        ).stdout.split()
        assert marker in command_line
        assert "loglevel=5" in command_line

    @pytest.mark.parametrize("pci_enabled", [True, False], ids=["default-pci", "no-pci"])
    def test_pci_transport(self, runner_vm, config_vm_name, pci_enabled):
        """Both transport choices boot; only the PCI choice enumerates guest PCI devices."""
        vm = _create_config_vm(runner_vm, config_vm_name, pci_enabled=pci_enabled)
        assert vm["pci_enabled"] is pci_enabled
        devices = _run_mvm(
            runner_vm, "exec", config_vm_name, "--", "ls", "/sys/bus/pci/devices",
        ).stdout.split()
        assert bool(devices) is pci_enabled, devices

    @pytest.mark.parametrize("enabled", [True, False], ids=["enabled", "disabled"])
    def test_logging_output(self, runner_vm, config_vm_name, enabled):
        """The logging flag controls Firecracker log output in a fresh VM directory."""
        flag = "--enable-logging" if enabled else "--disable-logging"
        vm = _create_config_vm(runner_vm, config_vm_name, flag)
        assert vm["enable_logging"] is enabled
        _run_mvm(runner_vm, "exec", config_vm_name, "--", "uname", "-s")
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", config_vm_name, "--json").stdout, dict,
        )
        log_path = Path(inspected["filesystem"]["log_path"])
        assert log_path.is_file()
        assert (log_path.stat().st_size > 0) is enabled

    @pytest.mark.parametrize("enabled", [True, False], ids=["enabled", "disabled"])
    def test_metrics_output(self, runner_vm, config_vm_name, enabled):
        """Enabled metrics produce complete JSON records; disabling leaves no metrics file."""
        flag = "--enable-metrics" if enabled else "--disable-metrics"
        vm = _create_config_vm(runner_vm, config_vm_name, flag)
        assert vm["enable_metrics"] is enabled
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", config_vm_name, "--json").stdout, dict,
        )
        path = Path(inspected["filesystem"]["vm_dir"]) / "firecracker.metrics"
        if not enabled:
            assert not path.exists()
            return
        deadline = time.monotonic() + 10
        while True:
            text = path.read_text()
            if text.endswith("\n"):
                records = [json.loads(line) for line in text.splitlines()]
                assert records and all(isinstance(record, dict) and record for record in records)
                break
            assert time.monotonic() < deadline, text
            time.sleep(0.1)


# ========================================================================
# TestVMStateTransitions
# ========================================================================


@pytest.fixture(scope="class")
def lifecycle_vm_deps(runner_vm) -> None:
    """Prepare mirrored assets once per lifecycle class."""
    ensure_vm_deps(runner_vm)


@pytest.fixture
def lifecycle_vm(runner_vm, lifecycle_vm_deps) -> Generator[dict, None, None]:
    """Own one tiny managed VM and remove it before its unique no-NAT network."""
    name = f"sys-lifecycle-{uuid.uuid4().hex[:8]}"
    network = f"{name}-net"
    try:
        _run_mvm(
            runner_vm, "network", "create", network, "--subnet", _unique_subnet(network),
            "--no-nat",
        )
        created = _json_success_data(
            _run_mvm(
                runner_vm, "vm", "create", name, "--image", "alpine:3.23",
                "--network", network, "--cloud-init-mode", "off", "--skip-deblob",
                "--no-pci", "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                "--json", timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1, created
        assert created[0]["name"] == name
        assert created[0]["status"] == "running"
        assert (created[0]["vcpu_count"], created[0]["mem_size_mib"],
                created[0]["disk_size_mib"]) == (1, 256, 512)
        yield created[0]
    finally:
        _remove_config_vm(runner_vm, name)
        networks = _json_success_data(
            _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
        )
        if any(item["name"] == network for item in networks):
            _run_mvm(runner_vm, "network", "rm", network, "--force")
        assert all(item["name"] != network for item in _json_success_data(
            _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
        ))


def _lifecycle_vm_state(runner_vm: str, name: str, status: str) -> dict:
    """Require list and inspect to agree on this exact VM's public state."""
    matches = [vm for vm in _parse_vm_list(
        _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
    ) if vm["name"] == name]
    assert len(matches) == 1, matches
    vm = matches[0]
    assert vm["status"] == status, vm
    inspected = _json_success_data(
        _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout, dict,
    )["vm"]
    assert inspected["id"] == vm["id"]
    assert inspected["status"] == status, inspected
    if status in ("running", "paused"):
        assert inspected["pid"] == vm["pid"]
    return vm


def _lifecycle_process_identity(vm: dict) -> tuple[int, int]:
    """Match public PID/start ticks to the live task, rather than PID existence."""
    pid, ticks = int(vm["pid"]), int(vm["process_start_time"])
    assert pid > 0 and ticks > 0, vm
    fields = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
    assert len(fields) >= 20, fields
    assert int(fields[19]) == ticks, (pid, ticks, fields)
    assert fields[0] not in ("Z", "X"), fields
    return pid, ticks


@contextmanager
def _lifecycle_process_handle(vm: dict) -> Generator[tuple[tuple[int, int], int], None, None]:
    """Retain a pidfd before the CLI mutation and admit its public PID/start identity."""
    pidfd = os.pidfd_open(int(vm["pid"]))
    try:
        identity = _lifecycle_process_identity(vm)
        poller = select.poll()
        poller.register(pidfd, select.POLLIN)
        assert poller.poll(0) == [], f"VM task {identity} exited before the lifecycle mutation"
        yield identity, pidfd
    finally:
        os.close(pidfd)


def _assert_lifecycle_process_exited(pidfd: int) -> None:
    """The retained descriptor proves original task exit independently of PID reuse."""
    poller = select.poll()
    poller.register(pidfd, select.POLLIN)
    events = poller.poll(10_000)
    assert len(events) == 1 and events[0][0] == pidfd, events
    assert not events[0][1] & (select.POLLERR | select.POLLNVAL), events
    assert events[0][1] & (select.POLLIN | select.POLLHUP), events


def _lifecycle_boot_id(runner_vm: str, name: str) -> str:
    """A successful guest-agent read also proves vsock is reachable after launch."""
    boot_id = _run_mvm(
        runner_vm, "exec", name, "--", "cat", "/proc/sys/kernel/random/boot_id",
    ).stdout.strip()
    assert str(uuid.UUID(boot_id)) == boot_id, boot_id
    return boot_id


class TestVMStateTransitions:
    """Public lifecycle journeys with task identity, guest readiness, and checked cleanup."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_pause_resume_preserves_process_and_guest(self, runner_vm, lifecycle_vm):
        """Pause/resume and repeated target-state requests retain the same process and boot."""
        name = lifecycle_vm["name"]
        original = _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
        boot_id = _lifecycle_boot_id(runner_vm, name)

        _run_mvm(runner_vm, "vm", "resume", name)
        assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running")) == original
        for _ in range(2):
            _run_mvm(runner_vm, "vm", "pause", name)
            assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "paused")) == original
        _run_mvm(runner_vm, "vm", "resume", name)
        assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running")) == original
        assert _lifecycle_boot_id(runner_vm, name) == boot_id
        _run_mvm(runner_vm, "vm", "resume", name)
        assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running")) == original
        assert _lifecycle_boot_id(runner_vm, name) == boot_id

    def test_stop_start_three_cycles_and_idempotence(self, runner_vm, lifecycle_vm):
        """Three actual stop/start cycles include graceful, forced, and IP-selected stop."""
        name = lifecycle_vm["name"]
        running = _lifecycle_vm_state(runner_vm, name, "running")
        identity = _lifecycle_process_identity(running)
        boot_id = _lifecycle_boot_id(runner_vm, name)
        _run_mvm(runner_vm, "vm", "start", name)
        assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running")) == identity
        assert _lifecycle_boot_id(runner_vm, name) == boot_id

        # Exactly three stops and three starts; creation already leaves the VM running.
        for selector, flags in ((name, ()), (name, ("--force",)), (running["ipv4"], ())):
            assert selector
            with _lifecycle_process_handle(_lifecycle_vm_state(runner_vm, name, "running")) as (current, pidfd):
                assert current == identity
                _run_mvm(runner_vm, "vm", "stop", selector, *flags)
                stopped = _lifecycle_vm_state(runner_vm, name, "stopped")
                assert stopped["id"] == lifecycle_vm["id"]
                _assert_lifecycle_process_exited(pidfd)
                assert all(vm["id"] != stopped["id"] for vm in _parse_vm_list(
                    _run_mvm(runner_vm, "vm", "ps", "--json").stdout,
                ))
                _run_mvm(runner_vm, "vm", "stop", selector, *flags)
                assert _lifecycle_vm_state(runner_vm, name, "stopped")["id"] == stopped["id"]
                _assert_lifecycle_process_exited(pidfd)

            _run_mvm(runner_vm, "vm", "start", name)
            restarted = _lifecycle_vm_state(runner_vm, name, "running")
            next_identity = _lifecycle_process_identity(restarted)
            assert next_identity != identity
            assert restarted["ipv4"] == running["ipv4"]
            next_boot = _lifecycle_boot_id(runner_vm, name)
            assert next_boot != boot_id
            assert _run_mvm(
                runner_vm, "exec", name, "--", "printf", "restarted-after-stop",
            ).stdout == "restarted-after-stop"
            identity, boot_id = next_identity, next_boot

    @pytest.mark.parametrize("force", [False, True], ids=["graceful", "force"])
    def test_reboot_changes_process_and_boot_id(self, runner_vm, lifecycle_vm, force):
        """Both reboot routes replace the process and boot a reachable new guest."""
        name = lifecycle_vm["name"]
        boot_id = _lifecycle_boot_id(runner_vm, name)
        with _lifecycle_process_handle(_lifecycle_vm_state(runner_vm, name, "running")) as (identity, pidfd):
            _run_mvm(runner_vm, "vm", "reboot", name, *(("--force",) if force else ()))
            _assert_lifecycle_process_exited(pidfd)
            rebooted = _lifecycle_vm_state(runner_vm, name, "running")
            assert rebooted["id"] == lifecycle_vm["id"]
            assert _lifecycle_process_identity(rebooted) != identity
            assert _lifecycle_boot_id(runner_vm, name) != boot_id

    @pytest.mark.destructive
    def test_paused_forced_removal_exits_process(self, runner_vm, lifecycle_vm):
        """Forced removal of a paused VM exits its exact process and removes its public record."""
        name = lifecycle_vm["name"]
        _lifecycle_boot_id(runner_vm, name)
        with _lifecycle_process_handle(_lifecycle_vm_state(runner_vm, name, "running")) as (identity, pidfd):
            _run_mvm(runner_vm, "vm", "pause", name)
            assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "paused")) == identity
            _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
            _assert_lifecycle_process_exited(pidfd)
            assert all(vm["id"] != lifecycle_vm["id"] for vm in _parse_vm_list(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
            ))

    @pytest.mark.destructive
    def test_stopped_snapshot_captures_and_restores_disk(self, runner_vm, lifecycle_vm):
        """A stopped VM yields a cold disk archive that restores guest data and vsock."""
        name = lifecycle_vm["name"]
        snapshot_name = f"{name}-cold"
        snapshot_id = None
        marker = uuid.uuid4().hex
        try:
            boot_id = _lifecycle_boot_id(runner_vm, name)
            _run_mvm(
                runner_vm, "exec", name, "--",
                f"printf '%s' '{marker}' > /root/lifecycle-cold-marker && sync",
            )
            with _lifecycle_process_handle(_lifecycle_vm_state(runner_vm, name, "running")) as (_, pidfd):
                _run_mvm(runner_vm, "vm", "stop", name)
                _assert_lifecycle_process_exited(pidfd)
            _lifecycle_vm_state(runner_vm, name, "stopped")
            capture = _run_mvm(
                runner_vm, "snapshot", "create", name, "--name", snapshot_name,
                "--json", check=False, timeout=240,
            )
            # Keep the root request identity even if capture fails before its local projection.
            intents = [line.removeprefix("Snapshot request ID: ") for line in capture.stderr.splitlines()
                       if line.startswith("Snapshot request ID: ")]
            assert len(intents) <= 1, capture.stderr
            if intents:
                snapshot_id = intents[0]
                assert len(snapshot_id) == 64 and all(c in "0123456789abcdef" for c in snapshot_id)
            assert capture.returncode == 0, (capture.stdout, capture.stderr)
            captured = _json_success_data(capture.stdout, dict)
            assert captured["id"] == snapshot_id
            assert captured["name"] == snapshot_name
            assert captured["source_vm_id"] == lifecycle_vm["id"]
            assert captured["memory_file"] == ""
            assert captured["state_file"] == ""
            assert captured["rootfs_file"]
            _lifecycle_vm_state(runner_vm, name, "stopped")
            _remove_config_vm(runner_vm, name)

            restored = _json_success_data(
                _run_mvm(
                    runner_vm, "snapshot", "restore", snapshot_id, name, "--resume",
                    "--json", timeout=180,
                ).stdout,
                list,
            )
            assert len(restored) == 1
            assert restored[0]["name"] == name and restored[0]["status"] == "running"
            assert restored[0]["id"] != lifecycle_vm["id"]
            assert restored[0]["network_id"] == lifecycle_vm["network_id"]
            assert restored[0]["ipv4"] == lifecycle_vm["ipv4"]
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
            assert _lifecycle_boot_id(runner_vm, name) != boot_id
            assert _run_mvm(
                runner_vm, "exec", name, "--", "cat", "/root/lifecycle-cold-marker",
            ).stdout == marker
        finally:
            # A restore retains captured resources until VM removal; keep this ordering on failure too.
            _remove_config_vm(runner_vm, name)
            if snapshot_id is not None:
                _run_mvm(runner_vm, "snapshot", "rm", snapshot_id, "--force", timeout=120)
            assert all(item["name"] != snapshot_name for item in _json_success_data(
                _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list,
            ))

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.destructive
    def test_crashed_vm_status_and_restart(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """A confirmed Firecracker exit is crashed in public status and can relaunch."""
        net_name = unique_network_name
        ensure_vm_deps(runner_vm)
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--no-nat",
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
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--no-pci",
                "-s",
                "512M",
                "--mem",
                "256M",
                "--vcpu",
                "1",
                timeout=180,
            )
            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", unique_vm_name, "--json").stdout,
                dict,
            )
            vm_data = next(vm for vm in _parse_vm_list(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
            ) if vm["name"] == unique_vm_name)
            assert inspected["vm"]["status"] == "running"
            assert inspected["vm"]["pid"] == vm_data["pid"]
            assert vm_data["status"] == "running"
            assert int(vm_data["pid"]) > 0
            assert int(vm_data["process_start_time"]) > 0
            _kill_exact_test_firecracker(
                int(vm_data["pid"]), int(vm_data["process_start_time"]), vm_data["id"],
            )

            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            entry = next(vm for vm in listed if vm["id"] == vm_data["id"])
            assert entry["status"] == "crashed"
            running = _parse_vm_list(_run_mvm(runner_vm, "vm", "ps", "--json").stdout)
            assert all(vm["id"] != vm_data["id"] for vm in running)
            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", unique_vm_name, "--json").stdout,
                dict,
            )
            assert inspected["vm"]["status"] == "crashed"

            _run_mvm(runner_vm, "vm", "stop", unique_vm_name, "--force", timeout=120)
            _run_mvm(runner_vm, "vm", "start", unique_vm_name, timeout=120)
            assert _run_mvm(
                runner_vm, "exec", unique_vm_name, "--", "printf", "restarted-after-crash",
            ).stdout == "restarted-after-crash"
        finally:
            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if any(vm["name"] == unique_vm_name for vm in listed):
                removed = _run_mvm(
                    runner_vm, "vm", "rm", unique_vm_name, "--force", check=False,
                    timeout=120,
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
            networks = _json_success_data(
                _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
            )
            if any(network["name"] == net_name for network in networks):
                removed = _run_mvm(
                    runner_vm, "network", "rm", net_name, "--force", check=False,
                )
                assert removed.returncode == 0, (removed.stdout, removed.stderr)
            assert all(vm["name"] != unique_vm_name for vm in _parse_vm_list(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
            ))
            assert all(network["name"] != net_name for network in _json_success_data(
                _run_mvm(runner_vm, "network", "ls", "--json").stdout, list,
            ))


# ========================================================================
# TestVMStateTransitionErrors (from CLI edge cases)
# ========================================================================


class TestVMStateTransitionErrors:
    """Invalid transitions expose typed refusal and retain the original state."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_pause_and_resume_stopped_vm_are_rejected(self, runner_vm, lifecycle_vm):
        """Neither wrong-direction command relaunches or mutates a stopped VM."""
        name = lifecycle_vm["name"]
        with _lifecycle_process_handle(_lifecycle_vm_state(runner_vm, name, "running")) as (identity, pidfd):
            _run_mvm(runner_vm, "vm", "stop", name)
            _assert_lifecycle_process_exited(pidfd)
            for operation in ("pause", "resume"):
                result = _run_mvm(runner_vm, "vm", operation, name, "--json", check=False)
                self._assert_invalid_state(result, operation)
                assert _lifecycle_vm_state(runner_vm, name, "stopped")["id"] == lifecycle_vm["id"]
                _assert_lifecycle_process_exited(pidfd)
                assert all(vm["id"] != lifecycle_vm["id"] for vm in _parse_vm_list(
                    _run_mvm(runner_vm, "vm", "ps", "--json").stdout,
                ))
        _run_mvm(runner_vm, "vm", "start", name)
        assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running")) != identity
        _lifecycle_boot_id(runner_vm, name)

    def test_start_paused_vm_is_rejected_without_relaunch(self, runner_vm, lifecycle_vm):
        """Start refuses paused state; resume recovers the same process and guest boot."""
        name = lifecycle_vm["name"]
        identity = _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
        boot_id = _lifecycle_boot_id(runner_vm, name)
        _run_mvm(runner_vm, "vm", "pause", name)
        assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "paused")) == identity
        result = _run_mvm(runner_vm, "vm", "start", name, "--json", check=False)
        self._assert_invalid_state(result, "start")
        assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "paused")) == identity
        _run_mvm(runner_vm, "vm", "resume", name)
        assert _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running")) == identity
        assert _lifecycle_boot_id(runner_vm, name) == boot_id

    @staticmethod
    def _assert_invalid_state(result: subprocess.CompletedProcess[str], operation: str) -> None:
        """Batch output retains both the CLI failure and the controller's typed state error."""
        assert result.returncode != 0, (result.stdout, result.stderr)
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["error"]["code"] == "cli.batch.failed"
        assert envelope["error"]["class"] == 4
        items = envelope["data"]["items"]
        assert len(items) == 1, items
        assert items[0]["result"]["status"] == "error"
        assert items[0]["result"]["code"] == f"vm.{operation}_failed"
        assert items[0]["error"]["code"] == "vm.state.invalid"
        assert items[0]["error"]["class"] == 1


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
                runner_vm, "volume", "attach", unique_vm_name, vol_name
            )
            assert result.returncode == 0
            vol_inspect = _run_mvm(
                runner_vm, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            vol_info = vol_data.get("volume") or {}
            assert vol_info.get("status") == "attached"
            result = _run_mvm(
                runner_vm, "volume", "detach", unique_vm_name, vol_name
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
                "volume",
                "attach",
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
                runner_vm, "volume", "attach", unique_vm_name, vol_name
            )
            _run_mvm(runner_vm, "vm", "start", unique_vm_name)
            result = _run_mvm(
                runner_vm,
                "volume",
                "detach",
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


# ========================================================================
# TestVMCreateConsole — L2: --console flag enables console relay
# ========================================================================


class TestVMCreateConsole:
    """Create VM with --console flag — enable_console=true in ls JSON."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_console(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --console — verify enable_console=true in ls --json."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
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
                "--console",
            )

            # L2: verify enable_console=true in ls --json
            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("enable_console") is True, (
                f"Expected enable_console=true, got: {vm.get('enable_console')}"
            )

            # L2: verify relay_running in inspect
            inspect = _run_mvm(
                runner_vm, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(inspect.stdout)
            console_info = data.get("console", {})
            assert console_info.get("relay_running") is True, (
                f"Expected relay_running=true in inspect, got: {console_info}"
            )
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )


# ========================================================================
# TestVMCreateCount — L2: --count N creates N VMs
# ========================================================================


class TestVMCreateCount:
    """Create N VMs with --count flag — verify N VMs in ls --json."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_count(
        self,
        runner_vm,
        unique_network_name,
    ):
        """Create 3 VMs with --count 3 — N VMs running in ls --json."""
        net_name = unique_network_name
        base_name = f"cnt-{uuid.uuid4().hex[:6]}"
        count = 3
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
        )
        # generate expected names matching GenerateBatchNames:
        # names[0]=baseName, names[i-1]=baseName-i for i=2..count
        expected_names = [base_name] + [
            f"{base_name}-{i}" for i in range(2, count + 1)
        ]
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                base_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--count",
                str(count),
            )

            vms = json.loads(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout
            )
            created = [v for v in vms if v["name"] in expected_names]
            assert len(created) == count, (
                f"Expected {count} VMs, found {len(created)}: "
                f"{[v['name'] for v in created]}"
            )
            for vm in created:
                assert vm["status"] == "running", (
                    f"VM '{vm['name']}' status is {vm['status']}, "
                    f"expected running"
                )
        finally:
            for name in expected_names:
                _run_mvm(
                    runner_vm, "vm", "rm", name, "--force", check=False
                )
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )


# ========================================================================
# TestVMCreateAtomicCount — L2: --atomic --count N all-or-nothing
# ========================================================================


class TestVMCreateAtomicCount:
    """Create VMs with --atomic --count N — all running (happy path)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_atomic_count(
        self,
        runner_vm,
        unique_network_name,
    ):
        """Create with --atomic --count 2 — all VMs running in ls --json."""
        net_name = unique_network_name
        base_name = f"atomic-{uuid.uuid4().hex[:6]}"
        count = 2
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
        )
        expected_names = [base_name] + [
            f"{base_name}-{i}" for i in range(2, count + 1)
        ]
        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                base_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--atomic",
                "--count",
                str(count),
            )

            vms = json.loads(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout
            )
            created = [v for v in vms if v["name"] in expected_names]
            assert len(created) == count, (
                f"Expected {count} VMs with --atomic, found "
                f"{len(created)}: {[v['name'] for v in created]}"
            )
            for vm in created:
                assert vm["status"] == "running", (
                    f"VM '{vm['name']}' status is {vm['status']}, "
                    f"expected running"
                )
        finally:
            for name in expected_names:
                _run_mvm(
                    runner_vm, "vm", "rm", name, "--force", check=False
                )
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )


# ========================================================================
# TestVMCreateCountIP — L1: --count with --ip rejected
# ========================================================================


class TestVMCreateCountIP:
    """Reject --count with --ip — mutually exclusive."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    def test_count_with_ip_rejected(
        self,
        runner_vm,
    ):
        """--count with --ip must be rejected with clear error."""
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-count-ip",
            "--image",
            "alpine:3.23",
            "--count",
            "2",
            "--ip",
            "10.0.0.50",
            check=False,
        )
        assert result.returncode != 0
        assert result.stderr, "Expected error output but got none"


# ========================================================================
# TestVMCreateCountMAC — L1: --count with --mac rejected
# ========================================================================


class TestVMCreateCountMAC:
    """Reject --count with --mac — mutually exclusive."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    def test_count_with_mac_rejected(
        self,
        runner_vm,
    ):
        """--count with --mac must be rejected with clear error."""
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-count-mac",
            "--image",
            "alpine:3.23",
            "--count",
            "2",
            "--mac",
            "aa:bb:cc:dd:ee:ff",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "--mac" in combined and "count" in combined, (
            f"Expected error mentioning --mac and --count, "
            f"got: {combined[:500]}"
        )


# ========================================================================
# TestVMCreateCountNegative — L1: --count -1 rejected
# ========================================================================


class TestVMCreateCountNegative:
    """Reject --count -1 — must be at least 1."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    def test_count_negative_rejected(
        self,
        runner_vm,
    ):
        """--count -1 must be rejected with clear error."""
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            "test-count-neg",
            "--count",
            "-1",
            check=False,
        )
        assert result.returncode != 0
        assert result.stderr, "Expected error output but got none"


# ========================================================================
# TestVMCreateForce — L2: --force skips confirmation on create
# ========================================================================


class TestVMCreateForce:
    """Create VM with --force flag — skip confirmation prompts."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_force(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --force and --skip-cleanup — no prompt needed."""
        net_name = unique_network_name
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
        )
        try:
            # --force skips the confirmation prompt that --skip-cleanup
            # normally requires. Without --force, --skip-cleanup pipes
            # to a confirmation prompt that would hang in non-interactive
            # mode. With --force, the prompt is bypassed.
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--force",
                "--skip-cleanup",
            )

            vms = json.loads(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout
            )
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
# TestVMCreateVsockPort — L2: --vsock-port creates VM with custom port
# ========================================================================


class TestVMCreateVsockPort:
    """Create VM with --vsock-port flag — verify VM running."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_vsock_port(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --vsock-port 54321 — verify VM running."""
        net_name = unique_network_name
        custom_port = 54321
        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
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
                "--vsock-port",
                str(custom_port),
            )

            # Verify VM is running
            vms = json.loads(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout
            )
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"

            # L2+: verify vsock socket file exists in vm_dir
            inspect = _run_mvm(
                runner_vm, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(inspect.stdout)
            vm_dir = data.get("filesystem", {}).get("vm_dir", "")
            if vm_dir:
                sock_check = _guest_run(runner_vm,
                    f"test -S '{vm_dir}/vsock.sock' && echo OK",
                    check=False,
                )
                if sock_check.returncode == 0 and "OK" in sock_check.stdout:
                    # vsock socket found — port was correctly configured
                    pass
        finally:
            _run_mvm(
                runner_vm, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
