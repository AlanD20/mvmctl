"""Host configuration system tests.

Migrated from tests/e2e/host/test_host.py.
The read-only cases require a provisioned runner and exercise the installed
candidate through the 0.3 structured JSON envelope.
"""

from __future__ import annotations

import json
import os
import shlex
import tempfile

import pytest

from tests.system.conftest import _guest_run, _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_host]


def _success_data(result):
    assert result.returncode == 0, result.stderr or result.stdout
    envelope = json.loads(result.stdout)
    assert envelope["schema_version"] == 1
    assert envelope["status"] == "success"
    assert envelope["error"] is None
    data = envelope["data"]
    assert isinstance(data, dict)
    return data


class TestHostInfo:
    """Test host info command (read-only, non-destructive)."""

    def test_host_info_detects_first_capacity_snapshot(self, runner_vm):
        """A fresh operator cache must not need a prior refresh to show host info."""
        mirror = os.environ.get("MVM_ASSET_MIRROR")
        assert mirror, "the runner must provide its pre-cached asset mirror"
        with tempfile.TemporaryDirectory(
            prefix="mvm-host-info-", dir="/home/runner/.cache"
        ) as cache_dir:
            env = (
                f"MVM_CACHE_DIR={shlex.quote(cache_dir)} "
                f"MVM_ASSET_MIRROR={shlex.quote(mirror)}"
            )
            _guest_run(
                runner_vm,
                f"{env} /usr/local/bin/mvm init --non-interactive "
                "--skip-host --skip-network --binary-version 1.16.0",
                timeout=180,
            )
            first = _guest_run(
                runner_vm,
                f"MVM_CACHE_DIR={shlex.quote(cache_dir)} "
                "/usr/local/bin/mvm host info --json",
            )
            data = _success_data(first)
            assert data["hostname"]
            assert data["memory"]["total_mib"] > 0
            assert data["detected_at"]

    def test_host_info_basic(self, runner_vm):
        """Show host info in human-readable format."""
        result = _run_mvm(runner_vm, "host", "info")
        stdout = result.stdout
        assert "Host Info" in stdout, (
            f"host info missing title 'Host Info':\n{stdout}"
        )
        assert "Hostname" in stdout, (
            f"host info missing 'Hostname' section:\n{stdout}"
        )
        assert "CPU" in stdout, f"host info missing 'CPU' section:\n{stdout}"
        assert "Memory" in stdout or "memory" in stdout.lower(), (
            f"host info missing 'Memory' section:\n{stdout}"
        )
        assert "imits" in stdout.lower(), (
            f"host info missing 'Limits' section:\n{stdout}"
        )
        assert "apacity" in stdout.lower(), (
            f"host info missing 'Capacity' section:\n{stdout}"
        )

    def test_host_info_json(self, runner_vm):
        """Show host info in JSON format with expected structure."""
        result = _run_mvm(runner_vm, "host", "info", "--json", check=False)
        data = _success_data(result)
        assert "hostname" in data, (
            f"host info --json missing 'hostname': {list(data.keys())}"
        )
        assert "os" in data, (
            f"host info --json missing 'os': {list(data.keys())}"
        )
        assert "cpu" in data, (
            f"host info --json missing 'cpu': {list(data.keys())}"
        )
        assert "memory" in data, (
            f"host info --json missing 'memory': {list(data.keys())}"
        )
        assert "storage" in data, (
            f"host info --json missing 'storage': {list(data.keys())}"
        )
        assert "limits" in data, (
            f"host info --json missing 'limits': {list(data.keys())}"
        )
        assert "capacity" in data, (
            f"host info --json missing 'capacity': {list(data.keys())}"
        )
        cpu = data["cpu"]
        assert "model" in cpu, f"cpu missing 'model': {list(cpu.keys())}"
        assert "vendor" in cpu, f"cpu missing 'vendor': {list(cpu.keys())}"
        assert "cores" in cpu, f"cpu missing 'cores': {list(cpu.keys())}"
        assert "architecture" in cpu, (
            f"cpu missing 'architecture': {list(cpu.keys())}"
        )
        assert "numa_nodes" in cpu, (
            f"cpu missing 'numa_nodes': {list(cpu.keys())}"
        )
        cap = data["capacity"]
        assert "recommended_max_vms" in cap, (
            f"capacity missing 'recommended_max_vms': {list(cap.keys())}"
        )
        assert "limiting_resource" in cap, (
            f"capacity missing 'limiting_resource': {list(cap.keys())}"
        )
        limits = data["limits"]
        assert "pid_max" in limits, (
            f"limits missing 'pid_max': {list(limits.keys())}"
        )
        assert "fd_max" in limits, (
            f"limits missing 'fd_max': {list(limits.keys())}"
        )
        assert "conntrack_max" in limits, (
            f"limits missing 'conntrack_max': {list(limits.keys())}"
        )
        assert "tap_devices_max" in limits, (
            f"limits missing 'tap_devices_max': {list(limits.keys())}"
        )

    def test_host_info_refresh_json(self, runner_vm):
        """Re-detect host info and verify JSON output has meaningful values."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        assert isinstance(data.get("cpu", {}).get("model"), str), (
            f"cpu.model must be a string: {data.get('cpu', {})}"
        )
        assert len(data["cpu"]["model"]) > 0, "cpu.model must not be empty"
        assert data.get("memory", {}).get("total_mib", 0) > 0, (
            f"memory.total_mib must be > 0: {data.get('memory', {})}"
        )
        rec_vms = data.get("capacity", {}).get("recommended_max_vms", -1)
        assert isinstance(rec_vms, int), (
            f"recommended_max_vms must be int: {type(rec_vms)}"
        )
        assert rec_vms >= 0, f"recommended_max_vms must be >= 0: {rec_vms}"
        assert "detected_at" in data, "host info missing 'detected_at'"
        assert isinstance(data["detected_at"], str), (
            "detected_at must be a string"
        )
        assert len(data["detected_at"]) > 0, "detected_at must not be empty"

    def test_host_info_json_field_types(self, runner_vm):
        """Verify JSON field types are correct after --refresh."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        assert isinstance(data["cpu"]["cores"], int), (
            f"cpu.cores must be int: {type(data['cpu']['cores'])}"
        )
        assert isinstance(data["cpu"]["numa_nodes"], int), (
            f"cpu.numa_nodes must be int: {type(data['cpu']['numa_nodes'])}"
        )
        assert isinstance(data["memory"]["total_mib"], int), (
            f"memory.total_mib must be int: {type(data['memory']['total_mib'])}"
        )
        assert isinstance(data["memory"]["available_mib"], int), (
            f"memory.available_mib must be int: {type(data['memory']['available_mib'])}"
        )
        assert isinstance(data["limits"]["pid_max"], int), (
            f"limits.pid_max must be int: {type(data['limits']['pid_max'])}"
        )
        assert isinstance(data["limits"]["fd_max"], int), (
            f"limits.fd_max must be int: {type(data['limits']['fd_max'])}"
        )
        assert isinstance(data["capacity"]["recommended_max_vms"], int), (
            f"capacity.recommended_max_vms must be int: "
            f"{type(data['capacity']['recommended_max_vms'])}"
        )


class TestHostStatus:
    """Test host status command (read-only, non-destructive)."""

    def test_host_status_basic(self, runner_vm):
        """Show current host configuration state."""
        result = _run_mvm(runner_vm, "host", "status")
        stdout = result.stdout
        assert "/dev/kvm" in stdout, (
            f"host status missing '/dev/kvm' check:\n{stdout}"
        )
        assert "ip_forward" in stdout, (
            f"host status missing 'ip_forward' check:\n{stdout}"
        )

    def test_host_status_json(self, runner_vm):
        """Show current host configuration state in JSON format."""
        result = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        data = _success_data(result)
        assert "kvm_accessible" in data, (
            f"host status --json missing 'kvm_accessible': {list(data.keys())}"
        )
        assert isinstance(data["kvm_accessible"], bool), (
            f"kvm_accessible must be bool: {type(data['kvm_accessible'])}"
        )
        assert "missing_binaries" in data, (
            f"host status --json missing 'missing_binaries': {list(data.keys())}"
        )
        assert "ip_forward" in data, (
            f"host status --json missing 'ip_forward': {list(data.keys())}"
        )
        assert "state" in data, (
            f"host status --json missing 'state': {list(data.keys())}"
        )
        assert data["state"] is None, (
            "caller-owned SQLite must not claim root administrator admission"
        )

    def test_host_status_json_virtualization(self, runner_vm):
        """host status --json includes virtualization section with all expected fields."""
        result = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        data = _success_data(result)
        resources = data["resources"]
        assert isinstance(resources["modules_loaded"], dict)
        assert isinstance(resources["dev_kvm_status"], str)
        assert isinstance(resources["user_in_kvm_group"], bool)
        assert isinstance(resources["dev_net_tun_accessible"], bool)

    def test_host_status_json_payload_types(self, runner_vm):
        """The provisioned runner returns typed live-status fields."""
        result = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        data = _success_data(result)
        assert isinstance(data["kvm_accessible"], bool)
        assert isinstance(data["missing_binaries"], (list, type(None)))
        assert isinstance(data["ip_forward"], str)


class TestHostStatusEnhanced:
    """Enhanced host status and info tests — covers status JSON and info sections."""

    def test_host_status_json_structure(self, runner_vm):
        """host status --json returns all expected nested fields."""
        result = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        data = _success_data(result)
        assert isinstance(data["kvm_accessible"], bool)
        assert data.get("missing_binaries") is None or isinstance(
            data["missing_binaries"], list
        )
        assert isinstance(data["ip_forward"], str)
        assert data["state"] is None

    def test_host_status_json_virtualization_nested(self, runner_vm):
        """host status --json virtualization section has correct types."""
        result = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        data = _success_data(result)
        resources = data["resources"]
        assert isinstance(resources["modules_loaded"], dict)
        assert isinstance(resources["dev_kvm_status"], str)
        assert isinstance(resources["user_in_kvm_group"], bool)
        assert isinstance(resources["dev_net_tun_accessible"], bool)

    def test_host_info_json_os_section(self, runner_vm):
        """host info --json os section contains kernel and release."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        os_section = data.get("os", {})
        assert "kernel" in os_section, (
            f"os missing 'kernel': {list(os_section.keys())}"
        )
        assert "release" in os_section, (
            f"os missing 'release': {list(os_section.keys())}"
        )
        assert isinstance(os_section["kernel"], str)
        assert len(os_section["kernel"]) > 0

    def test_host_info_json_storage_section(self, runner_vm):
        """host info --json storage section contains total_bytes and free_bytes."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        storage = data.get("storage", {})
        assert "total_bytes" in storage, (
            f"storage missing 'total_bytes': {list(storage.keys())}"
        )
        assert "free_bytes" in storage, (
            f"storage missing 'free_bytes': {list(storage.keys())}"
        )
        assert isinstance(storage["total_bytes"], int)
        assert isinstance(storage["free_bytes"], int)

    def test_host_info_json_omits_untrusted_setup_state(self, runner_vm):
        """Capacity data must not present caller-owned SQLite as root setup authority."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        assert "setup" not in data

    def test_host_info_json_capacity_current(self, runner_vm):
        """host info --json capacity.current section contains usage fields."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        cap = data.get("capacity", {})
        current = cap.get("current", {})
        assert "pids" in current, (
            f"current missing 'pids': {list(current.keys())}"
        )
        assert "fds" in current, (
            f"current missing 'fds': {list(current.keys())}"
        )
        assert "conntrack" in current, (
            f"current missing 'conntrack': {list(current.keys())}"
        )
        assert "tap_devices" in current, (
            f"current missing 'tap_devices': {list(current.keys())}"
        )
        assert "arp_entries" in current, (
            f"current missing 'arp_entries': {list(current.keys())}"
        )
        for key in ("pids", "fds", "conntrack", "tap_devices", "arp_entries"):
            assert isinstance(current[key], int), f"current.{key} must be int"

    def test_host_info_json_ip_local_port_range(self, runner_vm):
        """host info --json limits section contains ip_local_port_range as list."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        limits = data.get("limits", {})
        assert "ip_local_port_range" in limits
        port_range = limits["ip_local_port_range"]
        assert isinstance(port_range, list)
        assert len(port_range) == 2
        assert all(isinstance(p, int) for p in port_range)

    def test_host_info_json_virtualization_section(self, runner_vm):
        """host info --json contains virtualization section with all expected fields."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        virt = data.get("virtualization")
        assert virt is not None, (
            f"host info --json missing 'virtualization' section: {list(data.keys())}"
        )
        assert "cpu_has_vmx" in virt, (
            f"virtualization missing 'cpu_has_vmx': {list(virt.keys())}"
        )
        assert isinstance(virt["cpu_has_vmx"], bool)
        assert "nested_virt_available" in virt, (
            f"virtualization missing 'nested_virt_available': {list(virt.keys())}"
        )
        assert isinstance(virt["nested_virt_available"], bool)
        assert "ept_available" in virt, (
            f"virtualization missing 'ept_available': {list(virt.keys())}"
        )
        assert isinstance(virt["ept_available"], bool)
        assert "hypervisor" in virt, (
            f"virtualization missing 'hypervisor': {list(virt.keys())}"
        )
        assert isinstance(virt["hypervisor"], bool)
        assert "smt_active" in virt, (
            f"virtualization missing 'smt_active': {list(virt.keys())}"
        )
        assert isinstance(virt["smt_active"], bool)
        assert "modules" in virt, (
            f"virtualization missing 'modules': {list(virt.keys())}"
        )
        assert isinstance(virt["modules"], dict)

    def test_host_info_json_hugepages_section(self, runner_vm):
        """host info --json contains hugepages section."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        hp = data.get("hugepages")
        assert hp is not None, (
            f"host info --json missing 'hugepages' section: {list(data.keys())}"
        )
        assert "count_2mb" in hp, (
            f"hugepages missing 'count_2mb': {list(hp.keys())}"
        )
        assert isinstance(hp["count_2mb"], int)
        assert "free_2mb" in hp, (
            f"hugepages missing 'free_2mb': {list(hp.keys())}"
        )
        assert isinstance(hp["free_2mb"], int)

    def test_host_info_json_dependencies_section(self, runner_vm):
        """host info --json contains dependencies section."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        deps = data.get("dependencies")
        assert deps is not None, (
            f"host info --json missing 'dependencies' section: {list(data.keys())}"
        )
        assert "nftables_available" in deps
        assert isinstance(deps["nftables_available"], bool)
        if not deps["nftables_available"]:
            assert isinstance(deps["nftables_probe_error"], str)
            assert deps["nftables_probe_error"]
        assert "cloud_localds_available" in deps
        assert isinstance(deps["cloud_localds_available"], bool)
        assert "dev_net_tun" in deps
        assert isinstance(deps["dev_net_tun"], bool)

    def test_host_info_json_system_section(self, runner_vm):
        """host info --json contains system section."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        sys_sec = data.get("system")
        assert sys_sec is not None, (
            f"host info --json missing 'system' section: {list(data.keys())}"
        )
        assert "cgroup_version" in sys_sec
        assert isinstance(sys_sec["cgroup_version"], int)
        assert "ksm_disabled" in sys_sec
        assert isinstance(sys_sec["ksm_disabled"], bool)
        assert "dev_kvm_status" in sys_sec
        assert isinstance(sys_sec["dev_kvm_status"], str)
        assert "user_in_kvm_group" in sys_sec
        assert isinstance(sys_sec["user_in_kvm_group"], bool)

    def test_host_info_json_memory_swap(self, runner_vm):
        """host info --json memory section contains swap fields."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        mem = data.get("memory")
        assert mem is not None, (
            f"host info --json missing 'memory' section: {list(data.keys())}"
        )
        assert "swap_total_mib" in mem, (
            f"memory missing 'swap_total_mib': {list(mem.keys())}"
        )
        assert isinstance(mem["swap_total_mib"], int)
        assert "swap_used_mib" in mem, (
            f"memory missing 'swap_used_mib': {list(mem.keys())}"
        )
        assert isinstance(mem["swap_used_mib"], int)

    def test_host_info_json_kernel_minimum_version(self, runner_vm):
        """host info --json kernel section contains minimum_version_met."""
        result = _run_mvm(
            runner_vm, "host", "info", "--refresh", "--json", check=False
        )
        data = _success_data(result)
        kernel = data.get("kernel")
        assert kernel is not None, (
            f"host info --json missing 'kernel' section: {list(data.keys())}"
        )
        assert "minimum_version_met" in kernel, (
            f"kernel missing 'minimum_version_met': {list(kernel.keys())}"
        )
        assert isinstance(kernel["minimum_version_met"], bool)
        assert "version" in kernel, (
            f"kernel missing 'version': {list(kernel.keys())}"
        )
        assert isinstance(kernel["version"], str)
        assert len(kernel["version"]) > 0


class TestHostCleanSafety:
    """Test host clean safety mechanisms (non-destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.needs_kvm,
        pytest.mark.slow,
        pytest.mark.domain_host,
    ]

    def test_host_clean_blocked_by_running_vm(
        self, runner_vm, unique_vm_name, created_network
    ):
        """Host clean should be blocked when a VM is running."""
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

        try:
            result = _run_mvm(
                runner_vm,
                "host",
                "clean",
                "--force",
                check=False,
            )
            assert result.returncode != 0
            assert "running" in result.stderr.lower()
        finally:
            _run_mvm(
                runner_vm,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestHostResetSafety:
    """Test host reset safety mechanisms (non-destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.needs_kvm,
        pytest.mark.slow,
        pytest.mark.domain_host,
    ]

    def test_host_reset_blocked_by_running_vm(
        self, runner_vm, unique_vm_name, created_network
    ):
        """Host reset should be blocked when a VM is running."""
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

        try:
            result = _run_mvm(
                runner_vm,
                "host",
                "reset",
                "--force",
                check=False,
            )
            assert result.returncode != 0
            assert "running" in result.stderr.lower()
        finally:
            _run_mvm(
                runner_vm,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestHostCleanDestructive:
    """Execute host clean --force (destructive).

    Excluded from default test runs via the ``host_reset`` marker and must be
    explicitly invoked.

    All commands run inside the test VM (no host sudo required).
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.host_reset,
        pytest.mark.domain_host,
    ]

    def test_host_clean_force(self, runner_vm):
        """Execute host clean --force and verify it exits successfully."""
        # Check host is initialized; if not, init first
        status = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        needs_init = status.returncode != 0

        if needs_init:
            _run_mvm(runner_vm, "host", "init")

        # Remove any running VMs first
        vms = json.loads(
            _run_mvm(runner_vm, "vm", "ls", "--json", check=False).stdout
            or "[]"
        )
        for vm in vms:
            _run_mvm(runner_vm, "vm", "rm", vm["name"], "--force", check=False)

        # Run host clean inside the test VM
        result = _run_mvm(runner_vm, "host", "clean", "--force", check=False)
        assert result.returncode == 0, (
            f"host clean --force failed: {result.stderr}"
        )
        assert len(result.stdout.strip()) > 0, (
            f"host clean --force produced no stdout: stderr={result.stderr}"
        )

    def test_host_reset_force(self, runner_vm):
        """Execute host reset --force and verify it exits successfully."""
        status = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        needs_init = status.returncode != 0

        if needs_init:
            _run_mvm(runner_vm, "host", "init")

        # Remove any running VMs first
        vms = json.loads(
            _run_mvm(runner_vm, "vm", "ls", "--json", check=False).stdout
            or "[]"
        )
        for vm in vms:
            _run_mvm(runner_vm, "vm", "rm", vm["name"], "--force", check=False)

        result = _run_mvm(runner_vm, "host", "reset", "--force", check=False)
        assert result.returncode == 0, (
            f"host reset --force failed: {result.stderr}"
        )
        assert len(result.stdout.strip()) > 0, (
            f"host reset --force produced no stdout: stderr={result.stderr}"
        )


class TestHostInit:
    """Test host init command.

    Excluded from default test runs via the ``host_reset`` marker and must be
    explicitly invoked.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.host_reset,
        pytest.mark.domain_host,
    ]

    def test_host_init_success(self, runner_vm):
        """host init should succeed inside the test VM.

        Inside the test VM, host init was already run during provisioning.
        If the runner user lacks passwordless sudo, host init returns
        NeedsInteraction — accept that as a valid outcome.
        """
        status = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        if status.returncode == 0:
            try:
                data = json.loads(status.stdout)
                if data and data.get("state", {}).get("initialized"):
                    pytest.skip("Host already initialized during provisioning")
            except (json.JSONDecodeError, AttributeError, TypeError):
                pass

        result = _run_mvm(runner_vm, "host", "init", check=False)
        err = (result.stdout + result.stderr).lower()
        if result.returncode != 0 and ("needs sudo" in err or "needsinteraction" in err):
            pytest.skip("Host init needs sudo — not available inside test VM")
        assert result.returncode == 0, f"host init failed: {result.stderr}"

        # Verify host status reflects initialized state
        check = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        if check.returncode == 0:
            data = json.loads(check.stdout)
            state = data.get("state", {})
            assert state.get("initialized") is True, (
                f"Host should be initialized after init: {data}"
            )
