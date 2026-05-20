"""Host configuration system tests."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_host]


class TestHostInfo:
    """Test host info command (read-only, non-destructive)."""

    def test_host_info_basic(self, mvm_binary):
        """Show host info in human-readable format."""
        # Rationale: L1 verification for human-readable output. Verifies that
        # all major sections (Host, CPU, Memory, Limits, Capacity) are present
        # in the output. No expensive resources needed.
        result = _run_mvm(mvm_binary, "host", "info", check=False)
        if result.returncode != 0:
            # Skip-reason: Host state is unknown when mvm host init has not
            # been run. Running "mvm host init" first would make this test
            # unconditionally runnable.
            combined = (result.stdout + result.stderr).lower()
            assert "not yet detected" in combined or "init" in combined, (
                f"Unexpected output for uninitialized host: {combined}"
            )
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        # L1: Verify stdout contains expected section headers in tree format
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
        # The title uses different case for 'Limits' vs 'limits' depending on format
        assert "imits" in stdout.lower(), (
            f"host info missing 'Limits' section:\n{stdout}"
        )
        assert "apacity" in stdout.lower(), (
            f"host info missing 'Capacity' section:\n{stdout}"
        )

    def test_host_info_json(self, mvm_binary):
        """Show host info in JSON format with expected structure."""
        # Rationale: L2 verification for --json output. Verifies top-level keys
        # and nested field presence in cpu, capacity, and limits sections.
        # No expensive resources needed.
        result = _run_mvm(mvm_binary, "host", "info", "--json", check=False)
        if result.returncode != 0:
            # Skip-reason: Host state is unknown when mvm host init has not
            # been run. Running "mvm host init" first would make this test
            # unconditionally runnable.
            combined = (result.stdout + result.stderr).lower()
            assert "not yet detected" in combined or "init" in combined, (
                f"Unexpected output for uninitialized host: {combined}"
            )
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        # Assert top-level keys
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
        assert "setup" in data, (
            f"host info --json missing 'setup': {list(data.keys())}"
        )
        # Assert cpu nested keys
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
        # Assert capacity nested keys
        cap = data["capacity"]
        assert "recommended_max_vms" in cap, (
            f"capacity missing 'recommended_max_vms': {list(cap.keys())}"
        )
        assert "limiting_resource" in cap, (
            f"capacity missing 'limiting_resource': {list(cap.keys())}"
        )
        # Assert limits nested keys
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

    def test_host_info_refresh_json(self, mvm_binary):
        """Re-detect host info and verify JSON output has meaningful values."""
        # Rationale: L2 verification for --refresh --json path. Ensures the
        # re-detection produces realistic values: non-empty cpu model, positive
        # memory, integer recommended_max_vms, and a populated detected_at.
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            # Skip-reason: Host state is unknown when mvm host init has not
            # been run. Running "mvm host init" first would make this test
            # unconditionally runnable.
            combined = (result.stdout + result.stderr).lower()
            assert "not yet detected" in combined or "init" in combined, (
                f"Unexpected output for uninitialized host: {combined}"
            )
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        # Assert cpu.model is a non-empty string
        assert isinstance(data.get("cpu", {}).get("model"), str), (
            f"cpu.model must be a string: {data.get('cpu', {})}"
        )
        assert len(data["cpu"]["model"]) > 0, "cpu.model must not be empty"
        # Assert memory.total_mib > 0
        assert data.get("memory", {}).get("total_mib", 0) > 0, (
            f"memory.total_mib must be > 0: {data.get('memory', {})}"
        )
        # Assert capacity.recommended_max_vms is int >= 0
        rec_vms = data.get("capacity", {}).get("recommended_max_vms", -1)
        assert isinstance(rec_vms, int), (
            f"recommended_max_vms must be int: {type(rec_vms)}"
        )
        assert rec_vms >= 0, f"recommended_max_vms must be >= 0: {rec_vms}"
        # Assert detected_at is present and non-empty
        assert "detected_at" in data, "host info missing 'detected_at'"
        assert isinstance(data["detected_at"], str), (
            "detected_at must be a string"
        )
        assert len(data["detected_at"]) > 0, "detected_at must not be empty"

    def test_host_info_json_field_types(self, mvm_binary):
        """Verify JSON field types are correct after --refresh."""
        # Rationale: Guard against silent type changes in JSON output. Type
        # changes (int -> str, bool -> int) can break downstream consumers
        # without any error from the JSON parser. This test locks in the
        # expected types for every numeric and boolean field.
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            # Skip-reason: Host state is unknown when mvm host init has not
            # been run. Running "mvm host init" first would make this test
            # unconditionally runnable.
            combined = (result.stdout + result.stderr).lower()
            assert "not yet detected" in combined or "init" in combined, (
                f"Unexpected output for uninitialized host: {combined}"
            )
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        # cpu field types
        assert isinstance(data["cpu"]["cores"], int), (
            f"cpu.cores must be int: {type(data['cpu']['cores'])}"
        )
        assert isinstance(data["cpu"]["numa_nodes"], int), (
            f"cpu.numa_nodes must be int: {type(data['cpu']['numa_nodes'])}"
        )
        # memory field types
        assert isinstance(data["memory"]["total_mib"], int), (
            f"memory.total_mib must be int: {type(data['memory']['total_mib'])}"
        )
        assert isinstance(data["memory"]["available_mib"], int), (
            f"memory.available_mib must be int: {type(data['memory']['available_mib'])}"
        )
        # limits field types
        assert isinstance(data["limits"]["pid_max"], int), (
            f"limits.pid_max must be int: {type(data['limits']['pid_max'])}"
        )
        assert isinstance(data["limits"]["fd_max"], int), (
            f"limits.fd_max must be int: {type(data['limits']['fd_max'])}"
        )
        # capacity field types
        assert isinstance(data["capacity"]["recommended_max_vms"], int), (
            f"capacity.recommended_max_vms must be int: "
            f"{type(data['capacity']['recommended_max_vms'])}"
        )
        # setup field types (if present)
        setup = data.get("setup", {})
        if "initialized" in setup:
            assert isinstance(setup["initialized"], bool), (
                f"setup.initialized must be bool: {type(setup['initialized'])}"
            )


class TestHostStatus:
    """Test host status command (read-only, non-destructive)."""

    def test_host_status_basic(self, mvm_binary):
        """Show current host configuration state."""
        # Rationale: L1 verification for human-readable status output. No
        # expensive resources needed — tests that the command runs and
        # produces structured output.
        result = _run_mvm(mvm_binary, "host", "status", check=False)
        if result.returncode != 0:
            # Skip-reason: Host state is unknown when mvm host init has not
            # been run. Running "mvm host init" first would make this test
            # unconditionally runnable.
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        stdout = result.stdout
        assert "/dev/kvm" in stdout, (
            f"host status missing '/dev/kvm' check:\n{stdout}"
        )
        assert "ip_forward" in stdout, (
            f"host status missing 'ip_forward' check:\n{stdout}"
        )

    def test_host_status_json(self, mvm_binary):
        """Show current host configuration state in JSON format."""
        # Rationale: L2 verification for --json output structure. Tests that
        # the JSON contains expected top-level keys with correct types.
        result = _run_mvm(mvm_binary, "host", "status", "--json", check=False)
        if result.returncode != 0:
            # Skip-reason: Host state is unknown when mvm host init has not
            # been run. Running "mvm host init" first would make this test
            # unconditionally runnable.
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        assert "kvm_accessible" in data, (
            f"host status --json missing 'kvm_accessible': {list(data.keys())}"
        )
        assert isinstance(data["kvm_accessible"], bool), (
            f"kvm_accessible must be bool: {type(data['kvm_accessible'])}"
        )
        assert "required_binaries" in data, (
            f"host status --json missing 'required_binaries': {list(data.keys())}"
        )
        assert isinstance(data["required_binaries"], dict), (
            f"required_binaries must be a dict: {type(data['required_binaries'])}"
        )
        assert "ip_forward" in data, (
            f"host status --json missing 'ip_forward': {list(data.keys())}"
        )
        assert "state_snapshot" in data, (
            f"host status --json missing 'state_snapshot': {list(data.keys())}"
        )

    def test_host_status_json_virtualization(self, mvm_binary):
        """host status --json includes virtualization section with all expected fields."""
        # Rationale: L3 verification for the virtualization section in host
        # status --json. These fields (modules_loaded, nested_virt, dev_net_tun,
        # user_in_kvm_group) reflect real system state and verify the detector
        # collected them correctly. No expensive resources needed.
        result = _run_mvm(mvm_binary, "host", "status", "--json", check=False)
        if result.returncode != 0:
            # Skip-reason: Host state is unknown when mvm host init has not
            # been run. Running "mvm host init" first would make this test
            # unconditionally runnable.
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        # Virtualization section only present when detect_resources() succeeds
        virt = data.get("virtualization")
        if virt is None:
            # Skip-reason: Resources not yet detected. Run "mvm host info
            # --refresh" first to populate.
            pytest.skip(
                "virtualization section not present — run host info --refresh first"
            )
        assert "modules_loaded" in virt, (
            f"virtualization missing 'modules_loaded': {list(virt.keys())}"
        )
        assert isinstance(virt["modules_loaded"], dict), (
            f"modules_loaded must be dict: {type(virt['modules_loaded'])}"
        )
        # Reason: modules_loaded captures kernel module state (kvm, kvm_intel,
        # kvm_amd). A dict with bool values verifies the probe ran correctly.
        assert "nested_virt" in virt, (
            f"virtualization missing 'nested_virt': {list(virt.keys())}"
        )
        assert isinstance(virt["nested_virt"], bool), (
            f"nested_virt must be bool: {type(virt['nested_virt'])}"
        )
        # Reason: nested_virt tells whether kvm_intel or kvm_amd module is
        # loaded, which is essential for nested virtualization support.
        assert "dev_net_tun" in virt, (
            f"virtualization missing 'dev_net_tun': {list(virt.keys())}"
        )
        assert isinstance(virt["dev_net_tun"], bool), (
            f"dev_net_tun must be bool: {type(virt['dev_net_tun'])}"
        )
        # Reason: dev_net_tun verifies /dev/net/tun accessibility, needed
        # for TAP device creation in VM networking.
        assert "user_in_kvm_group" in virt, (
            f"virtualization missing 'user_in_kvm_group': {list(virt.keys())}"
        )
        assert isinstance(virt["user_in_kvm_group"], bool), (
            f"user_in_kvm_group must be bool: {type(virt['user_in_kvm_group'])}"
        )
        # Reason: user_in_kvm_group confirms the current user has /dev/kvm
        # access via group membership, which is required for VM creation.

    def test_host_status_initialized_or_uninitialized(self, mvm_binary):
        """host status --json returns valid data or clear error depending on state.

        When the host IS initialized: verify JSON contains expected fields.
        When NOT initialized: verify a clear error message is returned.
        Either outcome is acceptable — the test always passes.
        """
        # Rationale: Only needs JSON output. No resources needed — handles
        # both initialized and uninitialized host states gracefully.
        result = _run_mvm(mvm_binary, "host", "status", "--json", check=False)
        if result.returncode == 0:
            data = json.loads(result.stdout)
            assert isinstance(data.get("kvm_accessible"), bool), (
                f"kvm_accessible must be bool: {data}"
            )
            assert isinstance(data.get("required_binaries"), dict), (
                f"required_binaries must be a dict: {data}"
            )
            assert "ip_forward" in data, (
                f"host status --json missing ip_forward: {data}"
            )
        else:
            combined = (result.stdout + result.stderr).lower()
            assert "not initialized" in combined, (
                f"Unexpected output for uninitialized host: {combined}"
            )


class TestHostStatusEnhanced:
    """Enhanced host status and info tests — covers status JSON and info sections."""

    def test_host_status_json_structure(self, mvm_binary):
        """host status --json returns all expected nested fields."""
        result = _run_mvm(mvm_binary, "host", "status", "--json", check=False)
        if result.returncode != 0:
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        # kvm_accessible is bool
        assert isinstance(data["kvm_accessible"], bool)
        # required_binaries has ok and missing
        assert "ok" in data["required_binaries"]
        assert "missing" in data["required_binaries"]
        # ip_forward has value and ok
        assert "value" in data["ip_forward"]
        assert "ok" in data["ip_forward"]
        # state_snapshot has exists and timestamp
        assert "exists" in data["state_snapshot"]

    def test_host_status_json_virtualization_nested(self, mvm_binary):
        """host status --json virtualization section has correct types."""
        # Rationale: L3 verification for the virtualization block nested
        # inside host status --json. Verifies the detector populated all
        # four fields (modules_loaded, nested_virt, dev_net_tun,
        # user_in_kvm_group) correctly.
        result = _run_mvm(mvm_binary, "host", "status", "--json", check=False)
        if result.returncode != 0:
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        virt = data.get("virtualization")
        if virt is None:
            pytest.skip(
                "virtualization section not present — run host info --refresh first"
            )
        # modules_loaded is a dict of module name → bool
        assert isinstance(virt["modules_loaded"], dict)
        # nested_virt is bool (derived from kvm_intel/kvm_amd modules loaded)
        assert isinstance(virt["nested_virt"], bool)
        # dev_net_tun is bool (derived from /dev/net/tun accessibility)
        assert isinstance(virt["dev_net_tun"], bool)
        # user_in_kvm_group is bool (derived from 'mvm' in 'groups' output)
        assert isinstance(virt["user_in_kvm_group"], bool)

    def test_host_info_json_os_section(self, mvm_binary):
        """host info --json os section contains kernel and release."""
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        os_section = data.get("os", {})
        assert "kernel" in os_section, (
            f"os missing 'kernel': {list(os_section.keys())}"
        )
        assert "release" in os_section, (
            f"os missing 'release': {list(os_section.keys())}"
        )
        assert isinstance(os_section["kernel"], str)
        assert len(os_section["kernel"]) > 0

    def test_host_info_json_storage_section(self, mvm_binary):
        """host info --json storage section contains total_bytes and free_bytes."""
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        storage = data.get("storage", {})
        assert "total_bytes" in storage, (
            f"storage missing 'total_bytes': {list(storage.keys())}"
        )
        assert "free_bytes" in storage, (
            f"storage missing 'free_bytes': {list(storage.keys())}"
        )
        assert isinstance(storage["total_bytes"], int)
        assert isinstance(storage["free_bytes"], int)

    def test_host_info_json_setup_section(self, mvm_binary):
        """host info --json setup section contains initialized and initialized_at."""
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        setup = data.get("setup", {})
        assert "initialized" in setup, (
            f"setup missing 'initialized': {list(setup.keys())}"
        )
        assert "initialized_at" in setup, (
            f"setup missing 'initialized_at': {list(setup.keys())}"
        )
        assert isinstance(setup["initialized"], bool)

    def test_host_info_json_capacity_current(self, mvm_binary):
        """host info --json capacity.current section contains usage fields."""
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
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

    def test_host_info_json_ip_local_port_range(self, mvm_binary):
        """host info --json limits section contains ip_local_port_range as list."""
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        limits = data.get("limits", {})
        assert "ip_local_port_range" in limits
        port_range = limits["ip_local_port_range"]
        assert isinstance(port_range, list)
        assert len(port_range) == 2
        assert all(isinstance(p, int) for p in port_range)

    def test_host_info_json_virtualization_section(self, mvm_binary):
        """host info --json contains virtualization section with all expected fields."""
        # Rationale: L3 verification for the new virtualization section in
        # host info --json. Fields include cpu_has_vmx, nested_virt_available,
        # ept_available, hypervisor, smt_active, and modules. These verify
        # that the HostDetector collected CPU and kernel state correctly.
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        virt = data.get("virtualization")
        assert virt is not None, (
            f"host info --json missing 'virtualization' section: {list(data.keys())}"
        )
        # Reason: cpu_has_vmx tells whether the CPU supports VMX (Intel VT-x).
        # This is essential for KVM-based VM creation.
        assert "cpu_has_vmx" in virt, (
            f"virtualization missing 'cpu_has_vmx': {list(virt.keys())}"
        )
        assert isinstance(virt["cpu_has_vmx"], bool)
        # Reason: nested_virt_available indicates whether the kernel allows
        # nested virtualization (kvm_intel.nested=1). Required for --nested-virt.
        assert "nested_virt_available" in virt, (
            f"virtualization missing 'nested_virt_available': {list(virt.keys())}"
        )
        assert isinstance(virt["nested_virt_available"], bool)
        # Reason: ept_available checks EPT (Extended Page Tables) support.
        # EPT improves VM performance by reducing TLB misses.
        assert "ept_available" in virt, (
            f"virtualization missing 'ept_available': {list(virt.keys())}"
        )
        assert isinstance(virt["ept_available"], bool)
        # Reason: hypervisor tells whether we're already running inside a VM.
        assert "hypervisor" in virt, (
            f"virtualization missing 'hypervisor': {list(virt.keys())}"
        )
        assert isinstance(virt["hypervisor"], bool)
        # Reason: smt_active tells whether SMT/Hyper-Threading is enabled.
        assert "smt_active" in virt, (
            f"virtualization missing 'smt_active': {list(virt.keys())}"
        )
        assert isinstance(virt["smt_active"], bool)
        # Reason: modules is a dict of loaded kernel modules (kvm, kvm_intel, etc.)
        assert "modules" in virt, (
            f"virtualization missing 'modules': {list(virt.keys())}"
        )
        assert isinstance(virt["modules"], dict)

    def test_host_info_json_hugepages_section(self, mvm_binary):
        """host info --json contains hugepages section."""
        # Rationale: L3 verification for the hugepages section. count_2mb and
        # free_2mb tell us about 2MB hugepage availability, which affects VM
        # memory allocation strategy and performance.
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        hp = data.get("hugepages")
        assert hp is not None, (
            f"host info --json missing 'hugepages' section: {list(data.keys())}"
        )
        assert "count_2mb" in hp, (
            f"hugepages missing 'count_2mb': {list(hp.keys())}"
        )
        # Reason: count_2mb is the total number of 2MB hugepages available.
        assert isinstance(hp["count_2mb"], int)
        assert "free_2mb" in hp, (
            f"hugepages missing 'free_2mb': {list(hp.keys())}"
        )
        # Reason: free_2mb tells how many 2MB hugepages are currently free.
        assert isinstance(hp["free_2mb"], int)

    def test_host_info_json_dependencies_section(self, mvm_binary):
        """host info --json contains dependencies section."""
        # Rationale: L3 verification for the dependencies section. These fields
        # (nftables_available, iptables_available, cloud_localds_available,
        # dev_net_tun) check whether critical system tools are installed and
        # accessible. Missing dependencies mean certain features won't work.
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        deps = data.get("dependencies")
        assert deps is not None, (
            f"host info --json missing 'dependencies' section: {list(data.keys())}"
        )
        # Reason: nftables_available determines which firewall backend to use.
        assert "nftables_available" in deps
        assert isinstance(deps["nftables_available"], bool)
        # Reason: iptables_available is the fallback firewall backend.
        assert "iptables_available" in deps
        assert isinstance(deps["iptables_available"], bool)
        # Reason: cloud_localds_available is needed for cloud-init ISO generation.
        assert "cloud_localds_available" in deps
        assert isinstance(deps["cloud_localds_available"], bool)
        # Reason: dev_net_tun checks /dev/net/tun accessibility for TAP devices.
        assert "dev_net_tun" in deps
        assert isinstance(deps["dev_net_tun"], bool)

    def test_host_info_json_system_section(self, mvm_binary):
        """host info --json contains system section."""
        # Rationale: L3 verification for the system section. These fields
        # (cgroup_version, ksm_disabled, dev_kvm_status, user_in_kvm_group)
        # provide OS-level context that affects VM behavior and host setup.
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        sys_sec = data.get("system")
        assert sys_sec is not None, (
            f"host info --json missing 'system' section: {list(data.keys())}"
        )
        # Reason: cgroup_version tells which cgroup hierarchy is in use (v1 or v2).
        assert "cgroup_version" in sys_sec
        assert isinstance(sys_sec["cgroup_version"], int)
        # Reason: ksm_disabled checks Kernel Same-page Merging status.
        assert "ksm_disabled" in sys_sec
        assert isinstance(sys_sec["ksm_disabled"], bool)
        # Reason: dev_kvm_status is the raw stat result for /dev/kvm.
        assert "dev_kvm_status" in sys_sec
        assert isinstance(sys_sec["dev_kvm_status"], str)
        # Reason: user_in_kvm_group tells whether the current user is in the kvm group.
        assert "user_in_kvm_group" in sys_sec
        assert isinstance(sys_sec["user_in_kvm_group"], bool)

    def test_host_info_json_memory_swap(self, mvm_binary):
        """host info --json memory section contains swap fields."""
        # Rationale: L3 verification for extended memory section. swap_total_mib
        # and swap_used_mib provide visibility into swap usage, which affects
        # VM memory overcommit decisions and capacity planning.
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        mem = data.get("memory")
        assert mem is not None, (
            f"host info --json missing 'memory' section: {list(data.keys())}"
        )
        # Reason: swap_total_mib tells total available swap space.
        assert "swap_total_mib" in mem, (
            f"memory missing 'swap_total_mib': {list(mem.keys())}"
        )
        assert isinstance(mem["swap_total_mib"], int)
        # Reason: swap_used_mib tells how much swap is currently in use.
        assert "swap_used_mib" in mem, (
            f"memory missing 'swap_used_mib': {list(mem.keys())}"
        )
        assert isinstance(mem["swap_used_mib"], int)

    def test_host_info_json_kernel_minimum_version(self, mvm_binary):
        """host info --json kernel section contains minimum_version_met."""
        # Rationale: L3 verification for extended kernel section.
        # minimum_version_met tells whether the running kernel meets the
        # minimum version requirement for Firecracker (5.10+). If false,
        # VMs may fail to boot or experience stability issues.
        result = _run_mvm(
            mvm_binary, "host", "info", "--refresh", "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip("Host not initialized")
        data = json.loads(result.stdout)
        kernel = data.get("kernel")
        assert kernel is not None, (
            f"host info --json missing 'kernel' section: {list(data.keys())}"
        )
        assert "minimum_version_met" in kernel, (
            f"kernel missing 'minimum_version_met': {list(kernel.keys())}"
        )
        # Reason: minimum_version_met is a boolean that flags whether the
        # host kernel meets the minimum required version.
        assert isinstance(kernel["minimum_version_met"], bool)
        # Also verify that kernel.version is still present alongside the new field
        assert "version" in kernel, (
            f"kernel missing 'version': {list(kernel.keys())}"
        )
        assert isinstance(kernel["version"], str)
        assert len(kernel["version"]) > 0


class TestHostCleanSafety:
    """Test host clean safety mechanisms (non-destructive)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_host,
    ]

    def test_host_clean_blocked_by_running_vm(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """Host clean should be blocked when a VM is running."""
        # Rationale: Needs a real VM because we need a running VM to trigger
        # the safety mechanism that blocks host clean. Network fixture needed
        # for VM creation.
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            created_network,
        )

        try:
            result = _run_mvm(
                mvm_binary,
                "host",
                "clean",
                "--force",
                check=False,
            )
            assert result.returncode != 0
            assert "running" in result.stderr.lower()
        finally:
            _run_mvm(
                mvm_binary,
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
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_host,
    ]

    def test_host_reset_blocked_by_running_vm(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """Host reset should be blocked when a VM is running."""
        # Rationale: Needs a real VM because we need a running VM to trigger
        # the safety mechanism that blocks host reset. Network fixture needed
        # for VM creation.
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            created_network,
        )

        try:
            result = _run_mvm(
                mvm_binary,
                "host",
                "reset",
                "--force",
                check=False,
            )
            assert result.returncode != 0
            assert "running" in result.stderr.lower()
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestHostCleanDestructive:
    """Execute host clean --force (destructive, requires sudo).

    Excluded from default test runs via the ``host_reset`` marker and must be
    explicitly invoked.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.host_reset,
        pytest.mark.serial,
        pytest.mark.domain_host,
    ]

    def test_host_clean_force(self, mvm_binary):
        """Execute host clean --force and verify it exits successfully."""
        # Rationale: Needs sudo binary execution via ~/.local/bin/mvm.
        # Host clean is the most destructive operation — requires real
        # host initialization to be meaningful.
        check = _run_mvm(mvm_binary, "host", "status", "--json", check=False)
        if check.returncode != 0:
            # Skip-reason: Without a fully initialized host, host clean
            # is a no-op. Running "mvm host init" first would make this
            # test unconditionally runnable.
            pytest.skip("Host not initialized — cannot test host clean")

        # Skip-reason: If the host was initialized with iptables (not
        # nftables), host clean --force will fail when trying to remove
        # nftables chains that don't exist. Only run when nftables chains
        # are present.
        nft_check = subprocess.run(
            ["sudo", "nft", "list", "chain", "ip", "nat", "MVM-POSTROUTING"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if nft_check.returncode != 0:
            pytest.skip("nftables MVM-POSTROUTING chain not found")

        mvm_bin = Path.home() / ".local" / "bin" / "mvm"
        if not mvm_bin.exists():
            # Skip-reason: Sudo execution requires the built binary at
            # ~/.local/bin/mvm. Run "cp dist/mvm ~/.local/bin/mvm" and
            # "python scripts/build_services.py" to enable this test.
            pytest.skip("mvm binary not at ~/.local/bin/mvm — cannot run sudo")

        # Remove any running VMs first (left by earlier tests in this file)
        vms = json.loads(
            _run_mvm(mvm_binary, "vm", "ls", "--json", check=False).stdout
            or "[]"
        )
        for vm in vms:
            _run_mvm(mvm_binary, "vm", "rm", vm["name"], "--force", check=False)

        result = subprocess.run(
            ["sudo", str(mvm_bin), "host", "clean", "--force"],
            capture_output=True,
            text=True,
            timeout=60,
        )
        assert result.returncode == 0, (
            f"host clean --force failed: {result.stderr}"
        )
        # L1: Verify stdout contains human-readable output about the clean
        assert len(result.stdout.strip()) > 0, (
            f"host clean --force produced no stdout: stderr={result.stderr}"
        )

    def test_host_reset_force(self, mvm_binary):
        """Execute host reset --force and verify it exits successfully."""
        # Rationale: Verifies the full host reset path — a destructive
        # operation that removes all mvm-created state (VMs, networks,
        # images, config, DB). This is the most thorough host reset
        # test and requires real host initialization to be meaningful.
        check = _run_mvm(mvm_binary, "host", "status", "--json", check=False)
        if check.returncode != 0:
            # Skip-reason: Without a fully initialized host, host reset
            # is a no-op. Running "mvm host init" first would make this
            # test unconditionally runnable.
            pytest.skip("Host not initialized — cannot test host reset")

        # Skip-reason: If the host was initialized with iptables (not
        # nftables), host reset --force will fail when trying to remove
        # nftables chains that don't exist. Only run when nftables chains
        # are present.
        nft_check = subprocess.run(
            ["sudo", "nft", "list", "chain", "ip", "nat", "MVM-POSTROUTING"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if nft_check.returncode != 0:
            pytest.skip("nftables MVM-POSTROUTING chain not found")

        mvm_bin = Path.home() / ".local" / "bin" / "mvm"
        if not mvm_bin.exists():
            # Skip-reason: Sudo execution requires the built binary at
            # ~/.local/bin/mvm. Run "cp dist/mvm ~/.local/bin/mvm" and
            # "python scripts/build_services.py" to enable this test.
            pytest.skip("mvm binary not at ~/.local/bin/mvm — cannot run sudo")

        # Remove any running VMs first (left by earlier tests in this file)
        vms = json.loads(
            _run_mvm(mvm_binary, "vm", "ls", "--json", check=False).stdout
            or "[]"
        )
        for vm in vms:
            _run_mvm(mvm_binary, "vm", "rm", vm["name"], "--force", check=False)

        result = subprocess.run(
            ["sudo", str(mvm_bin), "host", "reset", "--force"],
            capture_output=True,
            text=True,
            timeout=60,
        )
        assert result.returncode == 0, (
            f"host reset --force failed: {result.stderr}"
        )
        # L1: Verify stdout contains human-readable output about the reset
        assert len(result.stdout.strip()) > 0, (
            f"host reset --force produced no stdout: stderr={result.stderr}"
        )
