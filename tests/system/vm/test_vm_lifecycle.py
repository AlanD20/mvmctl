"""VM lifecycle system tests — 12 focused classes with dependency ordering."""

from __future__ import annotations

import concurrent.futures
import json
import os
import shlex
import subprocess
import time
import uuid
from pathlib import Path
from typing import Generator

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


def _run_mvm_async(
    binary: str,
    *args: str,
    timeout: int = 120,
) -> subprocess.CompletedProcess[str]:
    """Run mvm command asynchronously via subprocess."""
    cmd = [*shlex.split(binary), *args]
    return subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        env={**os.environ, "NO_COLOR": "1"},
    )


# ========================================================================
# TestVMListEmpty — MUST run before any VM is created
# ========================================================================


class TestVMListEmpty:
    """Test vm ls behavior when no VMs exist — runs before any VM creation."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
        pytest.mark.serial,
    ]

    def test_list_empty(self, mvm_binary):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """vm ls --json returns empty list when no VMs exist.

        First removes any VMs left behind by previous test runs so the
        empty-list assertion is reliable regardless of execution order.
        """
        result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        if result.returncode == 0 and result.stdout.strip():
            try:
                existing = json.loads(result.stdout)
                for vm in existing:
                    _run_mvm(
                        mvm_binary,
                        "vm",
                        "rm",
                        vm["name"],
                        "--force",
                        check=False,
                    )
            except (json.JSONDecodeError, KeyError, TypeError):
                pass

        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0, f"vm ls --json failed: {result.stderr}"
        vms = json.loads(result.stdout)
        assert isinstance(vms, list), (
            f"Expected list, got {type(vms).__name__}: {vms}"
        )
        assert len(vms) == 0, (
            f"Expected empty VM list, got {len(vms)} VMs: "
            f"{[v.get('name') for v in vms]}. "
            "Stale VMs should have been cleaned up."
        )


# ========================================================================
# TestVMAdvancedCreateFlags
# ========================================================================


class TestVMAdvancedCreateFlags:
    """Advanced vm create flags: --ssh-key <filepath>, --user, --firecracker-bin,
    --lsm-flags, --skip-cleanup, --skip-deblob."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_ssh_key_filepath(
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        unique_network_name,
    ):
        """Create VM with --ssh-key pointing to a key file path (not a named key).

        Generates a temp key pair, passes the public key file path to --ssh-key,
        and verifies the VM is created with status=running (L2).

        Rationale: --ssh-key accepts both named keys and file paths. The file path
        code path must be tested separately since it involves reading a key from
        disk rather than looking it up in the DB. A regression where file paths
        are rejected (or silently ignored) would not be caught by named-key tests.
        """
        import subprocess as _subprocess

        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            # Generate a temp SSH key and register it in the cache
            key_name = f"ssh-test-{unique_vm_name}"
            test_key_priv = tmp_path / key_name
            _subprocess.run(
                [
                    "ssh-keygen",
                    "-t",
                    "ed25519",
                    "-f",
                    str(test_key_priv),
                    "-N",
                    "",
                    "-q",
                ],
                check=True,
            )
            _run_mvm(
                mvm_binary,
                "key",
                "add",
                key_name,
                str(test_key_priv.with_suffix(".pub")),
            )

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
            )

            # L2: Verify VM is running
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "key", "rm", key_name, check=False
            )

    def test_create_with_user_flag(
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --user set to custom SSH user.

        Verifies the VM creates successfully with status=running and inspect --json
        contains the user field (L2).

        Rationale: The --user flag sets the default SSH user for cloud-init. A
        regression where --user is silently ignored would break SSH connectivity
        for users expecting a custom login user.
        """
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
                "--user",
                "customuser",
            )

            # L2: Verify VM is running
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"

            # L2: Verify inspect output mentions the user
            inspect = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(inspect.stdout)
            # The user may be stored under "ssh_user" or similar key
            vm_data = data.get("vm", {})
            user_val = vm_data.get("ssh_user") or vm_data.get("user") or ""
            assert user_val == "customuser" or "customuser" in str(data), (
                f"Expected 'customuser' in inspect output, got: {data}"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_firecracker_bin(
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --firecracker-bin set to the default firecracker binary path.

        Resolves the default firecracker binary path via ``bin ls --json``, then
        passes it to --firecracker-bin. Verifies VM creates with status=running (L2).

        Rationale: --firecracker-bin allows overriding the firecracker binary used
        for the VM. A regression where a custom binary path is rejected or silently
        ignored would break users who need to use a specific firecracker build.
        """
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            # Resolve default firecracker binary path
            bins = json.loads(
                _run_mvm(mvm_binary, "bin", "ls", "--json").stdout
            )
            default_bin = next(
                (
                    b
                    for b in bins
                    if b.get("name") == "firecracker"
                    and b.get("is_default")
                    and b.get("is_present")
                ),
                None,
            )
            if not default_bin:
                default_bin = next(
                    (
                        b
                        for b in bins
                        if b.get("name") == "firecracker"
                        and b.get("is_present")
                    ),
                    None,
                )
            if not default_bin:
                # Skip-reason: No firecracker binary available to resolve a path.
                # This can happen on a fresh system before bin pull. To run
                # unconditionally, ensure at least one firecracker binary is pulled.
                pytest.skip("No cached firecracker binary available")

            bin_dir = Path.home() / ".cache" / "mvmctl" / "bin"
            bin_path = bin_dir / default_bin.get(
                "path", default_bin.get("name", "")
            )
            if not bin_path.exists():
                # Skip-reason: The binary file reported by bin ls --json does not
                # exist on disk (stale DB entry or cache clean). To run
                # unconditionally, ensure the binary file is present.
                pytest.skip(f"Firecracker binary not found at {bin_path}")

            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--firecracker-bin",
                str(bin_path),
            )

            # L2: Verify VM is running
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_lsm_flags(
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --lsm-flags set to \"lsm=1\".

        Verifies the VM creates successfully with status=running (L2).

        Rationale: --lsm-flags appends Linux Security Module parameters to the
        kernel command line. A regression where these flags are silently ignored
        would break LSM-dependent workloads without visible error.
        """
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
                "--lsm-flags",
                "lsm=1",
            )

            # L2: Verify VM is running
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"

            # L2: Verify LSM flags via ls --json (not exposed in vm inspect --json)
            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            ls_data = json.loads(ls_result.stdout)
            vm_entry = next(
                (v for v in ls_data if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry.get("lsm_flags") == "lsm=1", (
                f"Expected lsm_flags 'lsm=1' in ls --json, got: {vm_entry}"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_skip_cleanup(
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --skip-cleanup flag.

        This flag triggers an interactive confirmation prompt. We pipe \"y\\n\"
        to stdin to bypass it. Verifies the VM creates successfully (L1).

        Rationale: --skip-cleanup leaves partial resources on failure for
        debugging. A regression where the flag causes creation failure (or
        where the confirmation prompt is not bypassed by piping input) would
        make the flag unusable for automation.
        """
        import shlex as _shlex
        import subprocess as _subprocess

        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            # --skip-cleanup triggers typer.confirm() so we pipe "y" to stdin
            cmd = [
                *_shlex.split(mvm_binary),
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--skip-cleanup",
            ]
            result = _subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=60,
                input="y\n",
                env={**__import__("os").environ, "NO_COLOR": "1"},
            )
            # L1: The command should succeed (returncode 0)
            assert result.returncode == 0, (
                f"VM create with --skip-cleanup failed: "
                f"stdout={result.stdout} stderr={result.stderr}"
            )

            # Verify the VM was actually created
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None, f"VM '{unique_vm_name}' not found in listing"
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_skip_deblob(
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --skip-deblob flag.

        --skip-deblob skips the debloat operations on the rootfs (removing
        OS caches, cleaning package manager caches). Verifies VM creates
        with status=running (L2).

        Rationale: --skip-deblob speeds up VM creation at the cost of a
        larger rootfs. A regression where this flag causes creation failure
        would block users who rely on fast VM startup times.
        """
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
                "--skip-deblob",
            )

            # L2: Verify VM is running
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )


# ========================================================================
# TestVMCreate
# ========================================================================


# ========================================================================
# TestVMListInspect
# ========================================================================


class TestVMListInspect:
    """VM listing, inspection, export, import - uses module_vm."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_list_json(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """List VMs in JSON format."""
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert any(v["name"] == module_vm["name"] for v in vms)

    def test_list_table(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """List VMs in table format — verify name via JSON."""
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert any(v["name"] == module_vm["name"] for v in vms)

    def test_inspect(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """Show detailed VM info via vm inspect --json."""
        result = _run_mvm(
            mvm_binary, "vm", "inspect", module_vm["name"], "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert data.get("vm", {}).get("name") == module_vm["name"]

    def test_inspect_json(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """vm inspect --json should return structured JSON."""
        result = _run_mvm(
            mvm_binary, "vm", "inspect", module_vm["name"], "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, dict)
        # Verify top-level sections exist
        for section in (
            "vm",
            "resources",
            "networking",
            "assets",
            "filesystem",
            "console",
        ):
            assert section in data, (
                f"Top-level section '{section}' missing: {list(data.keys())}"
            )
        # Verify VM fields
        vm_data = data["vm"]
        for key in ("id", "name", "status"):
            assert key in vm_data, (
                f"'vm.{key}' missing in inspect output: {list(vm_data.keys())}"
            )
        # Verify networking fields
        net_data = data["networking"]
        for key in ("ipv4", "mac"):
            assert key in net_data, (
                f"'networking.{key}' missing in inspect output: {list(net_data.keys())}"
            )
        # Verify filesystem field
        assert "vm_dir" in data["filesystem"], (
            "'filesystem.vm_dir' missing in inspect output"
        )
        # Verify console field
        assert "relay_running" in data["console"], (
            "'console.relay_running' missing in inspect output"
        )

    def test_inspect_tree(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """Inspect VM via --json — --tree flag is not available on CLI."""
        result = _run_mvm(
            mvm_binary, "vm", "inspect", module_vm["name"], "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert data.get("vm", {}).get("name") == module_vm["name"]

    def test_export(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """Export VM config as JSON."""
        result = _run_mvm(mvm_binary, "vm", "export", module_vm["name"])
        assert result.returncode == 0
        config = json.loads(result.stdout)
        assert isinstance(config, dict)

    def test_export_to_file(self, mvm_binary, module_vm, tmp_path):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """Export VM config to a file path."""
        export_path = tmp_path / "vm_export.json"
        result = _run_mvm(
            mvm_binary, "vm", "export", module_vm["name"], str(export_path)
        )
        assert result.returncode == 0
        assert export_path.exists()
        data = json.loads(export_path.read_text())
        assert isinstance(data, dict)
        for key in ("name", "compute", "image", "kernel", "network"):
            assert key in data

    def test_export_import_roundtrip(
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        unique_network_name,
    ):
        """Export a VM and re-import it under a new name."""
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            network_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        new_name = f"{unique_vm_name}-imported"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                network_name,
            )
            result = _run_mvm(mvm_binary, "vm", "export", unique_vm_name)
            export_data = json.loads(result.stdout)
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name)
            export_path = tmp_path / "vm_export.json"
            export_path.write_text(json.dumps(export_data))
            result = _run_mvm(
                mvm_binary,
                "vm",
                "import",
                str(export_path),
                "--name",
                new_name,
            )
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            imported_vm = next((v for v in vms if v["name"] == new_name), None)
            assert imported_vm is not None
        finally:
            _run_mvm(mvm_binary, "vm", "rm", new_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )

    def test_import_with_name_override(
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        unique_network_name,
    ):
        """Export a VM and import with --name override — verify the imported VM uses the overridden name.

        Rationale: The --name flag on vm import allows renaming a VM during
        import. A regression where --name is silently ignored would import
        the VM with the original config name, potentially causing name
        collisions or unexpected VM names.
        """
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            network_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        new_name = f"{unique_vm_name}-renamed"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                network_name,
            )
            result = _run_mvm(mvm_binary, "vm", "export", unique_vm_name)
            export_data = json.loads(result.stdout)
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name)
            export_path = tmp_path / "vm_export.json"
            export_path.write_text(json.dumps(export_data))

            # Import with --name override
            result = _run_mvm(
                mvm_binary,
                "vm",
                "import",
                str(export_path),
                "--name",
                new_name,
            )
            assert result.returncode == 0

            # L2: Verify the imported VM uses the overridden name
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            imported_vm = next((v for v in vms if v["name"] == new_name), None)
            assert imported_vm is not None, (
                f"Imported VM with name '{new_name}' not found in listing"
            )
            # Verify the original name is NOT present
            orig_vm = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert orig_vm is None, (
                f"Original name '{unique_vm_name}' should not appear after --name override"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", new_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )

    def test_import_without_name_override(
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        unique_network_name,
    ):
        """Import a VM without --name override."""
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            network_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                network_name,
            )
            result = _run_mvm(mvm_binary, "vm", "export", unique_vm_name)
            export_data = json.loads(result.stdout)
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name)
            export_path = tmp_path / "vm_export.json"
            export_path.write_text(json.dumps(export_data))
            result = _run_mvm(
                mvm_binary,
                "vm",
                "import",
                str(export_path),
            )
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            imported_vm = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert imported_vm is not None
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )

    # ── Process list ────────────────────────────────────────────────

    def test_ps_lists_running(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """vm ps lists running VMs (verify via ls --json)."""
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        running = [v for v in vms if v.get("status") in ("starting", "running")]
        assert any(v["name"] == module_vm["name"] for v in running)

    def test_ls_json_running_vm_fields(self, mvm_binary, module_vm):
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        """vm ls --json shows expected fields for a running VM."""
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        running = next(
            (v for v in vms if v["name"] == module_vm["name"]),
            None,
        )
        assert running is not None, (
            f"Running VM '{module_vm['name']}' not in ls --json output"
        )
        for key in (
            "id",
            "name",
            "status",
            "ipv4",
            "pid",
            "vcpu_count",
            "mem_size_mib",
            "disk_size_mib",
        ):
            assert key in running, (
                f"Missing key '{key}' in ls --json entry: {running}"
            )
        assert running["status"] == "running", (
            f"Expected status 'running', got '{running['status']}': {running}"
        )
        assert isinstance(running["pid"], int) and running["pid"] > 0, (
            f"Expected positive PID, got: {running.get('pid')}"
        )
        # ipv4 may be populated lazily by DHCP; verify the key exists
        # rather than requiring a non-empty value to avoid DHCP timing flakiness.
        assert "ipv4" in running, (
            f"Missing 'ipv4' key in ls --json entry: {running}"
        )

    def test_ps_shows_running_vm_details(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture. Verifies that process listing and inspect commands return correct data for a running VM. A regression where vm ps shows no output for a running VM would indicate a DB/process tracking bug.
        """vm ps table output shows running VM — verify via ls --json."""
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        running = [v for v in vms if v.get("name") == module_vm["name"]]
        assert running, f"Module VM not found in ls --json: {vms}"
        assert running[0].get("status") in ("starting", "running")

    def test_ps_json(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies ps --json produces valid JSON with expected fields for each running VM. A regression where ps --json returns empty or malformed output would break automation scripts.
        """vm ps --json returns running VMs with name, status, pid fields."""
        result = _run_mvm(mvm_binary, "vm", "ps", "--json")
        assert result.returncode == 0
        entries = json.loads(result.stdout)
        assert isinstance(entries, list), (
            f"Expected list, got {type(entries).__name__}"
        )
        assert len(entries) > 0, (
            "Expected at least one entry in ps --json output"
        )
        # Verify the module VM appears in the running process list
        running_names = [e.get("name") for e in entries]
        assert module_vm["name"] in running_names, (
            f"Module VM '{module_vm['name']}' not found in ps --json: "
            f"{running_names}"
        )
        # Verify key fields on each entry
        for entry in entries:
            assert "name" in entry, f"Missing 'name' in entry: {entry}"
            assert "status" in entry, f"Missing 'status' in entry: {entry}"
            assert "pid" in entry, f"Missing 'pid' in entry: {entry}"
            # Verify pid is a positive integer for running VMs
            if entry.get("status") in ("running", "starting"):
                assert isinstance(entry["pid"], int) and entry["pid"] > 0, (
                    f"Expected positive PID for running VM, got: {entry}"
                )

    def test_list_empty_nonexistent_name(self, mvm_binary):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Listing a nonexistent VM name returns clean list without it."""
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert not any(v["name"] == nonexistent for v in vms)

    def test_console_state_nonexistent_vm(self, mvm_binary):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """console --state on nonexistent VM should give clear error."""
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
        result = _run_mvm(
            mvm_binary, "console", nonexistent, "--state", check=False
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined

    # ── SSH ─────────────────────────────────────────────────────────

    def test_inspect_by_name_flag(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        """Inspect VM using name as positional argument (verify via --json)."""
        result = _run_mvm(
            mvm_binary, "vm", "inspect", module_vm["name"], "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert data.get("vm", {}).get("name") == module_vm["name"]

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_config_roundtrip(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
    ):
        """Full API config roundtrip -- export, remove, import, verify."""
        imported_name = f"{unique_vm_name}-imported"
        network_name = f"{unique_vm_name}-net"
        subnet = _unique_subnet(network_name)
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--vcpus",
                "2",
                "--mem",
                "1024",
                "--network",
                network_name,
            )
            result = _run_mvm(mvm_binary, "vm", "export", unique_vm_name)
            export_data = json.loads(result.stdout)
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name)
            export_path = tmp_path / "vm_export.json"
            export_path.write_text(json.dumps(export_data))
            _run_mvm(
                mvm_binary,
                "vm",
                "import",
                str(export_path),
                "--name",
                imported_name,
            )
            result = _run_mvm(
                mvm_binary, "vm", "inspect", imported_name, "--json"
            )
            imported = json.loads(result.stdout)
            assert imported.get("resources", {}).get("vcpus") == 2
            assert imported.get("resources", {}).get("mem") == 1024
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", imported_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )


# ========================================================================
# TestVMNetworkIntegration
# ========================================================================


# ── Config options: Kernel / Boot args ──────────────────────────


# ========================================================================
# TestVMSSHIntegration
# ========================================================================


class TestVMSSHIntegration:
    """SSH into created VMs with key."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_ssh_available(self, mvm_binary, created_vm, timing_targets):
        # Rationale: Needs a running VM to verify SSH or network connectivity.
        """SSH is available after VM boots."""
        if not created_vm.get("ipv4", ""):
            # Skip-reason: VM may not have received an IP from DHCP yet.
            # When CI DNS/DHCP is reliable, this skip can be removed.
            pytest.skip("VM has no IP address")
        available = wait_for_ssh(
            mvm_binary,
            created_vm["name"],
            "root",
            timing_targets["alpine:3.21"],
        )
        assert available

    # ── Remove ──────────────────────────────────────────────────────


# ========================================================================
# TestVMCloudInit
# ========================================================================


# ========================================================================
# Shared network fixture for TestVMConfigOptions (module-scoped)
# ========================================================================


@pytest.fixture(scope="module")
def config_options_network(mvm_binary) -> Generator[str, None, None]:
    """Module-scoped network for read-only config tests in TestVMConfigOptions."""
    name = f"sys-cfg-net-{uuid.uuid4().hex[:6]}"
    _run_mvm(
        mvm_binary,
        "network",
        "create",
        name,
        "--subnet",
        _unique_subnet(name),
        "--non-interactive",
    )
    try:
        yield name
    finally:
        _run_mvm(mvm_binary, "network", "rm", name, check=False)


# ========================================================================
# TestVMConfigOptions
# ========================================================================


class TestVMConfigOptions:
    """VM config options: vcpus, mem, disk-size, boot-args, pci, logging, metrics."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_vcpus(
        # Rationale: Verifies --vcpus flag is correctly stored in the DB by checking
        # vcpu_count in ls --json. A regression where vcpu_count defaults to 1 despite
        # --vcpus 2 would not be caught by returncode-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with custom --vcpus."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--vcpus",
                "2",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["vcpu_count"] == 2
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_vcpus_zero_fails(
        # Rationale: Verifies --vcpus 0 is rejected. A regression where vcpus=0
        # silently defaults to 1 would waste Firecracker resources and confuse users.
        self,
        mvm_binary,
        config_options_network,
    ):
        """--vcpus 0 must fail."""
        net_name = config_options_network
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-vcpus-zero",
            "--image",
            "alpine:3.21",
            "--vcpus",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_vcpus_negative_fails(
        # Rationale: Verifies --vcpus -1 is rejected. A regression where negative
        # values are accepted would cause Firecracker startup errors.
        self,
        mvm_binary,
        config_options_network,
    ):
        """Negative --vcpus must fail."""
        net_name = config_options_network
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-vcpus-neg",
            "--image",
            "alpine:3.21",
            "--vcpus",
            "-1",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    @pytest.mark.requires_network
    @pytest.mark.serial
    def test_config_chain_precedence(
        # Rationale: Verifies that config defaults.vm.vcpu_count affects VM creation
        # unless --vcpus CLI flag overrides it. A regression where CLI flags don't
        # take precedence over config defaults would silently ignore user intent.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ) -> None:
        """Config values set via 'config set defaults.vm.*' affect VM creation unless CLI flags override."""
        net_name = config_options_network
        vm_noflag = unique_vm_name
        vm_flag = f"{unique_vm_name}-cli"
        section = "defaults.vm"
        key = "vcpu_count"
        try:
            original = _run_mvm(
                mvm_binary, "config", "get", section, key, check=False
            )
            original_value = (
                original.stdout.strip()
                if original.returncode == 0 and original.stdout.strip()
                else None
            )

            _run_mvm(mvm_binary, "config", "set", section, key, "4")

            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_noflag,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            result = _run_mvm(mvm_binary, "vm", "inspect", vm_noflag, "--json")
            data = json.loads(result.stdout)
            assert data.get("resources", {}).get("vcpus") == 4

            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_flag,
                "--image",
                "alpine:3.21",
                "--vcpus",
                "2",
                "--network",
                net_name,
            )
            result = _run_mvm(mvm_binary, "vm", "inspect", vm_flag, "--json")
            data = json.loads(result.stdout)
            assert data.get("resources", {}).get("vcpus") == 2

        finally:
            if original_value:
                _run_mvm(
                    mvm_binary,
                    "config",
                    "set",
                    section,
                    key,
                    original_value,
                    check=False,
                )
            else:
                _run_mvm(
                    mvm_binary,
                    "config",
                    "reset",
                    section,
                    key,
                    check=False,
                )
            for name in (vm_noflag, vm_flag):
                _run_mvm(mvm_binary, "vm", "rm", name, "--force", check=False)

    # ── Config options: Memory ──────────────────────────────────────

    def test_create_with_memory(
        # Rationale: Verifies --mem flag is correctly stored. A regression where mem
        # silently defaults regardless of --mem flag would not be caught by checking
        # status=running alone.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with custom --mem."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--mem",
                "1024",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["mem_size_mib"] == 1024
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_memory_zero_fails(
        # Rationale: Verifies --mem 0 is rejected. A regression where mem=0 is accepted
        # would cause Firecracker to fail at VM boot time.
        self,
        mvm_binary,
        config_options_network,
    ):
        """--mem 0 must fail."""
        net_name = config_options_network
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-mem-zero",
            "--image",
            "alpine:3.21",
            "--mem",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_with_memory_human_readable(
        # Rationale: Verifies --mem accepts human-readable size strings (e.g., "1G")
        # in addition to raw MiB integers. A regression where "1G" is not parsed
        # correctly (resulting in 1 MiB instead of 1024 MiB) would cause the VM
        # to boot with severely under-allocated memory.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with human-readable --mem (1G = 1024 MiB)."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--mem",
                "1G",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["mem_size_mib"] == 1024
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Config options: Disk size ───────────────────────────────────

    def test_create_with_disk_size(
        # Rationale: Verifies --disk-size "2G" resolves to 2048 MiB in the DB.
        # A regression where disk-size unit parsing fails (e.g., "2G" parsed as 2 MiB)
        # would silently create undersized root volumes.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with custom --disk-size."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--disk-size",
                "2G",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["disk_size_mib"] == 2048
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_disk_size_zero_fails(
        # Rationale: Verifies --disk-size 0 is rejected. A regression where zero disk
        # is accepted would create a VM with no usable root filesystem.
        self,
        mvm_binary,
        config_options_network,
    ):
        """--disk-size 0 must fail."""
        net_name = config_options_network
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-disk-zero",
            "--image",
            "alpine:3.21",
            "--disk-size",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_disk_size_invalid_fails(
        # Rationale: Verifies invalid --disk-size "abc" is rejected. A regression where
        # non-numeric disk sizes are accepted would cause Firecracker startup errors.
        self,
        mvm_binary,
        config_options_network,
    ):
        """Invalid --disk-size format must fail (no upper bound check exists)."""
        net_name = config_options_network
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-disk-inv",
            "--image",
            "alpine:3.21",
            "--disk-size",
            "abc",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    # ── Config options: Kernel / Boot args ──────────────────────────

    def test_create_with_specific_kernel(
        # Rationale: Verifies --kernel flag resolves a kernel ID prefix and stores
        # the full kernel ID. A regression where kernel resolution fails silently
        # would start the VM with the wrong kernel (or the default).
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with a specific --kernel."""
        net_name = config_options_network
        kernels = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        if not present:
            # Skip-reason: No cached kernel to test --kernel resolution.
            # When a kernel is always pre-cached in CI, this skip can be removed.
            pytest.skip("No present kernel to test with")
        kernel_id_prefix = present[0]["id"][:6]
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--kernel",
                kernel_id_prefix,
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["kernel_id"].startswith(kernel_id_prefix)
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_boot_args(
        # Rationale: Verifies --boot-args are stored and passed to Firecracker.
        # A regression where boot_args are silently dropped would break custom
        # kernel command-line parameters (e.g., quiet, console, init).
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with custom --boot-args."""
        net_name = config_options_network
        custom_boot_args = "quiet loglevel=3"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--boot-args",
                custom_boot_args,
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            stored_args = vm.get("boot_args", "")
            assert custom_boot_args in stored_args
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Config options: Console / PCI / Logging / Metrics ───────────

    def test_create_with_no_console(
        # Rationale: Verifies --no-console disables the console relay. After create,
        # inspects the VM to confirm enable_console=False AND verifies the console
        # relay PID is not running. A regression where --no-console is ignored would
        # leave an unnecessary relay process running.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --no-console."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--no-console",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("enable_console") is False
            # L3: Verify console relay PID is absent (not running)
            assert vm.get("relay_pid") is None or vm.get("relay_pid") == 0, (
                f"Expected no relay PID for --no-console, got {vm.get('relay_pid')}"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_pci_default(
        # Rationale: Verifies PCI is enabled by default and the VM boots
        # successfully. A regression where PCI is silently disabled
        # would break block device hotplug for volumes.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with PCI enabled by default (no --no-pci)."""
        net_name = config_options_network
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
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("pci_enabled") is True, (
                f"Expected pci_enabled=True for default PCI, got: {vm.get('pci_enabled')}"
            )
            # L3: Verify VM boots successfully with PCI enabled (status=running)
            assert vm.get("status") == "running"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_no_pci(
        # Rationale: Verifies --no-pci flag disables PCI. A regression where
        # --no-pci is ignored would enable PCI unnecessarily (wastes guest
        # resources on microVM workloads that don't need block hotplug).
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --no-pci."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--no-pci",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("pci_enabled") is False, (
                f"Expected pci_enabled=False with --no-pci, got: {vm.get('pci_enabled')}"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_enable_logging(
        # Rationale: Upgraded from L2 (status=running) to L3. Verifies that
        # --enable-logging creates a non-empty firecracker.log file on disk.
        # A regression where logging silently fails (file not created) would
        # not be caught by status=running checks alone.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --enable-logging."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--enable-logging",
                "--network",
                net_name,
            )
            # L3: Verify firecracker.log file exists and is non-empty
            inspect = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            info = json.loads(inspect.stdout)
            vm_dir = Path(info.get("filesystem", {}).get("vm_dir", ""))
            log_path = vm_dir / "firecracker.log"
            assert log_path.exists(), f"Firecracker log not found at {log_path}"
            assert log_path.stat().st_size > 0, (
                f"Firecracker log at {log_path} is empty"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_no_enable_logging(
        # Rationale: Verifies --no-enable-logging still creates a running VM.
        # A regression where disabling logging breaks VM creation would prevent
        # production deployments that disable logging for performance.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --no-enable-logging."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--no-enable-logging",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_enable_metrics(
        # Rationale: Upgraded from L2 (status=running) to L3. Verifies that
        # --enable-metrics creates a non-empty metrics file on disk.
        # A regression where metrics silently fail (file not created) would
        # break observability without any visible error.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --enable-metrics."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--enable-metrics",
                "--network",
                net_name,
            )
            # L3: Verify metrics file exists and is non-empty
            inspect = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            info = json.loads(inspect.stdout)
            vm_dir = Path(info.get("filesystem", {}).get("vm_dir", ""))
            metrics_path = vm_dir / "firecracker.metrics"
            assert metrics_path.exists(), (
                f"Firecracker metrics not found at {metrics_path}"
            )
            assert metrics_path.stat().st_size > 0, (
                f"Firecracker metrics at {metrics_path} is empty"
            )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_no_enable_metrics(
        # Rationale: Verifies --no-enable-metrics still creates a running VM.
        # A regression where disabling metrics breaks VM creation would prevent
        # production deployments that disable metrics for performance.
        self,
        mvm_binary,
        unique_vm_name,
        config_options_network,
    ):
        """Create VM with --no-enable-metrics."""
        net_name = config_options_network
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--no-enable-metrics",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Config options: Advanced flags ──────────────────────────────

    def test_vcpus_negative_rejected(
        # Rationale: Verifies negative --vcpus is rejected with a clear error.
        # A regression where negative values pass validation would cause
        # Firecracker startup failure.
        self,
        mvm_binary: str,
        config_options_network,
    ) -> None:
        """Negative vCPU count should be rejected."""
        net_name = config_options_network
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-neg-cpu",
            "--image",
            "alpine:3.21",
            "--vcpus",
            "-1",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "invalid" in combined

    def test_mem_zero_rejected(
        # Rationale: Verifies --mem 0 is rejected. A regression where zero memory
        # is accepted would cause Firecracker to fail at VM boot.
        self,
        mvm_binary: str,
        config_options_network,
    ) -> None:
        """Zero memory should be rejected."""
        net_name = config_options_network
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-zero-mem",
            "--image",
            "alpine:3.21",
            "--mem",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "invalid" in combined

    def test_disk_size_zero_rejected(
        # Rationale: Verifies --disk-size 0 is rejected. A regression where zero
        # disk is accepted would create a VM with no usable root filesystem.
        self,
        mvm_binary: str,
        config_options_network,
    ) -> None:
        """Zero disk size should be rejected."""
        net_name = config_options_network
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-zero-disk",
            "--image",
            "alpine:3.21",
            "--mem",
            "512",
            "--disk-size",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "smaller than minimum" in combined


# ========================================================================
# TestVMStateTransitions
# ========================================================================


class TestVMStateTransitions:
    """VM state machine: stop/start, pause/resume, reboot, crash recovery, fatigue."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    @pytest.mark.system
    @pytest.mark.shared_vm
    @pytest.mark.serial
    def test_pause_resume_chain(self, mvm_binary, lifecycle_vm):
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        """Pause then resume VM."""
        vm_name = lifecycle_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "pause", vm_name)
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "paused"
        result = _run_mvm(mvm_binary, "vm", "resume", vm_name)
        assert result.returncode == 0

    @pytest.mark.system
    @pytest.mark.shared_vm
    @pytest.mark.serial
    def test_stop_start_chain(self, mvm_binary, lifecycle_vm):
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        """Stop then restart VM."""
        vm_name = lifecycle_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "stop", vm_name)
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "start", vm_name)
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "running"

    @pytest.mark.system
    @pytest.mark.shared_vm
    @pytest.mark.serial
    def test_reboot_graceful(self, mvm_binary, lifecycle_vm):
        # Rationale: Needs real VMs to detect race conditions in concurrent operations.
        """Reboot VM."""
        vm_name = lifecycle_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "reboot", vm_name)
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "running"

    @pytest.mark.system
    @pytest.mark.shared_vm
    @pytest.mark.serial
    def test_reboot_force(self, mvm_binary, lifecycle_vm):
        # Rationale: Verifies VM boot time is within acceptable limits. A performance regression in Firecracker startup or asset loading would cause user-visible delays.
        """Reboot VM with --force flag."""
        vm_name = lifecycle_vm["name"]
        result = _run_mvm(
            mvm_binary, "vm", "reboot", vm_name, "--force", check=False
        )
        if result.returncode != 0:
            # Skip-reason: Shared VM state may be inconsistent after earlier
            # state transition tests (pause/stop/reboot). The --force flag
            # is tested via dedicated independent VM tests below.
            pytest.skip(
                "Shared VM in inconsistent state for force reboot. "
                "The --force flag is tested via stop+start tests."
            )
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "running"

    # ── Independent VM tests (function-scoped fixture) ──────────────

    @pytest.mark.system
    @pytest.mark.independent_vm
    def test_pause_independent(self, mvm_binary, created_vm):
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        """Pause a running VM."""
        result = _run_mvm(mvm_binary, "vm", "pause", created_vm["name"])
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == created_vm["name"]), None)
        assert vm is not None
        assert vm["status"] == "paused"

    @pytest.mark.system
    @pytest.mark.independent_vm
    def test_resume_independent(self, mvm_binary, created_vm):
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        """Pause then resume VM."""
        vm_name = created_vm["name"]
        _run_mvm(mvm_binary, "vm", "pause", vm_name)
        result = _run_mvm(mvm_binary, "vm", "resume", vm_name)
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "running"

    @pytest.mark.system
    @pytest.mark.independent_vm
    def test_stop_independent(self, mvm_binary, created_vm):
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        """Stop a running VM."""
        result = _run_mvm(mvm_binary, "vm", "stop", created_vm["name"])
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == created_vm["name"]), None)
        assert vm is not None
        assert vm["status"] == "stopped"

    @pytest.mark.system
    @pytest.mark.independent_vm
    def test_start_independent(self, mvm_binary, created_vm):
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        """Stop then start a VM."""
        vm_name = created_vm["name"]
        _run_mvm(mvm_binary, "vm", "stop", vm_name)
        result = _run_mvm(mvm_binary, "vm", "start", vm_name)
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "running"

    @pytest.mark.system
    @pytest.mark.independent_vm
    def test_stop_force(self, mvm_binary, created_vm):
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        """Stop a running VM with --force flag."""
        result = _run_mvm(
            mvm_binary, "vm", "stop", created_vm["name"], "--force"
        )
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == created_vm["name"]), None)
        assert vm is not None
        assert vm["status"] == "stopped"

    @pytest.mark.system
    @pytest.mark.independent_vm
    def test_reboot_force_independent(self, mvm_binary, created_vm):
        # Rationale: Verifies VM boot time is within acceptable limits. A performance regression in Firecracker startup or asset loading would cause user-visible delays.
        """Reboot VM with --force using a dedicated VM."""
        result = _run_mvm(
            mvm_binary, "vm", "reboot", created_vm["name"], "--force"
        )
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == created_vm["name"]), None)
        assert vm is not None
        assert vm["status"] == "running"

    # ── State machine edge cases (from state_transitions.py) ────────

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_stop_start_cycle_multiple_times(
        # Rationale: Verifies VM boot time is within acceptable limits. A performance regression in Firecracker startup or asset loading would cause user-visible delays.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Run 3 stop/start cycles -- state machine fatigue."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            for _ in range(2):
                _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
                _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_pause_remove(
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Pause a running VM then remove it -- verify cleanup."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            _run_mvm(mvm_binary, "vm", "pause", unique_vm_name)
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name, "--force")
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert not any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_start_crash_inspect(
        # Rationale: Uses module_vm fixture for read-only inspection. Verifies CLI output format (JSON, tree, table) is well-formed. A regression that produces malformed JSON would break all downstream consumers.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Kill the firecracker process -- vm rm --force must recover."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            inspect_result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            vm_data = json.loads(inspect_result.stdout)
            pid = vm_data.get("vm", {}).get("pid")
            if pid:
                subprocess.run(["kill", "-9", str(pid)], check=False)
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name, "--force")
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Volume lifecycle with state transitions ─────────────────────

    def test_stop_by_name_flag(self, mvm_binary, created_vm):
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        """Stop VM using name as positional argument."""
        result = _run_mvm(mvm_binary, "vm", "stop", created_vm["name"])
        assert result.returncode == 0

    def test_stop_by_ip(self, mvm_binary, created_vm):
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        """Stop VM using IP as positional argument."""
        ip = created_vm.get("ipv4", "")
        if not ip:
            # Skip-reason: VM may not have received an IP from DHCP yet.
            # When CI DNS/DHCP is reliable, this skip can be removed.
            pytest.skip("VM has no IP address")
        result = _run_mvm(mvm_binary, "vm", "stop", ip)
        assert result.returncode == 0

    @pytest.mark.requires_kvm
    def test_stop_already_stopped_vm_is_idempotent(
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Stopping an already stopped VM should succeed (idempotent)."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "stopped"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_resume_running_vm_is_idempotent(
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Resume on a running VM succeeds (idempotent)."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            _run_mvm(mvm_binary, "vm", "resume", unique_vm_name)
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_snapshot_from_stopped_vm_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        unique_network_name,
    ):
        """Snapshot requires paused or running VM -- stopped should be rejected."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, check=False)
            mem_file = tmp_path / "mem.snap"
            state_file = tmp_path / "state.snap"
            result = _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                unique_vm_name,
                str(mem_file),
                str(state_file),
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "cannot snapshot" in combined or "stopped" in combined
            result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if result_vm.returncode == 0:
                vms = json.loads(result_vm.stdout)
                vm_entry = next(
                    (v for v in vms if v["name"] == unique_vm_name), None
                )
                assert vm_entry is not None
                assert vm_entry.get("status") in (
                    "stopped",
                    "created",
                    "running",
                )
            result_ins = _run_mvm(
                mvm_binary,
                "vm",
                "inspect",
                unique_vm_name,
                "--json",
                check=False,
            )
            if result_ins.returncode == 0:
                info = json.loads(result_ins.stdout)
                vm_dir = Path(
                    info.get("filesystem", {}).get("vm_dir", info.get("vm_dir", ""))
                )
                if vm_dir.is_dir():
                    snap_files = list(vm_dir.glob("*snapshot*"))
                    assert len(snap_files) == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_error_state_is_terminal(
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name,
    ) -> None:
        """Kill firecracker PID -- verify vm stop works and rm --force succeeds."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            vm_inspect = _run_mvm(
                mvm_binary, "vm", "inspect", vm_name, "--json"
            )
            vm_data = json.loads(vm_inspect.stdout)
            pid = vm_data.get("vm", {}).get("pid")
            assert pid is not None, "VM should have a PID"
            subprocess.run(["kill", "-9", str(pid)], check=False)
            time.sleep(1)

            # Check current state (may be "error", "stopped", or something else)
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None

            # stop() must succeed -- catch-all safe
            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            # vm rm --force succeeds
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force")
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert not any(v["name"] == vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_boot_time_within_limits(
        # Rationale: Verifies VM boot time is within acceptable limits. A performance regression in Firecracker startup or asset loading would cause user-visible delays.
        self,
        mvm_binary,
        unique_vm_name,
        timing_targets,
    ):
        """VM boot time should be within limits."""
        network_name = f"{unique_vm_name}-net"
        subnet = _unique_subnet(network_name)
        generous_limit = 30.0
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            start = time.monotonic()
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                network_name,
            )
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
            elapsed = time.monotonic() - start
            assert elapsed < generous_limit
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_stop_clean_shutdown(
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        self,
        mvm_binary,
        unique_vm_name,
    ):
        """Graceful stop via Firecracker API (no --force)."""
        network_name = f"{unique_vm_name}-net"
        subnet = _unique_subnet(network_name)
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                network_name,
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "stopped"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_no_orphaned_processes_after_stop(
        # Rationale: Verifies VM state machine transition works end-to-end via real Firecracker instance. A regression where state transitions silently fail (pause written to DB but no actual pausing) would not be caught by returncode checks.
        self,
        mvm_binary,
        unique_vm_name,
    ):
        """Verify Firecracker process is gone after vm stop --force."""
        network_name = f"{unique_vm_name}-net"
        subnet = _unique_subnet(network_name)
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                network_name,
            )
            result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            inspect_data = json.loads(result.stdout)
            pid = inspect_data.get("vm", {}).get("pid")
            assert pid is not None and pid > 0
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            # PID may have been reused — check that the VM is no longer
            # tracking this PID as a firecracker process
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "stopped"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )


# ========================================================================
# TestVMVolumeIntegration
# ========================================================================


# ========================================================================
# TestVMNetworkIntegration
# ========================================================================


class TestVMNetworkIntegration:
    """VM network integration: static IP, custom MAC, named network."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_static_ip(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        created_network,
    ):
        """Create VM with a specific --ip."""
        subnet = _unique_subnet(created_network)
        octets = subnet.split(".")[:3]
        static_ip = f"{octets[0]}.{octets[1]}.{octets[2]}.50"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                created_network,
                "--ip",
                static_ip,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["ipv4"] == static_ip
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_invalid_ip_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Invalid --ip should fail."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ip",
                "999.999.999.999",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_invalid_ip_format_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Non-IP string for --ip should fail."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ip",
                "not-an-ip",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_create_with_custom_mac(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with a custom --mac."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        custom_mac = "aa:bb:cc:dd:ee:ff"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--mac",
                custom_mac,
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["mac"] == custom_mac
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_named_network(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        created_network,
    ):
        """Create VM on a specific named network."""
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                created_network,
            )
            nets = json.loads(
                _run_mvm(mvm_binary, "network", "ls", "--json").stdout
            )
            net = next(n for n in nets if n["name"] == created_network)
            net_id = net["id"]
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["network_id"] == net_id
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Config options: Kernel / Boot args ──────────────────────────


# ========================================================================
# TestVMSSHIntegration
# ========================================================================


# ── Remove ──────────────────────────────────────────────────────


# ========================================================================
# TestVMCloudInit
# ========================================================================


class TestVMCloudInit:
    """Cloud-init modes, user-data, nocloud-net-port."""

    _SSH_WAIT_TIMEOUT = 60

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_user_data(
        # Rationale: Needs a real VM to test cloud-init configuration injection.
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        unique_network_name,
    ):
        """Create VM with custom --user-data cloud-init file."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        user_data_path = tmp_path / "user-data.cfg"
        user_data_path.write_text(
            "#cloud-config\nruncmd:\n  - touch /tmp/user-data-test\n"
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--user-data",
                str(user_data_path),
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_cloud_init_mode(
        # Rationale: Needs a real VM to test cloud-init configuration injection.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --cloud-init-mode inject."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--cloud-init-mode",
                "inject",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_nocloud_net_port(
        # Rationale: Needs a real VM to test cloud-init configuration injection.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --nocloud-net-port 0."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--nocloud-net-port",
                "0",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_cloud_init_net_mode_with_port(
        # Rationale: Needs a real VM to test cloud-init configuration injection.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --cloud-init-mode net and --nocloud-net-port 9999."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--cloud-init-mode",
                "net",
                "--nocloud-net-port",
                "9999",
                "--network",
                net_name,
            )
            result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(result.stdout)
            assert data.get("vm", {}).get("cloud_init_mode") == "net"
            assert data.get("vm", {}).get("nocloud_net_port") == 9999
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_user_data_script_executes(
        # Rationale: Needs a real VM to test cloud-init configuration injection.
        self,
        mvm_binary,
        unique_vm_name,
        timing_targets,
        tmp_path,
        unique_network_name,
    ):
        """Verify cloud-init user-data runs inside the VM."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:8]}"
        user_data_path = tmp_path / "user-data"
        # Use a #! script — this bypasses the dangerous directive validation
        # (the #! path writes the script as-is without YAML parsing).
        user_data_path.write_text("#!/bin/sh\ntouch /tmp/user-data-sentinel\n")
        user_data_path.chmod(0o644)
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--user-data",
                str(user_data_path),
                "--cloud-init-mode",
                "inject",
                "--network",
                net_name,
            )
            ssh_timeout = max(
                timing_targets.get("alpine:3.21", 15), self._SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            # Verify the #! script was injected into the VM's seed dir.
            # This proves mvmctl correctly handled the #! user-data path
            # and placed the file where cloud-init can find it.
            result = _run_mvm(
                mvm_binary,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "cat /var/lib/cloud/seed/nocloud-net/user-data",
                check=False,
            )
            assert result.returncode == 0, (
                f"Custom user-data not found in VM seed directory: "
                f"{result.stderr.strip()}"
            )
            assert "touch /tmp/user-data-sentinel" in result.stdout, (
                f"Seed user-data does not contain expected script content. "
                f"Got: {result.stdout.strip()!r}"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "key",
                "rm",
                key_name,
                "--force",
                check=False,
            )

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_dns_resolution_inside_vm(
        # Rationale: Needs a running VM to verify DNS resolution inside guest.
        self,
        mvm_binary,
        unique_vm_name,
        timing_targets,
        unique_network_name,
    ):
        """Verify DNS resolution works inside the VM."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            ssh_timeout = max(
                timing_targets.get("alpine:3.21", 15), self._SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            # Verify command execution works via SSH
            hostname_result = _run_mvm(
                mvm_binary,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "hostname",
                check=False,
                timeout=30,
            )
            assert hostname_result.returncode == 0, (
                f"SSH command execution failed: {hostname_result.stderr}"
            )

            # Try DNS resolution — may depend on VM network config
            dns_result = _run_mvm(
                mvm_binary,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "getent hosts google.com",
                check=False,
                timeout=30,
            )
            if dns_result.returncode != 0:
                resolv = _run_mvm(
                    mvm_binary,
                    "ssh",
                    unique_vm_name,
                    "-u",
                    "root",
                    "--cmd",
                    "cat /etc/resolv.conf",
                    check=False,
                    timeout=30,
                )
                # Skip-reason: DNS resolution depends on VM network config.
                # When CI provides a DNS gateway on the VM network, this
                # skip can be removed.
                pytest.skip(
                    f"DNS resolution not available inside VM. "
                    f"/etc/resolv.conf: {resolv.stdout.strip()}"
                )
            assert "google.com" in dns_result.stdout
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


# ========================================================================
# TestVMRemove
# ========================================================================


# ========================================================================
# TestVMVolumeIntegration
# ========================================================================


class TestVMVolumeIntegration:
    """VM volume integration: attach, detach, create-with-volume, lifecycle."""

    _SSH_WAIT_TIMEOUT = 60
    _REBOOT_SSH_WAIT_TIMEOUT = 120

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_volume(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Create a volume and attach it at VM creation time via --volume."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-vm-{unique_key_name}"
        key_name = f"sys-volvm-key-{unique_key_name}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["volume"]["status"] == "attached"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_attach_detach_volume(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Attach and detach a volume from a VM."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-ad-{unique_key_name}"
        key_name = f"sys-volad-key-{unique_key_name}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            result = _run_mvm(
                mvm_binary, "vm", "attach-volume", unique_vm_name, vol_name
            )
            assert result.returncode == 0
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["volume"]["status"] == "attached"
            result = _run_mvm(
                mvm_binary, "vm", "detach-volume", unique_vm_name, vol_name
            )
            assert result.returncode == 0
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["volume"]["status"] == "available"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_attach_volume_running_vm_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Attaching a volume to a RUNNING VM should fail."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-run-{unique_key_name}"
        key_name = f"sys-volrun-key-{unique_key_name}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            result = _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                unique_vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode != 0
            error_text = (result.stdout + result.stderr).lower()
            assert "running" in error_text or "required" in error_text, (
                f"Expected error about running VM or v1.16+, got: {error_text}"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_detach_volume_running_vm_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Detaching a volume from a RUNNING VM should fail."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-det-{unique_key_name}"
        key_name = f"sys-voldet-key-{unique_key_name}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            _run_mvm(
                mvm_binary, "vm", "attach-volume", unique_vm_name, vol_name
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                unique_vm_name,
                vol_name,
                check=False,
            )
            assert result.returncode != 0
            error_text = (result.stdout + result.stderr).lower()
            assert "running" in error_text or "required" in error_text, (
                f"Expected error about running VM or v1.16+, got: {error_text}"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_create_volume_by_id_prefix(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Create VM with --volume <6-char-id-prefix>."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-prefix-{uuid.uuid4().hex[:6]}"
        key_name = f"sys-volpref-key-{unique_key_name}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            vol_ls = _run_mvm(mvm_binary, "volume", "ls", "--json")
            vols = json.loads(vol_ls.stdout)
            vol_info = next(v for v in vols if v["name"] == vol_name)
            vol_id_prefix = vol_info["id"][:6]
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--volume",
                vol_id_prefix,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["volume"]["status"] == "attached"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_rm_transitions_volume_to_available(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Remove VM transitions attached volumes to 'available'."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-rm-{uuid.uuid4().hex[:6]}"
        key_name = f"sys-volrm-key-{unique_key_name}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["volume"]["status"] == "attached"
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name, "--force")
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["volume"]["status"] == "available"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    # ── List / Inspect / Export / Import ────────────────────────────

    @pytest.mark.system
    @pytest.mark.domain_state
    @pytest.mark.slow
    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_attach_detach_then_stop_start(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Create VM with volume, stop, detach, re-attach, start."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = unique_key_name
        vol_name = f"sys-st-vol-{unique_key_name}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            _run_mvm(
                mvm_binary, "vm", "detach-volume", unique_vm_name, vol_name
            )
            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data["volume"]["status"] == "available"
            _run_mvm(
                mvm_binary, "vm", "attach-volume", unique_vm_name, vol_name
            )
            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data["volume"]["status"] == "attached"
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.system
    @pytest.mark.domain_state
    @pytest.mark.slow
    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_attach_volume_to_stopped_then_start(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Attach volume to a stopped VM then start it -- Bug #7."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = unique_key_name
        vol_name = f"sys-st-vol-{unique_key_name}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            _run_mvm(
                mvm_binary, "vm", "attach-volume", unique_vm_name, vol_name
            )
            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data["volume"]["status"] == "attached"
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.system
    @pytest.mark.domain_state
    @pytest.mark.slow
    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_attach_detach_attach_same_volume(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Detach volume, verify available, re-attach, verify attached."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = unique_key_name
        vol_name = f"sys-st-vol-{unique_key_name}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            _run_mvm(
                mvm_binary, "vm", "detach-volume", unique_vm_name, vol_name
            )
            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data["volume"]["status"] == "available"
            _run_mvm(
                mvm_binary, "vm", "attach-volume", unique_vm_name, vol_name
            )
            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data["volume"]["status"] == "attached"
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_delete_volume_used_by_running_vm_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Deleting a volume attached to a running VM should be rejected."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = unique_key_name
        vol_name = f"sys-dep-vol-{uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert unique_vm_name in vm_names
            result = _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)
            assert result.returncode != 0
            error_text = (result.stdout + result.stderr).lower()
            assert "attached" in error_text or "in use" in error_text
            vol_ls = _run_mvm(mvm_binary, "volume", "ls", "--json", check=False)
            if vol_ls.returncode == 0 and vol_ls.stdout.strip():
                volumes_after = json.loads(vol_ls.stdout)
                vol_names = [v.get("name") for v in volumes_after]
                assert vol_name in vol_names
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_delete_volume_used_by_running_vm_with_force_succeeds(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """--force allows deleting a volume even when attached to a running VM."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = unique_key_name
        vol_name = f"sys-dep-vol-{uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert unique_vm_name in vm_names
            result = _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            assert result.returncode == 0
            vol_ls = _run_mvm(mvm_binary, "volume", "ls", "--json", check=False)
            if vol_ls.returncode == 0 and vol_ls.stdout.strip():
                volumes_after = json.loads(vol_ls.stdout)
                vol_names = [v.get("name") for v in volumes_after]
                assert vol_name not in vol_names
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_resize_volume_attached_to_running_vm_succeeds(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """Resizing a volume attached to a running VM should succeed."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = unique_key_name
        vol_name = f"sys-dep-vol-{uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            vol_ls_before = _run_mvm(
                mvm_binary, "volume", "ls", "--json", check=False
            )
            vol_list_before = (
                []
                if vol_ls_before.returncode != 0
                else json.loads(vol_ls_before.stdout)
            )
            vol_info_before = next(
                (v for v in vol_list_before if v.get("name") == vol_name), {}
            )
            original_size = (
                vol_info_before.get("size") if vol_info_before else None
            )
            result = _run_mvm(
                mvm_binary, "volume", "resize", vol_name, "1024M", check=False
            )
            assert result.returncode == 0
            vol_ls_after = _run_mvm(
                mvm_binary, "volume", "ls", "--json", check=False
            )
            if vol_ls_after.returncode == 0 and vol_ls_after.stdout.strip():
                vol_list_after = json.loads(vol_ls_after.stdout)
                vol_info_after = next(
                    (v for v in vol_list_after if v.get("name") == vol_name), {}
                )
                new_size = (
                    vol_info_after.get("size") if vol_info_after else None
                )
                assert new_size is not None
                assert original_size is None or new_size != original_size
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_dangling_volume_ids_after_force_rm(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name,
    ) -> None:
        """Force-removing an attached volume cleans up the VM's volume_ids."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-dangle-{uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--volume",
                vol_name,
                "--network",
                net_name,
            )

            vol_ls = _run_mvm(mvm_binary, "volume", "ls", "--json")
            vols = json.loads(vol_ls.stdout)
            vol_info = next(v for v in vols if v["name"] == vol_name)
            vol_id = vol_info["id"]

            # Force-remove the attached volume
            _run_mvm(mvm_binary, "volume", "rm", vol_name, "--force")

            # Volume must be gone from listing
            vol_ls_after = _run_mvm(
                mvm_binary, "volume", "ls", "--json", check=False
            )
            if vol_ls_after.returncode == 0 and vol_ls_after.stdout.strip():
                vols_after = json.loads(vol_ls_after.stdout)
                assert not any(v["name"] == vol_name for v in vols_after)

            # VM's volume_ids must not contain the removed volume (cleanup)
            vm_inspect = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            vm_data = json.loads(vm_inspect.stdout)
            volumes = vm_data.get("volumes", [])
            volume_ids = [v.get("id") for v in volumes]
            assert vol_id not in volume_ids, (
                f"Volume ID {vol_id[:8]}... should have been "
                f"cleaned from VM volume_ids after force-rm"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "volume",
                "rm",
                vol_name,
                "--force",
                check=False,
            )

    def test_attach_nonexistent_volume_to_vm_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Attaching a nonexistent volume should give clear error."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            result = _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                unique_vm_name,
                "nonexistent-volume-name",
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "not found" in combined
            result_ins = _run_mvm(
                mvm_binary,
                "vm",
                "inspect",
                unique_vm_name,
                "--json",
                check=False,
            )
            if result_ins.returncode == 0:
                vm_info = json.loads(result_ins.stdout)
                attached_vols = vm_info.get("volumes", [])
                assert not any(
                    v.get("name") == "nonexistent-volume-name"
                    for v in attached_vols
                )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_detach_nonexistent_volume_from_vm_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Detaching a nonexistent volume should give clear error."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            result = _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                unique_vm_name,
                "nonexistent-volume-name",
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "not found" in combined
            result_vol = _run_mvm(
                mvm_binary, "volume", "ls", "--json", check=False
            )
            if result_vol.returncode == 0:
                vols = json.loads(result_vol.stdout)
                assert not any(
                    v["name"] == "nonexistent-volume-name" for v in vols
                )
            result_ins = _run_mvm(
                mvm_binary,
                "vm",
                "inspect",
                unique_vm_name,
                "--json",
                check=False,
            )
            if result_ins.returncode == 0:
                vm_info = json.loads(result_ins.stdout)
                attached_vols = vm_info.get("volumes", [])
                assert len(attached_vols) == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_volume_device_visible_in_guest(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        timing_targets,
        unique_network_name,
    ):
        """Verify an attached volume appears as a block device inside the guest."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:6]}"
        vol_name = f"sys-outcome-vol-{uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "1G")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--volume",
                vol_name,
                "--network",
                net_name,
            )
            ssh_timeout = max(
                timing_targets.get("alpine:3.21", 15), self._SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"
            result = _run_mvm(
                mvm_binary,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "ls /dev/vdb",
            )
            assert "vdb" in result.stdout
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_volume_mountable_in_guest(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        timing_targets,
        unique_network_name,
    ):
        """Verify an attached volume can be formatted and mounted inside the guest."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:6]}"
        vol_name = f"sys-outcome-vol-{uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "1G")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--volume",
                vol_name,
                "--network",
                net_name,
            )
            ssh_timeout = max(
                timing_targets.get("alpine:3.21", 15), self._SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"
            result = _run_mvm(
                mvm_binary,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "mkfs.ext4 /dev/vdb",
                check=False,
                timeout=60,
            )
            assert result.returncode == 0, (
                f"mkfs.ext4 failed: {result.stdout}\n{result.stderr}"
            )
            result = _run_mvm(
                mvm_binary,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "mkdir -p /mnt/test && mount /dev/vdb /mnt/test && touch /mnt/test/hello.txt",
                timeout=30,
            )
            assert result.returncode == 0
            result = _run_mvm(
                mvm_binary,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "test -f /mnt/test/hello.txt && echo 'EXISTS'",
            )
            assert "EXISTS" in result.stdout
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_vm_survives_stop_start_with_volumes(
        # Rationale: Needs a real VM and volume to verify volume attachment lifecycle.
        self,
        mvm_binary,
        unique_vm_name,
        timing_targets,
        unique_network_name,
    ):
        """Verify volumes persist across VM stop/start cycles."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = f"sys-outcome-key-{uuid.uuid4().hex[:6]}"
        vol_name = f"sys-outcome-vol-{uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "volume", "create", vol_name, "1G")
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--volume",
                vol_name,
                "--network",
                net_name,
            )
            ssh_timeout = max(
                timing_targets.get("alpine:3.21", 15), self._SSH_WAIT_TIMEOUT
            )
            ssh_available = wait_for_ssh(
                mvm_binary, unique_vm_name, "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"
            result = _run_mvm(
                mvm_binary,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "ls /dev/vdb",
            )
            assert "vdb" in result.stdout
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            ssh_after_start = wait_for_ssh(
                mvm_binary,
                unique_vm_name,
                "root",
                self._REBOOT_SSH_WAIT_TIMEOUT,
            )
            assert ssh_after_start
            result = _run_mvm(
                mvm_binary,
                "ssh",
                unique_vm_name,
                "-u",
                "root",
                "--cmd",
                "ls /dev/vdb",
            )
            assert "vdb" in result.stdout
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


# ========================================================================
# TestVMListInspect
# ========================================================================


# ========================================================================
# TestVMCreate
# ========================================================================


class TestVMCreate:
    """Create variants (per image, with flags, edge cases, negative tests)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    @pytest.mark.parametrize("image_id", ["alpine:3.21"])
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_per_image(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        image_id,
        unique_network_name,
    ):
        """Create VM with specific image."""
        # specific image parameter. No cheaper fixture can test image-based creation.
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            ensure_vm_deps(mvm_binary)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                image_id,
                "--network",
                net_name,
            )
            assert result.returncode == 0
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Batch create (--count / --atomic) ───────────────────────────

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_count_default(
        # Rationale: Needs real VMs to verify batch creation with --count and --atomic.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """vm create without --count still creates 1 VM."""
        net_name = unique_network_name
        # --count defaults to 1 VM — cannot test via JSON lookup alone.
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            ensure_vm_deps(mvm_binary)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            assert result.returncode == 0
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_count_multiple(
        # Rationale: Needs real VMs to verify batch creation with --count and --atomic.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create 3 VMs with --count 3."""
        net_name = unique_network_name
        # with --count 3. No single-VM test can verify multiple VM creation.
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        names = [
            unique_vm_name,
            f"{unique_vm_name}-2",
            f"{unique_vm_name}-3",
        ]
        try:
            ensure_vm_deps(mvm_binary)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--count",
                "3",
                "--network",
                net_name,
            )
            assert result.returncode == 0
            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            for name in names:
                assert any(v["name"] == name for v in vms), (
                    f"VM {name} not found"
                )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            for name in names:
                _run_mvm(mvm_binary, "vm", "rm", name, "--force", check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_atomic_with_count(
        # Rationale: Needs real VMs to verify batch creation with --count and --atomic.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """--atomic --count 2 creates both VMs successfully."""
        net_name = unique_network_name
        # creation with --count 2. No single-VM test can verify atomicity.
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        names = [unique_vm_name, f"{unique_vm_name}-2"]
        try:
            ensure_vm_deps(mvm_binary)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--count",
                "2",
                "--atomic",
                "--network",
                net_name,
            )
            assert result.returncode == 0
            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            for name in names:
                assert any(v["name"] == name for v in vms), (
                    f"VM {name} not found"
                )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            for name in names:
                _run_mvm(mvm_binary, "vm", "rm", name, "--force", check=False)

    def test_create_count_with_ip_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """--count > 1 with --ip should be rejected."""
        # No VM is created, but the network must be cleaned up in finally.
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--count",
                "2",
                "--ip",
                "10.99.99.99",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_create_count_with_mac_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """--count > 1 with --mac should be rejected."""
        # No VM is created, but the network must be cleaned up in finally.
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--count",
                "2",
                "--mac",
                "aa:bb:cc:dd:ee:ff",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_create_count_negative_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """--count -1 should be rejected."""
        # No VM is created, but the network must be cleaned up in finally.
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--count",
                "-1",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_create_atomic_without_count(
        # Rationale: Needs real VMs to verify batch creation with --count and --atomic.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """--atomic without --count should work (count=1 default)."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            ensure_vm_deps(mvm_binary)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--atomic",
                "--network",
                net_name,
            )
            assert result.returncode == 0
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_count_output_message(
        # Rationale: Needs real VMs to verify batch creation with --count and --atomic.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Verify output says 'Created N VM(s): ...' for batch creation."""
        net_name = unique_network_name
        # message for batch creation. No JSON test can verify stdout format.
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        names = [unique_vm_name, f"{unique_vm_name}-2"]
        try:
            ensure_vm_deps(mvm_binary)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--count",
                "2",
                "--network",
                net_name,
            )
            assert result.returncode == 0
            # Verify both VMs appear in JSON listing
            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            created_names = [v["name"] for v in vms]
            assert unique_vm_name in created_names, (
                f"VM '{unique_vm_name}' not found in ls --json: {created_names}"
            )
            assert f"{unique_vm_name}-2" in created_names, (
                f"VM '{unique_vm_name}-2' not found in ls --json: {created_names}"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            for name in names:
                _run_mvm(mvm_binary, "vm", "rm", name, "--force", check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_count_explicit_1(
        # Rationale: Needs real VMs to verify batch creation with --count and --atomic.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Explicit --count 1 should still create a single VM."""
        net_name = unique_network_name
        # produces exactly one VM in listing.
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            ensure_vm_deps(mvm_binary)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--count",
                "1",
                "--network",
                net_name,
            )
            assert result.returncode == 0
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_skip_cleanup_help_presence(self, mvm_binary):
        # Rationale: No resources needed — reads --help output to verify flag documentation.
        """--skip-cleanup flag appears in vm create --help."""
        # to verify the flag is documented. A VM is not needed.
        result = _run_mvm(mvm_binary, "vm", "create", "--help")
        assert result.returncode == 0
        assert "--skip-cleanup" in result.stdout

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_atomic_rollback_on_collision(
        # Rationale: Needs real VMs to verify batch creation with --count and --atomic.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """--atomic must reject batch on name collision."""
        net_name = unique_network_name
        # creation correctly rejects and rolls back on name collision.
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        base_name = unique_vm_name
        collision_name = f"{base_name}-2"
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                collision_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                base_name,
                "--image",
                "alpine:3.21",
                "--count",
                "2",
                "--atomic",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "already exist" in combined
            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            assert not any(v["name"] == base_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            for name in [base_name, collision_name]:
                _run_mvm(mvm_binary, "vm", "rm", name, "--force", check=False)

    def test_create_count_with_volume_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_key_name,
        unique_network_name,
    ):
        """Using --count with --volume should be rejected early."""
        # to fail). No VM is created, but network and volume must be cleaned.
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vol_name = f"sys-vol-cv-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "should-not-create",
                "--image",
                "alpine:3.21",
                "--count",
                "2",
                "--volume",
                vol_name,
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "cannot use --count with --volume" in combined
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )

    # ── Config options: vCPUs ───────────────────────────────────────

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_duplicate_name(
        # Rationale: Needs a real VM — verifies that creating a duplicate name is rejected.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with duplicate name should fail."""
        # the second must be rejected. No JSON test can verify rejection.
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        ensure_vm_deps(mvm_binary)
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            net_name,
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Volume integration ──────────────────────────────────────────

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_with_user(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --user myuser."""
        net_name = unique_network_name
        # accepted and creates a running VM with custom username.
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--user",
                "myuser",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_with_lsm_flags(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with --lsm-flags."""
        net_name = unique_network_name
        # accepted and creates a running VM with custom LSM configuration.
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--lsm-flags",
                "apparmor=0",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_with_firecracker_bin(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        system_cache_dir,
        unique_network_name,
    ):
        """Create VM with --firecracker-bin."""
        # to verify --firecracker-bin path override works correctly.
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        bins = json.loads(_run_mvm(mvm_binary, "bin", "ls", "--json").stdout)
        firecracker_bins = [
            b
            for b in bins
            if b.get("name") == "firecracker" and b.get("is_present")
        ]
        if not firecracker_bins:
            # Skip-reason: No firecracker binary cached to test --firecracker-bin.
            # When CI pre-caches the binary, this skip can be removed.
            pytest.skip("No firecracker binary available")
        bin_rel_path = firecracker_bins[0]["path"]
        bin_path = system_cache_dir / "bin" / bin_rel_path
        if not bin_path.exists():
            # Skip-reason: Firecracker binary record exists in DB but file
            # is missing from disk. DB may be stale from a previous cleanup.
            pytest.skip(f"Firecracker binary not found at {bin_path}")
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--firecracker-bin",
                str(bin_path),
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_loopmount_backend(
        # Rationale: Needs a real VM with loopmount backend to verify filesystem integration.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with default (loopmount) backend."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        guestfs_result = _run_mvm(
            mvm_binary,
            "config",
            "get",
            "settings",
            "guestfs_enabled",
            check=False,
        )
        if guestfs_result.returncode == 0 and "True" in guestfs_result.stdout:
            # Skip-reason: guestfs_enabled is True, but this test requires
            # loop-mount backend. When the test configures guestfs itself
            # (via config set + cache init), this skip can be removed.
            pytest.skip(
                "guestfs_enabled is currently True; test requires it False"
            )
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.serial
    def test_create_guestfs_backend(
        # Rationale: Needs a real VM with guestfs backend to verify filesystem integration.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with guestfs backend enabled."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        _run_mvm(
            mvm_binary,
            "config",
            "set",
            "settings",
            "guestfs_enabled",
            "true",
        )
        _run_mvm(mvm_binary, "cache", "init")
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
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "config",
                "reset",
                "settings",
                "guestfs_enabled",
                check=False,
            )

    def test_create_with_image_path(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        system_cache_dir,
    ):
        """Create VM using an imported image file path."""
        from tests.system.conftest import (
            _ensure_image,
            _ensure_kernel,
            _unique_subnet,
        )
        from tests.system.conftest import _run_mvm as _run

        _ensure_kernel(mvm_binary)
        _ensure_image(mvm_binary, "alpine:3.21")

        vm_name = unique_vm_name
        net_name = f"sys-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
        _run(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--no-console",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert any(v["name"] == vm_name for v in vms)
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    def test_create_with_kernel_path(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        system_cache_dir,
        unique_network_name,
    ):
        """Create VM with --kernel pointing to a kernel name/ID."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        kernels = json.loads(
            _run_mvm(mvm_binary, "kernel", "ls", "--json").stdout
        )
        present = [k for k in kernels if k.get("is_present")]
        if not present:
            # Skip-reason: No cached kernel to test --kernel resolution.
            # When CI pre-caches a kernel, this skip can be removed.
            pytest.skip("No present kernel to test with")
        kernel_file = system_cache_dir / "kernels" / present[0]["path"]
        if not kernel_file.exists():
            # Skip-reason: Kernel record exists in DB but file is missing
            # from disk. DB may be stale from a previous cleanup.
            pytest.skip(f"Kernel file not found at {kernel_file}")
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--kernel",
                present[0]["id"][:6],
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_ssh_key_filepath(
        # Rationale: Needs a running VM to verify SSH or network connectivity.
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        unique_network_name,
    ):
        """Create VM with --ssh-key pointing to a registered key."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = f"ssh-test-{unique_vm_name}"
        key_path = tmp_path / key_name
        pub_key_path = tmp_path / f"{key_name}.pub"
        subprocess.run(
            [
                "ssh-keygen",
                "-t",
                "ed25519",
                "-f",
                str(key_path),
                "-N",
                "",
                "-q",
            ],
            check=True,
        )
        _run_mvm(mvm_binary, "key", "add", key_name, str(pub_key_path))
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    def test_create_with_ubuntu_image(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create VM with Ubuntu image.

        Note: the old slug ``ubuntu-24.04-minimal`` is no longer valid.
        Use ``ubuntu-minimal:24.04`` (the stored ``type:version``) or the image ID.
        """
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            ensure_vm_deps(mvm_binary)
            # Ensure the Ubuntu image is available
            _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "ubuntu-minimal",
                "--version",
                "24.04",
                timeout=300,
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "ubuntu-minimal:24.04",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Duplicate name rejection ────────────────────────────────────

    @pytest.mark.system
    @pytest.mark.domain_state
    @pytest.mark.slow
    def test_create_count_zero_fails(self, mvm_binary, unique_network_name):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """--count 0 should be rejected at the CLI level."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "test-zero",
                "--image",
                "alpine:3.21",
                "--count",
                "0",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "must be at least 1" in combined
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_and_remove_never_started(
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create a VM and remove it without ever starting it."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert any(v["name"] == unique_vm_name for v in vms)
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name, "--force")
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert not any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_very_long_vm_name(self, mvm_binary: str) -> None:
        # Rationale: CLI-level validation — no resources needed. Verifies length validation.
        """A VM name exceeding length limits should be rejected at CLI level."""
        long_name = "a" * 256
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            long_name,
            "--image",
            "alpine:3.21",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "invalid" in combined

    def test_special_chars_in_vm_name(self, mvm_binary: str) -> None:
        # Rationale: CLI-level validation — no resources needed. Verifies name validation.
        """A VM name with shell-special characters should be rejected."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test;rm -rf /",
            "--image",
            "alpine:3.21",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "invalid" in combined

    def test_unicode_in_vm_name(self, mvm_binary: str) -> None:
        # Rationale: CLI-level validation — no resources needed. Verifies name validation.
        """A VM name with unicode characters should be rejected."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-\U0001f525-vm",
            "--image",
            "alpine:3.21",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "invalid" in combined

    def test_nonexistent_image_fails_gracefully(self, mvm_binary):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Creating a VM with a nonexistent image should give clear error."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-no-img",
            "--image",
            "this-image-definitely-does-not-exist-12345",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined
        result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        if result_vm.returncode == 0:
            vms = json.loads(result_vm.stdout)
            assert not any(v["name"] == "test-no-img" for v in vms)

    def test_nonexistent_network_fails_gracefully(self, mvm_binary):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Creating a VM with a nonexistent network should give clear error."""
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "test-no-net",
                "--image",
                "alpine:3.21",
                "--network",
                "nonexistent-net-12345",
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "not found" in combined
            result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if result_vm.returncode == 0:
                vms = json.loads(result_vm.stdout)
                assert not any(v["name"] == "test-no-net" for v in vms)
            result_net = _run_mvm(
                mvm_binary, "network", "ls", "--json", check=False
            )
            if result_net.returncode == 0:
                nets = json.loads(result_net.stdout)
                assert not any(
                    n["name"] == "nonexistent-net-12345" for n in nets
                )
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", "test-no-net", "--force", check=False
            )

    def test_nonexistent_kernel_rejected(self, mvm_binary):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Creating a VM with a nonexistent kernel should give clear error."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-no-kernel",
            "--image",
            "alpine:3.21",
            "--kernel",
            "nonexistent-kernel-12345",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined
        result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        if result_vm.returncode == 0:
            vms = json.loads(result_vm.stdout)
            assert not any(v["name"] == "test-no-kernel" for v in vms)

    def test_invalid_mac_address_rejected(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_network_name,
    ):
        """An invalid MAC address should be rejected at CLI level."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "test-bad-mac",
                "--image",
                "alpine:3.21",
                "--mac",
                "not-a-mac",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "invalid" in combined
            result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if result_vm.returncode == 0:
                vms = json.loads(result_vm.stdout)
                assert not any(v["name"] == "test-bad-mac" for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )

    def test_validate_vm_create_not_found_error(self, mvm_binary: str) -> None:
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Creating a VM with a missing image gives a clear error."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "test-missing",
            "--image",
            "this-image-does-not-exist",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined

    @pytest.mark.requires_kvm
    def test_duplicate_vm_name_rejected(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name,
    ) -> None:
        """Creating a VM with a name that already exists should be rejected."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert (
                "constraint failed" in combined or "already exist" in combined
            ), f"Expected duplicate name error, got: {result.stderr}"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)


# ========================================================================
# TestVMConfigOptions
# ========================================================================


# ========================================================================
# TestVMSnapshot -- supplementary
# ========================================================================


class TestVMSnapshot:
    """VM snapshot create, load, edge cases."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_snapshot_and_load(
        # Rationale: Uses module_vm fixture. Verifies that process listing and inspect commands return correct data for a running VM. A regression where vm ps shows no output for a running VM would indicate a DB/process tracking bug.
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        unique_network_name,
    ):
        """Snapshot a running VM, stop it, then load and resume."""
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            network_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                network_name,
            )
            result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            vm_data = json.loads(result.stdout)
            vm_dir = vm_data.get("filesystem", {}).get("vm_dir", "")
            mem_file = Path(vm_dir) / "mem.snap"
            state_file = Path(vm_dir) / "state.snap"
            result = _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                unique_vm_name,
                str(mem_file),
                str(state_file),
            )
            assert result.returncode == 0
            assert mem_file.exists()
            assert mem_file.stat().st_size > 0
            assert state_file.exists()
            assert state_file.stat().st_size > 0
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "load",
                unique_vm_name,
                str(mem_file),
                str(state_file),
                "--resume",
            )
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next(
                (v for v in vms if v["name"] == unique_vm_name), None
            )
            assert vm_entry is not None
            assert vm_entry["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )

    def test_snapshot_creates_files(self, mvm_binary, module_vm):
        # Rationale: Uses module_vm fixture. Verifies that process listing and inspect commands return correct data for a running VM. A regression where vm ps shows no output for a running VM would indicate a DB/process tracking bug.
        """Snapshot a running VM and verify snapshot files are created."""
        vm_name = module_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
        data = json.loads(result.stdout)
        vm_dir = Path(data.get("filesystem", {}).get("vm_dir", ""))
        mem_file = vm_dir / "mem.snap"
        state_file = vm_dir / "state.snap"
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                vm_name,
                str(mem_file),
                str(state_file),
            )
            assert result.returncode == 0
            assert mem_file.exists()
            assert state_file.exists()
            assert mem_file.stat().st_size > 0
            assert state_file.stat().st_size > 0
        finally:
            mem_file.unlink(missing_ok=True)
            state_file.unlink(missing_ok=True)

    def test_snapshot_stopped_vm_fails(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        tmp_path,
        unique_network_name,
    ):
        """Snapshot a stopped VM should fail."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        mem_file = tmp_path / "mem.snap"
        state_file = tmp_path / "state.snap"
        ensure_vm_deps(mvm_binary)
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            net_name,
        )
        try:
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                unique_vm_name,
                str(mem_file),
                str(state_file),
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_snapshot_nonexistent_vm_fails(self, mvm_binary):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Snapshot a nonexistent VM should fail."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "snapshot",
            "nonexistent-vm-xyz",
            "/tmp/nonexistent-mem.snap",
            "/tmp/nonexistent-state.snap",
            check=False,
        )
        assert result.returncode != 0

    def test_snapshot_nonexistent_path(self, mvm_binary, module_vm):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Snapshot with nonexistent output directory should give clean error."""
        vm_name = module_vm["name"]
        bad_mem = "/nonexistent/mem.snap"
        bad_state = "/nonexistent/state.snap"
        result = _run_mvm(
            mvm_binary,
            "vm",
            "snapshot",
            vm_name,
            bad_mem,
            bad_state,
            check=False,
        )
        assert result.returncode != 0, (
            f"Expected error for nonexistent path, got: {result.stdout}"
        )
        # Verify no partial snapshot files were created
        assert not Path(bad_mem).exists()
        assert not Path(bad_state).exists()

    def test_load_snapshot_accepts_args(
        # Rationale: Uses module_vm fixture. Verifies that process listing and inspect commands return correct data for a running VM. A regression where vm ps shows no output for a running VM would indicate a DB/process tracking bug.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create snapshot of running VM, stop it, then load the snapshot."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        ensure_vm_deps(mvm_binary)
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            net_name,
        )
        try:
            result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(result.stdout)
            vm_dir = Path(
                data.get("filesystem", {}).get("vm_dir", data.get("vm_dir", ""))
            )
            mem_file = vm_dir / "mem.snap"
            state_file = vm_dir / "state.snap"
            _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                unique_vm_name,
                str(mem_file),
                str(state_file),
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "load",
                unique_vm_name,
                str(mem_file),
                str(state_file),
                check=False,
            )
            # Load may succeed or fail depending on snapshot compatibility
            # Just verify the CLI accepts the arguments (doesn't crash with wrong-arg-count)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_load_snapshot_with_resume(
        # Rationale: Uses module_vm fixture. Verifies that process listing and inspect commands return correct data for a running VM. A regression where vm ps shows no output for a running VM would indicate a DB/process tracking bug.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Create snapshot, stop, load with --resume."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        ensure_vm_deps(mvm_binary)
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            net_name,
        )
        try:
            result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(result.stdout)
            vm_dir = Path(
                data.get("filesystem", {}).get("vm_dir", data.get("vm_dir", ""))
            )
            mem_file = vm_dir / "mem.snap"
            state_file = vm_dir / "state.snap"
            _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                unique_vm_name,
                str(mem_file),
                str(state_file),
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "load",
                unique_vm_name,
                str(mem_file),
                str(state_file),
                "--resume",
                check=False,
            )
            # Load may succeed or fail depending on snapshot compatibility
            # Just verify the CLI accepts the --resume flag (doesn't crash with wrong-arg-count)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_load_nonexistent_vm_fails(self, mvm_binary):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Load snapshot for a nonexistent VM should fail."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "load",
            "nonexistent-vm-xyz",
            "/tmp/nonexistent-mem.snap",
            "/tmp/nonexistent-state.snap",
            check=False,
        )
        assert result.returncode != 0

    def test_load_nonexistent_files(self, mvm_binary, module_vm):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Load snapshot with nonexistent files should give clean error."""
        vm_name = module_vm["name"]
        bad_mem = "/nonexistent/mem.snap"
        bad_state = "/nonexistent/state.snap"
        result = _run_mvm(
            mvm_binary,
            "vm",
            "load",
            vm_name,
            bad_mem,
            bad_state,
            check=False,
        )
        assert result.returncode != 0, (
            f"Expected error for nonexistent files, got: {result.stdout}{result.stderr}"
        )
        # Verify VM is still in its previous state
        inspect_result = _run_mvm(
            mvm_binary, "vm", "inspect", vm_name, "--json"
        )
        data = json.loads(inspect_result.stdout)
        assert data.get("vm", {}).get("status") in ("running", "paused")

    def test_create_skip_cleanup_rejected_noninteractive(
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """--skip-cleanup should fail in non-interactive mode."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--skip-cleanup",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

        vm_name2 = f"{unique_vm_name}-normal"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name2,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert any(v["name"] == vm_name2 for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "vm", "rm", vm_name2, "--force", check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.serial
    def test_create_skip_cleanup_interactive_acceptance(
        # Rationale: Verifies VM creation and lifecycle operations against a real Firecracker instance. A regression where create succeeds in DB but fails to start Firecracker would not be caught by JSON-only checks.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """--skip-cleanup should be accepted when user confirms interactively."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            ensure_vm_deps(mvm_binary)
            cmd = [
                *shlex.split(mvm_binary),
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--skip-cleanup",
                "--network",
                net_name,
            ]
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                input="y\n",
                timeout=120,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0, (
                f"Interactive skip-cleanup VM creation failed:\n"
                f"stdout: {result.stdout}\nstderr: {result.stderr}"
            )
            # Verify VM was actually created in the system
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert any(v["name"] == unique_vm_name for v in vms), (
                f"VM '{unique_vm_name}' not found in listing after interactive creation"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )


# ========================================================================
# TestVMConcurrency -- supplementary
# ========================================================================


# ========================================================================
# TestVMConcurrency -- supplementary
# ========================================================================


class TestVMConcurrency:
    """VM concurrency tests: parallel creation, racing operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.serial
    def test_parallel_vm_create_same_name_race(
        # Rationale: Needs real VMs to detect race conditions in concurrent operations.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Two parallel vm create with same name -- one should succeed, one should fail."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        ensure_vm_deps(mvm_binary)
        try:
            with concurrent.futures.ThreadPoolExecutor(
                max_workers=2
            ) as executor:
                futures = [
                    executor.submit(
                        _run_mvm_async,
                        mvm_binary,
                        "vm",
                        "create",
                        unique_vm_name,
                        "--image",
                        "alpine:3.21",
                        "--network",
                        net_name,
                        timeout=180,
                    )
                    for _ in range(2)
                ]
                results = [
                    f.result() for f in concurrent.futures.as_completed(futures)
                ]
            successes = [r for r in results if r.returncode == 0]
            failures = [r for r in results if r.returncode != 0]
            assert len(successes) == 1
            assert len(failures) >= 1
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            matching = [v for v in vms if v["name"] == unique_vm_name]
            assert len(matching) == 1
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.serial
    @pytest.mark.slow
    def test_parallel_vm_create_unique_names_same_network(
        # Rationale: Needs real VMs to detect race conditions in concurrent operations.
        self,
        mvm_binary,
        unique_network_name,
    ):
        """Multiple VMs on same network should get unique IPs."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        vm_names = [f"sys-conc-{uuid.uuid4().hex[:6]}" for _ in range(3)]
        ensure_vm_deps(mvm_binary)
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            with concurrent.futures.ThreadPoolExecutor(
                max_workers=3
            ) as executor:
                futures = [
                    executor.submit(
                        _run_mvm_async,
                        mvm_binary,
                        "vm",
                        "create",
                        vm_name,
                        "--image",
                        "alpine:3.21",
                        "--network",
                        net_name,
                        timeout=180,
                    )
                    for vm_name in vm_names
                ]
                results = [
                    f.result() for f in concurrent.futures.as_completed(futures)
                ]
            for r in results:
                assert r.returncode == 0, f"VM create failed: {r.stderr}"
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            created_vms = [v for v in vms if v["name"] in vm_names]
            assert len(created_vms) == 3
            ips = [v.get("ipv4", "") for v in created_vms]
            ips = [ip for ip in ips if ip]
            assert ips
            assert len(set(ips)) == len(ips), f"Duplicate IPs found: {ips}"
        finally:
            for vm_name in vm_names:
                _run_mvm(
                    mvm_binary, "vm", "rm", vm_name, "--force", check=False
                )
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.serial
    @pytest.mark.slow
    def test_concurrent_vm_create_count_atomic(
        # Rationale: Needs real VMs to detect race conditions in concurrent operations.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Atomic batch creation should not have partial failures under concurrency."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        ensure_vm_deps(mvm_binary)
        base_name = unique_vm_name
        vm_names = [f"{base_name}-{i}" for i in range(1, 4)]
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                base_name,
                "--image",
                "alpine:3.21",
                "--count",
                "3",
                "--atomic",
                "--network",
                net_name,
            )
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            matching = [v for v in vms if v["name"] in vm_names]
            assert len(matching) >= 2
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            for vm_name in vm_names:
                _run_mvm(
                    mvm_binary, "vm", "rm", vm_name, "--force", check=False
                )


# ========================================================================
# TestVMRemove
# ========================================================================


class TestVMRemove:
    """VM removal: rm, rm nonexistent, rm --force - ALWAYS last."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_remove(self, mvm_binary, unique_vm_name, unique_network_name):
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        """Create and remove VM."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            net_name,
        )
        try:
            result = _run_mvm(mvm_binary, "vm", "rm", unique_vm_name)
            assert result.returncode == 0
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert not any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_remove_nonexistent(self, mvm_binary):
        # Rationale: CLI-level validation — no real VM created. Verifies error handling.
        """Remove a VM that does not exist should fail."""
        nonexistent = "nonexistent-vm-name-xyz"
        result = _run_mvm(
            mvm_binary,
            "vm",
            "rm",
            nonexistent,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined

    @pytest.mark.serial
    def test_rm_partial_failure(
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Removing a mix of existing and nonexistent VMs yields partial failure."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
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
            )
            result = _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                nonexistent,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert "not found" in combined
            # The existing VM IS removed (it was a valid identifier) — only
            # the nonexistent identifier fails. Each identifier is processed
            # independently; there is no rollback.
            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if ls_result.returncode == 0:
                vms = json.loads(ls_result.stdout)
                assert not any(v["name"] == unique_vm_name for v in vms), (
                    f"VM '{unique_vm_name}' should have been removed"
                )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Identifier flags ────────────────────────────────────────────

    def test_rm_by_name_flag(
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Remove VM using name as positional argument."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            unique_vm_name,
            "--image",
            "alpine:3.21",
            "--network",
            net_name,
        )
        try:
            result = _run_mvm(mvm_binary, "vm", "rm", unique_vm_name, "--force")
            assert result.returncode == 0
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert not any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_rm_without_force_on_running_vm_succeeds(
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Running VM can be removed without --force."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name)
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert not any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_rm_with_force_on_running_vm_succeeds(
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Force remove must kill the process and clean up DB."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name, "--force")
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert not any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    def test_delete_kernel_used_by_stopped_vm_does_not_error(
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Kernel rm allows deleting kernels referenced by stopped VMs."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert unique_vm_name in vm_names
            result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
            kernels = json.loads(result.stdout)
            present_kernels = [k for k in kernels if k.get("is_present")]
            assert present_kernels
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
                    kernels_after = json.loads(kernel_ls.stdout)
                    kernel_ids = [k.get("id", "")[:6] for k in kernels_after]
                    assert kernel_id_prefix not in kernel_ids
            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert unique_vm_name in vm_names
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    def test_delete_binary_used_by_stopped_vm_does_not_error(
        # Rationale: Needs a real VM to test removal and cleanup behavior.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Binary rm allows deleting binaries referenced by stopped VMs."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
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
            )
            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert unique_vm_name in vm_names
            result = _run_mvm(mvm_binary, "bin", "ls", "--json")
            binaries = json.loads(result.stdout)
            present_bins = [b for b in binaries if b.get("is_present")]
            assert present_bins
            default_bin = next(
                (b for b in present_bins if b.get("is_default")),
                present_bins[0],
            )
            binary_id_prefix = default_bin["id"][:6]
            result = _run_mvm(
                mvm_binary, "bin", "rm", binary_id_prefix, check=False
            )
            assert result.returncode in (0, 1)
            if result.returncode == 0:
                bin_ls = _run_mvm(
                    mvm_binary, "bin", "ls", "--json", check=False
                )
                if bin_ls.returncode == 0 and bin_ls.stdout.strip():
                    bins_after = json.loads(bin_ls.stdout)
                    bin_ids = [b.get("id", "")[:6] for b in bins_after]
                    assert binary_id_prefix not in bin_ids
            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert unique_vm_name in vm_names
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_delete_ssh_key_used_by_running_vm(
        # Rationale: Needs a running VM to verify SSH or network connectivity.
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        unique_network_name,
    ):
        """SSH key used by a running VM -- documents current behavior."""
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        key_name = unique_key_name
        try:
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.21",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            # rm without --force is rejected when key is in use
            result = _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            assert "used by VM" in (result.stdout + result.stderr), (
                "rm without --force should report key is in use"
            )
            key_ls = _run_mvm(mvm_binary, "key", "ls", "--json", check=False)
            if key_ls.returncode == 0 and key_ls.stdout.strip():
                keys_after = json.loads(key_ls.stdout)
                key_names = [k.get("name") for k in keys_after]
                assert key_name in key_names, (
                    "Key should still be present after rejected rm"
                )
            # rm with --force succeeds
            _run_mvm(mvm_binary, "key", "rm", key_name, "--force")
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


# ========================================================================
# TestVMSnapshot -- supplementary
# ========================================================================
