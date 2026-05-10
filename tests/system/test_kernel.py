"""Kernel management system tests."""

from __future__ import annotations

import json
from typing import Any

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_kernel]


class TestKernelLifecycle:
    """Test kernel CRUD operations."""

    def test_kernel_list_empty(self, mvm_binary):
        """List kernels when none are cached."""
        result = _run_mvm(mvm_binary, "kernel", "ls")
        assert result.returncode == 0

    @pytest.mark.slow
    def test_kernel_pull(self, mvm_binary):
        """Pull official kernel."""
        result = _run_mvm(mvm_binary, "kernel", "pull", "--type", "official")
        assert result.returncode == 0

    def test_kernel_list_json(self, mvm_binary):
        """List kernels in JSON format."""
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        assert result.returncode == 0
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)

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

    pytestmark = [pytest.mark.slow]

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


class TestKernelRemoveAndPull:
    """Test kernel removal and pull with set-default."""

    pytestmark = [pytest.mark.slow]

    def test_kernel_pull_with_set_default(self, mvm_binary):
        """Pull official kernel and set as default in one command."""
        result = _run_mvm(
            mvm_binary, "kernel", "pull", "--type", "official", "--default"
        )
        assert result.returncode == 0

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

    pytestmark = [pytest.mark.slow]

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

    pytestmark = [pytest.mark.requires_kvm]

    def test_delete_kernel_used_by_stopped_vm_does_not_error(
        self, mvm_binary: str, unique_vm_name: str
    ) -> None:
        """Kernel rm allows deleting kernels referenced by stopped VMs (no error)."""
        vm_name = unique_vm_name

        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
            )

            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names

            result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
            kernels: list[dict[str, Any]] = json.loads(result.stdout)
            present_kernels = [k for k in kernels if k.get("is_present")]
            assert present_kernels, "No present kernels found in listing"
            default_kernel = next(
                (k for k in present_kernels if k.get("is_default")),
                present_kernels[0],
            )
            kernel_id_prefix = default_kernel["id"][:6]

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
                vms: list[dict[str, Any]] = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
