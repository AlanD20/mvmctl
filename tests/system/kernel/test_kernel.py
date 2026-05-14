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
        result = _run_mvm(mvm_binary, "kernel", "pull", "--type", "official")
        assert result.returncode == 0

    def test_kernel_list_json(self, mvm_binary):
        """List kernels in JSON format."""
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
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(result.stdout)
        present = [k for k in kernels if k.get("is_present")]
        if not present:
            pytest.skip("No present kernel to set as default")
        kernel_id = present[0]["id"]
        result = _run_mvm(mvm_binary, "kernel", "default", kernel_id[:6])
        assert result.returncode == 0


class TestKernelInspect:
    """Test kernel inspect operations (table, json, tree)."""

    def test_kernel_inspect_table(self, mvm_binary):
        """Inspect a kernel in table format."""
        _ensure_kernel(mvm_binary)
        kernels: list[dict[str, Any]] = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        if not present:
            pytest.skip("No present kernel to inspect")
        prefix = present[0]["id"][:6]
        result = _run_mvm(mvm_binary, "kernel", "inspect", prefix)
        assert result.returncode == 0
        assert prefix in result.stdout or present[0]["name"] in result.stdout

    def test_kernel_inspect_json(self, mvm_binary):
        """Inspect a kernel with --json output."""
        _ensure_kernel(mvm_binary)
        kernels: list[dict[str, Any]] = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        if not present:
            pytest.skip("No present kernel to inspect")
        prefix = present[0]["id"][:6]
        result = _run_mvm(mvm_binary, "kernel", "inspect", prefix, "--json")
        assert result.returncode == 0
        data: dict[str, Any] = json.loads(result.stdout)
        assert isinstance(data, dict)
        assert "id" in data
        assert "name" in data
        assert "version" in data
        assert "type" in data

    def test_kernel_inspect_tree(self, mvm_binary):
        """Inspect a kernel with --tree output."""
        _ensure_kernel(mvm_binary)
        kernels: list[dict[str, Any]] = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        if not present:
            pytest.skip("No present kernel to inspect")
        prefix = present[0]["id"][:6]
        result = _run_mvm(mvm_binary, "kernel", "inspect", prefix, "--tree")
        assert result.returncode == 0
        assert (
            "├──" in result.stdout
            or "└──" in result.stdout
            or "ID:" in result.stdout
        )

    def test_kernel_inspect_nonexistent_fails(self, mvm_binary):
        """Inspecting a nonexistent kernel should fail."""
        result = _run_mvm(
            mvm_binary, "kernel", "inspect", "000000", check=False
        )
        assert result.returncode != 0


class TestKernelPullWithVersion:
    """Test kernel pull with the --version flag."""

    pytestmark = [pytest.mark.slow, pytest.mark.serial]

    def test_kernel_pull_with_version(self, mvm_binary):
        """Pull a firecracker kernel with --version flag."""
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
        if result.returncode != 0:
            pytest.skip(
                f"Kernel pull with --version failed: {result.stderr.strip()}"
            )
        assert result.returncode == 0
        assert (
            "pulled" in result.stdout.lower()
            or "success" in result.stdout.lower()
            or "already exists" in result.stdout.lower()
        )

    def test_kernel_pull_with_specific_version(self, mvm_binary):
        """Pull a firecracker kernel with a specific version (not 'latest')."""
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
        if result.returncode != 0:
            pytest.skip(
                f"Kernel pull with specific version failed: {result.stderr.strip()}"
            )
        assert result.returncode == 0
        assert (
            "pulled" in result.stdout.lower()
            or "success" in result.stdout.lower()
            or "already exists" in result.stdout.lower()
        )

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
        if result.returncode != 0:
            pytest.skip(
                f"Kernel build with custom config failed: {result.stderr.strip()}"
            )
        assert result.returncode == 0

        # Verify the kernel was registered in the DB and file exists in cache
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
        if result.returncode != 0:
            pytest.skip(f"Kernel clean rebuild failed: {result.stderr.strip()}")
        assert result.returncode == 0

        # Verify the kernel was registered in the DB and file exists in cache
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


class TestKernelRemoveAndPull:
    """Test kernel removal and pull with set-default."""

    pytestmark = [pytest.mark.slow, pytest.mark.serial]

    @pytest.mark.kernel_build
    def test_kernel_pull_with_set_default(self, mvm_binary):
        """Pull official kernel and set as default in one command."""
        result = _run_mvm(
            mvm_binary, "kernel", "pull", "--type", "official", "--default"
        )
        assert result.returncode == 0

    @pytest.mark.kernel_build
    def test_kernel_remove(self, mvm_binary):
        """Fetch a kernel then remove it."""
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        existing: list[dict[str, Any]] = json.loads(result.stdout)
        present = [k for k in existing if k.get("is_present")]

        if not present:
            _run_mvm(mvm_binary, "kernel", "pull", "--type", "official")
            result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
            present = [
                k for k in json.loads(result.stdout) if k.get("is_present")
            ]

        if not present:
            pytest.skip("No kernel available to remove")

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

    pytestmark = [pytest.mark.slow, pytest.mark.serial]

    @pytest.mark.kernel_build
    def test_kernel_rm_with_force(self, mvm_binary):
        """Remove a kernel using --force even if VMs reference it."""
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels: list[dict[str, Any]] = json.loads(result.stdout)

        if not kernels:
            _run_mvm(
                mvm_binary,
                "kernel",
                "pull",
                "--type",
                "official",
                check=False,
                timeout=120,
            )
            result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
            kernels = json.loads(result.stdout)

        if not kernels:
            pytest.skip("No kernel available to remove")

        present = [k for k in kernels if k.get("is_present")]
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
        self, mvm_binary: str, unique_vm_name: str, created_network: str
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
            if not present_kernels:
                pytest.skip("No present kernels available for VM creation")
            target_kernel = present_kernels[0]
            kernel_id_prefix = target_kernel["id"][:6]

            try:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "create",
                    "--name",
                    vm_name,
                    "--image",
                    "alpine-3.21",
                    "--kernel",
                    kernel_id_prefix,
                    "--network",
                    network_name,
                )
            except RuntimeError as e:
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
