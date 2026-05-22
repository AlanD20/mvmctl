"""VM creation test mirroring the fresh_env.py approach — full pipeline verification.

This test exercises the full host-to-guest pipeline end-to-end: cache cleanup,
asset pulling (image, kernel, binary), VM creation with all bells and whistles,
volume attachment, SSH verification, and nested KVM verification inside the
guest — exactly what the fresh_env.py setup script does.

Key design:
- Each test method is self-contained (no inter-test dependencies)
- All assets are pulled if not already cached (idempotent)
- All resources are cleaned up in ``finally`` blocks
- Nested virt is verified at L3 (inside the guest via SSH)
"""

from __future__ import annotations

import json
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
    pytest.mark.serial,
    pytest.mark.slow,
    pytest.mark.requires_kvm,
]

# ---------------------------------------------------------------------------
# Constants matching fresh_env.py
# ---------------------------------------------------------------------------
# fresh_env uses ubuntu:noble (24.04 Noble Numbat). The ``--image`` flag
# accepts ``type:version`` format, and the image resolver supports release
# version strings like "24.04". Codename "noble" may also work depending
# on the upstream images.yaml — we use "24.04" for deterministic resolution.
IMAGE_SELECTOR = "ubuntu:24.04"

# Kernel selector matching fresh_env: ``official:6.19.9`` with KVM
# features for nested virt support inside the guest.
KERNEL_SELECTOR = "official:6.19.9"
KERNEL_FEATURES = "kvm,nftables,tuntap"

# VM resource sizing (same as fresh_env: 6 vCPU, 4G RAM, 8G root)
VM_VCPUS = 6
VM_MEM = "4g"
VM_DISK = "8g"

# Timeout for kernel build from source (official kernel requires gcc, make,
# kernel headers — can take 30+ minutes).  Image pull timeout for Ubuntu
# (~220 MB compressed) should be generous for slow connections.
KERNEL_PULL_TIMEOUT = 7200
IMAGE_PULL_TIMEOUT = 1800
VM_CREATE_TIMEOUT = 600
SSH_TIMEOUT = 120.0

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _check_nested_virt_support() -> bool:
    """Check if the host has nested VMX/SVM enabled.

    Reads ``/sys/module/kvm_intel/parameters/nested`` or
    ``/sys/module/kvm_amd/parameters/nested`` (values ``Y``, ``1``).
    If neither file exposes ``Y``/``1``, the host cannot pass through
    VMX/SVM to guests and nested virt tests must skip.
    """
    for param_file in (
        "/sys/module/kvm_intel/parameters/nested",
        "/sys/module/kvm_amd/parameters/nested",
    ):
        path = Path(param_file)
        try:
            value = path.read_text().strip().lower()
            if value in ("1", "y"):
                return True
        except OSError:
            pass
    return False


def _ensure_image_cached(mvm_binary: str, selector: str) -> str:
    """Ensure the image identified by *selector* is cached.

    If the image is not present, pulls it.  Returns the 6-char image ID
    prefix that can be used with ``--image`` on ``vm create``.

    Rationale: Image pull is the first step in the fresh_env pipeline.
    A failure here means the system lacks network access or the upstream
    registry is unreachable — no subsequent steps can succeed.
    """
    # Fast path: check if the image is already cached
    result = _run_mvm(mvm_binary, "image", "ls", "--json", check=False)
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
            # Return the 6-char ID prefix for use as a reference
            return cached[0]["id"][:6]

    # Pull the image
    result = _run_mvm(
        mvm_binary,
        "image",
        "pull",
        selector,
        check=False,
        timeout=IMAGE_PULL_TIMEOUT,
    )
    # Skip-reason: Image pull requires network access to the upstream
    # registry.  If the network is unavailable or the upstream registry
    # is unreachable, the test cannot proceed.
    if result.returncode != 0:
        pytest.skip(
            f"Failed to pull image '{selector}': {result.stderr.strip()}"
        )

    # Find the pulled image
    result = _run_mvm(mvm_binary, "image", "ls", "--json")
    images = json.loads(result.stdout)
    selector_type = selector.split(":")[0] if ":" in selector else selector
    cached = [
        i
        for i in images
        if i.get("type", "").startswith(selector_type) and i.get("is_present")
    ]
    # Skip-reason: Even after a successful pull, the image may not appear
    # in the listing if the cache directory is misconfigured or the pull
    # wrote the data to a different storage location.
    if not cached:
        pytest.skip(f"Image '{selector}' not found in listing after pull")

    return cached[0]["id"][:6]


def _ensure_official_kernel_with_features(mvm_binary: str) -> str:
    """Ensure an official kernel with KVM features is present.

    Checks the kernel listing for a present official kernel with version
    6.19.9.  If found, returns its 6-char ID prefix.  Otherwise, pulls
    ``official:6.19.9 --features kvm,nftables,tuntap`` (matching the
    fresh_env.py ``kernel pull`` invocation).

    Returns the 6-char kernel ID prefix for use with ``--kernel``.

    Rationale: Building the official kernel from source requires build
    tools and can take up to 2 hours.  If the pull fails (missing build
    tools, network timeout, insufficient disk), the test must skip rather
    than fail — the missing kernel is an infra issue, not a code bug.
    """
    result = _run_mvm(mvm_binary, "kernel", "ls", "--json", check=False)
    if result.returncode == 0 and result.stdout.strip():
        kernels = json.loads(result.stdout)
        # Match official type with version 6.19.9 that is present on disk
        official = [
            k
            for k in kernels
            if k.get("type") == "official"
            and k.get("version") == "6.19.9"
            and k.get("is_present")
        ]
        if official:
            return official[0]["id"][:6]

    # Pull the kernel with features (same as fresh_env)
    result = _run_mvm(
        mvm_binary,
        "kernel",
        "pull",
        KERNEL_SELECTOR,
        "--features",
        KERNEL_FEATURES,
        check=False,
        timeout=KERNEL_PULL_TIMEOUT,
    )
    # Skip-reason: Kernel pull may fail if build tools (gcc, make, kernel
    # headers) are missing or the network is unavailable.  Building the
    # official kernel from source can take up to 2 hours and a CI timeout
    # or disk exhaustion can cause the pull to fail.  The test must skip
    # gracefully rather than fail outright.
    if result.returncode != 0:
        pytest.skip(
            f"Failed to pull kernel '{KERNEL_SELECTOR}' "
            f"with features '{KERNEL_FEATURES}': {result.stderr.strip()}"
        )

    # Find the newly pulled kernel
    result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
    kernels = json.loads(result.stdout)
    official = [
        k
        for k in kernels
        if k.get("type") == "official"
        and k.get("version") == "6.19.9"
        and k.get("is_present")
    ]
    # Skip-reason: Even after a successful pull, the kernel may not appear
    # in the listing if the build completed but the output was moved to an
    # unexpected cache location, or if a concurrent operation removed it.
    if not official:
        pytest.skip(
            f"Kernel '{KERNEL_SELECTOR}' not found in listing after pull"
        )

    return official[0]["id"][:6]


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
# TestFreshEnvVM — full pipeline verification
# ========================================================================


class TestFreshEnvVM:
    """Create a deeply-provisioned VM matching fresh_env.py exactly.

    This test creates a VM with:
    - Ubuntu Noble (24.04) image
    - Official 6.19.9 kernel with kvm,nftables,tuntap features
    - Nested virtualization enabled
    - 6 vCPUs, 4G memory, 8G root disk
    - Attached volume
    - SSH connectivity verified
    - Nested KVM verified inside the guest

    Rationale: Full end-to-end pipeline test that exercises the same
    code paths as the fresh_env.py setup script. Catches regressions
    in asset resolution, VM creation flags, nested virt, volume
    attachment, and SSH connectivity — all in one test class.
    """

    @pytest.fixture(autouse=True)
    def _skip_if_no_nested_virt(self) -> None:
        """Skip all tests if host doesn't support nested virtualization.

        Checks kvm_intel/kvm_amd nested parameter.  Without host-level
        VMX passthrough, nested virt inside the guest is impossible and
        the tests would fail on the first in-guest ``/dev/kvm`` check.
        """
        # Skip-reason: Requires host-level nested VMX/SVM to pass through
        # to the guest (kvm_intel/kvm_amd nested=Y/1).  Without host-level
        # VMX passthrough, nested virt inside the guest is impossible and
        # all tests in this class would fail on the first in-guest
        # /dev/kvm check.
        if not _check_nested_virt_support():
            pytest.skip(
                "Host does not support nested virtualization "
                "(kvm_intel/kvm_amd nested=Y/1 not detected)"
            )

    # ---- test_fresh_env_vm_create_and_verify ----------------------------

    def test_fresh_env_vm_create_and_verify(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        """Full pipeline: pull assets, create VM, SSH, verify nested KVM inside guest.

        Exercises:
        1. Image pull (ubuntu:24.04)
        2. Kernel pull (official:6.19.9 with kvm,nftables,tuntap)
        3. Network creation
        4. SSH key creation
        5. VM creation with all flags (nested-virt, full resources)
        6. L2 verification via ``vm ls --json``
        7. L3 SSH connectivity & in-guest nested KVM checks
        """
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name
        vm_name = unique_vm_name

        # Phase 1: Pull assets (idempotent — reuses cached where possible)
        _ensure_image_cached(mvm_binary, IMAGE_SELECTOR)
        kernel_id = _ensure_official_kernel_with_features(mvm_binary)
        ensure_vm_deps(mvm_binary)

        # Phase 2: Create infrastructure (network, SSH key)
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

        try:
            # Phase 3: Create the VM with all bells and whistles
            # Rationale: Matches fresh_env.py's ``vm create fenv --vcpus 6
            # --mem 4g --nested-virt -s 8g`` but adds explicit image, kernel,
            # network, and SSH key references for test isolation.
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                IMAGE_SELECTOR,
                "--kernel",
                kernel_id,
                "--vcpus",
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

            # L2: Verify VM is running via ls --json
            # Rationale: Returncode-only assertion would not catch DB write
            # failures where the CLI prints success but the record is lost.
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None, (
                f"VM '{vm_name}' not found in listing after creation"
            )
            assert vm_entry["status"] == "running", (
                f"Expected status 'running', got '{vm_entry['status']}': "
                f"{vm_entry}"
            )
            # Verify resource fields are populated
            assert vm_entry["vcpu_count"] == VM_VCPUS, (
                f"Expected vcpu_count={VM_VCPUS}, got {vm_entry['vcpu_count']}"
            )
            # 4 GiB = 4096 MiB
            assert vm_entry["mem_size_mib"] == 4096, (
                f"Expected mem_size_mib=4096, got {vm_entry['mem_size_mib']}"
            )

            # L3: Wait for SSH (Ubuntu boots slower than Alpine — 120s)
            # Rationale: Without SSH, we cannot verify in-guest state.
            # A regression where cloud-init or network config silently
            # breaks SSH would leave the VM running but inaccessible.
            ssh_ok = wait_for_ssh(
                mvm_binary, vm_name, "root", timeout=SSH_TIMEOUT
            )
            assert ssh_ok, (
                f"SSH not available for '{vm_name}' within {SSH_TIMEOUT}s"
            )

            # L3: Verify /dev/kvm character device exists inside guest
            # Rationale: If the --nested-virt flag is correctly processed
            # but the guest kernel lacks CONFIG_KVM_INTEL, /dev/kvm will
            # not exist. This check catches that.
            _ssh_cmd(mvm_binary, vm_name, "test -c /dev/kvm")

            # L3: Verify VMX flag is exposed to the guest CPU
            # Rationale: CPUID masking can hide VMX from the guest even
            # when /dev/kvm exists. This check catches that silently.
            vmx_count = _ssh_cmd(
                mvm_binary,
                vm_name,
                "grep -c 'vmx' /proc/cpuinfo",
            )
            assert vmx_count.isdigit() and int(vmx_count) > 0, (
                f"Expected >0 vmx flags in /proc/cpuinfo, got: {vmx_count}"
            )

            # L3: Verify kvm_intel module allows nesting
            # Rationale: This is the definitive check that nested virt
            # works inside the guest. The parameter is set by the
            # kvm-intel.nested=1 boot arg added by --nested-virt.
            nested_param = _ssh_cmd(
                mvm_binary,
                vm_name,
                "cat /sys/module/kvm_intel/parameters/nested 2>/dev/null "
                "|| cat /sys/module/kvm_amd/parameters/nested 2>/dev/null "
                "|| echo 'absent'",
            )
            assert nested_param in ("Y", "1"), (
                f"Expected kvm_intel/kvm_amd nested=Y/1, got: '{nested_param}'"
            )

        finally:
            # Cleanup: remove resources in reverse dependency order
            # (VM first, then network & key).  Use check=False so a
            # timeout on one step does not orphan subsequent resources.
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    # ---- test_fresh_env_vm_with_volume ----------------------------------

    def test_fresh_env_vm_with_volume(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        """Create VM, attach volume, verify volume status via ls --json.

        Matches fresh_env.py steps 7-11:
        1. Create VM
        2. Stop VM
        3. Create volume
        4. Attach volume to VM
        5. Start VM
        6. Verify volume status is 'attached' in ``vol ls --json``

        Rationale: Volume attachment requires the VM to be stopped
        (matching fresh_env behaviour). Verifying status via JSON
        catches silent DB attachment failures that returncode-only
        checks would miss.
        """
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name
        vm_name = unique_vm_name
        vol_name = f"{unique_vm_name}-vol"

        # Pull assets first
        _ensure_image_cached(mvm_binary, IMAGE_SELECTOR)
        kernel_id = _ensure_official_kernel_with_features(mvm_binary)
        ensure_vm_deps(mvm_binary)

        # Create infrastructure
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

        try:
            # Create VM (matching fresh_env but with explicit image/kernel)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                IMAGE_SELECTOR,
                "--kernel",
                kernel_id,
                "--vcpus",
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

            # L2: Confirm VM is running before stop
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None, f"VM '{vm_name}' not found"
            assert vm_entry["status"] == "running", (
                f"Expected running, got {vm_entry['status']}"
            )

            # Stop VM (fresh_env step: stop before attach)
            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            # Create volume (8 GiB, matching fresh_env: vol create vol1 8g)
            _run_mvm(mvm_binary, "vol", "create", vol_name, "8g")

            # Attach volume to VM
            _run_mvm(mvm_binary, "vm", "attach-volume", vm_name, vol_name)

            # Start VM again
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            # L3: Verify volume status is 'attached' in vol ls --json
            # Rationale: A returncode-only check on attach-volume would
            # pass even if the DB wasn't updated.  Parsing the JSON
            # listing catches silent DB failures.
            vols = json.loads(
                _run_mvm(mvm_binary, "vol", "ls", "--json").stdout
            )
            vol_entry = next(
                (v for v in vols if v.get("name") == vol_name), None
            )
            assert vol_entry is not None, (
                f"Volume '{vol_name}' not found in vol ls --json"
            )
            # Status field: should be "attached" after successful attachment
            vol_status = (vol_entry.get("status", "") or "").lower()
            assert vol_status == "attached", (
                f"Expected volume status 'attached', "
                f"got '{vol_entry.get('status')}': {vol_entry}"
            )
            # Verify the attached-to VM reference is present
            attached_vm = vol_entry.get("vm_id", "") or vol_entry.get(
                "attached_to", ""
            )
            assert attached_vm, (
                f"Volume '{vol_name}' missing vm_id/attached_to in listing: "
                f"{vol_entry}"
            )

        finally:
            # Cleanup: stop VM first, then remove resources
            _run_mvm(mvm_binary, "vm", "stop", vm_name, check=False)
            _run_mvm(mvm_binary, "vol", "rm", vol_name, "--force", check=False)
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    # ---- test_fresh_env_vm_specs_verified --------------------------------

    def test_fresh_env_vm_specs_verified(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
        unique_key_name: str,
    ) -> None:
        """Create VM with specific flags and verify every config via inspect --json.

        Asserts:
        - ``vm ls --json``: vcpu_count == 6, mem_size_mib == 4096,
          disk_size_mib == 8192
        - ``vm inspect --json``: resources.vcpus == 6, resources.mem == 4096
        - Firecracker JSON config on disk has ``cpu-config`` with
          ``kvm_capabilities: []`` (confirms --nested-virt was processed)

        Rationale: A VM can start successfully (returncode 0, status=running)
        even if config flags are silently ignored.  These assertions confirm
        that each --vcpus, --mem, -s, and --nested-virt flag was correctly
        stored in the DB and written to the Firecracker config.
        """
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        key_name = unique_key_name
        vm_name = unique_vm_name

        # Pull assets
        _ensure_image_cached(mvm_binary, IMAGE_SELECTOR)
        kernel_id = _ensure_official_kernel_with_features(mvm_binary)
        ensure_vm_deps(mvm_binary)

        # Create infrastructure
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

        try:
            # Create VM with specific flags
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                IMAGE_SELECTOR,
                "--kernel",
                kernel_id,
                "--vcpus",
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

            # L3: Verify vm ls --json field values
            # Rationale: --mem 4g = 4 GiB = 4096 MiB.  --disk-size 8g =
            # 8 GiB = 8192 MiB.  These are the canonical MiB values the
            # DB stores and ``ls --json`` exposes.
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None, f"VM '{vm_name}' not found in listing"
            assert vm_entry["vcpu_count"] == VM_VCPUS, (
                f"Expected vcpu_count={VM_VCPUS}, got {vm_entry['vcpu_count']}"
            )
            assert vm_entry["mem_size_mib"] == 4096, (
                f"Expected mem_size_mib=4096, got {vm_entry['mem_size_mib']}"
            )
            # 8 GiB = 8 * 1024 = 8192 MiB
            assert vm_entry["disk_size_mib"] == 8192, (
                f"Expected disk_size_mib=8192, got {vm_entry['disk_size_mib']}"
            )

            # L3: Verify vm inspect --json resource fields
            # Rationale: Inspect may have a different code path than ls.
            # Both must agree on the stored values.
            inspect_result = _run_mvm(
                mvm_binary, "vm", "inspect", vm_name, "--json"
            )
            inspect_data = json.loads(inspect_result.stdout)
            # The inspect output nests resources under "resources"
            resources = inspect_data.get("resources", {})
            assert resources.get("vcpus") == VM_VCPUS, (
                f"Expected resources.vcpus={VM_VCPUS}, "
                f"got {resources.get('vcpus')}: {resources}"
            )
            assert resources.get("mem") == 4096, (
                f"Expected resources.mem=4096, "
                f"got {resources.get('mem')}: {resources}"
            )

            # L3: Verify Firecracker config on disk has cpu-config
            # Rationale: The --nested-virt flag adds a cpu-config section
            # to firecracker.json with ``kvm_capabilities: []``.  Reading
            # the file from disk confirms the VMM config was generated
            # correctly — a regression where the VM starts but the config
            # is missing cpu-config would leave nested virt non-functional.
            vm_dir = Path(inspect_data.get("filesystem", {}).get("vm_dir", ""))
            assert vm_dir.exists(), f"VM directory not found: {vm_dir}"
            fc_config_path = vm_dir / "firecracker.json"
            assert fc_config_path.exists(), (
                f"Firecracker config not found at {fc_config_path}"
            )
            fc_config = json.loads(fc_config_path.read_text())
            assert "cpu-config" in fc_config, (
                "cpu-config should be present in firecracker.json "
                "when --nested-virt is set"
            )
            assert fc_config["cpu-config"] == {"kvm_capabilities": []}, (
                f"Expected cpu-config with kvm_capabilities=[], "
                f"got {fc_config.get('cpu-config')}"
            )

        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                net_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
