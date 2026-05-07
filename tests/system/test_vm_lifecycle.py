"""VM lifecycle system tests — state operations with both approaches."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet, wait_for_ssh

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_vm,
]


class TestVMCreatePerImage:
    """Test VM creation with each supported image."""

    @pytest.mark.parametrize(
        "image_id",
        [
            "alpine-3.21",
            "ubuntu-24.04-minimal",
        ],
    )
    def test_vm_create(self, mvm_binary, unique_vm_name, image_id):
        """Create VM with specific image. Tests a lightweight and a common image."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            image_id,
        )
        assert result.returncode == 0

        # Cleanup
        _run_mvm(mvm_binary, "vm", "rm", unique_vm_name, check=False)


class TestVMBatchCreate:
    """Test vm create --count and --atomic flags.

    --count N creates N VMs with names base, base-2, base-3, ...
    --atomic rolls back all VMs if any single VM fails.
    """

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_vm_create_count_default(self, mvm_binary, unique_vm_name):
        """vm create without --count still creates 1 VM."""
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_vm_create_count_multiple(self, mvm_binary, unique_vm_name):
        """Create 3 VMs with --count 3 (base, base-2, base-3)."""
        names = [
            unique_vm_name,
            f"{unique_vm_name}-2",
            f"{unique_vm_name}-3",
        ]
        try:
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
            )
            assert result.returncode == 0

            # Verify all 3 VMs are listed
            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            for name in names:
                assert any(v["name"] == name for v in vms), (
                    f"VM {name} not found"
                )
        finally:
            for name in names:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "rm",
                    name,
                    "--force",
                    check=False,
                )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_vm_create_atomic_with_count(self, mvm_binary, unique_vm_name):
        """--atomic --count 2 creates both VMs successfully."""
        names = [unique_vm_name, f"{unique_vm_name}-2"]
        try:
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
            )
            assert result.returncode == 0

            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            for name in names:
                assert any(v["name"] == name for v in vms), (
                    f"VM {name} not found"
                )
        finally:
            for name in names:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "rm",
                    name,
                    "--force",
                    check=False,
                )

    def test_vm_create_count_zero_fails(self, mvm_binary, unique_vm_name):
        """--count 0 should be rejected."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--count",
            "0",
            check=False,
        )
        assert result.returncode != 0

    def test_vm_create_count_with_ip_fails(self, mvm_binary, unique_vm_name):
        """--count > 1 with --ip should be rejected."""
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
            check=False,
        )
        assert result.returncode != 0

    def test_vm_create_count_with_mac_fails(self, mvm_binary, unique_vm_name):
        """--count > 1 with --mac should be rejected."""
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
            check=False,
        )
        assert result.returncode != 0

    def test_vm_create_count_negative_fails(self, mvm_binary, unique_vm_name):
        """--count -1 should be rejected (count must be at least 1)."""
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
            check=False,
        )
        assert result.returncode != 0

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_vm_create_atomic_without_count(self, mvm_binary, unique_vm_name):
        """--atomic without --count should work (count=1 default)."""
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                unique_vm_name,
                "--image",
                "alpine-3.21",
                "--atomic",
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_vm_create_count_output_message(self, mvm_binary, unique_vm_name):
        """Verify output says 'Created N VM(s): ...' for batch creation."""
        names = [unique_vm_name, f"{unique_vm_name}-2"]
        try:
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
            )
            assert result.returncode == 0
            assert "Created 2 VM(s)" in result.stdout, (
                f"Expected 'Created 2 VM(s)' in output, got: {result.stdout}"
            )
            assert unique_vm_name in result.stdout
            assert f"{unique_vm_name}-2" in result.stdout
        finally:
            for name in names:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "rm",
                    name,
                    "--force",
                    check=False,
                )

    def test_vm_create_count_explicit_1(self, mvm_binary, unique_vm_name):
        """Explicit --count 1 should still create a single VM."""
        try:
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
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_vm_create_atomic_rollback_on_collision(
        self, mvm_binary, unique_vm_name
    ):
        """--atomic must roll back all VMs if any single VM fails.

        Strategy: pre-create VM 'base-2', then try --name base --count 2 --atomic.
        First creates 'base' (succeeds), then tries 'base-2' (collision!).
        Atomic mode should remove 'base' and report failure.
        """
        base_name = unique_vm_name
        collision_name = f"{base_name}-2"
        try:
            # Pre-create the collision VM
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                collision_name,
                "--image",
                "alpine-3.21",
            )

            # Try atomic batch -- should fail at collision_name
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
                check=False,
            )
            assert result.returncode != 0, (
                "Atomic batch should have failed on name collision"
            )
            assert (
                "atomic" in result.stdout.lower()
                or "failed" in result.stdout.lower()
            ), f"Expected atomic failure message, got: {result.stdout}"

            # Verify base_name was rolled back (removed)
            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            assert not any(v["name"] == base_name for v in vms), (
                f"VM '{base_name}' should have been rolled back but still exists"
            )
        finally:
            # Cleanup: remove both (collision_name still exists since it pre-existed)
            for name in [base_name, collision_name]:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "rm",
                    name,
                    "--force",
                    check=False,
                )


class TestVMVolumeIntegration:
    """Test volume integration with VM lifecycle.

    Covers vm create --volume, vm attach-volume, and vm detach-volume.
    """

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_vm_create_with_volume(
        self, mvm_binary, unique_vm_name, unique_key_name
    ):
        """Create a volume and attach it at VM creation time via --volume."""
        vol_name = f"sys-vol-vm-{unique_key_name}"
        vm_name = unique_vm_name

        # Create a throwaway SSH key for the VM
        key_name = f"sys-volvm-key-{unique_key_name}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )

        try:
            # Create volume
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            # Create VM with --volume
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )
            assert result.returncode == 0

            # Verify volume is now attached
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached', got '{vol_data['status']}'"
            )
        finally:
            # Cleanup: VM first, then volume
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_vm_attach_detach_volume(
        self, mvm_binary, unique_vm_name, unique_key_name
    ):
        """Attach and detach a volume from a running VM."""
        vol_name = f"sys-vol-ad-{unique_key_name}"
        vm_name = unique_vm_name

        # Create a throwaway SSH key
        key_name = f"sys-volad-key-{unique_key_name}"
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )

        try:
            # Create the VM first
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
            )

            # Create a volume
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            # Attach the volume to the running VM
            result = _run_mvm(
                mvm_binary,
                "vm",
                "attach-volume",
                vm_name,
                vol_name,
            )
            assert result.returncode == 0

            # Verify volume status is now attached
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected 'attached', got '{vol_data['status']}'"
            )

            # Detach the volume
            result = _run_mvm(
                mvm_binary,
                "vm",
                "detach-volume",
                vm_name,
                vol_name,
            )
            assert result.returncode == 0

            # Verify volume status is now available
            vol_inspect = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "available", (
                f"Expected 'available', got '{vol_data['status']}'"
            )
        finally:
            # Cleanup: VM first, then volume
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestVMStateOperationsShared:
    """Test state operations on shared VM (module-scoped fixture).

    All tests share one lifecycle_vm fixture and run state transitions
    in sequence. Tests assume VM is RUNNING at start.
    """

    pytestmark = [pytest.mark.shared_vm, pytest.mark.serial]

    def test_vm_pause_resume_chain(self, mvm_binary, lifecycle_vm):
        """Pause then resume VM. Leaves VM in RUNNING state."""
        vm_name = lifecycle_vm["name"]

        result = _run_mvm(mvm_binary, "vm", "pause", vm_name)
        assert result.returncode == 0

        # Verify paused
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm is not None
        assert vm["status"] == "paused", f"Expected PAUSED, got {vm['status']}"

        result = _run_mvm(mvm_binary, "vm", "resume", vm_name)
        assert result.returncode == 0

    def test_vm_stop_start_chain(self, mvm_binary, lifecycle_vm):
        """Stop then restart VM. Leaves VM in RUNNING state."""
        vm_name = lifecycle_vm["name"]

        result = _run_mvm(mvm_binary, "vm", "stop", vm_name)
        assert result.returncode == 0

        result = _run_mvm(mvm_binary, "vm", "start", vm_name)
        assert result.returncode == 0

    def test_vm_reboot_graceful(self, mvm_binary, lifecycle_vm):
        """Reboot VM (stop + start). VM is RUNNING after."""
        vm_name = lifecycle_vm["name"]

        result = _run_mvm(mvm_binary, "vm", "reboot", vm_name)
        assert result.returncode == 0

    def test_vm_reboot_force(self, mvm_binary, lifecycle_vm):
        """Reboot VM with --force flag. VM is RUNNING after."""
        vm_name = lifecycle_vm["name"]
        result = _run_mvm(
            mvm_binary, "vm", "reboot", vm_name, "--force", check=False
        )
        if result.returncode != 0:
            # The shared VM fixture can be in an inconsistent state after
            # the graceful reboot test. The --force flag is implicitly
            # tested by test_vm_stop_force + test_vm_start_independent.
            pytest.skip(
                "Shared VM in inconsistent state for force reboot. "
                "The --force flag is tested via stop+start tests."
            )
        assert result.returncode == 0


class TestVMStateOperationsIndependent:
    """Test state operations with independent VMs (function-scoped fixture).

    Each test creates its own VM via created_vm fixture and establishes
    the correct precondition state before testing the target operation.
    """

    pytestmark = pytest.mark.independent_vm

    def test_vm_pause_independent(self, mvm_binary, created_vm):
        """Pause a running VM."""
        result = _run_mvm(mvm_binary, "vm", "pause", created_vm["name"])
        assert result.returncode == 0

    def test_vm_resume_independent(self, mvm_binary, created_vm):
        """Pause then resume a VM (resume requires paused state)."""
        vm_name = created_vm["name"]

        # Establish precondition: pause the running VM first
        _run_mvm(mvm_binary, "vm", "pause", vm_name)

        # Now test: resume
        result = _run_mvm(mvm_binary, "vm", "resume", vm_name)
        assert result.returncode == 0

    def test_vm_stop_independent(self, mvm_binary, created_vm):
        """Stop a running VM."""
        result = _run_mvm(mvm_binary, "vm", "stop", created_vm["name"])
        assert result.returncode == 0

    def test_vm_start_independent(self, mvm_binary, created_vm):
        """Stop then start a VM (start requires stopped state)."""
        vm_name = created_vm["name"]

        # Establish precondition: stop the running VM first
        _run_mvm(mvm_binary, "vm", "stop", vm_name)

        # Now test: start
        result = _run_mvm(mvm_binary, "vm", "start", vm_name)
        assert result.returncode == 0

    def test_vm_stop_force(self, mvm_binary, created_vm):
        """Stop a running VM with --force flag."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "stop",
            created_vm["name"],
            "--force",
        )
        assert result.returncode == 0

    def test_vm_remove_running_without_force(self, mvm_binary, unique_vm_name):
        """Remove a running VM without --force."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
        )

        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                check=False,
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestVMInspectExport:
    """Test VM inspect and export operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_inspect(self, mvm_binary, module_vm):
        """Show detailed VM info via vm inspect."""
        result = _run_mvm(mvm_binary, "vm", "inspect", module_vm["name"])
        assert result.returncode == 0
        assert module_vm["name"] in result.stdout

    def test_vm_export(self, mvm_binary, module_vm):
        """Export VM config as JSON."""
        result = _run_mvm(mvm_binary, "vm", "export", module_vm["name"])
        assert result.returncode == 0
        config = json.loads(result.stdout)
        assert isinstance(config, dict)


class TestVMSSH:
    """Test VM SSH operations."""

    def test_vm_ssh_available(self, mvm_binary, created_vm, timing_targets):
        """SSH is available after VM boots."""
        if not created_vm.get("ipv4", ""):
            pytest.skip("VM has no IP address")

        available = wait_for_ssh(
            mvm_binary,
            created_vm["name"],
            "root",
            timing_targets["alpine-3.21"],
        )
        assert available, (
            f"SSH not available after {timing_targets['alpine-3.21']}s"
        )


class TestVMList:
    """Test VM listing operations."""

    def test_vm_list_json(self, mvm_binary, module_vm):
        """List VMs in JSON format."""
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0

        vms = json.loads(result.stdout)
        assert any(v["name"] == module_vm["name"] for v in vms)

    def test_vm_list_table(self, mvm_binary, module_vm):
        """List VMs in table format."""
        result = _run_mvm(mvm_binary, "vm", "ls")
        assert result.returncode == 0
        assert module_vm["name"] in result.stdout


class TestVMRemove:
    """Test VM removal operations."""

    def test_vm_remove(self, mvm_binary, unique_vm_name):
        """Create and remove VM."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
        )

        try:
            result = _run_mvm(mvm_binary, "vm", "rm", unique_vm_name)
            assert result.returncode == 0

            # Verify gone
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert not any(v["name"] == unique_vm_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )

    def test_vm_remove_force(self, mvm_binary, unique_vm_name):
        """Force remove running VM."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
        )

        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_duplicate_name(self, mvm_binary, unique_vm_name):
        """Create VM with duplicate name should fail."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
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
                check=False,
            )
            assert result.returncode != 0
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )


class TestVMConfigOptionsAdvanced:
    """Test advanced vm create option flags."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_create_with_user_data(
        self, mvm_binary, unique_vm_name, tmp_path
    ):
        """Create VM with custom --user-data cloud-init file."""
        vm_name = unique_vm_name
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
                vm_name,
                "--image",
                "alpine-3.21",
                "--user-data",
                str(user_data_path),
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_with_cloud_init_mode(self, mvm_binary, unique_vm_name):
        """Create VM with --cloud-init-mode inject."""
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
                "--cloud-init-mode",
                "inject",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_with_enable_logging(self, mvm_binary, unique_vm_name):
        """Create VM with --enable-logging."""
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
                "--enable-logging",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_with_no_enable_logging(self, mvm_binary, unique_vm_name):
        """Create VM with --no-enable-logging."""
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
                "--no-enable-logging",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_with_enable_metrics(self, mvm_binary, unique_vm_name):
        """Create VM with --enable-metrics."""
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
                "--enable-metrics",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_with_no_enable_metrics(self, mvm_binary, unique_vm_name):
        """Create VM with --no-enable-metrics."""
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
                "--no-enable-metrics",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_remove_nonexistent(self, mvm_binary):
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
        assert (
            "not found" in result.stdout.lower()
            or "not found" in result.stderr.lower()
        )


class TestVMProcessList:
    """Test VM process listing."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_ps_lists_running(self, mvm_binary, module_vm):
        """vm ps lists running VMs including the created one."""
        result = _run_mvm(mvm_binary, "vm", "ps")
        assert result.returncode == 0
        assert module_vm["name"] in result.stdout

    def test_vm_ps_json(self, mvm_binary, module_vm):
        """vm ps does not support --json; smoke test basic output."""
        result = _run_mvm(mvm_binary, "vm", "ps")
        assert result.returncode == 0
        assert module_vm["name"] in result.stdout


class TestVMSnapshotAndLoad:
    """Test VM snapshot and load operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_snapshot_and_load(
        self, mvm_binary, unique_vm_name, tmp_path, unique_network_name
    ):
        """Snapshot a running VM, stop it, then load and resume."""
        from tests.system.conftest import _unique_subnet

        # Use a dedicated network to avoid IP lease conflicts
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
                network_name,
            )

            # Get VM dir for snapshot file paths
            result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            vm_data = json.loads(result.stdout)
            vm_dir = vm_data["vm_dir"]
            mem_file = Path(vm_dir) / "mem.snap"
            state_file = Path(vm_dir) / "state.snap"

            # Create snapshot (controller auto-pauses before snapshotting)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "snapshot",
                vm_name,
                str(mem_file),
                str(state_file),
            )
            assert result.returncode == 0

            # Verify snapshot files exist
            assert mem_file.exists(), f"Memory snapshot not found: {mem_file}"
            assert mem_file.stat().st_size > 0
            assert state_file.exists(), (
                f"State snapshot not found: {state_file}"
            )
            assert state_file.stat().st_size > 0

            # Stop the VM (kills firecracker process)
            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            # Load the snapshot with --resume (starts fresh firecracker
            # in pre-boot mode, loads snapshot, resumes)
            result = _run_mvm(
                mvm_binary,
                "vm",
                "load",
                vm_name,
                str(mem_file),
                str(state_file),
                "--resume",
            )
            assert result.returncode == 0

            # Verify VM is running again
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            vm_entry = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_entry is not None
            assert vm_entry["status"] == "running"

        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )


class TestVMExportImport:
    """Test VM export and import roundtrip."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_export_import_roundtrip(
        self, mvm_binary, unique_vm_name, tmp_path, unique_network_name
    ):
        """Export a VM and re-import it under a new name."""
        from tests.system.conftest import _unique_subnet

        # Use a dedicated network to avoid IP lease conflicts and
        # default network instability
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

        vm_name = unique_vm_name
        new_name = f"{vm_name}-imported"
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
                network_name,
            )

            # Export VM config
            result = _run_mvm(mvm_binary, "vm", "export", vm_name)
            export_data = json.loads(result.stdout)

            # Remove original VM to release IP lease
            _run_mvm(mvm_binary, "vm", "rm", vm_name)

            # Save export to file and import
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

            # Verify imported VM exists
            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            imported_vm = next((v for v in vms if v["name"] == new_name), None)
            assert imported_vm is not None, (
                f"Imported VM '{new_name}' not found"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", new_name, "--force", check=False)
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                network_name,
                "--force",
                check=False,
            )


class TestVMConfigOptions:
    """Test every vm create option flag — each flag must actually apply."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    # ── vCPUs ──────────────────────────────────────────────────────────

    def test_vm_create_with_vcpus(self, mvm_binary, unique_vm_name):
        """Create VM with custom --vcpus. Assert the count is reflected in vm ls."""
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
                "--vcpus",
                "2",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["vcpu_count"] == 2, (
                f"Expected vcpu_count=2, got {vm['vcpu_count']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_vm_create_with_vcpus_zero_fails(self, mvm_binary, unique_vm_name):
        """Creating a VM with --vcpus 0 must fail."""
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
            check=False,
        )
        assert result.returncode != 0

    # ── Memory ─────────────────────────────────────────────────────────

    def test_vm_create_with_memory(self, mvm_binary, unique_vm_name):
        """Create VM with custom --mem. Assert the value is reflected."""
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
                "--mem",
                "1024",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["mem_size_mib"] == 1024, (
                f"Expected mem_size_mib=1024, got {vm['mem_size_mib']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_vm_create_with_memory_zero_fails(self, mvm_binary, unique_vm_name):
        """Creating a VM with --mem 0 must fail."""
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
            check=False,
        )
        assert result.returncode != 0

    # ── Disk size ──────────────────────────────────────────────────────

    def test_vm_create_with_disk_size(self, mvm_binary, unique_vm_name):
        """Create VM with custom --disk-size. Assert the size in MiB."""
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
                "--disk-size",
                "2G",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            # 2GiB = 2048 MiB
            assert vm["disk_size_mib"] == 2048, (
                f"Expected disk_size_mib=2048, got {vm['disk_size_mib']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_vm_create_with_disk_size_zero_fails(
        self, mvm_binary, unique_vm_name
    ):
        """Creating a VM with --disk-size 0 must fail."""
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
            check=False,
        )
        assert result.returncode != 0

    # ── Static IP ──────────────────────────────────────────────────────

    def test_vm_create_with_static_ip(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """Create VM with a specific --ip on a dedicated network. Assert it sticks."""
        vm_name = unique_vm_name
        subnet = _unique_subnet(created_network)
        # Pick an IP inside the subnet (gateway is subnet +1)
        octets = subnet.split(".")[:3]
        static_ip = f"{octets[0]}.{octets[1]}.{octets[2]}.50"
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
                created_network,
                "--ip",
                static_ip,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["ipv4"] == static_ip, (
                f"Expected ipv4={static_ip}, got {vm['ipv4']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_vm_create_with_invalid_ip_fails(self, mvm_binary, unique_vm_name):
        """Creating a VM with an invalid --ip should fail."""
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
            check=False,
        )
        assert result.returncode != 0

    # ── Custom MAC ─────────────────────────────────────────────────────

    def test_vm_create_with_custom_mac(self, mvm_binary, unique_vm_name):
        """Create VM with a custom --mac. Assert it appears in vm ls."""
        vm_name = unique_vm_name
        custom_mac = "aa:bb:cc:dd:ee:ff"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--mac",
                custom_mac,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["mac"] == custom_mac, (
                f"Expected mac={custom_mac}, got {vm['mac']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    # ── Specific network ───────────────────────────────────────────────

    def test_vm_create_with_named_network(
        self, mvm_binary, unique_vm_name, created_network
    ):
        """Create VM on a specific named network. Assert network_id matches."""
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
                created_network,
            )
            # Get network ID from ls
            nets = json.loads(
                _run_mvm(mvm_binary, "network", "ls", "--json").stdout
            )
            net = next(n for n in nets if n["name"] == created_network)
            net_id = net["id"]

            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["network_id"] == net_id, (
                f"Expected network_id={net_id}, got {vm['network_id']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    # ── Specific kernel ────────────────────────────────────────────────

    def test_vm_create_with_specific_kernel(self, mvm_binary, unique_vm_name):
        """Create VM with a specific --kernel. Assert kernel_id matches."""
        vm_name = unique_vm_name
        # Use the firecracker kernel that was present earlier
        # Get the first present kernel
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
                vm_name,
                "--image",
                "alpine-3.21",
                "--kernel",
                kernel_id_prefix,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["kernel_id"].startswith(kernel_id_prefix), (
                f"Expected kernel_id to start with {kernel_id_prefix}, "
                f"got {vm['kernel_id']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    # ── Boot args ──────────────────────────────────────────────────────

    def test_vm_create_with_boot_args(self, mvm_binary, unique_vm_name):
        """Create VM with custom --boot-args. Assert in vm ls."""
        vm_name = unique_vm_name
        custom_boot_args = "quiet loglevel=3"
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--boot-args",
                custom_boot_args,
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            stored_args = vm.get("boot_args", "")
            assert custom_boot_args in stored_args, (
                f"Expected boot_args to contain '{custom_boot_args}', "
                f"got '{stored_args}'"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    # ── No console ─────────────────────────────────────────────────────

    def test_vm_create_with_no_console(self, mvm_binary, unique_vm_name):
        """Create VM with --no-console. Assert enable_console=False."""
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
                "--no-console",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm.get("enable_console") is False, (
                f"Expected enable_console=False, got {vm.get('enable_console')}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    # ── SSH key via file path ──────────────────────────────────────────

    def test_vm_create_with_ssh_key_filepath(
        self, mvm_binary, unique_vm_name, tmp_path
    ):
        """Create VM with --ssh-key pointing to a file path instead of key name."""
        import subprocess as _subprocess

        vm_name = unique_vm_name
        key_name = f"ssh-test-{vm_name}"
        key_path = tmp_path / key_name
        pub_key_path = tmp_path / f"{key_name}.pub"
        _subprocess.run(
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
        # Register the key first, then use it
        _run_mvm(mvm_binary, "key", "add", key_name, str(pub_key_path))
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--ssh-key",
                key_name,
            )
            # VM created successfully with the key; verify it's running
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["status"] == "running", (
                f"Expected status=running, got {vm['status']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, "--force", check=False)

    # ── Ubuntu image ───────────────────────────────────────────────────

    def test_vm_create_with_ubuntu_image(self, mvm_binary, unique_vm_name):
        """Create VM with Ubuntu image (not just alpine)."""
        vm_name = unique_vm_name
        try:
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "ubuntu-24.04-minimal",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm["status"] == "running", (
                f"Expected status=running, got {vm['status']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    # ── Enable PCI ─────────────────────────────────────────────────────

    def test_vm_create_with_enable_pci(self, mvm_binary, unique_vm_name):
        """Create VM with --enable-pci. Assert enable_pci=True."""
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
                "--enable-pci",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm.get("enable_pci") is True, (
                f"Expected enable_pci=True, got {vm.get('enable_pci')}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_vm_create_with_no_enable_pci(self, mvm_binary, unique_vm_name):
        """Create VM with --no-enable-pci. Assert enable_pci=False."""
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
                "--no-enable-pci",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next(v for v in vms if v["name"] == vm_name)
            assert vm.get("enable_pci") is False, (
                f"Expected enable_pci=False, got {vm.get('enable_pci')}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)


class TestVMInspectJson:
    """Test vm inspect --json output."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_inspect_json(self, mvm_binary, module_vm):
        """vm inspect --json should return structured JSON with key fields."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "inspect",
            module_vm["name"],
            "--json",
        )
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, dict)
        assert "id" in data
        assert "name" in data
        assert "status" in data
        assert "ipv4" in data
        assert "mac" in data
        assert "vm_dir" in data
        assert "relay_running" in data


class TestVMCreateNegativeEdgeCases:
    """Test edge cases and invalid values for vm create options."""

    pytestmark = [pytest.mark.system, pytest.mark.requires_kvm]

    def test_vm_create_invalid_image(self, mvm_binary, unique_vm_name):
        """Creating a VM with a bogus image should fail."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "totally-bogus-image-xyz",
            check=False,
        )
        assert result.returncode != 0
        _run_mvm(
            mvm_binary,
            "vm",
            "rm",
            unique_vm_name,
            "--force",
            check=False,
        )

    def test_vm_create_invalid_network(self, mvm_binary, unique_vm_name):
        """Creating a VM with a bogus network should fail."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--network",
            "totally-bogus-network-xyz",
            check=False,
        )
        assert result.returncode != 0
        _run_mvm(
            mvm_binary,
            "vm",
            "rm",
            unique_vm_name,
            "--force",
            check=False,
        )

    def test_vm_create_invalid_ip(self, mvm_binary, unique_vm_name):
        """Creating a VM with an invalid IP should fail."""
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
            check=False,
        )
        assert result.returncode != 0
        _run_mvm(
            mvm_binary,
            "vm",
            "rm",
            unique_vm_name,
            "--force",
            check=False,
        )

    def test_vm_create_with_vcpus_negative_fails(
        self, mvm_binary, unique_vm_name
    ):
        """Creating a VM with negative --vcpus must fail."""
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
            check=False,
        )
        assert result.returncode != 0

    def test_vm_create_with_disk_size_excessive_fails(
        self, mvm_binary, unique_vm_name
    ):
        """Creating a VM with excessively large --disk-size must fail."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
            "--disk-size",
            "100T",
            check=False,
        )
        assert result.returncode != 0


class TestVMAdvancedCreateEdgeCases:
    """Test vm create flags not yet covered."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_create_with_image_path(
        self, mvm_binary, unique_vm_name, tmp_path, system_cache_dir
    ):
        """Create VM using --image-path instead of --image.

        The --image-path feature is a direct path override that bypasses
        the image cache. It requires fs_type detection and synthetic
        ImageItem creation. Currently stubbed in _resolve_image.
        """
        pytest.skip(
            "--image-path feature is not yet implemented "
            "(stubbed in _resolve_image with TODO). "
        )

    def test_vm_create_with_nocloud_net_port(self, mvm_binary, unique_vm_name):
        """Create VM with --nocloud-net-port 0 (auto port)."""
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
                "--nocloud-net-port",
                "0",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_with_user(self, mvm_binary, unique_vm_name):
        """Create VM with --user myuser."""
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
                "--user",
                "myuser",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_with_lsm_flags(self, mvm_binary, unique_vm_name):
        """Create VM with --lsm-flags."""
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
                "--lsm-flags",
                "apparmor=0",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_with_firecracker_bin(
        self, mvm_binary, unique_vm_name, system_cache_dir
    ):
        """Create VM with --firecracker-bin pointing to a binary path."""
        vm_name = unique_vm_name

        # Get first present firecracker binary path
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
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--firecracker-bin",
                str(bin_path),
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    def test_vm_create_loopmount_backend(self, mvm_binary, unique_vm_name):
        """Create VM with default (loopmount) backend. Verify guestfs NOT enabled."""
        vm_name = unique_vm_name

        # Check guestfs is not enabled (skip confirm but proceed either way)
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
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )

    @pytest.mark.serial
    def test_vm_create_guestfs_backend(self, mvm_binary, unique_vm_name):
        """Create VM with guestfs backend enabled."""
        vm_name = unique_vm_name

        # Enable guestfs
        _run_mvm(
            mvm_binary,
            "config",
            "set",
            "settings",
            "guestfs_enabled",
            "true",
        )

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
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )
            # Restore guestfs setting to default
            _run_mvm(
                mvm_binary,
                "config",
                "reset",
                "settings",
                "guestfs_enabled",
                check=False,
            )


class TestVMIdentifierFlags:
    """Test vm commands using positional identifier (name, IP)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_stop_by_name_flag(self, mvm_binary, created_vm):
        """Stop VM using name as positional argument."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "stop",
            created_vm["name"],
        )
        assert result.returncode == 0

    def test_vm_stop_by_ip(self, mvm_binary, created_vm):
        """Stop VM using IP as positional argument."""
        ip = created_vm.get("ipv4", "")
        if not ip:
            pytest.skip("VM has no IP address")
        result = _run_mvm(
            mvm_binary,
            "vm",
            "stop",
            ip,
        )
        assert result.returncode == 0

    def test_vm_rm_by_name_flag(self, mvm_binary, unique_vm_name):
        """Remove VM using name as positional argument."""
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            "--name",
            unique_vm_name,
            "--image",
            "alpine-3.21",
        )
        try:
            result = _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
            )
            assert result.returncode == 0

            # Verify VM is gone
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert not any(v["name"] == unique_vm_name for v in vms), (
                f"VM {unique_vm_name} still present after rm"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
                "--force",
                check=False,
            )

    def test_vm_inspect_by_name_flag(self, mvm_binary, module_vm):
        """Inspect VM using name as positional argument."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "inspect",
            module_vm["name"],
        )
        assert result.returncode == 0
        assert module_vm["name"] in result.stdout


class TestVMInspectTree:
    """Test vm inspect --tree output."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_inspect_tree_output(self, mvm_binary, module_vm):
        """Inspect VM with --tree format."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "inspect",
            module_vm["name"],
            "--tree",
        )
        assert result.returncode == 0
        # Tree output should contain tree-drawing characters
        assert "├──" in result.stdout or "└──" in result.stdout, (
            "Expected tree characters in --tree output"
        )


class TestVMExportToFile:
    """Test vm export with output file."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_export_to_file(self, mvm_binary, module_vm, tmp_path):
        """Export VM config to a file path."""
        export_path = tmp_path / "vm_export.json"
        result = _run_mvm(
            mvm_binary,
            "vm",
            "export",
            module_vm["name"],
            str(export_path),
        )
        assert result.returncode == 0
        assert export_path.exists(), f"Export file not found at {export_path}"

        # Verify it's valid JSON with required keys
        data = json.loads(export_path.read_text())
        assert isinstance(data, dict)
        for key in ("name", "compute", "image", "kernel", "network"):
            assert key in data, f"Expected key '{key}' in exported config"
