"""VM SSH access system tests."""

from __future__ import annotations

import json
import os
import shlex
import time as _time
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import _json_success_data, _run_mvm, _unique_subnet
from tests.system.keys.test_keys import _KeyOwner, _default_ids, _key_inventory

pytestmark = [pytest.mark.system, pytest.mark.domain_ssh]


def _ssh_inventory(runner_vm, domain):
    rows = _json_success_data(_run_mvm(runner_vm, domain, "ls", "--json").stdout, list)
    if domain in ("kernel", "key"):
        # Kernel listing may refresh missing rows; restoring a key default advances updated_at.
        rows = [{key: value for key, value in row.items() if key != "updated_at"} for row in rows]
    return sorted(rows, key=lambda row: json.dumps(row, sort_keys=True))


@pytest.fixture
def ssh_baseline(runner_vm):
    """Check exact public inventories/defaults and config after dependent owned teardown."""
    assert os.geteuid() != 0, "SSH journeys require the installed CLI's non-root operator route"
    domains = ("vm", "network", "key", "volume", "image", "kernel", "bin")
    before = {domain: _ssh_inventory(runner_vm, domain) for domain in domains}
    config = _json_success_data(_run_mvm(runner_vm, "config", "ls", "--json").stdout, dict)
    try:
        yield before
    finally:
        failures = []
        for domain, baseline in before.items():
            try:
                assert _ssh_inventory(runner_vm, domain) == baseline, domain
            except Exception as error:
                failures.append((f"{domain} baseline/default readback", repr(error)))
        try:
            assert _json_success_data(
                _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict,
            ) == config, "SSH journey changed configuration"
        except Exception as error:
            failures.append(("config readback", repr(error)))
        assert not failures, failures


@pytest.fixture
def ssh_vm_preflight(runner_vm, ssh_baseline):
    """Fail before resource publication when the bounded runner prerequisites are absent."""
    images = [item for item in ssh_baseline["image"]
              if item["type"] == "alpine" and item["version"] == "3.23" and item["is_present"]]
    assert len(images) == 1 and images[0]["minimum_rootfs_size_mib"] == 360, images
    live = [vm for vm in ssh_baseline["vm"] if vm["status"] not in ("stopped", "failed")]
    assert len(live) < 2, ("maximum two live inner VMs", live)
    host = _json_success_data(_run_mvm(runner_vm, "host", "status", "--json").stdout, dict)
    assert host["resources"]["nftables_available"] is True, host


@pytest.fixture
def ssh_vm(runner_vm, ssh_baseline, ssh_vm_preflight, key_vm_owner):
    """One function-owned VM with its own network and explicit authorized cached key."""
    owned = key_vm_owner
    key_name = f"{owned['name']}-ssh"
    owned["keys"].create(key_name, "--algorithm", "ed25519")
    owned["ssh_key_name"] = key_name
    vm = _create_key_vm(runner_vm, owned, (key_name,))
    owned["ipv4"] = vm["ipv4"]
    assert owned["ipv4"], vm
    _assert_owned_key_ssh(
        runner_vm, owned, owned["keys"].entry(key_name)["private_key_path"], "ssh-ready",
    )
    return owned


class TestSSHConnect:
    """Real installed-CLI SSH cases with function-owned VM/key/network teardown."""

    pytestmark = [pytest.mark.tier2, pytest.mark.usefixtures("ssh_baseline"), pytest.mark.timeout(600)]

    @pytest.mark.needs_kvm
    @pytest.mark.slow
    def test_ssh_command_execution(self, runner_vm, ssh_vm):
        """Resolve the VM's stored key and user, then execute a command by VM name."""
        result = _run_mvm(
            runner_vm, "ssh", ssh_vm["name"], "--cmd", "echo hello", check=False,
        )
        assert result.returncode == 0, (result.stdout, result.stderr)
        assert result.stdout.strip() == "hello", (result.stdout, result.stderr)

    def test_ssh_nonexistent_vm(self, runner_vm, ssh_baseline):
        """Reject an unknown VM selector before any SSH connection attempt."""
        name = f"sys-ssh-absent-{uuid.uuid4().hex[:10]}"
        assert all(vm["name"] != name for vm in ssh_baseline["vm"])
        result = _run_mvm(runner_vm, "ssh", name, "--cmd", "echo hi", check=False)
        assert result.returncode != 0, (result.stdout, result.stderr)
        assert f"Invalid IP address: {name}" in result.stderr, result.stderr

    def test_ssh_timeout_flag_fails_fast(self, runner_vm, ssh_baseline):
        """Unknown selectors still fail fast with the explicit connect timeout flag."""
        name = f"sys-ssh-absent-timeout-{uuid.uuid4().hex[:10]}"
        assert all(vm["name"] != name for vm in ssh_baseline["vm"])
        start = _time.monotonic()
        result = _run_mvm(
            runner_vm, "ssh", name, "--timeout", "2", "--cmd", "echo hi", check=False,
        )
        elapsed = _time.monotonic() - start
        assert result.returncode != 0, (elapsed, result.stdout, result.stderr)
        assert f"Invalid IP address: {name}" in result.stderr, result.stderr
        assert elapsed < 10, ("unknown selector should fail before probing", elapsed)

    @pytest.mark.needs_kvm
    @pytest.mark.slow
    def test_ssh_with_ip_flag(self, runner_vm, ssh_vm):
        """Connect using the owned VM's IP as the positional selector."""
        result = _run_mvm(
            runner_vm, "ssh", ssh_vm["ipv4"], "-u", "root", "--cmd", "whoami", check=False,
        )
        assert result.returncode == 0, (result.stdout, result.stderr)
        assert result.stdout.strip() == "root", (result.stdout, result.stderr)

    @pytest.mark.needs_kvm
    @pytest.mark.slow
    def test_ssh_with_user_flag(self, runner_vm, ssh_vm):
        """SSH authenticates with the explicit --user flag."""
        result = _run_mvm(
            runner_vm, "ssh", ssh_vm["name"], "--user", "root", "--cmd", "whoami", check=False,
        )
        assert result.returncode == 0, (result.stdout, result.stderr)
        assert result.stdout.strip() == "root", (result.stdout, result.stderr)

    @pytest.mark.needs_kvm
    @pytest.mark.slow
    def test_ssh_with_key_path(self, runner_vm, ssh_vm, monkeypatch):
        """A real valid private key that is not provisioned cannot authenticate a healthy VM."""
        owner = ssh_vm["keys"]
        unauthorized = owner.source_pair("unauthorized")
        public = unauthorized.with_suffix(".pub").read_text().split()[:2]
        authorized = _run_mvm(
            runner_vm, "exec", ssh_vm["name"], "--", "cat", "/root/.ssh/authorized_keys",
        ).stdout.splitlines()
        assert tuple(public) not in {tuple(line.split()[:2]) for line in authorized if line.strip()}
        # Prevent an inherited SSH agent from supplying an unrelated authorized identity.
        monkeypatch.delenv("SSH_AUTH_SOCK", raising=False)
        start = _time.monotonic()
        result = _run_mvm(
            runner_vm, "ssh", ssh_vm["name"], "--key", str(unauthorized),
            "--user", "root", "--timeout", "2", "--cmd", "whoami", check=False,
        )
        elapsed = _time.monotonic() - start
        assert result.returncode != 0, (result.stdout, result.stderr)
        # Auth probes intentionally discard OpenSSH stderr; the public CLI reports their timeout.
        assert "SSH connection timed out after waiting 2s for VM to become reachable" in result.stderr, result.stderr
        assert result.stdout.strip() != "root", result.stdout
        assert elapsed < 10, ("unauthorized authentication exceeded bounded probe", elapsed)
        _assert_owned_key_ssh(
            runner_vm, ssh_vm, owner.entry(ssh_vm["ssh_key_name"])["private_key_path"],
            "authorized-after-rejection",
        )

    @pytest.mark.needs_kvm
    @pytest.mark.slow
    def test_ssh_with_exported_key_file(self, runner_vm, ssh_vm):
        """Export the VM's owned authorized key and authenticate with its exact file path."""
        owner = ssh_vm["keys"]
        name = ssh_vm["ssh_key_name"]
        exported = owner.export(name)
        assert exported.read_bytes() == Path(owner.entry(name)["private_key_path"]).read_bytes()
        result = _run_mvm(
            runner_vm, "ssh", ssh_vm["name"], "-u", "root",
            "--key", str(exported), "--cmd", "whoami", check=False,
        )
        assert result.returncode == 0, (result.stdout, result.stderr)
        assert result.stdout.strip() == "root", (result.stdout, result.stderr)

    @pytest.mark.needs_kvm
    @pytest.mark.slow
    def test_ssh_with_named_key(self, runner_vm, ssh_vm):
        """Resolve an owned cached key name and require successful guest authentication."""
        result = _run_mvm(
            runner_vm, "ssh", ssh_vm["name"], "--key", ssh_vm["ssh_key_name"],
            "--cmd", "exit", check=False,
        )
        assert result.returncode == 0, (result.stdout, result.stderr)
        proof = _run_mvm(
            runner_vm, "ssh", ssh_vm["name"], "--key", ssh_vm["ssh_key_name"],
            "--cmd", "printf '%s' named-key-ok", check=False,
        )
        assert proof.returncode == 0, (proof.stdout, proof.stderr)
        assert proof.stdout == "named-key-ok", (proof.stdout, proof.stderr)

    @pytest.mark.needs_kvm
    @pytest.mark.slow
    def test_ssh_with_timeout_flag_success(self, runner_vm, ssh_vm):
        """An explicit connect timeout permits a longer successful guest command."""
        start = _time.monotonic()
        result = _run_mvm(
            runner_vm, "ssh", ssh_vm["name"], "--timeout", "2",
            "--cmd", "sleep 3; echo timeout-test-ok", check=False,
        )
        elapsed = _time.monotonic() - start
        assert result.returncode == 0, (elapsed, result.stdout, result.stderr)
        assert result.stdout.strip() == "timeout-test-ok", (result.stdout, result.stderr)
        assert elapsed >= 3, ("guest command did not run past its connect budget", elapsed)


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
    assert len(pair) == 2 and all(binary["version"] == "1.17.0" for binary in pair), pair
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
