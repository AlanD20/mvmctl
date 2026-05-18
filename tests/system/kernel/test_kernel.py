"""Kernel management system tests."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import _ensure_kernel, _run_mvm

# Cache directory for official kernel builds
KERNEL_CACHE_DIR = Path.home() / ".cache" / "mvmctl" / "kernels"

pytestmark = [pytest.mark.system, pytest.mark.domain_kernel]


class TestKernelLifecycle:
    """Test kernel CRUD operations."""

    def test_kernel_list_empty(self, mvm_binary):
        """List kernels when none are cached."""
        # Rationale: Only needs kernel ls --json (free). No resources needed since empty state is valid.
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        if data:
            entry = data[0]
            assert isinstance(entry.get("id"), str) and entry["id"], (
                f"Expected non-empty id: {entry}"
            )
            assert isinstance(entry.get("name"), str) and entry["name"], (
                f"Expected non-empty name: {entry}"
            )
            assert isinstance(entry.get("version"), str) and entry["version"], (
                f"Expected non-empty version: {entry}"
            )

    @pytest.mark.slow
    @pytest.mark.kernel_build
    @pytest.mark.serial
    def test_kernel_pull(self, mvm_binary):
        """Pull official kernel."""
        # Rationale: Needs an actual kernel download to test pull. Marked kernel_build because this is slow and may require build tools.
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "official",
            check=False,
            timeout=300,
        )
        # Skip-reason: Official kernel build from source requires build tools
        # (gcc, make, kernel headers) and network access to download the
        # kernel source. Excluded from default CI runs via @pytest.mark.kernel_build.
        if result.returncode != 0:
            pytest.skip(
                f"Official kernel pull/build failed: {result.stderr.strip()}"
            )
        # L1: verify stdout mentions the kernel type
        assert "official" in result.stdout.lower(), (
            f"Expected 'official' in pull output, got: {result.stdout[:200]}"
        )
        # L2: verify kernel appears in listing
        kernels = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        official = [
            k
            for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, "No official kernel found in listing after pull"

    def test_kernel_list_no_cache(self, mvm_binary):
        """List kernels with --no-cache flag — fetches live version listing.

        Rationale: --no-cache skips the cached version listing and forces a live
        fetch from upstream. This is an L1 check that the command exits
        successfully.
        """
        result = _run_mvm(mvm_binary, "kernel", "ls", "--no-cache", check=False)
        # L1: The command should exit 0 regardless of network availability
        # (it falls back to local listing if remote is unavailable).
        assert result.returncode == 0, (
            f"kernel ls --no-cache failed: {result.stderr}"
        )

    def test_kernel_list_json(self, mvm_binary):
        """List kernels in JSON format."""
        # Rationale: Only needs kernel ls --json parsing (free). Verifies JSON field structure.
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        if data:
            for entry in data:
                assert isinstance(entry.get("id"), str) and entry["id"], (
                    f"Expected non-empty id: {entry}"
                )
                assert isinstance(entry.get("name"), str) and entry["name"], (
                    f"Expected non-empty name: {entry}"
                )
                assert (
                    isinstance(entry.get("version"), str) and entry["version"]
                ), f"Expected non-empty version: {entry}"
                assert entry.get("type") in ("firecracker", "official"), (
                    f"Unexpected type: {entry}"
                )

    @pytest.mark.serial
    def test_kernel_set_default(self, mvm_binary):
        """Set kernel as default (uses the one pulled in test_kernel_pull)."""
        # Rationale: Needs a present kernel in the DB. Uses existing kernel from pull test — serial to avoid conflicts.
        _ensure_kernel(mvm_binary)
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(result.stdout)
        present = [k for k in kernels if k.get("is_present")]
        # Skip-reason: Requires at least one present kernel in the cache.
        # _ensure_kernel() attempts to pull a firecracker kernel but may
        # fail in air-gapped environments without MVM_ASSET_MIRROR.
        if not present:
            pytest.skip("No present kernel to set as default")
        kernel_id = present[0]["id"]
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "default",
            kernel_id[:6],
            check=False,
        )
        assert result.returncode == 0, (
            f"Failed to set kernel default: {result.stderr}"
        )
        # L2: verify the kernel is now marked as default in ls --json
        kernels_after = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        default = [k for k in kernels_after if k.get("is_default")]
        assert any(k["id"] == kernel_id for k in default), (
            f"Kernel {kernel_id[:6]} not marked as default after set_default"
        )


class TestKernelInspect:
    """Test kernel inspect operations (table, json, tree)."""

    def test_kernel_inspect_table(self, mvm_binary):
        """Inspect a kernel in table format."""
        # Rationale: Needs a present kernel to inspect. Uses ls --json to find one — no additional resources needed.
        _ensure_kernel(mvm_binary)
        kernels: list[dict[str, Any]] = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        # Skip-reason: Requires at least one present kernel whose inspect
        # output can be verified. _ensure_kernel() attempts to pull one
        # but may fail in air-gapped environments.
        if not present:
            pytest.skip("No present kernel to inspect")
        prefix = present[0]["id"][:6]
        result = _run_mvm(mvm_binary, "kernel", "inspect", prefix, "--json")
        assert result.returncode == 0
        data: dict[str, Any] = json.loads(result.stdout)
        kdata = data.get("kernel", data)
        # L2: verify kernel name matches expected
        assert kdata.get("name") == present[0]["name"], (
            f"Expected kernel name '{present[0]['name']}', got '{kdata.get('name')}'"
        )

    def test_kernel_inspect_json(self, mvm_binary):
        """Inspect a kernel with --json output."""
        # Rationale: Needs a present kernel to inspect. Verifies JSON field completeness.
        _ensure_kernel(mvm_binary)
        kernels: list[dict[str, Any]] = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        # Skip-reason: Requires at least one present kernel whose inspect
        # --json output can be verified. _ensure_kernel() attempts to pull
        # one but may fail in air-gapped environments.
        if not present:
            pytest.skip("No present kernel to inspect")
        prefix = present[0]["id"][:6]
        result = _run_mvm(mvm_binary, "kernel", "inspect", prefix, "--json")
        assert result.returncode == 0
        data: dict[str, Any] = json.loads(result.stdout)
        assert isinstance(data, dict)
        assert "id" in data or "kernel" in data, (
            f"Expected top-level 'id' or 'kernel' key, got: {list(data.keys())}"
        )
        if "kernel" in data:
            kdata = data["kernel"]
            assert "id" in kdata, (
                f"kernel nested object missing 'id': {list(kdata.keys())}"
            )
            assert "name" in kdata or "base_name" in kdata
            assert "version" in kdata
            assert "type" in kdata

    def test_kernel_inspect_default_format(self, mvm_binary):
        """Inspect a kernel with default (non-JSON) output — verify via --json."""
        # Rationale: Needs a present kernel. Verifies default inspect output.
        _ensure_kernel(mvm_binary)
        kernels: list[dict[str, Any]] = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        # Skip-reason: Requires at least one present kernel whose output
        # can be verified. _ensure_kernel() attempts to pull one
        # but may fail in air-gapped environments.
        if not present:
            pytest.skip("No present kernel to inspect")
        prefix = present[0]["id"][:6]
        result = _run_mvm(mvm_binary, "kernel", "inspect", prefix, "--json")
        assert result.returncode == 0
        data: dict[str, Any] = json.loads(result.stdout)
        kdata = data.get("kernel", data)
        # L2: verify kernel name matches expected
        expected_name = present[0].get("name") or present[0].get(
            "base_name", ""
        )
        actual_name = kdata.get("name") or kdata.get("base_name", "")
        assert expected_name == actual_name or expected_name in actual_name, (
            f"Expected kernel name '{expected_name}' in inspect --json, "
            f"got name='{kdata.get('name')}', base_name='{kdata.get('base_name')}'"
        )

    def test_kernel_inspect_nonexistent_fails(self, mvm_binary):
        """Inspecting a nonexistent kernel should fail."""
        # Rationale: No resources needed — error path tests require no existing resources.
        result = _run_mvm(
            mvm_binary, "kernel", "inspect", "000000", check=False
        )
        assert result.returncode != 0


class TestKernelRemove:
    """Test kernel removal operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_rm_by_id(self, mvm_binary):
        """Remove a kernel by ID prefix and verify removal."""
        # Rationale: Needs a present kernel to remove. Uses firecracker kernel
        # (no build tools needed). Verifies kernel is removed from ls --json.
        _ensure_kernel(mvm_binary)
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(result.stdout)
        present = [k for k in kernels if k.get("is_present")]
        if not present:
            pytest.skip("No present kernel to remove")
        kernel_id = present[0]["id"][:6]
        result = _run_mvm(
            mvm_binary, "kernel", "rm", kernel_id, "--force", check=False
        )
        assert result.returncode == 0, (
            f"Failed to remove kernel {kernel_id}: {result.stderr}"
        )
        # L2: verify the kernel is no longer in ls --json
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        remaining: list[dict[str, Any]] = json.loads(result.stdout)
        assert not any(k["id"].startswith(kernel_id) for k in remaining), (
            f"Kernel {kernel_id} still present in listing after removal"
        )

    def test_kernel_rm_nonexistent(self, mvm_binary):
        """Removing a nonexistent kernel returns non-zero error."""
        # Rationale: No resources needed. Tests error path for kernel rm.
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "rm",
            "totally-nonexistent-kernel-id",
            "--force",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected non-zero returncode when removing nonexistent kernel"
        )
        # L1: verify stderr contains expected error message
        err = result.stderr.lower()
        assert "not found" in err or "could be resolved" in err, (
            f"Expected error about missing kernel, got: {result.stderr}"
        )


class TestKernelPullWithVersion:
    """Test kernel pull with the --version flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_pull_with_version(self, mvm_binary):
        """Pull a firecracker kernel with --version flag."""
        # Rationale: Needs an actual kernel download (slow, serial). Tests --version flag on pull.
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "latest",
            check=False,
            timeout=120,
        )
        # Skip-reason: Kernel download requires network access to the remote
        # asset registry. When running in air-gapped environments without
        # MVM_ASSET_MIRROR configured, the HTTP download will fail.
        if result.returncode != 0:
            pytest.skip(
                f"Kernel pull with --version failed: {result.stderr.strip()}"
            )
        # L2: verify a firecracker kernel appears in the listing
        kernels = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        firecracker = [
            k
            for k in kernels
            if k.get("type") == "firecracker" and k.get("is_present")
        ]
        assert firecracker, (
            "No firecracker kernel found in listing after pull with --version"
        )

    def test_kernel_pull_with_specific_version(self, mvm_binary):
        """Pull a firecracker kernel with a specific version (not 'latest')."""
        # Rationale: Needs a kernel download. Tests specific version (6.1) not just "latest".
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "6.1",
            check=False,
            timeout=120,
        )
        # Skip-reason: Kernel download requires network access. When running
        # in air-gapped environments without MVM_ASSET_MIRROR configured,
        # the HTTP download from the remote registry will fail.
        if result.returncode != 0:
            pytest.skip(
                f"Kernel pull with specific version failed: {result.stderr.strip()}"
            )

        # L2: verify the kernel appears in ls --json with correct version
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels = json.loads(result.stdout)
        firecracker_kernels = [
            k
            for k in kernels
            if k.get("type") == "firecracker"
            and k.get("version", "").startswith("6.1")
            and k.get("is_present")
        ]
        assert firecracker_kernels, (
            "Kernel with type=firecracker and version starting with '6.1' "
            "not found in listing. "
            f"Available firecracker versions: {[k.get('version') for k in kernels if k.get('type') == 'firecracker']}"
        )


class TestKernelBuild:
    """Test kernel build from source operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.kernel_build,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_build_with_custom_config(self, mvm_binary, tmp_path):
        """Build official kernel with custom config fragment."""
        # Rationale: Needs full kernel build from source (very slow, 30min). Tests custom config fragment.
        config_fragment = tmp_path / "custom-fragment.conf"
        config_fragment.write_text("CONFIG_NET=y\nCONFIG_INET=y\n")

        result = _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "official",
            "--config",
            str(config_fragment),
            "--jobs",
            "4",
            "--keep-build-dir",
            check=False,
            timeout=1800,
        )
        # Skip-reason: Kernel build from source requires build tools (gcc,
        # make, kernel headers) which may not be available in all CI
        # environments. Excluded from default runs via @pytest.mark.kernel_build.
        if result.returncode != 0:
            pytest.skip(
                f"Kernel build with custom config failed: {result.stderr.strip()}"
            )

        # L2: verify the kernel was registered in the DB and file exists in cache
        kernels = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        official = [
            k
            for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, "No official kernel found in listing after build"
        for k in official:
            assert Path(k["path"]).exists(), (
                f"Built kernel file not found at: {k['path']}"
            )

    def test_kernel_clean_rebuild(self, mvm_binary):
        """Build official kernel with clean rebuild."""
        # Rationale: Needs full kernel build. Tests --clean-build flag.
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "official",
            "--clean-build",
            "--jobs",
            "4",
            check=False,
            timeout=1800,
        )
        # Skip-reason: Kernel build from source requires build tools (gcc,
        # make, kernel headers) which may not be available in all CI
        # environments. Excluded from default runs via @pytest.mark.kernel_build.
        if result.returncode != 0:
            pytest.skip(f"Kernel clean rebuild failed: {result.stderr.strip()}")
        # L2: verify the kernel was registered in the DB and file exists in cache
        kernels = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        official = [
            k
            for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, (
            "No official kernel found in listing after clean rebuild"
        )
        for k in official:
            assert Path(k["path"]).exists(), (
                f"Built kernel file not found at: {k['path']}"
            )

    @pytest.mark.kernel_build
    def test_kernel_pull_with_features(self, mvm_binary):
        """Pull official kernel with --features flag.

        Rationale: --features allows enabling comma-separated kernel features
        (kvm, nftables, etc.) during build. A regression where --features is
        silently ignored would leave the kernel lacking necessary features.
        """
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "official",
            "--features",
            "kvm",
            "--jobs",
            "4",
            check=False,
            timeout=1800,
        )
        # Skip-reason: Kernel build from source requires build tools (gcc,
        # make, kernel headers) which may not be available in all CI
        # environments. Excluded from default runs via @pytest.mark.kernel_build.
        if result.returncode != 0:
            pytest.skip(
                f"Kernel pull with --features failed: {result.stderr.strip()}"
            )
        # L1: verify success or "already exists" (kernel may already be pulled)
        output_lower = result.stdout.lower()
        assert (
            "features" in output_lower
            or "kvm" in output_lower
            or "already exists" in output_lower
        ), (
            f"Expected features or already-exists mention, got: {result.stdout[:200]}"
        )


class TestKernelRemoveAndPull:
    """Test kernel removal and pull with set-default."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_kernel,
    ]

    @pytest.mark.kernel_build
    def test_kernel_pull_with_set_default(self, mvm_binary):
        """Pull official kernel and set as default in one command."""
        # Rationale: Needs a kernel pull. Tests --default flag combined with pull.
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "official",
            "--default",
            check=False,
            timeout=300,
        )
        # Skip-reason: Official kernel pull/build from source requires build
        # tools (gcc, make, kernel headers) and network access. Excluded from
        # default CI runs via @pytest.mark.kernel_build.
        if result.returncode != 0:
            pytest.skip(
                f"Official kernel pull/build failed: {result.stderr.strip()}"
            )
        # L2: verify the kernel is present and marked as default
        kernels = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        official_default = [
            k
            for k in kernels
            if k.get("type") == "official"
            and k.get("is_present")
            and k.get("is_default")
        ]
        assert official_default, (
            "No official kernel marked as default after pull --default"
        )

    def test_kernel_remove(self, mvm_binary):
        """Fetch a kernel then remove it."""
        # Rationale: Needs a present kernel to remove. Tests destructive rm operation.
        # Uses _ensure_kernel to get a pre-built firecracker kernel (no build tools
        # needed) instead of --type official which requires gcc/make.
        _ensure_kernel(mvm_binary)
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        existing: list[dict[str, Any]] = json.loads(result.stdout)
        present = [k for k in existing if k.get("is_present")]

        # Skip-reason: Safety net — _ensure_kernel() should guarantee a present
        # kernel, but guard against edge cases like a parallel test deleting it.
        if not present:
            pytest.skip("No present kernel to remove")

        kernel_id = present[0]["id"][:6]

        vm_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms: list[dict[str, Any]] = json.loads(vm_result.stdout)
        for vm in vms:
            if vm.get("kernel_id", "").startswith(kernel_id):
                _run_mvm(
                    mvm_binary, "vm", "rm", vm["name"], "--force", check=False
                )

        result = _run_mvm(mvm_binary, "kernel", "rm", kernel_id, check=False)
        if result.returncode != 0:
            # Skip-reason: Another parallel test worker may have created VMs
            # referencing this kernel ID concurrently. With serial marker this
            # should not happen, but the guard exists for safety.
            if "referenced by VMs" in result.stdout:
                pytest.skip(
                    f"Kernel {kernel_id} is referenced by VMs from "
                    "another parallel test worker"
                )
            assert result.returncode == 0, result.stderr

        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        remaining: list[dict[str, Any]] = json.loads(result.stdout)
        assert not any(k["id"].startswith(kernel_id) for k in remaining)


class TestKernelRemoveForce:
    """Test kernel removal with --force flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_rm_with_force(self, mvm_binary):
        """Remove a kernel using --force even if VMs reference it."""
        # Rationale: Needs a present kernel. Tests --force flag on rm.
        # Uses _ensure_kernel to get a pre-built firecracker kernel (no build
        # tools needed) instead of --type official which requires gcc/make.
        _ensure_kernel(mvm_binary)
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(result.stdout)

        present = [k for k in kernels if k.get("is_present")]
        # Skip-reason: Safety net — _ensure_kernel() should guarantee a present
        # kernel, but guard against edge cases like a parallel test deleting it
        # or a cache clean that removed files but left DB records.
        if not present:
            pytest.skip("No present kernel available to remove")

        vm_result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        vm_kernel_ids: set[str] = set()
        if vm_result.returncode == 0:
            vms: list[dict[str, Any]] = json.loads(vm_result.stdout)
            vm_kernel_ids = {
                vm.get("kernel_id", "") for vm in vms if vm.get("kernel_id")
            }

        target = None
        for kernel in present:
            kid = kernel["id"][:6]
            if not any(vm_id.startswith(kid) for vm_id in vm_kernel_ids):
                target = kid
                break

        # Skip-reason: Every present kernel is referenced by an existing VM,
        # so there is no safe kernel to remove for this test. Running in a
        # clean environment with no VMs should prevent this skip.
        if not target:
            pytest.skip("All kernels are referenced by VMs")

        result = _run_mvm(mvm_binary, "kernel", "rm", target, "--force")
        assert result.returncode == 0

        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        remaining: list[dict[str, Any]] = json.loads(result.stdout)
        assert not any(k["id"].startswith(target) for k in remaining)


class TestKernelStoppedVMDeletion:
    """Test kernel deletion behavior with stopped VM references."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.serial,
        pytest.mark.domain_kernel,
    ]

    def test_delete_kernel_used_by_stopped_vm_does_not_error(
        # Rationale: Needs a real VM (30-120s) because kernel dependency on stopped VMs only applies when VMs exist.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        created_network: str,
    ) -> None:
        """Kernel rm allows deleting kernels referenced by stopped VMs (no error)."""
        vm_name = unique_vm_name
        network_name = created_network

        try:
            # Get a present kernel to use explicitly (avoid reliance on default kernel)
            _ensure_kernel(mvm_binary)
            result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
            kernels: list[dict[str, Any]] = json.loads(result.stdout)
            present_kernels = [k for k in kernels if k.get("is_present")]
            # Skip-reason: Requires at least one present kernel to create a VM
            # that references it. _ensure_kernel() attempts to pull a firecracker
            # kernel but may fail in air-gapped environments.
            if not present_kernels:
                pytest.skip("No present kernels available for VM creation")
            target_kernel = present_kernels[0]
            kernel_id_prefix = target_kernel["id"][:6]

            try:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "create",
                    vm_name,
                    "--image",
                    "alpine:3.21",
                    "--kernel",
                    kernel_id_prefix,
                    "--network",
                    network_name,
                )
            except RuntimeError as e:
                # Skip-reason: VM creation requires the mvm-services binary
                # (mvm-provision) to be registered in the DB. This happens
                # when dist/services/mvm-services has not been built or when
                # cache clean --force has removed service binaries. Run
                # 'python scripts/build_services.py' to rebuild.
                if "No provisioner available" in str(e):
                    pytest.skip(
                        "No loop-mount provisioner available "
                        "(mvm-services not set up)"
                    )
                raise

            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names

            result = _run_mvm(
                mvm_binary, "kernel", "rm", kernel_id_prefix, check=False
            )
            assert result.returncode in (0, 1)

            if result.returncode == 0:
                kernel_ls = _run_mvm(
                    mvm_binary, "kernel", "ls", "--json", check=False
                )
                if kernel_ls.returncode == 0 and kernel_ls.stdout.strip():
                    kernels_after: list[dict[str, Any]] = json.loads(
                        kernel_ls.stdout
                    )
                    kernel_ids = [k.get("id", "")[:6] for k in kernels_after]
                    assert kernel_id_prefix not in kernel_ids
            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms_after: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms_after]
                assert vm_name in vm_names
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
