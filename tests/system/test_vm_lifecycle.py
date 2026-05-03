"""VM lifecycle system tests — state operations with both approaches."""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm, wait_for_ssh

pytestmark = [pytest.mark.system, pytest.mark.requires_kvm, pytest.mark.slow]


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


class TestVMStateOperationsShared:
    """Test state operations on shared VM (module-scoped fixture).

    All tests share one lifecycle_vm fixture and run state transitions
    in sequence. Tests assume VM is RUNNING at start.
    """

    pytestmark = pytest.mark.shared_vm

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
        result = _run_mvm(mvm_binary, "vm", "reboot", vm_name, "--force")
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

    def test_vm_inspect(self, mvm_binary, created_vm):
        """Show detailed VM info via vm inspect."""
        result = _run_mvm(mvm_binary, "vm", "inspect", created_vm["name"])
        assert result.returncode == 0
        assert created_vm["name"] in result.stdout

    def test_vm_export(self, mvm_binary, created_vm):
        """Export VM config as JSON."""
        result = _run_mvm(mvm_binary, "vm", "export", created_vm["name"])
        assert result.returncode == 0
        config = json.loads(result.stdout)
        assert isinstance(config, dict)


class TestVMSSH:
    """Test VM SSH operations."""

    def test_vm_ssh_available(self, mvm_binary, created_vm, timing_targets):
        """SSH is available after VM boots."""
        vm_ip = created_vm.get("ipv4", "")

        if not vm_ip:
            pytest.skip("VM has no IP address")

        available = wait_for_ssh(vm_ip, "root", timing_targets["alpine-3.21"])
        assert available, (
            f"SSH not available after {timing_targets['alpine-3.21']}s"
        )


class TestVMList:
    """Test VM listing operations."""

    def test_vm_list_json(self, mvm_binary, created_vm):
        """List VMs in JSON format."""
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        assert result.returncode == 0

        vms = json.loads(result.stdout)
        assert any(v["name"] == created_vm["name"] for v in vms)

    def test_vm_list_table(self, mvm_binary, created_vm):
        """List VMs in table format."""
        result = _run_mvm(mvm_binary, "vm", "ls")
        assert result.returncode == 0
        assert created_vm["name"] in result.stdout


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

    def test_vm_ps_lists_running(self, mvm_binary, created_vm):
        """vm ps lists running VMs including the created one."""
        result = _run_mvm(mvm_binary, "vm", "ps")
        assert result.returncode == 0
        assert created_vm["name"] in result.stdout

    def test_vm_ps_json(self, mvm_binary, created_vm):
        """vm ps does not support --json; smoke test basic output."""
        result = _run_mvm(mvm_binary, "vm", "ps")
        assert result.returncode == 0
        assert created_vm["name"] in result.stdout


class TestVMSnapshotAndLoad:
    """Test VM snapshot and load operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
    ]

    def test_vm_snapshot_and_load(self, mvm_binary, unique_vm_name, tmp_path):
        """Snapshot a running VM, stop it, then load and resume."""
        mem_file = tmp_path / "snapshot_mem"
        state_file = tmp_path / "snapshot_state"

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
                "snapshot",
                unique_vm_name,
                str(mem_file),
                str(state_file),
            )
            assert result.returncode == 0
            assert mem_file.exists()
            assert state_file.exists()

            result = _run_mvm(mvm_binary, "vm", "stop", unique_vm_name)
            assert result.returncode == 0

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
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["status"] == "running"
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                unique_vm_name,
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
        self, mvm_binary, unique_vm_name, tmp_path
    ):
        """Export a VM and re-import it under a new name."""
        import_name = f"{unique_vm_name}-imported"

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
            result = _run_mvm(mvm_binary, "vm", "export", unique_vm_name)
            assert result.returncode == 0
            config = json.loads(result.stdout)
            assert isinstance(config, dict)

            # Remove original VM to release IP lease before import
            _run_mvm(mvm_binary, "vm", "rm", unique_vm_name)

            config_path = tmp_path / "vm_config.json"
            config_path.write_text(result.stdout)

            result = _run_mvm(
                mvm_binary,
                "vm",
                "import",
                str(config_path),
                "--name",
                import_name,
            )
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(result.stdout)
            assert any(v["name"] == import_name for v in vms)
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                import_name,
                "--force",
                check=False,
            )


class TestVMCreateNegativePaths:
    """Test VM creation with invalid inputs."""

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
