"""Kernel import system tests — mvm kernel import command.

Tests the full lifecycle: importing a custom kernel from an existing
firecracker kernel file, verifying JSON/filesystem state, creating
a VM with the imported kernel, and verifying stop/start roundtrip.
Also tests auto-detected version from filename and error paths.
"""

from __future__ import annotations

import json
import os
import uuid
from typing import Any

import pytest

from tests.system.conftest import _ensure_kernel, _guest_run, _run_mvm, ensure_vm_deps

VM_CACHE_DIR = "/root/.cache/mvmctl"
VM_KERNEL_DIR = f"{VM_CACHE_DIR}/kernels"
VM_TMP_DIR = "/tmp"

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_kernel,
]

# ============================================================================
# Helpers — all operations run INSIDE the test VM
# ============================================================================


def _get_firecracker_kernel_path(runner_vm: str) -> str:
    """Return the absolute path of a present firecracker kernel inside the VM.

    Caller should invoke ``_ensure_kernel()`` first.
    """
    kernels = json.loads(
        _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
    )
    firecracker = [
        k for k in kernels
        if k.get("type") == "firecracker" and k.get("is_present")
    ]
    assert firecracker, (
        "No present firecracker kernel — _ensure_kernel should have pulled one"
    )

    fc = firecracker[0]
    fc_id = fc["id"][:6]
    inspect = json.loads(
        _run_mvm(runner_vm, "kernel", "inspect", fc_id, "--json").stdout
    )
    path = inspect.get("storage", {}).get("path", inspect.get("path", ""))

    if not path:
        pytest.fail(
            "Kernel inspect returned empty path for firecracker kernel"
        )

    # The path may be relative — resolve against kernel cache dir.
    # Use os.path.join to handle absolute paths correctly.
    full_path = os.path.join(VM_KERNEL_DIR, path) if not os.path.isabs(path) else path
    check = _guest_run(
        runner_vm,
        f"test -f {full_path} && echo exists || echo not-found",
        check=False,
    )
    assert "exists" in check.stdout, (
        f"Firecracker kernel path does not exist inside VM: "
        f"{path} (resolved: {full_path})"
    )
    return full_path


def _get_vm_status(runner_vm: str, vm_name: str) -> str | None:
    """Return the status of a VM by name, or None if not found."""
    result = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        return None
    vms = json.loads(result.stdout)
    for vm in vms:
        if vm.get("name") == vm_name:
            return vm.get("status")
    return None


# ============================================================================
# Test 1-3: Happy path import, VM creation, stop/start (shared state)
# ============================================================================


class TestKernelImportLifecycle:
    """Import a custom kernel, create a VM with it, verify stop/start.

    Tests 1-3 share state via class-level variables so the same imported
    kernel and VM are used across the sequence. Cleanup happens in
    ``teardown_class``.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_kernel,
        pytest.mark.requires_kvm,
    ]

    _binary: str | None = None
    _import_name: str | None = None
    _import_kernel_id: str | None = None
    _import_kernel_short_id: str | None = None
    _vm_name: str | None = None
    _network_name: str | None = None
    _network_subnet: str | None = None

    # ------------------------------------------------------------------
    # Step 1: Import the firecracker kernel as custom type
    # ------------------------------------------------------------------

    def test_import_firecracker_kernel(self, runner_vm: str) -> None:
        """Import a firecracker kernel as a custom kernel and verify state."""
        type(self)._binary = runner_vm
        _ensure_kernel(runner_vm)

        source_path = _get_firecracker_kernel_path(runner_vm)
        import_name = f"sys-test-import-{uuid.uuid4().hex[:6]}"
        type(self)._import_name = import_name

        import_version = "6.1"

        result = _run_mvm(
            runner_vm,
            "kernel",
            "import",
            import_name,
            source_path,
            "--version",
            import_version,
        )
        assert result.returncode == 0, f"kernel import failed: {result.stderr}"

        # ------------------------------------------------------------------
        # Option C verification: JSON state
        # ------------------------------------------------------------------
        kernels_after = json.loads(
            _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
        )
        imported = [
            k for k in kernels_after if k.get("base_name") == import_name
        ]
        assert len(imported) == 1, (
            f"Expected exactly 1 imported kernel with base_name='{import_name}', "
            f"found {len(imported)} in ls --json"
        )
        imported_kernel = imported[0]
        assert imported_kernel["type"] == "custom", (
            f"Expected type 'custom', got '{imported_kernel['type']}'"
        )
        assert imported_kernel["version"] == import_version
        assert imported_kernel.get("arch") and len(imported_kernel["arch"]) > 0, (
            f"Expected non-empty arch, got '{imported_kernel.get('arch')}'"
        )
        assert imported_kernel["is_present"] is True

        type(self)._import_kernel_id = imported_kernel["id"]
        type(self)._import_kernel_short_id = imported_kernel["id"][:6]

        # ------------------------------------------------------------------
        # Option C verification: Filesystem (inside VM)
        # ------------------------------------------------------------------
        kernel_path_rel = imported_kernel.get("path", "")
        assert kernel_path_rel, "Imported kernel path is empty in ls --json"
        full_path = kernel_path_rel if os.path.isabs(kernel_path_rel) else f"{VM_KERNEL_DIR}/{kernel_path_rel}"
        check = _guest_run(
            runner_vm,
            f"test -f {full_path} && echo exists || echo not-found",
            check=False,
        )
        assert "exists" in check.stdout, (
            f"Imported kernel file not found inside VM: {full_path}"
        )
        # Cross-check via stat: file must be non-empty
        size_check = _guest_run(
            runner_vm,
            f"stat -c%s {full_path}",
            check=False,
        )
        assert size_check.returncode == 0, (
            f"Failed to stat imported kernel file: {size_check.stderr}"
        )
        file_size = int(size_check.stdout.strip())
        assert file_size > 0, (
            f"Imported kernel file is empty: {full_path}"
        )

        # ------------------------------------------------------------------
        # Option C verification: inspect --json
        # ------------------------------------------------------------------
        inspect_result = _run_mvm(
            runner_vm,
            "kernel",
            "inspect",
            type(self)._import_kernel_short_id,
            "--json",
        )
        assert inspect_result.returncode == 0
        inspect_data = json.loads(inspect_result.stdout)
        kdata = inspect_data.get("kernel", {})
        assert kdata.get("type") == "custom", (
            f"Expected type 'custom', got '{kdata.get('type')}' in {kdata}"
        )
        assert kdata.get("base_name") == import_name
        assert kdata.get("version") == import_version
        assert kdata.get("arch") and len(kdata["arch"]) > 0, (
            f"Expected non-empty arch in inspect, got '{kdata.get('arch')}'"
        )
        assert kdata.get("is_present") is True
        assert kdata.get("name") == f"{import_name} {import_version}"

    # ------------------------------------------------------------------
    # Step 2: Create a VM with the imported kernel
    # ------------------------------------------------------------------

    def test_create_vm_with_imported_kernel(self, runner_vm: str) -> None:
        """Create a VM using the imported kernel (from step 1)."""
        assert self._import_kernel_short_id is not None, (
            "Step 1 must complete before step 2"
        )

        ensure_vm_deps(runner_vm)

        vm_name = f"sys-test-import-vm-{uuid.uuid4().hex[:8]}"
        network_name = f"sys-test-import-net-{uuid.uuid4().hex[:6]}"
        subnet = f"10.{hash(vm_name) % 254 + 1}.0.0/24"

        type(self)._vm_name = vm_name
        type(self)._network_name = network_name
        type(self)._network_subnet = subnet

        _run_mvm(
            runner_vm,
            "network",
            "create",
            network_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )

        _run_mvm(
            runner_vm,
            "vm",
            "create",
            vm_name,
            "--image",
            "alpine:3.23",
            "--kernel",
            self._import_kernel_short_id,
            "--network",
            network_name,
        )

        # ------------------------------------------------------------------
        # Option C verification: vm ls --json
        # ------------------------------------------------------------------
        vms = json.loads(
            _run_mvm(runner_vm, "vm", "ls", "--json").stdout
        )
        vm_entry = next(
            (v for v in vms if v.get("name") == vm_name), None
        )
        assert vm_entry is not None, (
            f"VM '{vm_name}' not found in vm ls --json after creation"
        )
        assert vm_entry.get("kernel_id", "").startswith(
            self._import_kernel_short_id
        ), (
            f"VM kernel_id '{vm_entry.get('kernel_id')}' does not match "
            f"imported kernel ID prefix '{self._import_kernel_short_id}'"
        )
        status = vm_entry.get("status", "")
        assert status in ("running", "starting"), (
            f"Expected VM status to be 'running' or 'starting', got '{status}'"
        )

        # ------------------------------------------------------------------
        # Option C verification: vm inspect --json
        # ------------------------------------------------------------------
        inspect_result = _run_mvm(
            runner_vm, "vm", "inspect", vm_name, "--json",
        )
        assert inspect_result.returncode == 0
        vm_inspect = json.loads(inspect_result.stdout)
        vm_data = vm_inspect.get("vm", {})
        assert vm_data.get("name") == vm_name
        kernel_id = (
            vm_inspect.get("vm", {}).get("kernel_id", "")
            or vm_inspect.get("assets", {}).get("kernel", {}).get("id", "")
        )
        assert kernel_id.startswith(self._import_kernel_short_id)
        assert vm_data.get("status") in ("running", "starting")

    # ------------------------------------------------------------------
    # Step 3: Verify imported kernel survives stop/start
    # ------------------------------------------------------------------

    def test_imported_kernel_stop_start(self, runner_vm: str) -> None:
        """Stop the VM from step 2, then start it again."""
        assert self._vm_name is not None, "Step 2 must complete before step 3"

        vm_name = self._vm_name

        result = _run_mvm(runner_vm, "vm", "stop", vm_name)
        assert result.returncode == 0, f"vm stop failed: {result.stderr}"

        import time
        time.sleep(2)

        status_after_stop = _get_vm_status(runner_vm, vm_name)
        assert status_after_stop in ("stopped", None), (
            f"Expected VM status 'stopped' after stop, got '{status_after_stop}'"
        )

        result = _run_mvm(runner_vm, "vm", "start", vm_name)
        assert result.returncode == 0, f"vm start failed: {result.stderr}"

        time.sleep(2)
        status_after_start = _get_vm_status(runner_vm, vm_name)
        assert status_after_start == "running", (
            f"Expected VM status 'running' after start, "
            f"got '{status_after_start}'"
        )

        # ------------------------------------------------------------------
        # Option C: verify kernel_id is still our imported kernel
        # ------------------------------------------------------------------
        vms = json.loads(
            _run_mvm(runner_vm, "vm", "ls", "--json").stdout
        )
        vm_entry = next(
            (v for v in vms if v.get("name") == vm_name), None
        )
        assert vm_entry is not None, (
            f"VM '{vm_name}' not found after stop/start cycle"
        )
        assert vm_entry.get("kernel_id", "").startswith(
            self._import_kernel_short_id
        ), (
            f"VM kernel_id changed after stop/start: "
            f"'{vm_entry.get('kernel_id')}' "
            f"(expected prefix '{self._import_kernel_short_id}')"
        )

    # ------------------------------------------------------------------
    # Teardown: clean up VM, network, and imported kernel
    # ------------------------------------------------------------------

    @classmethod
    def teardown_class(cls) -> None:
        """Clean up resources created during tests 1-3."""
        if cls._vm_name:
            _run_mvm(cls._binary, "vm", "rm", cls._vm_name, "--force", check=False)

        if cls._network_name:
            _run_mvm(cls._binary, "network", "rm", cls._network_name, check=False)

        if cls._import_name:
            _run_mvm(
                cls._binary, "kernel", "rm", cls._import_name, "--force", check=False,
            )


# ============================================================================
# Test 4: Import with auto-detected version from filename
# ============================================================================


class TestKernelImportAutoVersion:
    """Import a kernel without ``--version`` — version is auto-detected
    from the filename via ``KernelService.parse_filename()``."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_kernel,
    ]

    def test_import_auto_detected_version(
        self, runner_vm: str,
    ) -> None:
        """Copy firecracker kernel to a versioned temp filename inside the VM and
        import without ``--version``; verify version is auto-detected."""
        _ensure_kernel(runner_vm)
        source_path = _get_firecracker_kernel_path(runner_vm)

        # Copy to a temp path with version info in the filename (inside VM)
        temp_kernel = f"{VM_TMP_DIR}/vmlinux-6.1-x86_64-{uuid.uuid4().hex[:6]}"
        _guest_run(
            runner_vm,
            f"cp {source_path} {temp_kernel}",
        )

        # Verify the temp file is non-empty inside the VM
        size_check = _guest_run(
            runner_vm,
            f"stat -c%s {temp_kernel}",
        )
        assert int(size_check.stdout.strip()) > 0

        import_name = f"sys-test-auto-{uuid.uuid4().hex[:6]}"
        try:
            result = _run_mvm(
                runner_vm,
                "kernel",
                "import",
                import_name,
                temp_kernel,
            )
            assert result.returncode == 0, (
                f"kernel import (auto-version) failed: {result.stderr}"
            )

            # ------------------------------------------------------------------
            # Option C: verify via ls --json
            # ------------------------------------------------------------------
            kernels = json.loads(
                _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
            )
            imported = [
                k for k in kernels if k.get("base_name") == import_name
            ]
            assert len(imported) == 1, (
                f"Expected 1 kernel with base_name='{import_name}', "
                f"found {len(imported)}"
            )
            assert imported[0]["version"] == "6.1", (
                f"Expected auto-detected version '6.1', "
                f"got '{imported[0]['version']}'"
            )
            assert imported[0]["type"] == "custom"
            assert imported[0].get("arch") and len(imported[0]["arch"]) > 0, (
                f"Expected non-empty arch, got '{imported[0].get('arch')}'"
            )
            assert imported[0]["is_present"] is True

            # ------------------------------------------------------------------
            # Option C: verify via inspect --json
            # ------------------------------------------------------------------
            short_id = imported[0]["id"][:6]
            inspect_result = _run_mvm(
                runner_vm, "kernel", "inspect", short_id, "--json",
            )
            assert inspect_result.returncode == 0
            inspect_data = json.loads(inspect_result.stdout)
            assert inspect_data.get("kernel", {}).get("version") == "6.1"

            # ------------------------------------------------------------------
            # Option C: verify file exists on disk (inside VM)
            # ------------------------------------------------------------------
            kernel_path_rel = imported[0].get("path", "")
            assert kernel_path_rel, "path field is empty"
            full_path = kernel_path_rel if os.path.isabs(kernel_path_rel) else f"{VM_KERNEL_DIR}/{kernel_path_rel}"
            check = _guest_run(
                runner_vm,
                f"test -f {full_path} && echo exists || echo not-found",
                check=False,
            )
            assert "exists" in check.stdout, (
                f"Imported kernel file not found inside VM: {full_path}"
            )
            size_check = _guest_run(
                runner_vm,
                f"stat -c%s {full_path}",
            )
            assert int(size_check.stdout.strip()) > 0
        finally:
            _run_mvm(
                runner_vm, "kernel", "rm", import_name, "--force", check=False,
            )


# ============================================================================
# Test 5: Import with --default flag
# ============================================================================


class TestKernelImportDefault:
    """Import a kernel with --default flag and verify is_default=true."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_kernel,
    ]

    def test_import_kernel_with_default(
        self, runner_vm: str,
    ) -> None:
        """Import a kernel with --default and verify is_default=true in ls --json."""
        _ensure_kernel(runner_vm)
        source_path = _get_firecracker_kernel_path(runner_vm)

        import_name = f"sys-test-default-{uuid.uuid4().hex[:6]}"
        imported_prefix = None
        try:
            result = _run_mvm(
                runner_vm,
                "kernel",
                "import",
                import_name,
                source_path,
                "--version",
                "6.1",
                "--default",
            )
            assert result.returncode == 0, (
                f"kernel import --default failed: {result.stderr}"
            )

            kernels = json.loads(
                _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
            )
            imported = [
                k for k in kernels if k.get("base_name") == import_name
            ]
            assert len(imported) == 1, (
                f"Expected 1 imported kernel with base_name='{import_name}', "
                f"found {len(imported)}"
            )
            assert imported[0].get("is_default") is True, (
                "Imported kernel with --default should have is_default=True"
            )
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    runner_vm, "kernel", "rm", imported_prefix, "--force", check=False,
                )


# ============================================================================
# Error paths
# ============================================================================


class TestKernelImportError:
    """Error path tests — no state modification, no serial marker needed."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_kernel,
    ]

    def test_import_nonexistent_path_fails(self, runner_vm: str) -> None:
        """Importing a non-existent file path must fail."""
        result = _run_mvm(
            runner_vm,
            "kernel",
            "import",
            "should-not-exist",
            "/tmp/nonexistent-kernel-file.vmlinux",
            "--version",
            "6.1",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected kernel import of nonexistent path to fail"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "invalid value for 'path'" in combined or "source file not found" in combined, (
            f"Expected error mentioning 'invalid value for path', got: {combined}"
        )

    def test_import_empty_name_fails(self, runner_vm: str) -> None:
        """Importing with an empty name must fail."""
        _ensure_kernel(runner_vm)
        source_path = _get_firecracker_kernel_path(runner_vm)

        result = _run_mvm(
            runner_vm,
            "kernel",
            "import",
            "",
            source_path,
            "--version",
            "6.1",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected kernel import with empty name to fail"
        )

    def test_import_basic_succeeds(self, runner_vm: str) -> None:
        """Import a kernel with valid args (arch is auto-detected in Go CLI)."""
        _ensure_kernel(runner_vm)
        source_path = _get_firecracker_kernel_path(runner_vm)

        result = _run_mvm(
            runner_vm,
            "kernel",
            "import",
            f"sys-test-basic-{uuid.uuid4().hex[:4]}",
            source_path,
            "--version",
            "6.1",
            check=False,
        )
        assert result.returncode == 0, (
            f"Kernel import with valid args failed: {result.stderr}"
        )

    def test_import_duplicate_name_succeeds(self, runner_vm: str) -> None:
        """Importing a kernel with the same name+version+arch creates
        a new entry (different content-addressed ID). Type should be 'custom'."""
        _ensure_kernel(runner_vm)
        source_path = _get_firecracker_kernel_path(runner_vm)

        import_name = f"sys-test-dup-{uuid.uuid4().hex[:6]}"
        import_version = "6.1"

        try:
            result1 = _run_mvm(
                runner_vm,
                "kernel",
                "import",
                import_name,
                source_path,
                "--version",
                import_version,
            )
            assert result1.returncode == 0

            result2 = _run_mvm(
                runner_vm,
                "kernel",
                "import",
                import_name,
                source_path,
                "--version",
                import_version,
            )
            assert result2.returncode == 0

            kernels = json.loads(
                _run_mvm(runner_vm, "kernel", "ls", "--json").stdout
            )
            matching = [
                k for k in kernels if k.get("base_name") == import_name
            ]
            assert len(matching) >= 1, (
                f"Expected at least 1 entry with base_name='{import_name}', "
                f"found {len(matching)}"
            )
            for entry in matching:
                assert entry["type"] == "custom"
        finally:
            _run_mvm(
                runner_vm, "kernel", "rm", import_name, "--force", check=False,
            )


# ============================================================================
# Destructive cleanup — remove all imported kernels and VMs
# ============================================================================


class TestKernelImportCleanup:
    """Remove all custom (imported) kernels that may remain from
    previous test runs and any VMs referencing them.

    This runs last in the file and is explicitly destructive.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_kernel,
    ]

    def test_remove_all_custom_kernels(self, runner_vm: str) -> None:
        """Remove every kernel with type=custom and cleanup any
        VMs that reference them."""
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        assert result.returncode == 0
        all_kernels = json.loads(result.stdout) if result.stdout else []
        custom = [k for k in all_kernels if k.get("type") == "custom"]

        if not custom:
            return  # Nothing to clean up — test passes

        # Remove VMs referencing custom kernels
        custom_ids = {k["id"] for k in custom}
        vm_result = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
        if vm_result.returncode == 0 and vm_result.stdout.strip():
            vms = json.loads(vm_result.stdout)
            for vm in vms:
                vm_kid = vm.get("kernel_id", "")
                if vm_kid in custom_ids:
                    _run_mvm(
                        runner_vm, "vm", "rm", vm["name"], "--force", check=False,
                    )

        # Remove each custom kernel
        for kernel in custom:
            kid = kernel["id"][:6]
            _run_mvm(runner_vm, "kernel", "rm", kid, "--force", check=False)

        # Retry until no custom kernels remain
        for _retry in range(3):
            result = _run_mvm(runner_vm, "kernel", "ls", "--json")
            remaining = json.loads(result.stdout) if result.stdout else []
            custom_remaining = [
                k for k in remaining if k.get("type") == "custom"
            ]
            if not custom_remaining:
                break
            for k in custom_remaining:
                _run_mvm(
                    runner_vm, "kernel", "rm", k["id"][:6], "--force", check=False,
                )

        # Verify no custom kernels remain
        result = _run_mvm(runner_vm, "kernel", "ls", "--json")
        remaining = json.loads(result.stdout) if result.stdout else []
        custom_remaining = [k for k in remaining if k.get("type") == "custom"]
        assert not custom_remaining, (
            f"Custom kernels still remain after cleanup: {custom_remaining}"
        )
