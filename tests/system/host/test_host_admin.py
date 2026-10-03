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
_SECOND_OPERATOR = "hostqa2"
_ENROLLMENT_POLICY = "/etc/sudoers.d/mvmqa-host-init"
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
def host_directory_receipt():
    try:
        fd = os.open("/var/lib/mvmctl/host", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    except FileNotFoundError:
        return None
    try:
        def identity(info):
            return [info.st_dev, info.st_ino, info.st_uid, info.st_gid,
                    info.st_mode, info.st_nlink]
        before = identity(os.fstat(fd))
        entries = []
        with os.scandir(fd) as directory:
            for entry in directory:
                assert len(entries) < 64, "host-state directory exceeds receipt bound"
                assert len(os.fsencode(entry.name)) <= 255
                info = os.stat(entry.name, dir_fd=fd, follow_symlinks=False)
                entries.append({"name": entry.name, "identity": identity(info),
                                "size": info.st_size})
        assert identity(os.fstat(fd)) == before, "host-state directory identity changed"
        return {"identity": before, "entries": sorted(entries, key=lambda row: row["name"])}
    finally:
        os.close(fd)
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
    "host_directory": host_directory_receipt(),
    "root_user_state": [str(path) for path in
        (Path("/root/.cache/mvmctl"), Path("/root/.config/mvmctl")) if path.exists()]}))
"""
_SYSCTL_MOUNT_PROBE = r"""
import json, os, stat
from pathlib import Path

target = "/etc/sysctl.d"
directory = os.stat(target, follow_symlinks=False)
assert stat.S_ISDIR(directory.st_mode)
records = []
options = {}
for line in Path("/proc/self/mountinfo").read_text().splitlines():
    fields = line.split()
    separator = fields.index("-")
    records.append(fields[:5] + fields[6:separator + 3])
    options[fields[0]] = fields[5].split(",")
matching = [row for row in records if row[4] == target]
assert len(matching) <= 1, matching
source = max((row for row in records if row[4] != target
              and Path(target).is_relative_to(row[4])), key=lambda row: len(row[4]))
print(json.dumps({"directory": [directory.st_dev, directory.st_ino,
    directory.st_uid, directory.st_gid, stat.S_IMODE(directory.st_mode)],
    "source": source, "ids": sorted(row[0] for row in records),
    "target": matching[0] if matching else None,
    "options": options[matching[0][0]] if matching else [],
    "temporary": sorted(str(path) for parent in
        (Path("/etc/sysctl.d"), Path("/var/lib/mvmctl/host")) if parent.exists()
        for path in parent.iterdir() if path.name.startswith((".host-", ".admission-")))}))
"""


def _root_file_data(state, name):
    receipt = state["files"][name]
    assert receipt is not None, (name, state)
    assert receipt["identity"][2:6] == [0, 0, 0o600, 1], receipt
    return json.loads(receipt["content"])


def _require_error(result, code=None):
    assert result.returncode != 0, result.stdout + result.stderr
    envelope = json.loads(result.stdout)
    assert envelope["schema_version"] == 1 and envelope["status"] == "error"
    assert isinstance(envelope["error"]["code"], str) and envelope["error"]["code"]
    if code is not None:
        assert envelope["error"]["code"] == code, envelope
    return envelope["error"]


class _SysctlReadOnlyFault:
    """Own one child-only bind mount, preserving uncertain identity on failure."""

    def __init__(self, case):
        self.case = case
        self.before = None
        self.bound = None
        self.attempted = False

    def probe(self):
        return json.loads(
            self.case.root(shlex.join(["python3", "-c", _SYSCTL_MOUNT_PROBE])).stdout
        )

    def admit(self):
        current = self.probe()
        assert current["directory"] == self.before["directory"], current
        assert current["source"] == self.before["source"], current
        bound = current["target"]
        if bound is None:
            assert self.bound is None, "owned bind disappeared"
            return
        source = self.before["source"]
        expected_root = os.path.normpath(
            os.path.join(source[3], os.path.relpath("/etc/sysctl.d", source[4]))
        )
        assert bound[0] not in self.before["ids"], current
        assert bound[1:5] == [source[0], source[2], expected_root, "/etc/sysctl.d"]
        assert bound[-2:] == source[-2:], current
        assert self.bound is None or bound == self.bound, current
        self.bound = bound
        return current

    def __enter__(self):
        self.before = self.probe()
        assert self.before["target"] is None, self.before
        assert self.before["directory"][2:4] == [0, 0], self.before
        assert self.before["directory"][4] & 0o022 == 0, self.before
        self.case.fault_receipts.append({"before": self.before})
        self.attempted = True
        try:
            try:
                created = self.case.root(
                    "mount --bind /etc/sysctl.d /etc/sysctl.d", check=False
                )
            finally:
                self.admit()
            assert created.returncode == 0 and self.bound is not None, created.stderr
            self.case.root("mount -o remount,bind,ro /etc/sysctl.d")
            current = self.admit()
            assert "ro" in current["options"], current
            self.case.fault_receipts[-1]["bound"] = current
            return self
        except BaseException:
            self.restore()
            raise

    def restore(self):
        failures = []
        try:
            self.admit()
        except Exception as error:
            failures.append(f"admit attempted sysctl bind: {error!r}")
        if not failures and self.bound is not None:
            for command in (
                "mount -o remount,bind,rw /etc/sysctl.d",
                "umount /etc/sysctl.d",
            ):
                try:
                    self.admit()
                    self.case.root(command)
                except Exception as error:
                    failures.append(f"restore owned sysctl bind: {error!r}")
                    break
        try:
            final = self.probe()
            assert final["directory"] == self.before["directory"]
            assert final["source"] == self.before["source"]
            assert final["target"] is None, final
            assert final["ids"] == self.before["ids"], final
            assert final["temporary"] == [], final
            self.case.fault_receipts[-1]["restored"] = final
        except Exception as error:
            failures.append(f"sysctl bind restoration unproven: {error!r}")
        self.case.fault_receipts[-1]["cleanup_errors"] = failures
        assert not failures, "\n".join(failures)

    def __exit__(self, *_):
        self.restore()


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
        self.second_states = []
        self.fault_receipts = []
        self.enrollment_policy_receipts = []
        self.quiescent_reset = None

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

    def administrator(self, action, *, check=True):
        args = [SYSTEM_MVM_BINARY, "host", action, "--operator", _OPERATOR]
        if action == "reset":
            args.append("--force")
        return self.root(shlex.join([*args, "--json"]), check=check, timeout=180)

    def prepare_second_operator(self):
        self.root(
            "set -eu; useradd --create-home --user-group --shell /bin/sh hostqa2; "
            "passwd --lock hostqa2"
        )
        return self.second_state()

    def second_state(self):
        probe = _STATE_PROBE.replace(
            'pwd.getpwnam("hostqa")', 'pwd.getpwnam("hostqa2")'
        )
        state = json.loads(self.root(shlex.join(["python3", "-c", probe])).stdout)
        assert state["files"]["canonical"]["sha256"] == self.canonical[-1]
        assert state["root_user_state"] == [], state
        self.second_states.append(state)
        return state

    def enroll_second_from_root(self, *, check=True):
        return self.root(
            shlex.join(
                [
                    SYSTEM_MVM_BINARY,
                    "host",
                    "init",
                    "--operator",
                    _SECOND_OPERATOR,
                    "--json",
                ]
            ),
            check=check,
            timeout=180,
        )

    def enroll_second_with_password(self):
        # This independent administrator grant is removed before marker-only/reset claims.
        password = f"HostQA-{uuid.uuid4().hex}"
        content = "hostqa2 ALL=(root) PASSWD: /usr/local/bin/mvm host init --json\n"
        publish = r"""
import os, sys
fd = os.open(sys.argv[1], os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o440)
with os.fdopen(fd, "w") as stream:
    os.fchown(stream.fileno(), 0, 0)
    os.fchmod(stream.fileno(), 0o440)
    stream.write(sys.argv[2])
    stream.flush()
    os.fsync(stream.fileno())
"""
        receipt = None
        attempted = False
        try:
            self.root(
                shlex.join(["printf", "%s\\n", f"hostqa2:{password}"]) + " | chpasswd"
            )
            attempted = True
            self.root(
                shlex.join(["python3", "-c", publish, _ENROLLMENT_POLICY, content])
            )
            policy_probe = r"""
import hashlib, json, os, stat, sys
fd = os.open(sys.argv[1], os.O_RDONLY | os.O_NOFOLLOW)
with os.fdopen(fd, "rb") as stream:
    info = os.fstat(stream.fileno())
    data = stream.read()
    assert stat.S_ISREG(info.st_mode) and info.st_nlink == 1
    assert (info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode)) == (0, 0, 0o440)
    assert data.decode() == sys.argv[2]
    current = os.stat(sys.argv[1], follow_symlinks=False)
    assert (current.st_dev, current.st_ino, current.st_size, current.st_mtime_ns,
            current.st_ctime_ns) == (info.st_dev, info.st_ino, info.st_size,
                                    info.st_mtime_ns, info.st_ctime_ns)
    print(json.dumps([info.st_dev, info.st_ino, hashlib.sha256(data).hexdigest()]))
"""
            receipt = json.loads(
                self.root(
                    shlex.join(
                        ["python3", "-c", policy_probe, _ENROLLMENT_POLICY, content]
                    )
                ).stdout
            )
            self.enrollment_policy_receipts.append({"identity": receipt})
            self.root(shlex.join(["visudo", "-c", "-f", _ENROLLMENT_POLICY]))
            before = self.second_state()
            command = "sudo -k; LC_ALL=C sudo -n /usr/local/bin/mvm host init --json"
            denied = _run_mvm(
                self.runner_vm,
                "exec",
                self.child_id,
                "--user",
                _SECOND_OPERATOR,
                "--timeout",
                "10",
                "--",
                command,
                check=False,
            )
            assert denied.returncode != 0 and "sudo:" in denied.stderr, denied
            assert self.second_state() == before
            command = (
                shlex.join(["printf", "%s\\n", password])
                + " | LC_ALL=C sudo -S -p '' /usr/local/bin/mvm host init --json"
            )
            result = _run_mvm(
                self.runner_vm,
                "exec",
                self.child_id,
                "--user",
                _SECOND_OPERATOR,
                "--timeout",
                "10",
                "--",
                command,
                timeout=180,
            )
            return before, _success_data(result)
        finally:
            if receipt is not None:
                remove = r"""
import hashlib, json, os, stat, sys
expected = json.loads(sys.argv[2])
fd = os.open(sys.argv[1], os.O_RDONLY | os.O_NOFOLLOW)
with os.fdopen(fd, "rb") as stream:
    info = os.fstat(stream.fileno())
    assert stat.S_ISREG(info.st_mode) and info.st_nlink == 1
    assert (info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode)) == (0, 0, 0o440)
    assert [info.st_dev, info.st_ino, hashlib.file_digest(stream, "sha256").hexdigest()] == expected
    current = os.stat(sys.argv[1], follow_symlinks=False)
    assert (current.st_dev, current.st_ino) == (info.st_dev, info.st_ino)
    os.unlink(sys.argv[1])
assert not os.path.lexists(sys.argv[1])
"""
                self.root(
                    shlex.join(
                        [
                            "python3",
                            "-c",
                            remove,
                            _ENROLLMENT_POLICY,
                            json.dumps(receipt),
                        ]
                    )
                )
                self.enrollment_policy_receipts[-1]["removed"] = True
            elif attempted:
                self.root(
                    "set -eu; test ! -e /etc/sudoers.d/mvmqa-host-init; "
                    "test ! -L /etc/sudoers.d/mvmqa-host-init"
                )
            self.root("passwd --lock hostqa2")

    def initialize_operator(self):
        initialized = self.operator(
            "init",
            "--non-interactive",
            "--skip-host",
            "--skip-network",
            "--binary-version",
            "1.16.2",
            "--debug",
            timeout=180,
        )
        combined = initialized.stdout + initialized.stderr
        assert "Using local mirror for trusted release" in combined, combined
        assert "Downloading trusted release" not in combined, combined

    def second_operator(self, *args, check=True, timeout=60):
        assert self.child_id, "no owned child second-operator channel"
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
            _SECOND_OPERATOR,
            "--timeout",
            "10",
            "--",
            command,
            check=check,
            timeout=timeout,
        )

    def second_initialize_and_network_control(self):
        """Exercise real marker-only dispatch with freshly resolved second-user groups."""
        first_state = self.state()
        first_rows = _json_success_data(
            self.operator("network", "ls", "--json").stdout, list
        )
        initialized = self.second_operator(
            "init",
            "--non-interactive",
            "--skip-host",
            "--skip-network",
            "--binary-version",
            "1.16.2",
            "--debug",
            timeout=180,
        )
        assert "all set" in (initialized.stdout + initialized.stderr).lower()
        assert (
            "Downloading trusted release" not in initialized.stdout + initialized.stderr
        )
        assert (
            _json_success_data(
                self.second_operator("network", "ls", "--json").stdout, list
            )
            == []
        )
        name = f"sys-admin-op2-{uuid.uuid4().hex[:16]}"
        subnet = _unique_subnet(name)
        created = _success_data(
            self.second_operator(
                "network", "create", name, "--subnet", subnet, "--no-nat", "--json"
            )
        )
        self.child_networks.append(created)
        assert created["name"] == name and created["subnet"] == subnet, created
        assert created["nat_enabled"] is False and created["is_default"] is False
        rows = _json_success_data(
            self.second_operator("network", "ls", "--json").stdout, list
        )
        assert len(rows) == 1 and rows[0]["id"] == created["id"], rows
        for field in ("id", "name", "created_at", "subnet", "bridge"):
            assert rows[0][field] == created[field], (field, rows)
        links = json.loads(self.root("ip -j -d link show").stdout)
        matching = [row for row in links if row["ifname"] == created["bridge"]]
        assert len(matching) == 1, matching
        assert matching[0]["linkinfo"]["info_kind"] == "bridge", matching
        assert matching[0]["ifindex"] > 0 and matching[0].get("ifalias"), matching
        assert created["bridge"] in self.root("nft -j list ruleset").stdout
        assert self.state() == first_state
        assert _retained_rows(
            "network",
            _json_success_data(self.operator("network", "ls", "--json").stdout, list),
        ) == _retained_rows("network", first_rows)
        _json_success_data(
            self.second_operator("network", "rm", created["id"], "--json").stdout,
            type(None),
        )
        assert (
            _json_success_data(
                self.second_operator("network", "ls", "--json").stdout, list
            )
            == []
        )
        links = json.loads(self.root("ip -j -d link show").stdout)
        assert not any(row["ifname"] == created["bridge"] for row in links), links
        assert created["bridge"] not in self.root("nft -j list ruleset").stdout

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
        name = f"sys-admin-ctl-{uuid.uuid4().hex[:16]}"
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
                        "second_states": self.second_states,
                        "sysctl_faults": self.fault_receipts,
                        "enrollment_policy": self.enrollment_policy_receipts,
                        "quiescent_reset": self.quiescent_reset,
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
    def test_second_operator_authenticated_enrollment_and_active_reset_refusal(
        self, clean_host_child
    ):
        case = clean_host_child
        original = case.prepare()
        enrolled = case.require_enrolled(original)
        second_original = case.prepare_second_operator()
        assert second_original["files"]["enrollment"] is None
        assert second_original["groups"] == [second_original["gid"]]
        before, result = case.enroll_second_with_password()
        assert result["user_added_to_group"] is True, result
        assert len(result["changes"]) == 1, result
        assert result["changes"][0]["setting"] == "group_member:hostqa2", result
        second = case.second_state()
        both = case.state()
        assert both["group"]["members"] == [_OPERATOR, _SECOND_OPERATOR]
        assert second["groups"] == sorted(
            [second_original["gid"], both["group"]["gid"]]
        )
        for name in ("canonical", "sudoers", "sysctl", "journal", "enrollment"):
            assert both["files"][name] == enrolled["files"][name], name
        assert _root_file_data(both, "admission") == _root_file_data(
            enrolled, "admission"
        )
        assert before["files"]["enrollment"] is None
        record = _root_file_data(second, "enrollment")
        assert record == {
            "version": 1,
            "operator": _SECOND_OPERATOR,
            "uid": str(second["uid"]),
            "gid": str(second["gid"]),
            "member_existed": False,
            "completed": True,
        }, record
        repeated = _success_data(case.enroll_second_from_root())
        assert repeated["user_added_to_group"] is False and repeated["changes"] == []
        repeated_first, repeated_second = case.state(), case.second_state()
        for actual, expected in ((repeated_first, both), (repeated_second, second)):
            assert {key: value for key, value in actual.items() if key != "files"} == {
                key: value for key, value in expected.items() if key != "files"
            }
            assert {
                key: value
                for key, value in actual["files"].items()
                if key != "admission"
            } == {
                key: value
                for key, value in expected["files"].items()
                if key != "admission"
            }
            assert _root_file_data(actual, "admission") == _root_file_data(
                expected, "admission"
            )
        grants = case.root("LC_ALL=C sudo -n -l -U hostqa2").stdout
        assert [
            line.strip() for line in grants.splitlines() if line.strip().startswith("(")
        ] == ["(root) NOPASSWD: /usr/local/bin/mvm __mvm_privileged_v1 *"], grants
        case.initialize_operator()
        before_second_control = case.state()
        first_inventory = _json_success_data(
            case.operator("network", "ls", "--json").stdout, list
        )
        case.second_initialize_and_network_control()
        assert case.state() == before_second_control
        assert _retained_rows(
            "network",
            _json_success_data(case.operator("network", "ls", "--json").stdout, list),
        ) == _retained_rows("network", first_inventory)
        active = case.network_control()
        before_reset = case.state()
        refused = case.administrator("reset", check=False)
        error = _require_error(refused, "host.reset.failed")
        assert error["details"]["journal_retained"] is True, error
        stage = error["details"]["reset_failure_stage"]
        cause = error["details"]["reset_failure_cause"]
        assert stage == "acquire network and firewall reset guard", error
        assert "network.error" in cause, error
        assert "host reset requires all managed networks to be removed" in cause, error
        assert 0 < len(cause.encode("utf-8")) <= 512 and cause.isprintable(), error
        assert stage in error["message"] and cause in error["message"], error
        assert case.state() == before_reset
        assert _root_file_data(before_reset, "admission")["enabled"] is True
        case.require_network(active, present=True)
        case.remove_child_network(active)
        _json_success_data(case.administrator("reset").stdout, type(None))
        restored = case.state()
        second_restored = case.second_state()
        assert restored["group"] is None and second_restored["group"] is None
        assert restored["groups"] == original["groups"]
        assert second_restored["groups"] == second_original["groups"]
        assert restored["forward"] == original["forward"]
        for name in ("canonical", "sudoers", "sysctl"):
            assert restored["files"][name] == original["files"][name], name
        assert _root_file_data(restored, "enrollment")["reverted"] is True
        assert _root_file_data(second_restored, "enrollment")["reverted"] is True
        assert _root_file_data(restored, "journal")["reset_state"] == "complete"
        assert _root_file_data(restored, "admission") == {
            "version": 1,
            "enabled": False,
        }

    def test_filesystem_failure_preserves_init_and_reset_retry_authority(
        self, clean_host_child
    ):
        # A real EROFS failure proves partial-transaction retry, not SIGKILL/power loss.
        case = clean_host_child
        original = case.prepare()
        case.prepare_second_operator()
        with _SysctlReadOnlyFault(case):
            failed = case.administrator("init", check=False)
            _require_error(failed, "host.init.failed")
            assert "read-only file system" in (failed.stdout + failed.stderr).lower()
            partial = case.state()
            journal = _root_file_data(partial, "journal")
            assert journal["completed"] == 3 and journal["pending"] is True, journal
            assert journal["initialized"] is False and journal["operator"] == _OPERATOR
            assert journal["original_forward"] == original["forward"]
            assert (
                journal["original_sysctl"] is None
                and journal["original_sudoers"] is None
            )
            assert partial["forward"] == "1" and partial["group"]["members"] == [
                _OPERATOR
            ]
            assert (
                partial["files"]["sysctl"] is None
                and partial["files"]["sudoers"] is None
            )
            assert partial["files"]["enrollment"] is None
            assert _root_file_data(partial, "admission") == {
                "version": 1,
                "enabled": False,
            }
            competing = case.enroll_second_from_root(check=False)
            _require_error(competing, "host.init.failed")
            assert "original operator" in competing.stdout + competing.stderr
            _require_error(
                case.administrator("reset", check=False), "host.reset.failed"
            )
            assert case.state() == partial
            assert case.second_state()["files"]["enrollment"] is None
        case.require_enrolled(original)
        case.initialize_operator()
        control = case.network_control()
        case.remove_child_network(control)
        initialized = case.state()
        with _SysctlReadOnlyFault(case):
            failed = case.administrator("reset", check=False)
            error = _require_error(failed, "host.reset.failed")
            assert error["details"]["journal_retained"] is True, error
            stage = error["details"]["reset_failure_stage"]
            cause = error["details"]["reset_failure_cause"]
            assert stage == "restore sysctl", error
            assert cause == "read-only file system", error
            assert 0 < len(cause.encode("utf-8")) <= 512 and cause.isprintable(), error
            assert stage in error["message"] and cause in error["message"], error
            assert "read-only file system" in (failed.stdout + failed.stderr).lower()
            partial = case.state()
            journal = _root_file_data(partial, "journal")
            assert (
                journal["reset_state"] == "restoring"
                and journal.get("reset_step", 0) == 0
            )
            assert journal["initialized"] is True and journal["pending"] is False
            assert partial["forward"] == original["forward"]
            assert partial["group"] == initialized["group"]
            for name in ("canonical", "sysctl", "sudoers", "enrollment"):
                assert partial["files"][name] == initialized["files"][name], name
            assert _root_file_data(partial, "admission") == {
                "version": 1,
                "enabled": False,
            }
            _require_error(case.administrator("init", check=False), "host.init.failed")
            name = f"sys-admin-off-{uuid.uuid4().hex[:16]}"
            _require_error(
                case.operator(
                    "network",
                    "create",
                    name,
                    "--subnet",
                    _unique_subnet(name),
                    "--no-nat",
                    "--json",
                    check=False,
                )
            )
            assert case.state() == partial
            assert (
                _json_success_data(
                    case.operator("network", "ls", "--json").stdout, list
                )
                == []
            )
            case.require_network(control, present=False)
        _json_success_data(case.administrator("reset").stdout, type(None))
        restored = case.state()
        assert restored["group"] is None and restored["groups"] == original["groups"]
        assert restored["forward"] == original["forward"]
        for name in ("canonical", "sysctl", "sudoers"):
            assert restored["files"][name] == original["files"][name], name
        journal = _root_file_data(restored, "journal")
        assert journal["reset_state"] == "complete" and journal["reset_step"] == 4
        assert journal["initialized"] is False
        assert _root_file_data(restored, "enrollment")["reverted"] is True
        assert _root_file_data(restored, "admission") == {
            "version": 1,
            "enabled": False,
        }
        case.require_enrolled(original)
        final = case.network_control()
        case.remove_child_network(final)

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
        reset = case.administrator("reset", check=False)
        case.quiescent_reset = {
            "returncode": reset.returncode,
            "stdout": reset.stdout,
            "stderr": reset.stderr,
            "state": None,
            "capture_error": None,
        }
        try:
            restored = case.state()
            case.quiescent_reset["state"] = restored
        except Exception as error:
            case.quiescent_reset["capture_error"] = repr(error)
        # Preserve a failed reset as primary even if its state observation also fails.
        assert reset.returncode == 0, reset.stdout + reset.stderr
        assert case.quiescent_reset["capture_error"] is None, (
            "Quiescent reset state capture unproven: "
            + case.quiescent_reset["capture_error"]
        )
        _json_success_data(reset.stdout, type(None))
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
        name = f"sys-admin-off-{uuid.uuid4().hex[:16]}"
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
