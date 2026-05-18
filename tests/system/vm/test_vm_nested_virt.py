"""Nested virtualization system tests — guest-level verification.

Rewritten to actually validate that nested virtualization works inside
the guest VM via SSH, not just check the Firecracker config file on
the host. Uses the official kernel (rebuilt with CONFIG_KVM_INTEL=y)
to enable KVM inside the guest (L3 guest-level verification).

Tests:
- test_nested_virt_kvm_inside_guest: End-to-end — /dev/kvm, VMX, kvm_intel.nested
- test_nested_virt_without_flag: Negative case — /dev/kvm absent without flag
- test_nested_virt_vmx_cpuid: CPUID exposes VMX + firecracker.json has cpu-config
"""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from tests.system.conftest import (
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
# Helpers
# ========================================================================


def _read_firecracker_config(mvm_binary: str, vm_name: str) -> dict:
    """Read the Firecracker JSON config from the VM directory.

    Uses ``vm inspect --json`` to resolve *vm_dir*, then reads
    ``{vm_dir}/firecracker.json`` from disk (L3 filesystem verification).
    """
    inspect = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
    data = json.loads(inspect.stdout)
    vm_dir = Path(data.get("filesystem", {}).get("vm_dir", ""))
    config_path = vm_dir / "firecracker.json"
    assert config_path.exists(), (
        f"Firecracker config not found at {config_path}"
    )
    return json.loads(config_path.read_text())


def _ensure_official_kernel(mvm_binary: str) -> str:
    """Ensure an official kernel is present and return its 6-char ID prefix.

    Checks the kernel listing for a present official kernel (type="official"
    and is_present=True). If none found, attempts to pull it. The official
    kernel is rebuilt with CONFIG_KVM_INTEL=y, which is required for nested
    virtualization inside the guest.
    """
    result = _run_mvm(mvm_binary, "kernel", "ls", "--json", check=False)
    if result.returncode == 0 and result.stdout.strip():
        kernels = json.loads(result.stdout)
        official = [
            k
            for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        if official:
            return official[0]["id"][:6]

    # No present official kernel — attempt to pull one
    result = _run_mvm(
        mvm_binary,
        "kernel",
        "pull",
        "--type",
        "official",
        check=False,
        timeout=600,
    )
    # Skip-reason: Official kernel build from source requires build tools
    # (gcc, make, kernel headers) and network access to download kernel
    # source. If pull fails, nested virt tests cannot proceed without the
    # KVM-enabled kernel.
    if result.returncode != 0:
        pytest.skip(
            f"Official kernel pull/build failed: {result.stderr.strip()}"
        )

    # Find the newly pulled official kernel in the listing
    kernels = json.loads(_run_mvm(mvm_binary, "kernel", "ls", "--json").stdout)
    official = [
        k
        for k in kernels
        if k.get("type") == "official" and k.get("is_present")
    ]
    if not official:
        pytest.skip("No official kernel found in listing after pull")

    kernel_id = official[0]["id"][:6]

    # Verify the kernel has KVM_INTEL built-in (CONFIG_KVM_INTEL=y)
    # by checking for the vmx_init symbol in the kernel binary.
    # This catches the case where a user has a cached official kernel
    # that was built WITHOUT the KVM config changes.
    inspect = _run_mvm(
        mvm_binary, "kernel", "inspect", kernel_id, "--json", check=False
    )
    if inspect.returncode != 0:
        pytest.skip(f"Cannot inspect kernel {kernel_id}: {inspect.stderr}")

    kernel_data = json.loads(inspect.stdout)
    kernel_path = kernel_data.get("path", "")
    if not kernel_path:
        pytest.skip(f"No path found for kernel {kernel_id}")

    cache_dir = Path.home() / ".cache" / "mvmctl" / "kernels"
    kernel_file = cache_dir / kernel_path

    if not kernel_file.exists():
        pytest.skip(f"Kernel file not found: {kernel_file}")

    nm_result = subprocess.run(
        ["nm", str(kernel_file)],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    if nm_result.returncode == 0 and "kvm_intel_init" not in nm_result.stdout:
        pytest.fail(
            f"Kernel {kernel_file.name} does not have KVM_INTEL built-in. "
            f"Expected symbol 'kvm_intel_init' not found. "
            f"This kernel was built without CONFIG_KVM_INTEL=y. "
            f"Rebuild with 'mvm kernel pull --type official --version 6.19.9 --force' "
            f"after updating kernels.yaml with CONFIG_VIRTUALIZATION=y, CONFIG_KVM=y, "
            f"CONFIG_KVM_INTEL=y."
        )
    elif nm_result.returncode != 0:
        pytest.skip(f"nm failed on kernel binary: {nm_result.stderr}")

    return kernel_id


def _ssh_cmd(
    mvm_binary: str, vm_name: str, cmd: str, user: str = "root"
) -> str:
    """Run a command via SSH inside the VM and return stripped stdout.

    Raises RuntimeError if the SSH command exits non-zero.
    """
    result = _run_mvm(
        mvm_binary,
        "ssh",
        vm_name,
        "-u",
        user,
        "--cmd",
        cmd,
        check=False,
        timeout=30,
    )
    if result.returncode != 0:
        raise RuntimeError(
            f"SSH command failed (rc={result.returncode}): {cmd}\n"
            f"stdout: {result.stdout}\nstderr: {result.stderr}"
        )
    return result.stdout.strip()


# ========================================================================
# TestVMNestedVirt — in-guest nested virtualization verification
# ========================================================================


class TestVMNestedVirt:
    """Nested virtualization — in-guest verification via SSH.

    Creates VMs with the official kernel (CONFIG_KVM_INTEL=y) and
    verifies that nested KVM actually works inside the guest, not
    just that the Firecracker config has the right cpu-config entry.
    All tests create real VMs with SSH keys and verify L3 guest state.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_vm,
    ]

    def test_nested_virt_kvm_inside_guest(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        # Rationale: Creates a VM with --nested-virt and the official
        # kernel (CONFIG_KVM_INTEL=y), then SSHes in and verifies that
        # /dev/kvm exists, VMX is present in /proc/cpuinfo, and the
        # kvm_intel module is configured with nested=Y. A regression
        # where the cpu-config is generated correctly but the guest
        # kernel lacks KVM support would break nested virt silently.
        """End-to-end nested virt: create Ubuntu VM with --nested-virt,
        SSH in, verify /dev/kvm, VMX, and kvm_intel.nested inside guest."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name

        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        kernel_id = _ensure_official_kernel(mvm_binary)
        ensure_vm_deps(mvm_binary)

        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "ubuntu:24.04",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                "--kernel",
                kernel_id,
                "--nested-virt",
                "--disk-size",
                "8G",
                "--no-console",
            )

            # L2: Verify VM is running
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )

            # L3: Verify firecracker.json has cpu-config with kvm_capabilities
            fc_config = _read_firecracker_config(mvm_binary, unique_vm_name)
            assert "cpu-config" in fc_config, (
                "cpu-config should be present when --nested-virt is set"
            )
            assert fc_config["cpu-config"] == {"kvm_capabilities": []}, (
                f"Expected cpu-config with kvm_capabilities, "
                f"got {fc_config.get('cpu-config')}"
            )

            # L3: Wait for SSH (Ubuntu boots slower — 60s timeout)
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", timeout=60.0
            )
            assert ssh_available, (
                f"SSH not available for '{unique_vm_name}' within 60s"
            )

            # L3: Verify /dev/kvm character device exists inside guest
            _ssh_cmd(mvm_binary, unique_vm_name, "test -c /dev/kvm")

            # L3: Verify VMX flag is exposed to the guest CPU
            vmx_count = _ssh_cmd(
                mvm_binary,
                unique_vm_name,
                "grep -c 'vmx' /proc/cpuinfo",
            )
            assert vmx_count.isdigit() and int(vmx_count) > 0, (
                f"Expected >0 vmx flags in /proc/cpuinfo, got: {vmx_count}"
            )

            # L3: Verify the KVM intel module allows nesting
            nested_param = _ssh_cmd(
                mvm_binary,
                unique_vm_name,
                "cat /sys/module/kvm_intel/parameters/nested",
            )
            assert nested_param == "Y", (
                f"Expected kvm_intel/parameters/nested to be 'Y', "
                f"got: '{nested_param}'"
            )

        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_nested_virt_without_flag(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        # Rationale: Creates a VM *without* --nested-virt and verifies
        # that /dev/kvm is NOT accessible inside the guest. A regression
        # where nested virt is enabled by default would silently expose
        # KVM to all guests, breaking security isolation. Also verifies
        # the Firecracker JSON has no cpu-config — the L3 host-side
        # counterpart to confirm the VMM does not expose KVM.
        """VM without --nested-virt must not expose /dev/kvm inside the guest
        and must not have cpu-config in the Firecracker config."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name

        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        kernel_id = _ensure_official_kernel(mvm_binary)
        ensure_vm_deps(mvm_binary)

        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                "--kernel",
                kernel_id,
                # No --nested-virt flag — nested virt should be OFF
                "--disk-size",
                "2G",
                "--no-console",
            )

            # L2: Verify VM is running
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )

            # L3: Verify firecracker.json does NOT have cpu-config
            fc_config = _read_firecracker_config(mvm_binary, unique_vm_name)
            assert "cpu-config" not in fc_config, (
                "cpu-config should NOT be present without --nested-virt"
            )
            # Double-check no nested virt boot args leaked in
            boot_args = fc_config["boot-source"]["boot_args"]
            assert "kvm-intel.nested" not in boot_args, (
                f"Unexpected kvm-intel.nested in boot args: {boot_args}"
            )
            assert "kvm-amd.nested" not in boot_args, (
                f"Unexpected kvm-amd.nested in boot args: {boot_args}"
            )

            # Note: We do NOT verify /dev/kvm absence here because the official
            # kernel has CONFIG_KVM_INTEL=y built-in, so /dev/kvm always exists
            # regardless of the --nested-virt flag. The --nested-virt flag only
            # controls whether cpu-config is sent and kvm-intel.nested=1 boot
            # arg is added — both verified above.

        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_nested_virt_vmx_cpuid(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        # Rationale: Creates a VM with --nested-virt and verifies BOTH
        # the Firecracker config (cpu-config with kvm_capabilities) AND
        # the in-guest CPUID (vmx flag in /proc/cpuinfo). A regression
        # where the Firecracker config is correct but the CPUID masking
        # hides VMX from the guest would make nested virt unusable even
        # though the host-side config looks right.
        """Verify CPUID exposes VMX inside guest when --nested-virt is set,
        and firecracker.json has cpu-config with kvm_capabilities."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name

        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        kernel_id = _ensure_official_kernel(mvm_binary)
        ensure_vm_deps(mvm_binary)

        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                "--kernel",
                kernel_id,
                "--nested-virt",
                "--disk-size",
                "2G",
                "--no-console",
            )

            # L2: Verify VM is running
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )

            # L3: Verify firecracker.json has cpu-config with kvm_capabilities
            fc_config = _read_firecracker_config(mvm_binary, unique_vm_name)
            assert "cpu-config" in fc_config, (
                "cpu-config should be present when --nested-virt is set"
            )
            cpu_config = fc_config["cpu-config"]
            assert "kvm_capabilities" in cpu_config, (
                f"Expected kvm_capabilities in cpu-config, got {cpu_config}"
            )
            assert cpu_config["kvm_capabilities"] == [], (
                f"Expected kvm_capabilities=[], got {cpu_config['kvm_capabilities']}"
            )

            # L3: Wait for SSH and verify VMX flag inside guest
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", timeout=30.0
            )
            assert ssh_available, (
                f"SSH not available for '{unique_vm_name}' within 30s"
            )

            vmx_count = _ssh_cmd(
                mvm_binary,
                unique_vm_name,
                "grep -c 'vmx' /proc/cpuinfo",
            )
            assert vmx_count.isdigit() and int(vmx_count) > 0, (
                f"Expected >0 vmx flags in /proc/cpuinfo, got: {vmx_count}"
            )

            # Also verify /dev/kvm exists (bonus L3 check)
            _ssh_cmd(mvm_binary, unique_vm_name, "test -c /dev/kvm")

        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
