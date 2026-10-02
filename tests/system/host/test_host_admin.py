"""Clean administrator enrollment and rollback inside one owned child host."""

from __future__ import annotations

import json
import os
import platform
import shlex
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import (
    SYSTEM_MVM_BINARY,
    _guest_run,
    _json_success_data,
    _run_mvm,
    _unique_subnet,
)
from tests.system.host.test_host import _HostGuardCase, _success_data
from tests.system.host.test_system_install import _file_receipt
from tests.system.init.test_init import _retained_rows

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_host,
    pytest.mark.requires_kvm,
    pytest.mark.destructive,
]

_OPERATOR = "hostqa"
_CHILD_MIRROR = "/opt/mvmctl-test/mirror"
_CHILD_CANDIDATE = "/opt/mvmctl-test/mvm-candidate"
_STATE_PROBE = r"""
import grp, hashlib, json, os, pwd, stat
from pathlib import Path

account = pwd.getpwnam("hostqa")
assert account.pw_uid > 0 and account.pw_gid > 0
def receipt(path):
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except FileNotFoundError:
        return None
    with os.fdopen(fd, "rb") as stream:
        before = os.fstat(stream.fileno())
        assert stat.S_ISREG(before.st_mode) and before.st_nlink == 1, path
        data = stream.read()
        after = os.fstat(stream.fileno())
        def stable(info):
            return (info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode,
                    info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)
        assert stable(before) == stable(after) == stable(os.stat(path, follow_symlinks=False)), path
    return {"identity": [before.st_dev, before.st_ino, before.st_uid, before.st_gid,
                         stat.S_IMODE(before.st_mode), before.st_nlink],
            "sha256": hashlib.sha256(data).hexdigest(),
            "content": None if path == "/usr/local/bin/mvm" else data.decode()}
try:
    group = grp.getgrnam("mvm")
    group = {"gid": group.gr_gid, "members": sorted(group.gr_mem)}
except KeyError:
    group = None
files = {name: receipt(path) for name, path in {
    "canonical": "/usr/local/bin/mvm",
    "sudoers": "/etc/sudoers.d/mvm",
    "sysctl": "/etc/sysctl.d/mvmctl.conf",
    "journal": "/var/lib/mvmctl/host/journal.json",
    "admission": "/var/lib/mvmctl/host/admission.json",
    "enrollment": f"/var/lib/mvmctl/host/operator-{account.pw_uid}.json",
}.items()}
print(json.dumps({"uid": account.pw_uid, "gid": account.pw_gid,
    "groups": sorted(os.getgrouplist(account.pw_name, account.pw_gid)), "group": group,
    "forward": Path("/proc/sys/net/ipv4/ip_forward").read_text().strip(),
    "files": files,
    "root_user_state": [str(path) for path in
        (Path("/root/.cache/mvmctl"), Path("/root/.config/mvmctl")) if path.exists()]}))
"""


def _root_file_data(state, name):
    receipt = state["files"][name]
    assert receipt is not None, (name, state)
    assert receipt["identity"][2:6] == [0, 0, 0o600, 1], receipt
    return json.loads(receipt["content"])


class _CleanHostCase:
    """A fixed child administrator channel, never administration of the runner."""

    def __init__(self, runner_vm, receipt_path):
        self.runner_vm = runner_vm
        self.guard = _HostGuardCase(runner_vm)
        self.canonical = _file_receipt(SYSTEM_MVM_BINARY)
        self.receipt_path = receipt_path
        self.child_id = None
        self.parent_network = None
        self.states = []
        self.child_networks = []

    def root(self, command, *, check=True, timeout=60):
        assert self.child_id, "no owned child administrator channel"
        return _run_mvm(
            self.runner_vm,
            "exec",
            self.child_id,
            "--user",
            "root",
            "--timeout",
            "10",
            "--",
            command,
            check=check,
            timeout=timeout,
        )

    def operator(self, *args, check=True, timeout=60):
        assert self.child_id, "no owned child operator channel"
        command = shlex.join(
            [
                "env",
                "LC_ALL=C",
                "NO_COLOR=1",
                f"MVM_ASSET_MIRROR={_CHILD_MIRROR}",
                SYSTEM_MVM_BINARY,
                *args,
            ]
        )
        return _run_mvm(
            self.runner_vm,
            "exec",
            self.child_id,
            "--user",
            _OPERATOR,
            "--timeout",
            "10",
            "--",
            command,
            check=check,
            timeout=timeout,
        )

    def state(self):
        result = self.root(shlex.join(["python3", "-c", _STATE_PROBE]))
        state = json.loads(result.stdout)
        assert state["files"]["canonical"]["identity"][2:6] == [0, 0, 0o755, 1]
        assert state["files"]["canonical"]["sha256"] == self.canonical[-1]
        assert state["root_user_state"] == [], state
        self.states.append(state)
        return state

    def administrator(self, action):
        args = [SYSTEM_MVM_BINARY, "host", action, "--operator", _OPERATOR]
        if action == "reset":
            args.append("--force")
        return self.root(shlex.join([*args, "--json"]), timeout=180)

    def prepare(self):
        self.guard.require_launch_assets()
        assert (
            _json_success_data(
                _run_mvm(
                    self.runner_vm,
                    "config",
                    "get",
                    "settings",
                    "guestfs_enabled",
                    "--json",
                ).stdout,
                bool,
            )
            is False
        ), "the child fixture requires LoopMount"
        images = [
            row
            for row in self.guard.baseline["image"]
            if row["type"] == "ubuntu-minimal"
            and row["version"] in ("noble", "24.04")
            and row["is_present"]
        ]
        assert len(images) == 1, (
            "one exact cached clean Ubuntu-minimal image is required"
        )
        kernel_id = os.environ.get("MVM_RUNNER_KERNEL_ID")
        base_receipt = Path("/opt/mvmctl-test/base-inputs.json")
        if not kernel_id and base_receipt.is_file():
            kernel_id = json.loads(base_receipt.read_text())["kernel_id"]
        kernels = [
            row
            for row in self.guard.baseline["kernel"]
            if row["is_present"]
            and (row["id"] == kernel_id if kernel_id else row["is_default"])
        ]
        assert len(kernels) == 1 and kernels[0]["version"] == "7.0.11", kernels
        archive = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser() / (
            f"firecracker-v1.16.2-{platform.machine()}.tgz"
        )
        self.archive = archive
        self.archive_receipt = _file_receipt(str(archive))
        name = f"sys-admin-net-{uuid.uuid4().hex[:16]}"
        self.guard.attempt("network", name)
        # NAT is only for child OS package prerequisites and fixed checksum metadata.
        self.parent_network = self.guard.remember(
            "network",
            _success_data(
                _run_mvm(
                    self.runner_vm,
                    "network",
                    "create",
                    name,
                    "--subnet",
                    _unique_subnet(name),
                    "--json",
                )
            ),
        )
        name = f"sys-admin-vm-{uuid.uuid4().hex}"
        self.guard.attempt("vm", name)
        rows = _json_success_data(
            _run_mvm(
                self.runner_vm,
                "vm",
                "create",
                name,
                "--image",
                images[0]["id"],
                "--kernel",
                kernels[0]["id"],
                "--network",
                self.parent_network["id"],
                "--disk-size",
                "3G",
                "--vcpu",
                "1",
                "--mem",
                "512M",
                "--nested-virt",
                "--user",
                "root",
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        assert len(rows) == 1, rows
        child = self.guard.remember("vm", rows[0])
        self.child_id = child["id"]
        inspected = _success_data(
            _run_mvm(
                self.runner_vm,
                "vm",
                "inspect",
                self.child_id,
                "--json",
            )
        )
        self.guard.vm_paths = tuple(
            inspected["filesystem"][field] for field in ("vm_dir", "rootfs_path")
        )
        assert not images[0]["type"].startswith("mvm-test-runner")
        # Only this clean disposable child receives OS prerequisites. No host init here.
        self.root(
            "apt-get update -qq && apt-get install -y -qq --no-install-recommends "
            "python3 sudo passwd kmod iproute2 nftables util-linux ca-certificates && "
            "apt-get clean",
            timeout=300,
        )
        self.root(
            "set -eu; test ! -e /usr/local/bin/mvm; test ! -L /usr/local/bin/mvm; "
            "test ! -e /var/lib/mvmctl/host; test ! -L /var/lib/mvmctl/host; "
            "test ! -e /etc/sudoers.d/mvm; test ! -L /etc/sudoers.d/mvm; "
            "test ! -e /etc/sysctl.d/mvmctl.conf; test ! -L /etc/sysctl.d/mvmctl.conf; "
            "if getent group mvm; then exit 1; fi; "
            "test -x /usr/sbin/ip; useradd --create-home --user-group --shell /bin/sh hostqa; "
            "passwd --lock hostqa; sysctl -w net.ipv4.ip_forward=0; "
            "install -d -o root -g root -m 0755 /opt/mvmctl-test"
        )
        _run_mvm(
            self.runner_vm,
            "cp",
            "-f",
            SYSTEM_MVM_BINARY,
            f"{self.child_id}:{_CHILD_CANDIDATE}",
        )
        _run_mvm(
            self.runner_vm,
            "cp",
            "-f",
            str(archive),
            f"{self.child_id}:/opt/mvmctl-test/{archive.name}",
        )
        self.root(
            f"set -eu; chmod 0755 {_CHILD_CANDIDATE}; "
            f"{_CHILD_CANDIDATE} host install-system; "
            f"install -d -o root -g root -m 0755 {_CHILD_MIRROR}; "
            f"install -o root -g root -m 0444 /opt/mvmctl-test/{archive.name} "
            f"{_CHILD_MIRROR}/{archive.name}; "
            f"mount --bind {_CHILD_MIRROR} {_CHILD_MIRROR}; "
            f"mount -o remount,bind,ro {_CHILD_MIRROR}"
        )
        mirror_digest = self.root(
            f"sha256sum {_CHILD_MIRROR}/{archive.name}"
        ).stdout.split()[0]
        assert mirror_digest == self.archive_receipt[-1]
        clean = self.state()
        assert clean["group"] is None and clean["groups"] == [clean["gid"]], clean
        assert clean["forward"] == "0"
        assert all(
            clean["files"][name] is None
            for name in ("sudoers", "sysctl", "journal", "admission", "enrollment")
        ), clean
        broad = self.root("LC_ALL=C sudo -n -l -U hostqa /usr/bin/true", check=False)
        assert broad.returncode != 0, broad.stdout + broad.stderr
        return clean

    def require_enrolled(self, original):
        result = _success_data(self.administrator("init"))
        assert result["user_added_to_group"] is True, result
        assert isinstance(result["changes"], list) and result["changes"], result
        state = self.state()
        assert state["files"]["canonical"] == original["files"]["canonical"]
        assert state["group"]["members"] == [_OPERATOR], state
        assert state["group"]["gid"] in state["groups"]
        assert set(state["groups"]) == {*original["groups"], state["group"]["gid"]}
        policy = state["files"]["sudoers"]
        assert policy["identity"][2:6] == [0, 0, 0o440, 1], policy
        grants = [
            line
            for line in policy["content"].splitlines()
            if line.strip() and not line.startswith("#")
        ]
        assert grants == [
            "%mvm ALL=(root) NOPASSWD: /usr/local/bin/mvm __mvm_privileged_v1 *"
        ]
        journal = _root_file_data(state, "journal")
        assert journal["version"] == 2 and journal["initialized"] is True, journal
        assert journal["completed"] == 6 and journal["pending"] is False, journal
        assert journal["operator"] == _OPERATOR and journal["uid"] == str(state["uid"])
        assert journal["group_existed"] is False and journal["member_existed"] is False
        assert journal["original_forward"] == original["forward"]
        assert (
            journal["original_sysctl"] is None and journal["original_sudoers"] is None
        )
        enrollment = _root_file_data(state, "enrollment")
        assert enrollment["operator"] == _OPERATOR and enrollment["uid"] == str(
            state["uid"]
        )
        assert enrollment["member_existed"] is False and enrollment["completed"] is True
        assert not enrollment.get("reverted", False), enrollment
        assert _root_file_data(state, "admission") == {"version": 1, "enabled": True}
        assert state["forward"] == "1"
        assert state["files"]["sysctl"]["content"] == "net.ipv4.ip_forward = 1\n"
        listed = self.root("LC_ALL=C sudo -n -l -U hostqa").stdout
        assert [
            line.strip() for line in listed.splitlines() if line.strip().startswith("(")
        ] == [
            "(root) NOPASSWD: /usr/local/bin/mvm __mvm_privileged_v1 *",
        ], listed
        return state

    def network_control(self):
        name = f"sys-admin-control-{uuid.uuid4().hex[:16]}"
        subnet = _unique_subnet(name)
        created = _success_data(
            self.operator(
                "network",
                "create",
                name,
                "--subnet",
                subnet,
                "--no-nat",
                "--json",
            )
        )
        assert created["name"] == name and created["subnet"] == subnet, created
        assert created["nat_enabled"] is False and created["is_default"] is False
        self.child_networks.append(created)
        self.require_network(created, present=True)
        return created

    def require_network(self, receipt, *, present):
        rows = _json_success_data(self.operator("network", "ls", "--json").stdout, list)
        matching = [
            row
            for row in rows
            if row["id"] == receipt["id"] or row["name"] == receipt["name"]
        ]
        links = json.loads(self.root("ip -j -d link show").stdout)
        matching_links = [row for row in links if row["ifname"] == receipt["bridge"]]
        rules = self.root("nft -j list ruleset").stdout
        if present:
            assert len(matching) == len(matching_links) == 1, (matching, matching_links)
            for field in ("id", "name", "created_at", "subnet", "bridge"):
                assert matching[0][field] == receipt[field], (field, matching)
            assert matching_links[0]["linkinfo"]["info_kind"] == "bridge"
            assert matching_links[0]["ifindex"] > 0 and matching_links[0].get("ifalias")
            assert receipt["bridge"] in rules, rules
        else:
            assert matching == matching_links == []
            assert receipt["bridge"] not in rules, rules

    def remove_child_network(self, receipt):
        self.require_network(receipt, present=True)
        self.operator("network", "rm", receipt["id"])
        self.require_network(receipt, present=False)

    def cleanup(self):
        # Durable test receipts precede child destruction, including failed phases.
        failures = []
        try:
            self.receipt_path.write_text(
                json.dumps(
                    {
                        "child_id": self.child_id,
                        "parent_network": self.parent_network,
                        "states": self.states,
                        "child_networks": self.child_networks,
                    },
                    indent=2,
                ),
                encoding="utf-8",
            )
            print(f"Clean host administration receipts: {self.receipt_path}")
        except Exception as error:
            failures.append(f"preserve child receipts: {error!r}")
        try:
            self.guard.cleanup()
        except (Exception, pytest.fail.Exception) as error:
            failures.append(f"owned child/network cleanup: {error!r}")
        if "vm" in self.guard.attempted and "vm" not in self.guard.receipts:
            failures.append(
                "owned child cleanup unproven: VM creation was attempted for "
                f"{self.guard.attempted['vm']!r}, but no admitted VM identity "
                "was recovered; retain receipts for explicit reconciliation"
            )
        try:
            assert _file_receipt(SYSTEM_MVM_BINARY) == self.canonical
            if hasattr(self, "archive_receipt"):
                assert _file_receipt(str(self.archive)) == self.archive_receipt
            identity = self.guard.receipts.get("vm")
            if identity:
                uid = os.getuid()
                child_id = identity[0]
                paths = [
                    f"/var/lib/mvmctl/jailer/firecracker/{child_id}",
                    f"/run/mvmctl/vmm-runtime/{uid}/{child_id}",
                    f"/run/mvmctl/authority/{uid}/netns/{child_id}",
                ]
                probe = "import os,sys; assert all(not os.path.lexists(p) for p in sys.argv[1:]), sys.argv[1:]"
                _guest_run(
                    self.runner_vm,
                    shlex.join(["sudo", "-n", "python3", "-c", probe, *paths]),
                )
            if self.parent_network:
                bridge = self.parent_network["bridge"]
                links = json.loads(_guest_run(self.runner_vm, "ip -j link show").stdout)
                assert not any(row["ifname"] == bridge for row in links), links
                assert (
                    bridge
                    not in _guest_run(
                        self.runner_vm, "sudo -n nft -j list ruleset"
                    ).stdout
                )
        except Exception as error:
            failures.append(f"independent absence/canonical preservation: {error!r}")
        try:
            receipt = json.loads(self.receipt_path.read_text())
            receipt["attempts"] = self.guard.attempted
            receipt["identities"] = self.guard.receipts
            receipt["cleanup_errors"] = failures
            self.receipt_path.write_text(
                json.dumps(receipt, indent=2), encoding="utf-8"
            )
        except Exception as error:
            failures.append(f"retain partial-publication cleanup receipts: {error!r}")
        assert not failures, "Clean child teardown failed:\n" + "\n".join(failures)


@pytest.fixture
def clean_host_child(runner_vm, tmp_path):
    case = _CleanHostCase(runner_vm, tmp_path / "host-administration-receipt.json")
    try:
        yield case
    finally:
        case.cleanup()


class TestCleanHostAdministration:
    def test_clean_enrollment_marker_only_reset_and_reinit(self, clean_host_child):
        case = clean_host_child
        original = case.prepare()
        case.require_enrolled(original)
        initialized = case.operator(
            "init",
            "--non-interactive",
            "--skip-host",
            "--skip-network",
            "--binary-version",
            "1.16.2",
            "--debug",
            check=False,
            timeout=180,
        )
        assert initialized.returncode == 0, initialized.stdout + initialized.stderr
        combined = initialized.stdout + initialized.stderr
        assert "all set" in combined.lower()
        assert "Using local mirror for trusted release" in combined, combined
        assert "Downloading trusted release" not in combined, combined
        status = _success_data(case.operator("host", "status", "--json"))
        assert status["resources"]["nftables_available"] is True, status
        first = case.network_control()
        before_denials = case.state()
        before_networks = _json_success_data(
            case.operator("network", "ls", "--json").stdout, list
        )
        for command in (
            [SYSTEM_MVM_BINARY, "host", "init"],
            [SYSTEM_MVM_BINARY, "host", "reset", "--force"],
            [SYSTEM_MVM_BINARY, "host", "install-system"],
            ["/usr/sbin/ip", "link", "show"],
        ):
            refused = _run_mvm(
                case.runner_vm,
                "exec",
                case.child_id,
                "--user",
                _OPERATOR,
                "--timeout",
                "10",
                "--",
                shlex.join(["env", "LC_ALL=C", "sudo", "-n", *command]),
                check=False,
            )
            assert refused.returncode != 0, refused.stdout + refused.stderr
            assert "sudo:" in refused.stderr, refused.stdout + refused.stderr
            assert case.state() == before_denials
            case.require_network(first, present=True)
        direct = case.operator("host", "init", check=False)
        assert direct.returncode != 0
        assert "sudo /usr/local/bin/mvm host init" in direct.stdout + direct.stderr
        assert _retained_rows(
            "network",
            _json_success_data(
                case.operator("network", "ls", "--json").stdout,
                list,
            ),
        ) == _retained_rows("network", before_networks)
        assert case.state() == before_denials
        case.remove_child_network(first)
        assert (
            _json_success_data(case.operator("vm", "ls", "--json").stdout, list) == []
        )
        assert (
            _json_success_data(case.operator("network", "ls", "--json").stdout, list)
            == []
        )
        reset = case.administrator("reset")
        assert reset.returncode == 0, reset.stdout + reset.stderr
        _json_success_data(reset.stdout, type(None))
        restored = case.state()
        assert restored["group"] is None and restored["groups"] == original["groups"]
        assert restored["forward"] == original["forward"]
        for name in ("canonical", "sudoers", "sysctl"):
            assert restored["files"][name] == original["files"][name], name
        journal = _root_file_data(restored, "journal")
        assert journal["reset_state"] == "complete" and journal["reset_step"] == 4, (
            journal
        )
        assert journal["initialized"] is False
        assert _root_file_data(restored, "enrollment")["reverted"] is True
        assert _root_file_data(restored, "admission") == {
            "version": 1,
            "enabled": False,
        }
        name = f"sys-admin-disabled-{uuid.uuid4().hex[:16]}"
        refused = case.operator(
            "network",
            "create",
            name,
            "--subnet",
            _unique_subnet(name),
            "--no-nat",
            "--json",
            check=False,
        )
        assert refused.returncode != 0, refused.stdout + refused.stderr
        assert case.state() == restored
        assert (
            _json_success_data(case.operator("network", "ls", "--json").stdout, list)
            == []
        )
        case.require_network(first, present=False)
        case.require_enrolled(original)
        second = case.network_control()
        case.remove_child_network(second)
        assert (
            _json_success_data(case.operator("network", "ls", "--json").stdout, list)
            == []
        )
        assert _root_file_data(case.state(), "admission")["enabled"] is True
