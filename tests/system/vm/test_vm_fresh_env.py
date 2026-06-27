"""VM creation test mirroring the fresh_env.py approach — full pipeline verification.

This test exercises the full host-to-guest pipeline end-to-end: cache cleanup,
asset pulling (image, kernel, binary), VM creation with all bells and whistles,
volume attachment, SSH verification, and nested KVM verification inside the
guest — exactly what the fresh_env.py setup script does.
"""

from __future__ import annotations

import json

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
    pytest.mark.slow,
    pytest.mark.requires_kvm,
]

# ---------------------------------------------------------------------------
# Constants matching fresh_env.py
# ---------------------------------------------------------------------------
IMAGE_SELECTOR = "ubuntu:24.04"
KERNEL_SELECTOR = "official:7.0.11"
KERNEL_FEATURES = "kvm,nftables,tuntap"

VM_VCPUS = 6
VM_MEM = "4g"
VM_DISK = "8g"

KERNEL_PULL_TIMEOUT = 7200
IMAGE_PULL_TIMEOUT = 1800
VM_CREATE_TIMEOUT = 600
SSH_TIMEOUT = 120.0

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _check_nested_virt_support(runner_vm: str) -> bool:
    """Check if the test VM has nested VMX/SVM enabled.

    Reads the kvm params inside the test VM (NOT on the host).
    """
    for module in ("kvm_intel", "kvm_amd"):
        result = _guest_run(runner_vm,
            f"cat /sys/module/{module}/parameters/nested 2>/dev/null || echo absent",
            check=False,
        )
        if result.returncode == 0:
            value = result.stdout.strip().lower()
            if value in ("1", "y"):
                return True
    return False


def _ensure_image_cached(runner_vm: str, selector: str) -> str:
    """Ensure the image identified by *selector* is cached inside test VM."""
    result = _run_mvm(runner_vm, "image", "ls", "--json", check=False)
    if result.returncode == 0 and result.stdout.strip():
        images = json.loads(result.stdout)
        selector_type = selector.split(":")[0] if ":" in selector else selector
        cached = [
            i
            for i in images
            if i.get("type", "").startswith(selector_type)
            and i.get("is_present")
        ]
        if cached:
            return cached[0]["id"][:6]

    result = _run_mvm(
        runner_vm,
        "image",
        "pull",
        selector,
        check=False,
        timeout=IMAGE_PULL_TIMEOUT,
    )
    assert result.returncode == 0, (
        f"Failed to pull image '{selector}': {result.stderr.strip()}"
    )

    result = _run_mvm(runner_vm, "image", "ls", "--json")
    images = json.loads(result.stdout)
    selector_type = selector.split(":")[0] if ":" in selector else selector
    cached = [
        i
        for i in images
        if i.get("type", "").startswith(selector_type) and i.get("is_present")
    ]
    assert cached, f"Image '{selector}' not found in listing after pull"
    return cached[0]["id"][:6]


def _ensure_official_kernel_with_features(runner_vm: str) -> str:
    """Ensure an official kernel with KVM features is present inside test VM."""
    result = _run_mvm(runner_vm, "kernel", "ls", "--json", check=False)
    if result.returncode == 0 and result.stdout.strip():
        kernels = json.loads(result.stdout)
        official = [
            k
            for k in kernels
            if k.get("type") == "official"
            and k.get("version") == "7.0.11"
            and k.get("is_present")
        ]
        if official:
            return official[0]["id"][:6]

    result = _run_mvm(
        runner_vm,
        "kernel",
        "pull",
        KERNEL_SELECTOR,
        "--features",
        KERNEL_FEATURES,
        check=False,
        timeout=KERNEL_PULL_TIMEOUT,
    )
    assert result.returncode == 0, (
        f"Failed to pull kernel '{KERNEL_SELECTOR}' "
        f"with features '{KERNEL_FEATURES}': {result.stderr.strip()}"
    )

    result = _run_mvm(runner_vm, "kernel", "ls", "--json")
    kernels = json.loads(result.stdout)
    official = [
        k
        for k in kernels
        if k.get("type") == "official"
        and k.get("version") == "7.0.11"
        and k.get("is_present")
    ]
    assert official, f"Kernel '{KERNEL_SELECTOR}' not found in listing after pull"
    return official[0]["id"][:6]


def _ssh_cmd(
    runner_vm: str, vm_name: str, cmd: str, user: str = "root"
) -> str:
    """Run a command via SSH inside the VM and return stripped stdout."""
    result = _run_mvm(
        runner_vm,
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


def _read_firecracker_config(runner_vm: str, vm_name: str) -> dict:
    """Read the Firecracker JSON config from the VM directory inside the test VM."""
    inspect = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
    data = json.loads(inspect.stdout)
    vm_dir = data.get("filesystem", {}).get("vm_dir", "")

    # Read the file inside the test VM
    result = _guest_run(runner_vm,
        f"cat '{vm_dir}/firecracker.json'",
        timeout=30,
    )
    assert result.returncode == 0, (
        f"Firecracker config not found at {vm_dir}/firecracker.json"
    )
    return json.loads(result.stdout)


# ========================================================================
# TestFreshEnvVM
# ========================================================================


class TestFreshEnvVM:
    """Create a deeply-provisioned VM matching fresh_env.py exactly."""

    def test_fresh_env_vm_create_and_verify(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        """Full pipeline: pull assets, create VM, SSH, verify nested KVM inside guest."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name
        vm_name = unique_vm_name

        _ensure_image_cached(runner_vm, IMAGE_SELECTOR)
        kernel_id = _ensure_official_kernel_with_features(runner_vm)
        ensure_vm_deps(runner_vm)

        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )

        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                IMAGE_SELECTOR,
                "--kernel",
                kernel_id,
                "--vcpu",
                str(VM_VCPUS),
                "--mem",
                VM_MEM,
                "-s",
                VM_DISK,
                "--nested-virt",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                timeout=VM_CREATE_TIMEOUT,
            )

            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
            assert vm_entry["vcpu_count"] == VM_VCPUS
            assert vm_entry["mem_size_mib"] == 4096

            ssh_ok = wait_for_ssh(
                runner_vm, vm_name, "root", timeout=SSH_TIMEOUT
            )
            assert ssh_ok, f"SSH not available for '{vm_name}' within {SSH_TIMEOUT}s"

            _ssh_cmd(runner_vm, vm_name, "test -c /dev/kvm")

            vmx_count = _ssh_cmd(
                runner_vm, vm_name, "grep -c 'vmx' /proc/cpuinfo",
            )
            assert vmx_count.isdigit() and int(vmx_count) > 0

            nested_param = _ssh_cmd(
                runner_vm, vm_name,
                "cat /sys/module/kvm_intel/parameters/nested 2>/dev/null "
                "|| cat /sys/module/kvm_amd/parameters/nested 2>/dev/null "
                "|| echo 'absent'",
            )
            assert nested_param in ("Y", "1")
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_fresh_env_vm_with_volume(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        """Create VM, attach volume, verify volume status via ls --json."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name
        vm_name = unique_vm_name
        vol_name = f"{unique_vm_name}-vol"

        _ensure_image_cached(runner_vm, IMAGE_SELECTOR)
        kernel_id = _ensure_official_kernel_with_features(runner_vm)
        ensure_vm_deps(runner_vm)

        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )

        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                IMAGE_SELECTOR,
                "--kernel",
                kernel_id,
                "--vcpu",
                str(VM_VCPUS),
                "--mem",
                VM_MEM,
                "-s",
                VM_DISK,
                "--nested-virt",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                timeout=VM_CREATE_TIMEOUT,
            )

            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry["status"] == "running"

            _run_mvm(runner_vm, "vm", "stop", vm_name)

            _run_mvm(runner_vm, "vol", "create", vol_name, "8g")

            _run_mvm(runner_vm, "volume", "attach", vm_name, vol_name)

            _run_mvm(runner_vm, "vm", "start", vm_name)

            vols = json.loads(
                _run_mvm(runner_vm, "vol", "ls", "--json").stdout
            )
            vol_entry = next(
                (v for v in vols if v.get("name") == vol_name), None
            )
            assert vol_entry is not None
            vol_status = (vol_entry.get("status", "") or "").lower()
            assert vol_status == "attached"

            attached_vm = vol_entry.get("vm_id", "") or vol_entry.get(
                "attached_to", ""
            )
            assert attached_vm
        finally:
            _run_mvm(runner_vm, "vm", "stop", vm_name, check=False)
            _run_mvm(runner_vm, "vol", "rm", vol_name, "--force", check=False)
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_fresh_env_vm_specs_verified(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        """Create VM with specific flags and verify every config via inspect --json."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name
        vm_name = unique_vm_name

        _ensure_image_cached(runner_vm, IMAGE_SELECTOR)
        kernel_id = _ensure_official_kernel_with_features(runner_vm)
        ensure_vm_deps(runner_vm)

        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )

        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                IMAGE_SELECTOR,
                "--kernel",
                kernel_id,
                "--vcpu",
                str(VM_VCPUS),
                "--mem",
                VM_MEM,
                "-s",
                VM_DISK,
                "--nested-virt",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                timeout=VM_CREATE_TIMEOUT,
            )

            result = _run_mvm(runner_vm, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry["vcpu_count"] == VM_VCPUS
            assert vm_entry["mem_size_mib"] == 4096
            assert vm_entry["disk_size_mib"] == 8192

            inspect_result = _run_mvm(
                runner_vm, "vm", "inspect", vm_name, "--json"
            )
            inspect_data = json.loads(inspect_result.stdout)
            resources = inspect_data.get("resources", {})
            assert resources.get("vcpu") == VM_VCPUS
            assert resources.get("mem") == 4096

            vm_dir = inspect_data.get("filesystem", {}).get("vm_dir", "")
            assert vm_dir, "VM directory not found in inspect output"

            # Verify firecracker config inside test VM
            fc_config_json = _guest_run(runner_vm,
                f"cat '{vm_dir}/firecracker.json'",
            )
            fc_config = json.loads(fc_config_json.stdout)
            assert "cpu-config" in fc_config, (
                "cpu-config should be present in firecracker.json "
                "when --nested-virt is set"
            )
            assert (
                fc_config.get("cpu-config", {}) == {}
                or fc_config["cpu-config"] == {"kvm_capabilities": []}
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)

    def test_vm_without_nested_virt_flag(
        self,
        runner_vm: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        """VM without --nested-virt must not have cpu-config in firecracker.json."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name
        vm_name = unique_vm_name

        _ensure_image_cached(runner_vm, IMAGE_SELECTOR)
        kernel_id = _ensure_official_kernel_with_features(runner_vm)
        ensure_vm_deps(runner_vm)

        _run_mvm(
            runner_vm,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        _run_mvm(
            runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
        )

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
                "--ssh-key",
                key_name,
                "--kernel",
                kernel_id,
                timeout=VM_CREATE_TIMEOUT,
            )

            vms = json.loads(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry["status"] == "running"

            fc_config = _read_firecracker_config(runner_vm, vm_name)
            assert "cpu-config" not in fc_config
            boot_args = fc_config["boot-source"]["boot_args"]
            assert "kvm-intel.nested" not in boot_args
            assert "kvm-amd.nested" not in boot_args
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                runner_vm, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)
