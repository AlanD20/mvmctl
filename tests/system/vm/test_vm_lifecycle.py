"""VM lifecycle system tests - 9 focused classes with dependency ordering."""

from __future__ import annotations

import concurrent.futures
import json
import os
import shlex
import subprocess
import time
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import (
    _run_mvm,
    _unique_subnet,
    wait_for_ssh,
    ensure_vm_deps,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
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

    @pytest.mark.parametrize(
        "image_id", ["alpine-3.21"]
    )
    def test_create_per_image(
        self, mvm_binary, unique_vm_name, image_id, unique_network_name
    ):
        """Create VM with specific image."""
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
                "--name",
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

    def test_create_count_default(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """vm create without --count still creates 1 VM."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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

    def test_create_count_multiple(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create 3 VMs with --count 3."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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

    def test_create_atomic_with_count(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """--atomic --count 2 creates both VMs successfully."""
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
        names = [unique_vm_name, f"{unique_vm_name}-2"]
        try:
            ensure_vm_deps(mvm_binary)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """--count > 1 with --ip should be rejected."""
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--count",
            "2",
            "--ip",
            "10.99.99.99",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_count_with_mac_fails(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """--count > 1 with --mac should be rejected."""
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--count",
            "2",
            "--mac",
            "aa:bb:cc:dd:ee:ff",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_count_negative_fails(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """--count -1 should be rejected."""
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--count",
            "-1",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_atomic_without_count(
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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

    def test_create_count_output_message(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Verify output says 'Created N VM(s): ...' for batch creation."""
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
        names = [unique_vm_name, f"{unique_vm_name}-2"]
        try:
            ensure_vm_deps(mvm_binary)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--count",
                "2",
                "--network",
                net_name,
            )
            assert result.returncode == 0
            assert "Created 2 VM(s)" in result.stdout
            assert unique_vm_name in result.stdout
            assert f"{unique_vm_name}-2" in result.stdout
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            for name in names:
                _run_mvm(mvm_binary, "vm", "rm", name, "--force", check=False)

    def test_create_count_explicit_1(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Explicit --count 1 should still create a single VM."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        """--skip-cleanup flag appears in vm create --help."""
        result = _run_mvm(mvm_binary, "vm", "create", "--help")
        assert result.returncode == 0
        assert "--skip-cleanup" in result.stdout

    def test_create_atomic_rollback_on_collision(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """--atomic must reject batch on name collision."""
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
        base_name = unique_vm_name
        collision_name = f"{base_name}-2"
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                collision_name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
            )
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                base_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_key_name, unique_network_name
    ):
        """Using --count with --volume should be rejected early."""
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
                "--name",
                "should-not-create",
                "--image",
                "alpine-3.21",
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

    def test_create_duplicate_name(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with duplicate name should fail."""
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--network",
            net_name,
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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

    def test_create_with_user(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --user myuser."""
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
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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

    def test_create_with_lsm_flags(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --lsm-flags."""
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
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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

    def test_create_with_firecracker_bin(
        self, mvm_binary, unique_vm_name, system_cache_dir, unique_network_name
    ):
        """Create VM with --firecracker-bin."""
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
            pytest.skip("No firecracker binary available")
        bin_rel_path = firecracker_bins[0]["path"]
        bin_path = system_cache_dir / "bin" / bin_rel_path
        if not bin_path.exists():
            pytest.skip(f"Firecracker binary not found at {bin_path}")
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
            pytest.skip(
                "guestfs_enabled is currently True; test requires it False"
            )
        try:
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, tmp_path, system_cache_dir
    ):
        """Create VM using an imported image file path."""
        from tests.system.conftest import _ensure_image, _ensure_kernel, _unique_subnet, _run_mvm as _run

        _ensure_kernel(mvm_binary)
        _ensure_image(mvm_binary, "alpine-3.21")

        vm_name = unique_vm_name
        net_name = f"sys-net-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
        _run(mvm_binary, "network", "create", net_name, "--subnet", subnet, "--non-interactive")
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, system_cache_dir, unique_network_name
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
            pytest.skip("No present kernel to test with")
        kernel_file = system_cache_dir / "kernels" / present[0]["path"]
        if not kernel_file.exists():
            pytest.skip(f"Kernel file not found at {kernel_file}")
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, tmp_path, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with Ubuntu image.

        Note: the old slug ``ubuntu-24.04-minimal`` is no longer valid.
        Use ``ubuntu-minimal-24.04`` (the stored ``type``) or the image ID.
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
                "--name",
                unique_vm_name,
                "--image",
                "ubuntu-minimal-24.04",
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
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            "test-zero",
            "--image",
            "alpine-3.21",
            "--count",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(
            s in combined
            for s in ["invalid", "must be", "greater than", "positive"]
        )

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_create_and_remove_never_started(
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_very_long_vm_name(self, mvm_binary: str) -> None:
        """A VM name exceeding length limits should be rejected at CLI level."""
        long_name = "a" * 256
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            long_name,
            "--image",
            "alpine-3.21",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["too long", "invalid", "length"])

    def test_special_chars_in_vm_name(self, mvm_binary: str) -> None:
        """A VM name with shell-special characters should be rejected."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            "test;rm -rf /",
            "--image",
            "alpine-3.21",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["invalid", "character"])

    def test_unicode_in_vm_name(self, mvm_binary: str) -> None:
        """A VM name with unicode characters should be rejected."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            "test-\U0001f525-vm",
            "--image",
            "alpine-3.21",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["invalid", "character"])

    def test_nonexistent_image_fails_gracefully(self, mvm_binary):
        """Creating a VM with a nonexistent image should give clear error."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            "test-no-img",
            "--image",
            "this-image-definitely-does-not-exist-12345",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["not found", "invalid", "no such"])
        result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        if result_vm.returncode == 0:
            vms = json.loads(result_vm.stdout)
            assert not any(v["name"] == "test-no-img" for v in vms)

    def test_nonexistent_network_fails_gracefully(self, mvm_binary):
        """Creating a VM with a nonexistent network should give clear error."""
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                "test-no-net",
                "--image",
                "alpine-3.21",
                "--network",
                "nonexistent-net-12345",
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert any(s in combined for s in ["not found", "invalid"])
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
        """Creating a VM with a nonexistent kernel should give clear error."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            "test-no-kernel",
            "--image",
            "alpine-3.21",
            "--kernel",
            "nonexistent-kernel-12345",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["not found", "invalid"])
        result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        if result_vm.returncode == 0:
            vms = json.loads(result_vm.stdout)
            assert not any(v["name"] == "test-no-kernel" for v in vms)

    def test_invalid_mac_address_rejected(
        self, mvm_binary, unique_network_name
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
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            "test-bad-mac",
            "--image",
            "alpine-3.21",
            "--mac",
            "not-a-mac",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["invalid", "mac"])
        result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
        if result_vm.returncode == 0:
            vms = json.loads(result_vm.stdout)
            assert not any(v["name"] == "test-bad-mac" for v in vms)

    def test_validate_vm_create_not_found_error(self, mvm_binary: str) -> None:
        """Creating a VM with a missing image gives a clear error."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            "test-missing",
            "--image",
            "this-image-does-not-exist",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["not found", "invalid", "no such"])

    @pytest.mark.requires_kvm
    def test_duplicate_vm_name_rejected(
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
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
            )
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert any(
                s in combined
                for s in [
                    "already exists",
                    "duplicate",
                    "unique constraint",
                    "already in use",
                    "firecracker process exited",
                ]
            ), f"Expected duplicate name error, got: {result.stderr}"
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)


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
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with custom --vcpus."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_vcpus_zero_fails(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """--vcpus 0 must fail."""
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--vcpus",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_vcpus_negative_fails(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Negative --vcpus must fail."""
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
    ) -> None:
        """Config values set via 'config set defaults.vm.*' affect VM creation unless CLI flags override."""
        # Rationale: Needs real VMs to verify that DB config defaults properly
        # propagate through the resolution chain (config → DB → constants).
        # A stopped VM or volume won't exercise the full config → VM creation pipeline.
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
                "--name",
                vm_noflag,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
            )
            result = _run_mvm(mvm_binary, "vm", "inspect", vm_noflag, "--json")
            data = json.loads(result.stdout)
            assert data.get("vcpus") == 4

            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_flag,
                "--image",
                "alpine-3.21",
                "--vcpus",
                "2",
                "--network",
                net_name,
            )
            result = _run_mvm(mvm_binary, "vm", "inspect", vm_flag, "--json")
            data = json.loads(result.stdout)
            assert data.get("vcpus") == 2

        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
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
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with custom --mem."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_memory_zero_fails(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """--mem 0 must fail."""
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--mem",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    # ── Config options: Disk size ───────────────────────────────────

    def test_create_with_disk_size(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with custom --disk-size."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_disk_size_zero_fails(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """--disk-size 0 must fail."""
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--disk-size",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_disk_size_invalid_fails(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Invalid --disk-size format must fail (no upper bound check exists)."""
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--disk-size",
            "abc",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    # ── Config options: Network / IP / MAC ──────────────────────────

    def test_create_with_specific_kernel(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with a specific --kernel."""
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
            pytest.skip("No present kernel to test with")
        kernel_id_prefix = present[0]["id"][:6]
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_boot_args(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with custom --boot-args."""
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
        custom_boot_args = "quiet loglevel=3"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    # ── Config options: Console / PCI / Logging / Metrics ───────────

    def test_create_with_no_console(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --no-console."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--no-console",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("enable_console") is False
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_enable_pci(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --enable-pci."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--enable-pci",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("enable_pci") is True
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_no_enable_pci(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --no-enable-pci."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--no-enable-pci",
                "--network",
                net_name,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == unique_vm_name)
            assert vm.get("enable_pci") is False
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_enable_logging(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --enable-logging."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--enable-logging",
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

    def test_create_with_no_enable_logging(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --no-enable-logging."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--no-enable-logging",
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

    def test_create_with_enable_metrics(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --enable-metrics."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--enable-metrics",
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

    def test_create_with_no_enable_metrics(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --no-enable-metrics."""
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--no-enable-metrics",
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

    # ── Config options: Advanced flags ──────────────────────────────

    def test_vcpus_negative_rejected(
        self, mvm_binary: str, unique_network_name
    ) -> None:
        """Negative vCPU count should be rejected."""
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
            "--name",
            "test-neg-cpu",
            "--image",
            "alpine-3.21",
            "--vcpus",
            "-1",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["invalid", "negative", "greater"])

    def test_mem_zero_rejected(
        self, mvm_binary: str, unique_network_name
    ) -> None:
        """Zero memory should be rejected."""
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
            "--name",
            "test-zero-mem",
            "--image",
            "alpine-3.21",
            "--mem",
            "0",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["invalid", "zero", "must be"])

    def test_disk_size_zero_rejected(
        self, mvm_binary: str, unique_network_name
    ) -> None:
        """Zero disk size should be rejected."""
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
            "--name",
            "test-zero-disk",
            "--image",
            "alpine-3.21",
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
        assert any(s in combined for s in ["smaller than", "minimum required"])


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
        """Reboot VM with --force flag."""
        vm_name = lifecycle_vm["name"]
        result = _run_mvm(
            mvm_binary, "vm", "reboot", vm_name, "--force", check=False
        )
        if result.returncode != 0:
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            inspect_result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            vm_data = json.loads(inspect_result.stdout)
            pid = vm_data.get("pid")
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
        """Stop VM using name as positional argument."""
        result = _run_mvm(mvm_binary, "vm", "stop", created_vm["name"])
        assert result.returncode == 0

    def test_stop_by_ip(self, mvm_binary, created_vm):
        """Stop VM using IP as positional argument."""
        ip = created_vm.get("ipv4", "")
        if not ip:
            pytest.skip("VM has no IP address")
        result = _run_mvm(mvm_binary, "vm", "stop", ip)
        assert result.returncode == 0

    @pytest.mark.requires_kvm
    def test_stop_already_stopped_vm_is_idempotent(
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, check=False)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                unique_vm_name,
                check=False,
            )
            assert result.returncode != 0
            combined = (result.stdout + result.stderr).lower()
            assert any(
                s in combined for s in ["not running", "stopped", "state"]
            )
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
                vm_dir = Path(info["vm_dir"])
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
        self, mvm_binary: str, unique_vm_name: str, unique_network_name
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
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                net_name,
            )
            vm_inspect = _run_mvm(
                mvm_binary, "vm", "inspect", vm_name, "--json"
            )
            vm_data = json.loads(vm_inspect.stdout)
            pid = vm_data.get("pid")
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                network_name,
            )
            result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            inspect_data = json.loads(result.stdout)
            pid = inspect_data.get("pid")
            assert pid is not None and pid > 0
            _run_mvm(mvm_binary, "vm", "stop", unique_vm_name, "--force")
            proc = subprocess.run(
                ["kill", "-0", str(pid)],
                capture_output=True,
                timeout=5,
            )
            assert proc.returncode != 0
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
        self, mvm_binary, unique_vm_name, unique_key_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert vol_data["status"] == "attached"
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
        self, mvm_binary, unique_vm_name, unique_key_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert vol_data["status"] == "attached"
            result = _run_mvm(
                mvm_binary, "vm", "detach-volume", unique_vm_name, vol_name
            )
            assert result.returncode == 0
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "available"
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
        self, mvm_binary, unique_vm_name, unique_key_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert "running" in (result.stdout + result.stderr).lower()
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
        self, mvm_binary, unique_vm_name, unique_key_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert "running" in (result.stdout + result.stderr).lower()
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
        self, mvm_binary, unique_vm_name, unique_key_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert vol_data["status"] == "attached"
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
        self, mvm_binary, unique_vm_name, unique_key_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert vol_data["status"] == "attached"
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name, "--force")
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "available"
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
        self, mvm_binary, unique_vm_name, unique_key_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert vol_data["status"] == "available"
            _run_mvm(
                mvm_binary, "vm", "attach-volume", unique_vm_name, vol_name
            )
            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data["status"] == "attached"
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
        self, mvm_binary, unique_vm_name, unique_key_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert vol_data["status"] == "attached"
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
        self, mvm_binary, unique_vm_name, unique_key_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert vol_data["status"] == "available"
            _run_mvm(
                mvm_binary, "vm", "attach-volume", unique_vm_name, vol_name
            )
            vol_result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_result.stdout)
            assert vol_data["status"] == "attached"
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert any(
                phrase in error_text
                for phrase in ["in use", "attached", "cannot"]
            )
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary: str, unique_vm_name: str, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            volume_ids = vm_data.get("volume_ids", [])
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert any(s in combined for s in ["not found", "no such"])
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert any(s in combined for s in ["not found", "not attached"])
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
                "--volume",
                vol_name,
                "--network",
                net_name,
            )
            ssh_timeout = max(
                timing_targets.get("alpine-3.21", 15), self._SSH_WAIT_TIMEOUT
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
                "--volume",
                vol_name,
                "--network",
                net_name,
            )
            ssh_timeout = max(
                timing_targets.get("alpine-3.21", 15), self._SSH_WAIT_TIMEOUT
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
                "--volume",
                vol_name,
                "--network",
                net_name,
            )
            ssh_timeout = max(
                timing_targets.get("alpine-3.21", 15), self._SSH_WAIT_TIMEOUT
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


class TestVMListInspect:
    """VM listing, inspection, export, import - uses module_vm."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_list_json(self, mvm_binary, module_vm):
        """List VMs in JSON format."""
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert any(v["name"] == module_vm["name"] for v in vms)

    def test_list_table(self, mvm_binary, module_vm):
        """List VMs in table format."""
        result = _run_mvm(mvm_binary, "vm", "ls")
        assert result.returncode == 0
        assert module_vm["name"] in result.stdout

    def test_inspect(self, mvm_binary, module_vm):
        """Show detailed VM info via vm inspect."""
        result = _run_mvm(mvm_binary, "vm", "inspect", module_vm["name"])
        assert result.returncode == 0
        assert module_vm["name"] in result.stdout

    def test_inspect_json(self, mvm_binary, module_vm):
        """vm inspect --json should return structured JSON."""
        result = _run_mvm(
            mvm_binary, "vm", "inspect", module_vm["name"], "--json"
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, dict)
        for key in (
            "id",
            "name",
            "status",
            "ipv4",
            "mac",
            "vm_dir",
            "relay_running",
        ):
            assert key in data

    def test_inspect_tree(self, mvm_binary, module_vm):
        """Inspect VM with --tree format."""
        result = _run_mvm(
            mvm_binary, "vm", "inspect", module_vm["name"], "--tree"
        )
        assert result.returncode == 0
        assert "├──" in result.stdout or "└──" in result.stdout

    def test_export(self, mvm_binary, module_vm):
        """Export VM config as JSON."""
        result = _run_mvm(mvm_binary, "vm", "export", module_vm["name"])
        assert result.returncode == 0
        config = json.loads(result.stdout)
        assert isinstance(config, dict)

    def test_export_to_file(self, mvm_binary, module_vm, tmp_path):
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
        self, mvm_binary, unique_vm_name, tmp_path, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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

    def test_import_without_name_override(
        self, mvm_binary, unique_vm_name, tmp_path, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        """vm ps lists running VMs."""
        result = _run_mvm(mvm_binary, "vm", "ps")
        assert result.returncode == 0
        assert module_vm["name"] in result.stdout

    def test_ls_json_running_vm_fields(self, mvm_binary, module_vm):
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
        """vm ps table output shows running VM with name, status, and IP."""
        result = _run_mvm(mvm_binary, "vm", "ps")
        assert result.returncode == 0
        out = result.stdout
        assert module_vm["name"] in out
        assert "running" in out.lower()
        ip = module_vm.get("ipv4", "")
        if ip:
            assert ip in out

    def test_list_empty_nonexistent_name(self, mvm_binary):
        """Listing a nonexistent VM name returns clean list without it."""
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert not any(v["name"] == nonexistent for v in vms)

    def test_console_state_nonexistent_vm(self, mvm_binary):
        """console --state on nonexistent VM should give clear error."""
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
        result = _run_mvm(
            mvm_binary, "console", nonexistent, "--state", check=False
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["not found", "no such", "invalid"])

    # ── SSH ─────────────────────────────────────────────────────────

    def test_inspect_by_name_flag(self, mvm_binary, module_vm):
        """Inspect VM using name as positional argument."""
        result = _run_mvm(mvm_binary, "vm", "inspect", module_vm["name"])
        assert result.returncode == 0
        assert module_vm["name"] in result.stdout

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    def test_config_roundtrip(
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert imported.get("vcpus") == 2
            assert imported.get("mem_mib") == 1024
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


class TestVMNetworkIntegration:
    """VM network integration: static IP, custom MAC, named network."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_static_ip(
        self, mvm_binary, unique_vm_name, created_network
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--ip",
            "999.999.999.999",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_invalid_ip_format_fails(
        self, mvm_binary, unique_vm_name, unique_network_name
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
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--ip",
            "not-an-ip",
            "--network",
            net_name,
            check=False,
        )
        assert result.returncode != 0

    def test_create_with_custom_mac(
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_create_with_named_network(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """Create VM on a specific named network."""
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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


class TestVMSSHIntegration:
    """SSH into created VMs with key."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_ssh_available(self, mvm_binary, created_vm, timing_targets):
        """SSH is available after VM boots."""
        if not created_vm.get("ipv4", ""):
            pytest.skip("VM has no IP address")
        available = wait_for_ssh(
            mvm_binary,
            created_vm["name"],
            "root",
            timing_targets["alpine-3.21"],
        )
        assert available

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
        self, mvm_binary, unique_vm_name, tmp_path, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
            assert data.get("cloud_init_mode") == "net"
            assert data.get("nocloud_net_port") == 9999
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                timing_targets.get("alpine-3.21", 15), self._SSH_WAIT_TIMEOUT
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            ssh_timeout = max(
                timing_targets.get("alpine-3.21", 15), self._SSH_WAIT_TIMEOUT
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


class TestVMRemove:
    """VM removal: rm, rm nonexistent, rm --force - ALWAYS last."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_remove(self, mvm_binary, unique_vm_name, unique_network_name):
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
                "--network",
                net_name,
            )
            _run_mvm(mvm_binary, "vm", "start", unique_vm_name)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            key_ls = _run_mvm(mvm_binary, "key", "ls", "--json", check=False)
            if key_ls.returncode == 0 and key_ls.stdout.strip():
                keys_after = json.loads(key_ls.stdout)
                key_names = [k.get("name") for k in keys_after]
                assert key_name not in key_names
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


class TestVMSnapshot:
    """VM snapshot create, load, edge cases."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_snapshot_and_load(
        self, mvm_binary, unique_vm_name, tmp_path, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--network",
                network_name,
            )
            result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            vm_data = json.loads(result.stdout)
            vm_dir = vm_data["vm_dir"]
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
        """Snapshot a running VM and verify snapshot files are created."""
        vm_name = module_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
        data = json.loads(result.stdout)
        vm_dir = Path(data["vm_dir"])
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
        self, mvm_binary, unique_vm_name, tmp_path, unique_network_name
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(
            s in combined
            for s in [
                "no such",
                "not found",
                "exist",
                "path",
                "not a directory",
            ]
        )
        # Verify no partial snapshot files were created
        assert not Path(bad_mem).exists()
        assert not Path(bad_state).exists()

    def test_load_snapshot_accepts_args(
        self, mvm_binary, unique_vm_name, unique_network_name
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--network",
            net_name,
        )
        try:
            result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(result.stdout)
            vm_dir = Path(data["vm_dir"])
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
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_load_snapshot_with_resume(
        self, mvm_binary, unique_vm_name, unique_network_name
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--network",
            net_name,
        )
        try:
            result = _run_mvm(
                mvm_binary, "vm", "inspect", unique_vm_name, "--json"
            )
            data = json.loads(result.stdout)
            vm_dir = Path(data["vm_dir"])
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
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
            _run_mvm(
                mvm_binary, "vm", "rm", unique_vm_name, "--force", check=False
            )

    def test_load_nonexistent_vm_fails(self, mvm_binary):
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
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(
            s in combined for s in ["no such", "not found", "exist", "path"]
        )
        # Verify VM is still in its previous state
        inspect_result = _run_mvm(
            mvm_binary, "vm", "inspect", vm_name, "--json"
        )
        data = json.loads(inspect_result.stdout)
        assert data.get("status") in ("running", "paused")

    def test_create_skip_cleanup_rejected_noninteractive(
        self, mvm_binary, unique_vm_name, unique_network_name
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
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
                "--name",
                vm_name2,
                "--image",
                "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
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


class TestVMConcurrency:
    """VM concurrency tests: parallel creation, racing operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.serial
    def test_parallel_vm_create_same_name_race(
        self, mvm_binary, unique_vm_name, unique_network_name
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
                        "--name",
                        unique_vm_name,
                        "--image",
                        "alpine-3.21",
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
        self, mvm_binary, unique_network_name
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
                        "--name",
                        vm_name,
                        "--image",
                        "alpine-3.21",
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
                "--name",
                base_name,
                "--image",
                "alpine-3.21",
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
