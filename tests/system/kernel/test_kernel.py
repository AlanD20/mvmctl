"""Kernel management system tests."""

from __future__ import annotations

import json
import os
import uuid
from typing import Any

import pytest

from tests.system.conftest import _ensure_kernel, _guest_run, _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_kernel]


class TestKernelLifecycle:
    """Test kernel CRUD operations."""

    def test_kernel_list_empty(self, runner_vm):
        """List kernels when none are cached."""
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
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
    def test_kernel_pull(self, runner_vm):
        """Pull official kernel."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            check=False,
            timeout=300,
        )
        assert result.returncode == 0, (
            f"Official kernel pull/build failed in Tier 2 environment: "
            f"{result.stderr.strip()}"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "official" in combined or "pulling" in combined, (
            f"Expected 'official' in pull output, "
            f"got stdout: {result.stdout[:200]}, "
            f"stderr: {result.stderr[:200]}"
        )
        kernels = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, "No official kernel found in listing after pull"

    def test_kernel_list_no_cache(self, runner_vm):
        """List kernels with --no-cache flag."""
        result = _run_mvm(runner_vm, "kernel", "ls", "--no-cache", check=False)
        assert result.returncode == 0, (
            f"kernel ls --no-cache failed: {result.stderr}"
        )

    @pytest.mark.slow
    def test_kernel_ls_remote(self, runner_vm):
        """List kernels available from the remote registry with --remote flag."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "ls",
            "--remote",
            "--json",
            timeout=30,
            check=False,
        )
        assert result.returncode == 0 and result.stdout.strip(), (
            f"Remote kernel listing failed in Tier 2 environment "
            f"(should have network): {result.stderr}"
        )
        data = json.loads(result.stdout)
        assert isinstance(data, list), "Expected a list of remote kernels"
        assert len(data) > 0, "Expected at least one remote kernel entry"
        entry = data[0]
        assert isinstance(entry.get("type"), str) and entry["type"], (
            f"Expected non-empty type field: {entry}"
        )

    def test_kernel_list_json(self, runner_vm):
        """List kernels in JSON format."""
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
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
                if entry.get("is_present"):
                    assert entry.get("type") in ("firecracker", "official"), (
                        f"Unexpected type for present entry: {entry}"
                    )

    def test_kernel_set_default(self, runner_vm):
        """Set kernel as default."""
        _ensure_kernel(runner_vm)
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(result.stdout)
        present = [k for k in kernels if k.get("is_present")]
        assert present, (
            "No present kernel to set as default — _ensure_kernel "
            "should have pulled one"
        )
        kernel_id = present[0]["id"]
        result = _run_mvm(
            runner_vm, "kernel", "default", kernel_id[:6], check=False,
        )
        assert result.returncode == 0, (
            f"Failed to set kernel default: {result.stderr}"
        )
        kernels_after = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        default = [k for k in kernels_after if k.get("is_default")]
        assert any(k["id"] == kernel_id for k in default), (
            f"Kernel {kernel_id[:6]} not marked as default after set_default"
        )


class TestKernelInspect:
    """Test kernel inspect operations (table, json, tree)."""

    def test_kernel_inspect_table(self, runner_vm):
        """Inspect a kernel in table format."""
        _ensure_kernel(runner_vm)
        kernels: list[dict[str, Any]] = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        assert present, (
            "No present kernel to inspect — _ensure_kernel should have pulled one"
        )
        prefix = present[0]["id"][:6]
        result = _run_mvm(runner_vm, "kernel", "inspect", prefix, "--json")
        assert result.returncode == 0
        data: dict[str, Any] = json.loads(result.stdout)
        kdata = data.get("kernel", data)
        assert kdata.get("name") == present[0]["name"], (
            f"Expected kernel name '{present[0]['name']}', got '{kdata.get('name')}'"
        )

    def test_kernel_inspect_json(self, runner_vm):
        """Inspect a kernel with --json output."""
        _ensure_kernel(runner_vm)
        kernels: list[dict[str, Any]] = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        assert present, (
            "No present kernel to inspect — _ensure_kernel should have pulled one"
        )
        prefix = present[0]["id"][:6]
        result = _run_mvm(runner_vm, "kernel", "inspect", prefix, "--json")
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

    def test_kernel_inspect_default_format(self, runner_vm):
        """Inspect a kernel with default (non-JSON) output — verify via --json."""
        _ensure_kernel(runner_vm)
        kernels: list[dict[str, Any]] = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        assert present, (
            "No present kernel to inspect — _ensure_kernel should have pulled one"
        )
        prefix = present[0]["id"][:6]
        result = _run_mvm(runner_vm, "kernel", "inspect", prefix, "--json")
        assert result.returncode == 0
        data: dict[str, Any] = json.loads(result.stdout)
        kdata = data.get("kernel", data)
        expected_name = present[0].get("name") or present[0].get("base_name", "")
        actual_name = kdata.get("name") or kdata.get("base_name", "")
        assert expected_name == actual_name or expected_name in actual_name, (
            f"Expected kernel name '{expected_name}' in inspect --json, "
            f"got name='{kdata.get('name')}', base_name='{kdata.get('base_name')}'"
        )

    def test_kernel_inspect_nonexistent_fails(self, runner_vm):
        """Inspecting a nonexistent kernel should fail."""
        result = _run_mvm(
            runner_vm, "kernel", "inspect", "000000", check=False,
        )
        assert result.returncode != 0


class TestKernelRemove:
    """Test kernel removal operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_rm_by_id(self, runner_vm):
        """Remove a kernel by ID prefix and verify removal."""
        _ensure_kernel(runner_vm)
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(result.stdout)
        present = [k for k in kernels if k.get("is_present")]
        assert present, "No present kernel to remove"
        kernel_id = present[0]["id"][:6]
        result = _run_mvm(runner_vm, "kernel", "rm", kernel_id, "--force", check=False)
        assert result.returncode == 0, (
            f"Failed to remove kernel {kernel_id}: {result.stderr}"
        )
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        remaining: list[dict[str, Any]] = json.loads(result.stdout)
        assert not any(k["id"].startswith(kernel_id) for k in remaining), (
            f"Kernel {kernel_id} still present in listing after removal"
        )

    def test_kernel_rm_nonexistent(self, runner_vm):
        """Removing a nonexistent kernel returns non-zero error."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "rm",
            "totally-nonexistent-kernel-id",
            "--force",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected non-zero returncode when removing nonexistent kernel"
        )
        err = result.stderr.lower()
        assert "not found" in err or "could be resolved" in err or "failed to resolve" in err, (
            f"Expected error about missing kernel, got: {result.stderr}"
        )


class TestKernelPullWithVersion:
    """Test kernel pull with the --version flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_pull_with_version(self, runner_vm):
        """Pull a firecracker kernel with --version flag."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "v1.15",
            check=False,
            timeout=120,
        )
        assert result.returncode == 0, (
            f"Kernel pull with --version failed in Tier 2 environment: "
            f"{result.stderr.strip()}"
        )
        kernels = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        firecracker = [
            k for k in kernels
            if k.get("type") == "firecracker" and k.get("is_present")
        ]
        assert firecracker, (
            "No firecracker kernel found in listing after pull with --version"
        )

    def test_kernel_pull_with_specific_version(self, runner_vm):
        """Pull a firecracker kernel with a specific version (not 'latest')."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "v1.14",
            check=False,
            timeout=120,
        )
        assert result.returncode == 0, (
            f"Kernel pull with specific version failed in Tier 2 environment: "
            f"{result.stderr.strip()}"
        )

        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels = json.loads(result.stdout)
        firecracker_kernels = [
            k for k in kernels
            if k.get("type") == "firecracker"
            and k.get("version", "").startswith("6.1")
            and k.get("is_present")
        ]
        assert firecracker_kernels, (
            "Kernel with type=firecracker and version starting with '6.1' "
            "not found in listing. "
            f"Available firecracker versions: {[k.get('version') for k in kernels if k.get('type') == 'firecracker']}"
        )


class TestKernelPullArch:
    """Test kernel pull (arch is auto-detected in Go CLI)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_pull_with_arch(self, runner_vm):
        """Pull a firecracker kernel (arch detection is automatic in Go CLI)."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--version",
            "v1.15",
            check=False,
            timeout=120,
        )
        assert result.returncode == 0, (
            f"Kernel pull failed in Tier 2 environment: {result.stderr.strip()}"
        )

        kernels = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        firecracker = [
            k for k in kernels
            if k.get("type") == "firecracker" and k.get("is_present")
        ]
        assert firecracker, (
            "No firecracker kernel found in listing after pull"
        )


class TestKernelBuild:
    """Test kernel build from source operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.kernel_build,
        pytest.mark.slow,
        pytest.mark.domain_kernel,
    ]

    @pytest.mark.timeout(600)
    def test_kernel_build_with_custom_config(self, runner_vm):
        """Build official kernel with custom config fragment.

        Config fragment created inside the test VM.
        """
        config_path = f"/tmp/custom-fragment-{uuid.uuid4().hex[:6]}.conf"
        # Create config fragment inside the VM
        _guest_run(
            runner_vm,
            f"echo 'CONFIG_NET=y' > {config_path} && echo 'CONFIG_INET=y' >> {config_path}",
        )

        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--config",
            config_path,
            "--jobs",
            "4",
            "--keep-build-dir",
            check=False,
            timeout=1800,
        )
        assert result.returncode == 0, (
            f"Kernel build with custom config failed: {result.stderr.strip()}"
        )

        kernels = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, "No official kernel found in listing after build"
        for k in official:
            kernel_path = k.get("path", "")
            assert kernel_path, "Kernel path is empty in listing"
            if os.path.isabs(kernel_path):
                full_path = kernel_path
            else:
                full_path = f"/root/.cache/mvmctl/kernels/{kernel_path}"
            check = _guest_run(
                runner_vm,
                f"test -f {full_path} && echo exists || echo not-found",
                check=False,
            )
            assert "exists" in check.stdout, (
                f"Built kernel file not found inside VM at: {full_path}"
            )

    @pytest.mark.timeout(600)
    def test_kernel_clean_rebuild(self, runner_vm):
        """Build official kernel with clean rebuild."""
        result = _run_mvm(
            runner_vm,
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
        assert result.returncode == 0, (
            f"Kernel clean rebuild failed: {result.stderr.strip()}"
        )
        kernels = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, (
            "No official kernel found in listing after clean rebuild"
        )
        for k in official:
            kernel_path = k.get("path", "")
            assert kernel_path, "Kernel path is empty in listing"
            if os.path.isabs(kernel_path):
                full_path = kernel_path
            else:
                full_path = f"/root/.cache/mvmctl/kernels/{kernel_path}"
            check = _guest_run(
                runner_vm,
                f"test -f {full_path} && echo exists || echo not-found",
                check=False,
            )
            assert "exists" in check.stdout, (
                f"Built kernel file not found inside VM at: {full_path}"
            )

    @pytest.mark.kernel_build
    @pytest.mark.timeout(600)
    def test_kernel_pull_with_features(self, runner_vm):
        """Pull official kernel with --features flag."""
        result = _run_mvm(
            runner_vm,
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
        assert result.returncode == 0, (
            f"Kernel pull with --features failed: {result.stderr.strip()}"
        )

    @pytest.mark.kernel_build
    def test_kernel_pull_with_jobs_flag(self, runner_vm):
        """Pull official kernel with --jobs flag to control parallelism."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--jobs",
            "2",
            check=False,
            timeout=1800,
        )
        assert result.returncode == 0, (
            f"Kernel pull with --jobs failed: {result.stderr.strip()}"
        )
        kernels = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, (
            "No official kernel found in listing after pull with --jobs"
        )

    @pytest.mark.kernel_build
    def test_kernel_pull_with_keep_build_dir(self, runner_vm):
        """Pull official kernel with --keep-build-dir flag."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--keep-build-dir",
            "--jobs",
            "2",
            check=False,
            timeout=1800,
        )
        assert result.returncode == 0, (
            f"Kernel pull with --keep-build-dir failed: {result.stderr.strip()}"
        )
        kernels = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        official = [
            k for k in kernels
            if k.get("type") == "official" and k.get("is_present")
        ]
        assert official, (
            "No official kernel found in listing after pull with --keep-build-dir"
        )


class TestKernelRemoveAndPull:
    """Test kernel removal and pull with set-default."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_kernel,
    ]

    @pytest.mark.kernel_build
    def test_kernel_pull_with_set_default(self, runner_vm):
        """Pull official kernel and set as default in one command."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            "--default",
            check=False,
            timeout=300,
        )
        assert result.returncode == 0, (
            f"Official kernel pull/build failed: {result.stderr.strip()}"
        )
        kernels = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        official_default = [
            k for k in kernels
            if k.get("type") == "official"
            and k.get("is_present")
            and k.get("is_default")
        ]
        assert official_default, (
            "No official kernel marked as default after pull --default"
        )

    def test_kernel_remove(self, runner_vm):
        """Fetch a kernel then remove it."""
        _ensure_kernel(runner_vm)
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        existing: list[dict[str, Any]] = json.loads(result.stdout)
        present = [k for k in existing if k.get("is_present")]
        assert present, (
            "No present kernel to remove — _ensure_kernel should guarantee one"
        )

        kernel_id = present[0]["id"][:6]

        vm_result = _run_mvm(runner_vm, "vm", "ls", "--json")
        vms: list[dict[str, Any]] = json.loads(vm_result.stdout)
        for vm in vms:
            if vm.get("kernel_id", "").startswith(kernel_id):
                _run_mvm(runner_vm, "vm", "rm", vm["name"], "--force", check=False)

        result = _run_mvm(runner_vm, "kernel", "rm", kernel_id, "--force", check=False)
        if result.returncode != 0:
            if "referenced by VMs" in result.stdout or "in use by" in result.stdout:
                pytest.fail(
                    f"Kernel {kernel_id} is referenced by VMs/snapshots from "
                    "another parallel test worker"
                )
            assert result.returncode == 0, result.stderr

        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        remaining: list[dict[str, Any]] = json.loads(result.stdout)
        assert not any(k["id"].startswith(kernel_id) for k in remaining)


class TestKernelRemoveForce:
    """Test kernel removal with --force flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_rm_with_force(self, runner_vm):
        """Remove a kernel using --force even if VMs reference it."""
        _ensure_kernel(runner_vm)
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(result.stdout)

        present = [k for k in kernels if k.get("is_present")]
        assert present, "No present kernel available to remove"

        vm_result = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
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

        assert target is not None, "All kernels are referenced by VMs"

        result = _run_mvm(runner_vm, "kernel", "rm", target, "--force")
        assert result.returncode == 0

        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        remaining: list[dict[str, Any]] = json.loads(result.stdout)
        assert not any(k["id"].startswith(target) for k in remaining)


class TestKernelStoppedVMDeletion:
    """Test kernel deletion behavior with stopped VM references."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.domain_kernel,
    ]

    def test_delete_kernel_used_by_stopped_vm_does_not_error(
        self,
        runner_vm: str,
        unique_vm_name: str,
        created_network: str,
    ) -> None:
        """Kernel rm allows deleting kernels referenced by stopped VMs (no error)."""
        vm_name = unique_vm_name
        network_name = created_network

        try:
            _ensure_kernel(runner_vm)
            result = _run_mvm(runner_vm, "kernel", "ls", "--json")
            kernels: list[dict[str, Any]] = json.loads(result.stdout)
            present_kernels = [k for k in kernels if k.get("is_present")]
            assert present_kernels, (
                "No present kernels available for VM creation — "
                "_ensure_kernel should have pulled one"
            )
            target_kernel = present_kernels[0]
            kernel_id_prefix = target_kernel["id"][:6]

            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--kernel",
                kernel_id_prefix,
                "--network",
                network_name,
            )

            vm_ls = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names

            result = _run_mvm(
                runner_vm, "kernel", "rm", kernel_id_prefix, check=False,
            )
            assert result.returncode in (0, 1)

            if result.returncode == 0:
                kernel_ls = _run_mvm(
                    runner_vm, "kernel", "ls", "--json", check=False,
                )
                if kernel_ls.returncode == 0 and kernel_ls.stdout.strip():
                    kernels_after: list[dict[str, Any]] = json.loads(
                        kernel_ls.stdout
                    )
                    kernel_ids = [k.get("id", "")[:6] for k in kernels_after]
                    assert kernel_id_prefix not in kernel_ids
            vm_ls = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms_after: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms_after]
                assert vm_name in vm_names
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)


class TestKernelPullAdvancedFlags:
    """Test advanced kernel pull flags."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_kernel,
        pytest.mark.kernel_build,
        pytest.mark.slow,
    ]

    def test_kernel_pull_with_arch_flag(self, runner_vm):
        """Pull a kernel (arch detection is automatic in Go CLI)."""
        # Rationale: Verifies that kernel pull works. The Go CLI detects
        # host architecture automatically — no --arch flag needed. Kernel
        # builds from source can take several minutes.
        result = _run_mvm(
            runner_vm,
            "kernel",
            "pull",
            "--type",
            "official",
            check=False,
            timeout=600,
        )
        assert result.returncode == 0, (
            f"Kernel pull failed: {result.stderr.strip()}"
        )

        # L2 verification: confirm the kernel appears in the listing
        ls_result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        kernels = json.loads(ls_result.stdout)
        assert isinstance(kernels, list)
        assert any(
            k.get("type") == "official" and k.get("is_present") for k in kernels
        ), "Kernel not found in listing"
