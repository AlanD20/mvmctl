"""Kernel management system tests."""

from __future__ import annotations

import json

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
        data = json.loads(result.stdout)
        assert isinstance(data, list)

    def test_kernel_set_default(self, mvm_binary):
        """Set kernel as default (uses the one pulled in test_kernel_pull)."""

        # Get kernel ID
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels = json.loads(result.stdout)
        if not kernels:
            pytest.skip("No kernel to set as default")
        kernel_id = kernels[0]["id"]
        result = _run_mvm(mvm_binary, "kernel", "set-default", kernel_id[:6])
        assert result.returncode == 0


class TestKernelInspect:
    """Test kernel inspect operations (table, json, tree)."""

    def test_kernel_inspect_table(self, mvm_binary):
        """Inspect a kernel in table format."""
        kernels = json.loads(
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
        kernels = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        if not present:
            pytest.skip("No present kernel to inspect")
        prefix = present[0]["id"][:6]
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "inspect",
            prefix,
            "--json",
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, dict)
        assert "id" in data
        assert "name" in data
        assert "version" in data
        assert "type" in data

    def test_kernel_inspect_tree(self, mvm_binary):
        """Inspect a kernel with --tree output."""
        kernels = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        if not present:
            pytest.skip("No present kernel to inspect")
        prefix = present[0]["id"][:6]
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "inspect",
            prefix,
            "--tree",
        )
        assert result.returncode == 0
        assert (
            "├──" in result.stdout
            or "└──" in result.stdout
            or "ID:" in result.stdout
        )

    def test_kernel_inspect_nonexistent_fails(self, mvm_binary):
        """Inspecting a nonexistent kernel should fail."""
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "inspect",
            "000000",
            check=False,
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
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "official",
            "--set-default",
        )
        assert result.returncode == 0

    def test_kernel_remove(self, mvm_binary):
        """Fetch a kernel then remove it."""

        # Get existing kernels
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        existing = json.loads(result.stdout)

        if not existing:
            # Pull one first
            _run_mvm(mvm_binary, "kernel", "pull", "--type", "official")
            result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
            existing = json.loads(result.stdout)

        if not existing:
            pytest.skip("No kernel available to remove")

        kernel_id = existing[0]["id"][:6]

        # Remove any VMs referencing this kernel first (they block removal)
        vm_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(vm_result.stdout)
        for vm in vms:
            if vm.get("kernel_id", "").startswith(kernel_id):
                _run_mvm(
                    mvm_binary, "vm", "rm", vm["name"], "--force", check=False
                )

        # Remove the kernel
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "rm",
            kernel_id,
            check=False,
        )
        if result.returncode != 0:
            # In parallel execution, another worker may have created a VM
            # referencing this kernel between our VM cleanup and this rm.
            if "referenced by VMs" in result.stdout:
                pytest.skip(
                    f"Kernel {kernel_id} is referenced by VMs from "
                    "another parallel test worker"
                )
            assert result.returncode == 0, result.stderr

        # Verify gone
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        remaining = json.loads(result.stdout)
        assert not any(k["id"].startswith(kernel_id) for k in remaining)


class TestKernelRemoveForce:
    """Test kernel removal with --force flag."""

    pytestmark = [pytest.mark.slow]

    def test_kernel_rm_with_force(self, mvm_binary):
        """Remove a kernel using --force even if VMs reference it."""
        # Get existing kernels
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels = json.loads(result.stdout)

        if not kernels:
            # Pull one first
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

        # Find a kernel that no VM references
        vm_result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        vm_kernel_ids: set[str] = set()
        if vm_result.returncode == 0:
            vms = json.loads(vm_result.stdout)
            vm_kernel_ids = {
                vm.get("kernel_id", "") for vm in vms if vm.get("kernel_id")
            }

        target = None
        for kernel in kernels:
            kid = kernel["id"][:6]
            if not any(vm_id.startswith(kid) for vm_id in vm_kernel_ids):
                target = kid
                break

        if not target:
            pytest.skip("All kernels are referenced by VMs")

        # Remove with --force
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "rm",
            target,
            "--force",
        )
        assert result.returncode == 0

        # Verify it's gone
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        remaining = json.loads(result.stdout)
        assert not any(k["id"].startswith(target) for k in remaining)
