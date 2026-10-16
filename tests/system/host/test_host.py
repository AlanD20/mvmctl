"""Host configuration system tests.

Migrated from tests/e2e/host/test_host.py.
The read-only cases require a provisioned runner and exercise the installed
candidate through the 0.3 structured JSON envelope.
"""

from __future__ import annotations

import json
import os
import tempfile
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import _json_success_data, _run_mvm, _unique_subnet

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


def _host_inventory(runner_vm, domain):
    data = _json_success_data(
        _run_mvm(runner_vm, domain, "ls", "--json").stdout,
        dict if domain == "config" else list,
    )
    return data if isinstance(data, dict) else sorted(data, key=lambda row: row["id"])


def _host_administrator_state(runner_vm):
    data = _success_data(_run_mvm(runner_vm, "host", "status", "--json"))
    return {
        field: data[field]
        for field in ("group_exists", "sudoers_exists", "ip_forward", "user_in_group")
    }


class _HostGuardCase:
    """Own only attempted unique resources; preserve the runner's prior state."""

    def __init__(self, runner_vm):
        self.runner_vm = runner_vm
        self.baseline = {
            domain: _host_inventory(runner_vm, domain)
            for domain in ("vm", "network", "image", "kernel", "bin", "key", "volume", "config")
        }
        self.administrator = _host_administrator_state(runner_vm)
        self.attempted = {}
        self.receipts = {}
        self.vm_paths = ()

    def require_launch_assets(self):
        assert os.geteuid() != 0, "host guards must run as the ordinary operator"
        assert os.environ.get("MVM_ASSET_MIRROR"), "the runner must provide its asset mirror"
        binaries = self.baseline["bin"]
        for member in ("firecracker", "jailer"):
            assert any(
                row["type"] == member and row["version"] == "1.17.0"
                and row["is_default"] and row["is_present"]
                for row in binaries
            ), f"preseeded default {member} 1.17.0 is required"

    def attempt(self, domain, name):
        assert not any(row["name"] == name for row in self.baseline[domain])
        self.attempted[domain] = name

    def remember(self, domain, row):
        assert row["name"] == self.attempted[domain], row
        assert row["id"] and row["created_at"], row
        self.receipts[domain] = (row["id"], row["created_at"])
        return row

    def create_running_vm(self):
        self.require_launch_assets()
        assert _json_success_data(_run_mvm(
            self.runner_vm, "config", "get", "settings", "guestfs_enabled", "--json",
        ).stdout, bool) is False, "the provisioned runner must use LoopMount"
        assert any(
            row["type"] == "alpine" and row["version"] == "3.23" and row["is_present"]
            for row in self.baseline["image"]
        ), "preseeded Alpine 3.23 is required"
        kernels = [row for row in self.baseline["kernel"] if row["is_default"] and row["is_present"]]
        assert len(kernels) == 1, kernels
        name = f"sys-host-net-{uuid.uuid4().hex[:16]}"
        self.attempt("network", name)
        network = self.remember("network", _success_data(_run_mvm(
            self.runner_vm, "network", "create", name,
            "--subnet", _unique_subnet(name), "--no-nat", "--json",
        )))
        assert not network["is_default"], network
        name = f"sys-host-vm-{uuid.uuid4().hex}"
        self.attempt("vm", name)
        rows = _json_success_data(_run_mvm(
            self.runner_vm, "vm", "create", name,
            "--image", "alpine:3.23", "--kernel", kernels[0]["id"],
            "--network", network["id"], "--disk-size", "512M",
            "--vcpu", "1", "--mem", "256M", "--user", "root",
            "--cloud-init-mode", "off", "--skip-deblob", "--json", timeout=180,
        ).stdout, list)
        assert len(rows) == 1, rows
        vm = self.remember("vm", rows[0])
        inspection = _success_data(_run_mvm(self.runner_vm, "vm", "inspect", vm["id"], "--json"))
        self.vm_paths = tuple(inspection["filesystem"][field] for field in ("vm_dir", "rootfs_path"))
        assert inspection["resources"]["vcpu"] == 1
        assert inspection["resources"]["mem"] == 256
        assert inspection["resources"]["disk"] == 512
        return vm

    def assert_healthy(self):
        identity = self.receipts["vm"]
        live = _json_success_data(_run_mvm(self.runner_vm, "vm", "ps", "--json").stdout, list)
        rows = [row for row in live if row["id"] == identity[0]]
        assert len(rows) == 1 and rows[0]["created_at"] == identity[1], live
        assert rows[0]["status"] == "running" and rows[0]["pid"] > 0, rows
        inspection = _success_data(_run_mvm(self.runner_vm, "vm", "inspect", identity[0], "--json"))
        assert inspection["vm"]["id"] == identity[0]
        assert inspection["vm"]["created_at"] == identity[1]
        assert inspection["vm"]["status"] == "running"
        assert _run_mvm(self.runner_vm, "exec", identity[0], "--", "uname", "-s").stdout.strip() == "Linux"
        return rows[0]

    def cleanup(self):
        failures = []
        for domain in ("vm", "network"):
            name = self.attempted.get(domain)
            if name is None:
                continue
            try:
                rows = [row for row in _host_inventory(self.runner_vm, domain) if row["name"] == name]
                assert len(rows) <= 1, rows
                if rows:
                    row = rows[0]
                    # A partial create can publish its row before returning an error.
                    # The name was absent before this case's recorded attempt.
                    if domain not in self.receipts:
                        self.remember(domain, row)
                    assert (row["id"], row["created_at"]) == self.receipts[domain], rows
                    _run_mvm(self.runner_vm, domain, "rm", row["id"], "--force", timeout=120)
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"{domain} cleanup: {error!r}")
            try:
                remaining = _host_inventory(self.runner_vm, domain)
                identity = self.receipts.get(domain)
                assert not any(
                    row["name"] == name or (identity is not None and row["id"] == identity[0])
                    for row in remaining
                ), remaining
                if domain == "vm":
                    live = _json_success_data(_run_mvm(self.runner_vm, "vm", "ps", "--json").stdout, list)
                    assert not any(row["name"] == name or (identity and row["id"] == identity[0]) for row in live), live
                    paths = list(self.vm_paths)
                    if identity:
                        paths.append(f"/run/mvmctl/runtime/{os.getuid()}/{identity[0]}")
                    for path in paths:
                        with pytest.raises(FileNotFoundError):
                            Path(path).lstat()
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"{domain} absence receipt: {error!r}")
        for domain, expected in self.baseline.items():
            try:
                assert _host_inventory(self.runner_vm, domain) == expected, domain
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"runner main {domain} baseline: {error!r}")
        try:
            assert _host_administrator_state(self.runner_vm) == self.administrator
        except (Exception, pytest.fail.Exception) as error:
            failures.append(f"administrator baseline: {error!r}")
        assert not failures, "Host guard teardown failed:\n" + "\n".join(failures)


@pytest.fixture
def host_guard_case(runner_vm):
    case = _HostGuardCase(runner_vm)
    try:
        yield case
    finally:
        case.cleanup()


class TestHostInfo:
    """Test host info command (read-only, non-destructive)."""

    def test_host_info_detects_first_capacity_snapshot(self, runner_vm, host_guard_case, monkeypatch):
        """A fresh operator cache must not need a prior refresh to show host info."""
        mirror = os.environ.get("MVM_ASSET_MIRROR")
        assert mirror, "the runner must provide its pre-cached asset mirror"
        host_guard_case.require_launch_assets()
        with tempfile.TemporaryDirectory(
            prefix="mvm-host-info-", dir="/home/runner/.cache"
        ) as cache_dir:
            with monkeypatch.context() as selection:
                selection.setenv("MVM_CACHE_DIR", cache_dir)
                selection.setenv("MVM_CONFIG_DIR", str(Path(cache_dir) / "config"))
                selection.setenv("MVM_TEMP_DIR", str(Path(cache_dir) / "tmp"))
                selection.delenv("MVM_WARM_POOL", raising=False)
                # Non-interactive init disables GuestFS before cache init. Its
                # config writes must remain private so no global appliance is built.
                _run_mvm(
                    runner_vm, "init", "--non-interactive", "--skip-host", "--skip-network",
                    "--binary-version", "1.17.0", timeout=180,
                )
                first = _run_mvm(runner_vm, "host", "info", "--json")
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


class TestHostMutationGuards:
    """The ordinary operator cannot clean active VMs or invoke administrator reset."""

    def test_host_clean_rejects_running_vm(self, runner_vm, host_guard_case):
        host_guard_case.create_running_vm()
        before_vm = host_guard_case.assert_healthy()
        before = {
            domain: _host_inventory(runner_vm, domain)
            for domain in host_guard_case.baseline
        }
        administrator = _host_administrator_state(runner_vm)
        refused = _run_mvm(runner_vm, "host", "clean", "--force", check=False)
        assert refused.returncode != 0
        assert "running" in refused.stderr.lower()
        after_vm = host_guard_case.assert_healthy()
        assert after_vm == before_vm
        for domain, expected in before.items():
            assert _host_inventory(runner_vm, domain) == expected, domain
        assert _host_administrator_state(runner_vm) == administrator

    def test_host_reset_requires_administrator(self, runner_vm, host_guard_case):
        assert os.geteuid() != 0, "administrator refusal must use the ordinary operator"
        before = _success_data(_run_mvm(runner_vm, "host", "status", "--json"))
        refused = _run_mvm(runner_vm, "host", "reset", "--force", check=False)
        assert refused.returncode != 0
        assert "sudo /usr/local/bin/mvm host reset" in (
            refused.stdout + refused.stderr
        ).lower()
        after = _success_data(_run_mvm(runner_vm, "host", "status", "--json"))
        for field in ("group_exists", "sudoers_exists", "ip_forward"):
            assert after[field] == before[field]
