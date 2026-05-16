"""Kernel import system tests — mvm kernel import command.

Tests the full lifecycle: importing a custom kernel from an existing
firecracker kernel file, verifying JSON/filesystem/DB state, creating
a VM with the imported kernel, and verifying stop/start roundtrip.
Also tests auto-detected version from filename and error paths.
"""

from __future__ import annotations

import json
import os
import shutil
import sqlite3
import time
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import _ensure_kernel, _run_mvm, ensure_vm_deps

KERNEL_CACHE_DIR = Path.home() / ".cache" / "mvmctl" / "kernels"

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_kernel,
]

# ============================================================================
# Helpers
# ============================================================================


def _get_firecracker_kernel_path(mvm_binary: str) -> str:
    """Return the absolute path of a present firecracker kernel.

    Caller should invoke ``_ensure_kernel()`` first.
    """
    kernels = json.loads(_run_mvm(mvm_binary, "kernel", "ls", "--json").stdout)
    firecracker = [
        k
        for k in kernels
        if k.get("type") == "firecracker" and k.get("is_present")
    ]
    if not firecracker:
        pytest.skip("No present firecracker kernel to import from")

    fc = firecracker[0]
    fc_id = fc["id"][:6]
    inspect = json.loads(
        _run_mvm(mvm_binary, "kernel", "inspect", fc_id, "--json").stdout
    )
    path = inspect.get("path", "")
    if not path or not os.path.exists(path):
        # The path may be relative (kernel.path stored as filename-only for
        # pulled kernels resolved via CacheUtils.get_kernels_dir()).  Try
        # resolving it.
        resolved = KERNEL_CACHE_DIR / path
        if resolved.exists():
            return str(resolved)
        pytest.skip(
            f"Firecracker kernel path does not exist: {path} (resolved: {resolved})"
        )
    return path


def _get_vm_status(mvm_binary: str, vm_name: str) -> str | None:
    """Return the status of a VM by name, or None if not found."""
    result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
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
    kernel and VM are used across the sequence.  Cleanup happens in
    ``teardown_class``.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_kernel,
        pytest.mark.serial,
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

    def test_import_firecracker_kernel(self, mvm_binary: str) -> None:
        # Rationale: Needs a real kernel file to test the import pipeline end-to-end
        """Import a firecracker kernel as a custom kernel and verify state."""
        type(self)._binary = mvm_binary
        _ensure_kernel(mvm_binary)

        source_path = _get_firecracker_kernel_path(mvm_binary)
        import_name = f"sys-test-import-{uuid.uuid4().hex[:6]}"
        type(self)._import_name = import_name

        import_version = "6.1"
        import_arch = "x86_64"

        # Run mvm kernel import
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "import",
            import_name,
            source_path,
            "--version",
            import_version,
            "--arch",
            import_arch,
        )
        assert result.returncode == 0, f"kernel import failed: {result.stderr}"

        # ------------------------------------------------------------------
        # Option C verification: JSON state
        # ------------------------------------------------------------------
        kernels_after = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
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
        assert imported_kernel["arch"] == import_arch
        assert imported_kernel["is_present"] is True

        # Store for dependent tests
        type(self)._import_kernel_id = imported_kernel["id"]
        type(self)._import_kernel_short_id = imported_kernel["id"][:6]

        # ------------------------------------------------------------------
        # Option C verification: Filesystem
        # ------------------------------------------------------------------
        kernel_path_rel = imported_kernel.get("path", "")
        assert kernel_path_rel, "Imported kernel path is empty in ls --json"
        full_path = KERNEL_CACHE_DIR / kernel_path_rel
        assert full_path.exists(), (
            f"Imported kernel file not found on disk: {full_path}"
        )
        # Cross-check via stat: file must be non-empty
        assert full_path.stat().st_size > 0, (
            f"Imported kernel file is empty: {full_path}"
        )

        # ------------------------------------------------------------------
        # Option C verification: inspect --json
        # ------------------------------------------------------------------
        inspect_result = _run_mvm(
            mvm_binary,
            "kernel",
            "inspect",
            type(self)._import_kernel_short_id,
            "--json",
        )
        assert inspect_result.returncode == 0
        inspect_data = json.loads(inspect_result.stdout)
        assert inspect_data["type"] == "custom"
        assert inspect_data["base_name"] == import_name
        assert inspect_data["version"] == import_version
        assert inspect_data["arch"] == import_arch
        assert inspect_data["is_present"] is True
        # The name is set to f"{name} {version}" by import_kernel()
        assert inspect_data["name"] == f"{import_name} {import_version}"

        # ------------------------------------------------------------------
        # Option C verification: DB-level
        # ------------------------------------------------------------------
        db_path = Path.home() / ".cache" / "mvmctl" / "mvmdb.db"
        conn = sqlite3.connect(str(db_path))
        try:
            conn.row_factory = sqlite3.Row
            cur = conn.execute(
                "SELECT * FROM kernels WHERE base_name = ?", (import_name,)
            )
            row = cur.fetchone()
            assert row is not None, (
                f"Imported kernel not found in DB: base_name='{import_name}'"
            )
            assert row["type"] == "custom"
            assert row["version"] == import_version
            assert row["arch"] == import_arch
            assert row["is_present"] == 1
        finally:
            conn.close()

    # ------------------------------------------------------------------
    # Step 2: Create a VM with the imported kernel
    # ------------------------------------------------------------------

    def test_create_vm_with_imported_kernel(self, mvm_binary: str) -> None:
        # Rationale: Needs a real VM to verify the imported kernel is used at boot
        """Create a VM using the imported kernel (from step 1)."""
        assert self._import_kernel_short_id is not None, (
            "Step 1 must complete before step 2"
        )

        ensure_vm_deps(mvm_binary)

        vm_name = f"sys-test-import-vm-{uuid.uuid4().hex[:8]}"
        network_name = f"sys-test-import-net-{uuid.uuid4().hex[:6]}"
        subnet = f"10.{hash(vm_name) % 254 + 1}.0.0/24"

        type(self)._vm_name = vm_name
        type(self)._network_name = network_name
        type(self)._network_subnet = subnet

        # Create network
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            network_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )

        # Create VM with imported kernel
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            vm_name,
            "--image",
            "alpine:3.21",
            "--kernel",
            self._import_kernel_short_id,
            "--network",
            network_name,
        )

        # ------------------------------------------------------------------
        # Option C verification: vm ls --json
        # ------------------------------------------------------------------
        vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
        vm_entry = next((v for v in vms if v.get("name") == vm_name), None)
        assert vm_entry is not None, (
            f"VM '{vm_name}' not found in vm ls --json after creation"
        )
        # Verify the kernel_id references our imported kernel
        assert vm_entry.get("kernel_id", "").startswith(
            self._import_kernel_short_id
        ), (
            f"VM kernel_id '{vm_entry.get('kernel_id')}' does not match "
            f"imported kernel ID prefix '{self._import_kernel_short_id}'"
        )
        # VM should be in a valid running/starting state
        status = vm_entry.get("status", "")
        assert status in ("running", "starting"), (
            f"Expected VM status to be 'running' or 'starting', got '{status}'"
        )

        # ------------------------------------------------------------------
        # Option C verification: vm inspect --json
        # ------------------------------------------------------------------
        inspect_result = _run_mvm(
            mvm_binary, "vm", "inspect", vm_name, "--json"
        )
        assert inspect_result.returncode == 0
        vm_inspect = json.loads(inspect_result.stdout)
        assert vm_inspect.get("name") == vm_name
        assert vm_inspect.get("kernel_id", "").startswith(
            self._import_kernel_short_id
        )
        assert vm_inspect.get("status") in ("running", "starting")

    # ------------------------------------------------------------------
    # Step 3: Verify imported kernel survives stop/start
    # ------------------------------------------------------------------

    def test_imported_kernel_stop_start(self, mvm_binary: str) -> None:
        # Rationale: Needs a real VM to verify the imported kernel survives stop/start
        """Stop the VM from step 2, then start it again."""
        assert self._vm_name is not None, "Step 2 must complete before step 3"

        vm_name = self._vm_name

        # Stop the VM
        result = _run_mvm(mvm_binary, "vm", "stop", vm_name)
        assert result.returncode == 0, f"vm stop failed: {result.stderr}"

        # Wait briefly for the stop to take effect
        time.sleep(2)

        # Verify stopped
        status_after_stop = _get_vm_status(mvm_binary, vm_name)
        assert status_after_stop in ("stopped", None), (
            f"Expected VM status 'stopped' after stop, got '{status_after_stop}'"
        )

        # Start the VM again
        result = _run_mvm(mvm_binary, "vm", "start", vm_name)
        assert result.returncode == 0, f"vm start failed: {result.stderr}"

        # After start, the VM should be running
        time.sleep(2)
        status_after_start = _get_vm_status(mvm_binary, vm_name)
        assert status_after_start == "running", (
            f"Expected VM status 'running' after start, "
            f"got '{status_after_start}'"
        )

        # ------------------------------------------------------------------
        # Option C: verify kernel_id is still our imported kernel
        # ------------------------------------------------------------------
        vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
        vm_entry = next((v for v in vms if v.get("name") == vm_name), None)
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
        b = cls._binary or "uv run mvm"

        # Remove VM first (may hold network references)
        if cls._vm_name:
            _run_mvm(b, "vm", "rm", cls._vm_name, "--force", check=False)

        # Remove network
        if cls._network_name:
            _run_mvm(b, "network", "rm", cls._network_name, check=False)

        # Remove imported kernel
        if cls._import_name:
            _run_mvm(
                b, "kernel", "rm", cls._import_name, "--force", check=False
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
        pytest.mark.serial,
    ]

    def test_import_auto_detected_version(
        self, mvm_binary: str, tmp_path: Path
    ) -> None:
        # Rationale: Needs a real kernel file to test version auto-detection from filename
        """Copy firecracker kernel to a versioned temp filename and
        import without ``--version``; verify version is auto-detected."""
        _ensure_kernel(mvm_binary)
        source_path = _get_firecracker_kernel_path(mvm_binary)

        # Copy to a temp path with version info in the filename
        # parse_filename() extracts "6.1" from "vmlinux-6.1-x86_64"
        temp_kernel = tmp_path / "vmlinux-6.1-x86_64"
        shutil.copy2(source_path, str(temp_kernel))
        assert temp_kernel.exists(), "Failed to copy kernel to temp path"

        # Verify the temp file is non-empty
        assert temp_kernel.stat().st_size > 0

        import_name = f"sys-test-auto-{uuid.uuid4().hex[:6]}"
        try:
            # Import WITHOUT --version — auto-detect from filename
            result = _run_mvm(
                mvm_binary,
                "kernel",
                "import",
                import_name,
                str(temp_kernel),
                "--arch",
                "x86_64",
            )
            assert result.returncode == 0, (
                f"kernel import (auto-version) failed: {result.stderr}"
            )

            # ------------------------------------------------------------------
            # Option C: verify via ls --json
            # ------------------------------------------------------------------
            kernels = json.loads(
                _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
            )
            imported = [k for k in kernels if k.get("base_name") == import_name]
            assert len(imported) == 1, (
                f"Expected 1 kernel with base_name='{import_name}', "
                f"found {len(imported)}"
            )
            assert imported[0]["version"] == "6.1", (
                f"Expected auto-detected version '6.1', "
                f"got '{imported[0]['version']}'"
            )
            assert imported[0]["type"] == "custom"
            assert imported[0]["arch"] == "x86_64"
            assert imported[0]["is_present"] is True

            # ------------------------------------------------------------------
            # Option C: verify via inspect --json
            # ------------------------------------------------------------------
            short_id = imported[0]["id"][:6]
            inspect_result = _run_mvm(
                mvm_binary,
                "kernel",
                "inspect",
                short_id,
                "--json",
            )
            assert inspect_result.returncode == 0
            inspect_data = json.loads(inspect_result.stdout)
            assert inspect_data["version"] == "6.1"

            # ------------------------------------------------------------------
            # Option C: verify file exists on disk
            # ------------------------------------------------------------------
            kernel_path_rel = imported[0].get("path", "")
            assert kernel_path_rel, "path field is empty"
            full_path = KERNEL_CACHE_DIR / kernel_path_rel
            assert full_path.exists(), (
                f"Imported kernel file not found: {full_path}"
            )
            assert full_path.stat().st_size > 0

            # ------------------------------------------------------------------
            # Option C: DB-level verification
            # ------------------------------------------------------------------
            db_path = Path.home() / ".cache" / "mvmctl" / "mvmdb.db"
            conn = sqlite3.connect(str(db_path))
            try:
                conn.row_factory = sqlite3.Row
                cur = conn.execute(
                    "SELECT * FROM kernels WHERE base_name = ?",
                    (import_name,),
                )
                row = cur.fetchone()
                assert row is not None, (
                    f"Kernel not found in DB: base_name='{import_name}'"
                )
                assert row["version"] == "6.1"
                assert row["arch"] == "x86_64"
                assert row["type"] == "custom"
                assert row["is_present"] == 1
            finally:
                conn.close()

        finally:
            _run_mvm(
                mvm_binary,
                "kernel",
                "rm",
                import_name,
                "--force",
                check=False,
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

    def test_import_nonexistent_path_fails(self, mvm_binary: str) -> None:
        # Rationale: Only needs CLI validation — no real resources needed
        """Importing a non-existent file path must fail."""
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "import",
            "should-not-exist",
            "/tmp/nonexistent-kernel-file.vmlinux",
            "--version",
            "6.1",
            "--arch",
            "x86_64",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected kernel import of nonexistent path to fail"
        )
        combined = (result.stdout + result.stderr).lower()
        assert any(
            s in combined
            for s in ["not found", "does not exist", "no such file"]
        ), f"Expected error message about missing file, got: {combined}"

    def test_import_empty_name_fails(self, mvm_binary: str) -> None:
        # Rationale: Only needs CLI validation — no real kernel creation needed
        """Importing with an empty name must fail."""
        # Use a real path (the firecracker kernel) but an empty name
        _ensure_kernel(mvm_binary)
        source_path = _get_firecracker_kernel_path(mvm_binary)

        result = _run_mvm(
            mvm_binary,
            "kernel",
            "import",
            "",
            source_path,
            "--version",
            "6.1",
            "--arch",
            "x86_64",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected kernel import with empty name to fail"
        )

    def test_import_unsupported_arch_fails(self, mvm_binary: str) -> None:
        # Rationale: Only needs CLI validation — no resources needed
        """Importing with an unsupported architecture must fail."""
        _ensure_kernel(mvm_binary)
        source_path = _get_firecracker_kernel_path(mvm_binary)

        result = _run_mvm(
            mvm_binary,
            "kernel",
            "import",
            f"bad-arch-{uuid.uuid4().hex[:4]}",
            source_path,
            "--version",
            "6.1",
            "--arch",
            "mips",
            check=False,
        )
        assert result.returncode != 0, (
            "Expected kernel import with unsupported arch to fail"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "arch" in combined, (
            f"Expected error mentioning 'arch', got: {combined}"
        )

    def test_import_duplicate_name_succeeds(self, mvm_binary: str) -> None:
        # Rationale: Needs a real kernel file to test duplicate import behavior
        """Importing a kernel with the same name+version+arch creates
        a new entry (different content-addressed ID). Type should be 'custom'.
        """
        _ensure_kernel(mvm_binary)
        source_path = _get_firecracker_kernel_path(mvm_binary)

        import_name = f"sys-test-dup-{uuid.uuid4().hex[:6]}"
        import_version = "6.1"
        import_arch = "x86_64"

        try:
            # First import
            result1 = _run_mvm(
                mvm_binary,
                "kernel",
                "import",
                import_name,
                source_path,
                "--version",
                import_version,
                "--arch",
                import_arch,
            )
            assert result1.returncode == 0

            # Second import with same name — should succeed (creates
            # separate entry with different content-addressed ID)
            result2 = _run_mvm(
                mvm_binary,
                "kernel",
                "import",
                import_name,
                source_path,
                "--version",
                import_version,
                "--arch",
                import_arch,
            )
            assert result2.returncode == 0

            # Verify both entries exist
            kernels = json.loads(
                _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
            )
            matching = [k for k in kernels if k.get("base_name") == import_name]
            assert len(matching) >= 2, (
                f"Expected at least 2 entries with base_name='{import_name}', "
                f"found {len(matching)}"
            )
            for entry in matching:
                assert entry["type"] == "custom"

        finally:
            # Clean up all matching entries
            _run_mvm(
                mvm_binary,
                "kernel",
                "rm",
                import_name,
                "--force",
                check=False,
            )


# ============================================================================
# Test 5: Destructive cleanup — remove all imported kernels and VMs
# ============================================================================


class TestKernelImportCleanup:
    """Remove all custom (imported) kernels that may remain from
    previous test runs and any VMs referencing them.

    This runs last in the file and is explicitly destructive.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_kernel,
        pytest.mark.serial,
    ]

    def test_remove_all_custom_kernels(self, mvm_binary: str) -> None:
        # Rationale: Destructive cleanup — removes any leftover custom kernels and VMs
        """Remove every kernel with type=custom and cleanup any
        VMs that reference them."""
        # Gather all custom kernels
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        assert result.returncode == 0
        all_kernels = json.loads(result.stdout) if result.stdout else []
        custom = [k for k in all_kernels if k.get("type") == "custom"]

        if not custom:
            pytest.skip("No custom (imported) kernels to clean up")

        # Remove VMs referencing custom kernels
        custom_ids = {k["id"] for k in custom}
        vm_result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        if vm_result.returncode == 0 and vm_result.stdout.strip():
            vms = json.loads(vm_result.stdout)
            for vm in vms:
                vm_kid = vm.get("kernel_id", "")
                if vm_kid in custom_ids:
                    _run_mvm(
                        mvm_binary,
                        "vm",
                        "rm",
                        vm["name"],
                        "--force",
                        check=False,
                    )

        # Remove each custom kernel
        for kernel in custom:
            kid = kernel["id"][:6]
            _run_mvm(
                mvm_binary,
                "kernel",
                "rm",
                kid,
                "--force",
                check=False,
            )

        # Verify no custom kernels remain
        result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        remaining = json.loads(result.stdout) if result.stdout else []
        custom_remaining = [k for k in remaining if k.get("type") == "custom"]
        assert len(custom_remaining) == 0, (
            f"Expected no custom kernels remaining after cleanup, "
            f"found {len(custom_remaining)}: "
            f"{[k.get('base_name') for k in custom_remaining]}"
        )
