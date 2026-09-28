"""VM SSH access system tests."""

from __future__ import annotations

import os
import shlex
import tempfile
import time as _time
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import _guest_run, _json_success_data, _run_mvm, _unique_subnet, wait_for_ssh
from tests.system.keys.test_keys import _KeyOwner, _default_ids, _key_inventory

pytestmark = [pytest.mark.system, pytest.mark.domain_ssh]


class TestSSHConnect:
    """Test mvm ssh connect (non-interactive via --cmd)."""

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_command_execution(self, runner_vm, module_vm, timing_targets):
        """Execute a command via SSH on a running VM using name as positional arg."""
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.23"]
        ssh_available = wait_for_ssh(
            runner_vm, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            runner_vm,
            "ssh",
            vm_info["name"],
            "--cmd",
            "echo hello",
            check=False,
        )
        assert result.returncode == 0, f"SSH command failed: {result.stderr}"
        assert "hello" in result.stdout, (
            f"Expected 'hello' in output, got: {result.stdout}"
        )

    def test_ssh_nonexistent_vm(self, runner_vm):
        """SSH to nonexistent VM should fail gracefully."""
        result = _run_mvm(
            runner_vm,
            "ssh",
            "nonexistent-vm-xyz123",
            "--cmd",
            "echo hi",
            check=False,
        )
        assert result.returncode != 0, (
            f"Expected non-zero exit for nonexistent VM, "
            f"got: {result.returncode}"
        )

    def test_ssh_timeout_flag_fails_fast(self, runner_vm):
        """SSH with --timeout to nonexistent VM should fail fast (<5s not 60s)."""
        start = _time.monotonic()
        result = _run_mvm(
            runner_vm,
            "ssh",
            "nonexistent-vm-timeout-test",
            "--timeout",
            "2",
            "--cmd",
            "echo hi",
            check=False,
        )
        elapsed = _time.monotonic() - start
        assert result.returncode != 0, (
            f"Expected non-zero exit for nonexistent VM, "
            f"got: {result.returncode} (took {elapsed:.1f}s)"
        )
        assert elapsed < 10, (
            f"--timeout 2 should fail fast, but took {elapsed:.1f}s "
            f"(default 60s timeout would have masked this)"
        )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_ip_flag(self, runner_vm, module_vm, timing_targets):
        """Connect via IP as positional arg instead of VM name."""
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.23"]
        ssh_available = wait_for_ssh(
            runner_vm, vm_info["name"], "root", ssh_timeout
        )
        assert ssh_available, f"SSH not available within {ssh_timeout}s"

        result = _run_mvm(
            runner_vm,
            "ssh",
            vm_info["ipv4"],
            "-u", "root",
            "--cmd",
            "whoami",
            check=False,
        )
        assert result.returncode == 0, f"SSH via IP failed: {result.stderr}"
        assert "root" in result.stdout, (
            f"Expected 'root' in output, got: {result.stdout}"
        )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_user_flag(self, runner_vm, module_vm, timing_targets):
        """SSH with explicit --user flag."""
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.23"]
        wait_for_ssh(runner_vm, vm_info["name"], "root", ssh_timeout)

        result = _run_mvm(
            runner_vm,
            "ssh",
            vm_info["name"],
            "-u",
            "root",
            "--cmd",
            "whoami",
            check=False,
        )
        assert result.returncode == 0, (
            f"SSH with --user failed: {result.stderr}"
        )
        assert "root" in result.stdout

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_key_path(
        self, runner_vm, module_vm, timing_targets
    ):
        """SSH with explicit --key pointing to a private key file.

        Creates a throwaway key inside the test VM, then attempts SSH
        with --key pointing to the unauthorized key file path.
        The SSH connection should attempt and fail with Permission denied
        since the key isn't authorized on the VM.
        """
        vm_info = module_vm
        ssh_timeout = timing_targets["alpine:3.23"]
        wait_for_ssh(runner_vm, vm_info["name"], "root", ssh_timeout)

        with tempfile.TemporaryDirectory(
            prefix="mvm-ssh-unauthorized-", dir=Path.home() / ".cache"
        ) as scratch:
            test_key = f"{scratch}/key"
            _guest_run(
                runner_vm,
                f"ssh-keygen -t ed25519 -f {shlex.quote(test_key)} -N '' -q",
                timeout=30,
            )

            result = _run_mvm(
                runner_vm,
                "ssh",
                vm_info["name"],
                "--key",
                test_key,
                "--cmd",
                "whoami",
                check=False,
            )
            assert result.returncode != 0, (
                f"SSH with unauthorized key should fail, got rc={result.returncode} "
                f"stdout: {result.stdout} stderr: {result.stderr}"
            )
            assert "Permission denied" in result.stderr or "timed out" in result.stderr, (
                f"Expected 'Permission denied' or 'timed out' for unauthorized key, "
                f"got stderr: {result.stderr}"
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_exported_key_file(
        self, runner_vm, created_vm, timing_targets
    ):
        """SSH with --key pointing to an exported private key file path.

        Uses created_vm fixture (which has an authorized key), exports the
        key to a temp dir inside the test VM, then SSHes with --key pointing
        to the exported file path. Verifies SSH succeeds with the exported key.
        """
        vm_info = created_vm
        ssh_timeout = timing_targets["alpine:3.23"]

        # Look up the key used by this VM via its ssh_keys field
        vm_ssh_keys = vm_info.get("ssh_keys", [])
        assert vm_ssh_keys, "VM has no ssh_keys — cannot test exported key"

        key_id = vm_ssh_keys[0]
        key_ls_result = _run_mvm(runner_vm, "key", "ls", "--json", check=False)
        assert key_ls_result.returncode == 0, "key ls --json failed"

        keys = _json_success_data(key_ls_result.stdout, list)
        matching_keys = [
            k for k in keys
            if k.get("id") == key_id or k.get("name") == key_id
        ]
        assert matching_keys, f"Key with id/name {key_id[:16]}... not found in cache"
        key_name = matching_keys[0]["name"]

        with tempfile.TemporaryDirectory(
            prefix="mvm-ssh-export-", dir=Path.home() / ".cache"
        ) as key_export_dir:
            export_result = _run_mvm(
                runner_vm, "key", "export", key_name, key_export_dir, check=False
            )
            assert export_result.returncode == 0, (
                f"Key export failed: {export_result.stderr}"
            )

            private_keys = [
                path for path in Path(key_export_dir).iterdir()
                if path.is_file() and path.suffix != ".pub"
            ]
            assert len(private_keys) == 1, private_keys

            ssh_available = wait_for_ssh(
                runner_vm, vm_info["name"], "root", ssh_timeout
            )
            assert ssh_available, f"SSH not available within {ssh_timeout}s"

            result = _run_mvm(
                runner_vm,
                "ssh",
                vm_info["name"],
                "-u", "root",
                "--key", str(private_keys[0]),
                "--cmd", "whoami",
                check=False,
            )
            assert result.returncode == 0, (
                f"SSH with exported key file failed: rc={result.returncode} "
                f"stdout={result.stdout} stderr={result.stderr}"
            )
            assert "root" in result.stdout, (
                f"Expected 'root' in output, got: {result.stdout}"
            )

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_named_key(
        self, runner_vm, module_vm, timing_targets
    ):
        """SSH with --key specifying a named key (from key cache).

        Creates a named key and attempts SSH. The connection may fail since
        the key isn't authorized on the module_vm — that's acceptable. We're
        verifying the CLI parsed the named key and attempted to use it.
        """
        import uuid as _uuid

        vm_info = module_vm

        key_name = f"sys-ssh-key-{_uuid.uuid4().hex[:6]}"
        try:
            _run_mvm(
                runner_vm, "key", "create", key_name, "--algorithm", "ed25519"
            )

            result = _run_mvm(
                runner_vm,
                "ssh",
                vm_info["name"],
                "--key",
                key_name,
                "--cmd",
                "exit",
                check=False,
            )
            # The connection may fail if the key isn't authorized on the VM
            if result.returncode != 0:
                combined = (result.stdout + result.stderr).lower()
                assert (
                    "permission denied" in combined
                    or "not found" in combined
                    or "no route to host" in combined
                    or "connection refused" in combined
                    or "timed out" in combined
                ), (
                    f"Unexpected error with named key: "
                    f"stdout={result.stdout} stderr={result.stderr}"
                )
        finally:
            _run_mvm(runner_vm, "key", "rm", key_name, "--force", check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_ssh_with_timeout_flag_success(
        self, runner_vm, module_vm, timing_targets
    ):
        """SSH with --timeout on a real VM should succeed."""
        vm_info = module_vm

        result = _run_mvm(
            runner_vm,
            "ssh",
            vm_info["name"],
            "--timeout",
            "30",
            "--cmd",
            "echo timeout-test-ok",
            check=False,
        )
        assert result.returncode == 0, (
            f"SSH with --timeout failed: {result.stderr}"
        )
        assert "timeout-test-ok" in result.stdout, (
            f"Expected 'timeout-test-ok' in output, got: {result.stdout}"
        )


@pytest.fixture
def key_vm_owner(runner_vm):
    """Own one preseed-only VM/network journey and its checked key resources."""
    mirror = os.environ.get("MVM_ASSET_MIRROR")
    assert mirror and Path(mirror).is_dir(), ("configured local asset mirror directory required", mirror)
    images = _json_success_data(_run_mvm(runner_vm, "image", "ls", "--json").stdout, list)
    selected = [image for image in images
                if image["type"] == "alpine" and image["version"] == "3.23" and image["is_present"]]
    assert len(selected) == 1, selected
    assert 0 < selected[0]["minimum_rootfs_size_mib"] <= 512, selected[0]
    kernels = _json_success_data(_run_mvm(runner_vm, "kernel", "ls", "--json").stdout, list)
    selected_kernels = [kernel for kernel in kernels if kernel["is_default"] and kernel["is_present"]]
    assert len(selected_kernels) == 1, selected_kernels
    binaries = _json_success_data(_run_mvm(runner_vm, "bin", "ls", "--json").stdout, list)
    pair = [binary for binary in binaries if binary["is_default"] and binary["is_present"]]
    assert {binary["type"] for binary in pair} == {"firecracker", "jailer"}, pair
    assert len(pair) == 2 and all(binary["version"] == "1.16.2" for binary in pair), pair
    before_vms = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
    before_networks = _json_success_data(_run_mvm(runner_vm, "network", "ls", "--json").stdout, list)
    token = uuid.uuid4().hex[:10]
    owned = {"name": f"sys-key-vm-{token}", "network": f"sys-key-net-{token}",
             "vm_id": None, "network_id": None, "image_id": selected[0]["id"],
             "kernel_id": selected_kernels[0]["id"], "vm_attempted": False,
             "network_attempted": False, "before_vm": before_vms, "before_network": before_networks}
    assert all(vm["name"] != owned["name"] for vm in before_vms), before_vms
    assert all(network["name"] != owned["network"] for network in before_networks), before_networks
    owner = _KeyOwner(runner_vm)
    owned["keys"] = owner
    try:
        owned["network_attempted"] = True
        created = _json_success_data(_run_mvm(
            runner_vm, "network", "create", owned["network"],
            "--subnet", _unique_subnet(owned["network"]), "--no-nat", "--json",
        ).stdout, dict)
        assert created["name"] == owned["network"], created
        assert created["id"] not in {network["id"] for network in before_networks}, created
        owned["network_id"] = created["id"]
        yield owned
    finally:
        failures = []
        vm_absent = False
        try:
            vms = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
            matching = _owned_key_resource(owned, "vm", vms)
            if matching is not None:
                _run_mvm(runner_vm, "vm", "rm", owned["vm_id"], "--force", timeout=120)
        except Exception as error:
            failures.append(("owned VM removal", repr(error)))
        try:
            remaining = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
            assert all(vm["name"] != owned["name"] and vm["id"] != owned["vm_id"]
                       for vm in remaining), remaining
            vm_absent = True
            _assert_key_resource_baseline(before_vms, remaining)
        except Exception as error:
            failures.append(("owned VM absence/baseline readback", repr(error)))
        try:
            networks = _json_success_data(_run_mvm(runner_vm, "network", "ls", "--json").stdout, list)
            matching = _owned_key_resource(owned, "network", networks)
            if matching is not None:
                assert vm_absent, "network retained: owned VM absence was not proven"
                _run_mvm(runner_vm, "network", "rm", owned["network_id"])
        except Exception as error:
            failures.append(("owned network removal", repr(error)))
        try:
            remaining = _json_success_data(_run_mvm(runner_vm, "network", "ls", "--json").stdout, list)
            assert all(network["name"] != owned["network"] and network["id"] != owned["network_id"]
                       for network in remaining), remaining
            _assert_key_resource_baseline(before_networks, remaining)
        except Exception as error:
            failures.append(("owned network absence/baseline readback", repr(error)))
        try:
            owner.cleanup()
        except Exception as error:
            failures.append(("owned key/artifact/default cleanup", repr(error)))
        assert not failures, failures


def _owned_key_resource(owned, domain, inventory):
    """Recover only the uniquely named, attempted publication absent from baseline."""
    name = owned["name" if domain == "vm" else "network"]
    baseline = owned[f"before_{domain}"]
    assert all(item["name"] != name for item in baseline), baseline
    matching = [item for item in inventory if item["name"] == name]
    assert len(matching) <= 1, matching
    if not matching:
        return None
    item = matching[0]
    assert owned[f"{domain}_attempted"], item
    assert isinstance(item["id"], str) and item["id"], item
    assert item["id"] not in {prior["id"] for prior in baseline}, item
    tracked = owned[f"{domain}_id"]
    assert tracked is None or tracked == item["id"], (tracked, item)
    owned[f"{domain}_id"] = item["id"]
    return item


def _assert_key_resource_baseline(baseline, remaining):
    """Keep unrelated resource membership, identity and default selections unchanged."""
    def identities(items):
        return {item["id"]: (item["name"], item.get("is_default")) for item in items}
    assert len(remaining) == len(baseline), (baseline, remaining)
    assert identities(remaining) == identities(baseline), (baseline, remaining)


def _create_key_vm(runner_vm, owned, key_names=None):
    options = ("--ssh-key", ",".join(key_names)) if key_names is not None else ()
    assert not owned["vm_attempted"], owned["name"]
    owned["vm_attempted"] = True
    try:
        created = _json_success_data(_run_mvm(
            runner_vm, "vm", "create", owned["name"], "--image", owned["image_id"],
            "--kernel", owned["kernel_id"], "--network", owned["network"],
            "--cloud-init-mode", "off", "--skip-deblob", "--no-pci",
            "--disk-size", "512M", "--mem", "256M", "--vcpu", "1", "--user", "root",
            *options, "--json", timeout=180,
        ).stdout, list)
        assert len(created) == 1, created
        vm = created[0]
        assert vm["name"] == owned["name"] and vm["status"] == "running", vm
        assert vm["id"] not in {prior["id"] for prior in owned["before_vm"]}, vm
        owned["vm_id"] = vm["id"]
        assert (vm["disk_size_mib"], vm["mem_size_mib"], vm["vcpu_count"]) == (512, 256, 1), vm
        assert vm["image_id"] == owned["image_id"] and vm["kernel_id"] == owned["kernel_id"], vm
        return vm
    finally:
        inventory = _json_success_data(
            _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
        )
        _owned_key_resource(owned, "vm", inventory)


def _assert_owned_key_ssh(runner_vm, owned, private_key, marker):
    """Allow bounded SSH readiness, then require authentication and the exact guest effect."""
    deadline = _time.monotonic() + 60
    while True:
        result = _run_mvm(
            runner_vm, "ssh", owned["name"], "--key", str(private_key),
            "--user", "root", "--timeout", "5", "--cmd", f"printf '%s' {shlex.quote(marker)}",
            check=False, timeout=15,
        )
        if result.returncode == 0:
            assert result.stdout == marker, (result.stdout, result.stderr)
            break
        assert _time.monotonic() < deadline, (result.returncode, result.stdout, result.stderr)
        _time.sleep(0.5)


class TestKeyRunningVMDependency:
    pytestmark = [pytest.mark.tier2, pytest.mark.needs_kvm, pytest.mark.needs_network, pytest.mark.slow]

    def test_delete_ssh_key_used_by_running_vm(self, runner_vm, key_vm_owner):
        """Removing a cached key leaves its already-provisioned running guest usable by SSH."""
        owned = key_vm_owner
        owner = owned["keys"]
        name = f"{owned['name']}-key"
        owner.create(name, "--algorithm", "ed25519")
        exported = owner.export(name)
        _create_key_vm(runner_vm, owned, (name,))
        _assert_owned_key_ssh(runner_vm, owned, exported, "before-key-removal")
        _run_mvm(runner_vm, "key", "rm", name)
        assert all(item["name"] != name for item in _key_inventory(runner_vm).values())
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", owned["name"], "--json").stdout, dict,
        )
        assert inspected["vm"]["id"] == owned["vm_id"]
        assert inspected["vm"]["status"] == "running"
        _assert_owned_key_ssh(runner_vm, owned, exported, "after-key-removal")


class TestKeyDefaults:
    pytestmark = [pytest.mark.tier2, pytest.mark.needs_kvm, pytest.mark.needs_network, pytest.mark.slow]

    def test_multiple_default_ssh_keys(self, runner_vm, key_vm_owner):
        """Both selected default keys are recorded, provisioned and authenticate the guest."""
        owned = key_vm_owner
        owner = owned["keys"]
        names = [f"{owned['name']}-key1", f"{owned['name']}-key2"]
        for name in names:
            owner.create(name, "--algorithm", "ed25519")
        _run_mvm(runner_vm, "key", "default", *names)
        assert _default_ids(_key_inventory(runner_vm)) == owner.defaults | {owner.names[name] for name in names}
        exported = {name: owner.export(name) for name in names}
        _create_key_vm(runner_vm, owned)
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", owned["name"], "--json").stdout, dict,
        )
        expected_names = {owner.baseline[key_id]["name"] for key_id in owner.defaults} | set(names)
        assert set(inspected["vm"]["ssh_keys"]) == expected_names, inspected["vm"]
        authorized = _run_mvm(
            runner_vm, "exec", owned["name"], "--", "cat", "/root/.ssh/authorized_keys",
        ).stdout.splitlines()
        admitted = {tuple(line.split()[:2]) for line in authorized if line.strip()}
        for name in names:
            public = Path(owner.entry(name)["public_key_path"]).read_text()
            assert tuple(public.split()[:2]) in admitted, (name, authorized)
            _assert_owned_key_ssh(runner_vm, owned, exported[name], name)
