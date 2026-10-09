"""Installed-candidate VM journeys with function-owned resources and checked cleanup."""

from __future__ import annotations

import base64
import json
import os
import re
import select
import stat
import subprocess
import time
import uuid
from contextlib import contextmanager
from pathlib import Path
from typing import Generator, Literal

import pytest

from tests.system.conftest import (
    _json_success_data,
    _parse_vm_list,
    _run_mvm,
    _unique_subnet,
    wait_for_ssh,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
]


def _kill_exact_test_firecracker(
    pid: int,
    start_ticks: int,
    vm_id: str,
    *,
    expected_placement: Literal["managed", "networkless"] = "managed",
) -> None:
    """Runner-admin fault injection, admitted from schema-6 authority before signaling."""
    qualification_host = os.environ.get("MVM_TEST_QUALIFICATION_HOST")
    assert (
        qualification_host and qualification_host == os.environ.get("MVM_TEST_VM")
    ), "administrator fault requires explicit exact qualification-host admission"
    assert re.fullmatch(r"[0-9a-f]{32}", vm_id) and pid > 0 and start_ticks > 0
    assert expected_placement in ("managed", "networkless")
    script = r"""
import hashlib
import json
import os
import re
import select
import signal
import stat
import sys

pid, ticks, vm_id, owner = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3], int(sys.argv[4])
expected_placement = sys.argv[5]
assert expected_placement in ("managed", "networkless")
assert os.geteuid() == 0 and int(os.environ["SUDO_UID"]) == owner and owner > 0
assert re.fullmatch(r"[0-9a-f]{32}", vm_id) and pid > 0 and ticks > 0
fds = []
def keep(fd):
    fds.append(fd)
    return fd

def unique_object(pairs):
    result = {}
    for key, value in pairs:
        assert key not in result, "duplicate authority field"
        result[key] = value
    return result

def directory(parent, name, private=False):
    fd = keep(os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=parent))
    info = os.fstat(fd)
    assert stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and not info.st_mode & 0o022
    if private:
        assert info.st_gid == 0 and stat.S_IMODE(info.st_mode) == 0o700
    return fd

def identity(info):
    return info.st_dev, info.st_ino

def bounded(fd, maximum):
    os.lseek(fd, 0, os.SEEK_SET)
    chunks, size = [], 0
    while True:
        chunk = os.read(fd, min(65536, maximum + 1 - size))
        if not chunk:
            break
        chunks.append(chunk)
        size += len(chunk)
        assert size <= maximum, "oversized fixed authority"
    return b"".join(chunks)

def record_bytes(fd):
    info = os.fstat(fd)
    assert stat.S_ISREG(info.st_mode) and info.st_uid == info.st_gid == 0
    assert stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1 and 0 < info.st_size <= 16384
    return info, bounded(fd, 16384)

def alive(fd):
    poller = select.poll()
    poller.register(fd, select.POLLIN)
    assert poller.poll(0) == [], "admitted process has already exited"

try:
    pidfd = keep(os.pidfd_open(pid))
    alive(pidfd)
    root = directory(None, "/")
    var = directory(root, "var")
    lib = directory(var, "lib")
    state = directory(lib, "mvmctl")
    instances = directory(state, "instances", True)
    owner_dir = directory(instances, str(owner), True)
    basename = vm_id + ".json"
    record_fd = keep(os.open(basename, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
                             dir_fd=owner_dir))
    record_info, raw = record_bytes(record_fd)
    record = json.loads(raw, object_pairs_hook=unique_object)
    assert set(record) == {"schema_version", "owner_uid", "vm_id", "network_placement",
                           "lifecycle", "launch_generation", "cleanup_generation", "launch", "active"}
    assert type(record["schema_version"]) is int and record["schema_version"] == 6
    assert type(record["owner_uid"]) is int and record["owner_uid"] == owner
    assert record["vm_id"] == vm_id and record["lifecycle"] == "active"
    assert record["network_placement"] == expected_placement
    generation, cleanup = record["launch_generation"], record["cleanup_generation"]
    assert type(generation) is int and type(cleanup) is int and generation > cleanup >= 0
    launch, active = record["launch"], record["active"]
    assert launch["pid"] == pid and launch["start_time_ticks"] == ticks
    assert launch["host_boot_id"] == open("/proc/sys/kernel/random/boot_id", encoding="ascii").read().strip()
    assert launch["network_placement"] == record["network_placement"]
    execution = launch["execution_identity"]
    assert type(execution["uid"]) is int and execution["uid"] not in (0, owner)
    assert type(execution["gid"]) is int and execution["gid"] > 0
    assert launch["release"]["version"] == "1.16.2" and launch["release"]["architecture"] == "x86_64"
    digest = launch["release"]["firecracker_sha256"]
    assert re.fullmatch(r"[0-9a-f]{64}", digest)
    proc_fd = keep(os.open(f"/proc/{pid}", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW))
    def process_identity():
        alive(pidfd)
        stat_fd = keep(os.open("stat", os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=proc_fd))
        fields = bounded(stat_fd, 16384).decode("ascii").rpartition(") ")[2].split()
        assert len(fields) >= 20 and int(fields[19]) == ticks and fields[0] not in ("Z", "X")
        status_fd = keep(os.open("status", os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=proc_fd))
        lines = bounded(status_fd, 65536).decode("ascii").splitlines()
        for name, section, expected in (("Uid:", "user_ids", execution["uid"]),
                                        ("Gid:", "group_ids", execution["gid"])):
            values = [line.split()[1:] for line in lines if line.startswith(name)]
            assert values == [[str(expected)] * 4]
            assert active[section] == dict.fromkeys(("real", "effective", "saved", "filesystem"), expected)
        assert [line.split()[1:] for line in lines if line.startswith("Groups:")] == [[]]
        assert active["supplementary_groups"] == []
        for path, section in (("root", "jail_root"), ("ns/mnt", "mount_namespace")):
            info = os.stat(path, dir_fd=proc_fd)
            assert identity(info) == (active[section]["device"], active[section]["inode"])
        cgroup_fd = keep(os.open("cgroup", os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=proc_fd))
        assert bounded(cgroup_fd, 16384).decode("ascii") == f"0::/mvmctl/{vm_id}\n"
        # /proc's root/exe/ns links are kernel-provided observations, never caller paths.
        exe_fd = keep(os.open("exe", os.O_RDONLY | os.O_CLOEXEC, dir_fd=proc_fd))
        exe = os.fstat(exe_fd)
        expected_exe = active["executable"]
        observed = {"device": exe.st_dev, "inode": exe.st_ino, "size_bytes": exe.st_size,
                    "mode": exe.st_mode, "uid": exe.st_uid, "gid": exe.st_gid, "links": exe.st_nlink}
        assert {key: expected_exe[key] for key in observed} == observed
        assert expected_exe["manifest_sha256"] == digest
        assert hashlib.sha256(bounded(exe_fd, 64 * 1024 * 1024)).hexdigest() == digest
        alive(pidfd)

    process_identity()
    # Re-admit the same root record immediately before the signal; no journal repair or lock creation.
    current_fd = keep(os.open(basename, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
                              dir_fd=owner_dir))
    current_info, current_raw = record_bytes(current_fd)
    assert identity(current_info) == identity(record_info) and current_raw == raw
    process_identity()
    assert launch["host_boot_id"] == open("/proc/sys/kernel/random/boot_id", encoding="ascii").read().strip()
    signal.pidfd_send_signal(pidfd, signal.SIGKILL)
    poller = select.poll()
    poller.register(pidfd, select.POLLIN)
    events = poller.poll(10_000)
    assert len(events) == 1 and events[0][0] == pidfd
    assert not events[0][1] & (select.POLLERR | select.POLLNVAL)
    assert events[0][1] & (select.POLLIN | select.POLLHUP)
    print(json.dumps({"schema_version": 6, "owner_uid": owner, "vm_id": vm_id,
                      "network_placement": record["network_placement"],
                      "launch_generation": generation, "host_boot_id": launch["host_boot_id"],
                      "pid": pid, "start_time_ticks": ticks, "exited": True}))
finally:
    for fd in reversed(fds):
        os.close(fd)
"""
    result = subprocess.run(
        [
            "/usr/bin/sudo",
            "-n",
            "/usr/bin/python3",
            "-I",
            "-u",
            "-c",
            script,
            str(pid),
            str(start_ticks),
            vm_id,
            str(os.getuid()),
            expected_placement,
        ],
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    assert result.returncode == 0, (
        "runner administrator crash admission failed",
        result.stdout,
        result.stderr,
    )
    receipt = json.loads(result.stdout)
    assert receipt["schema_version"] == 6 and receipt["owner_uid"] == os.getuid()
    assert receipt["network_placement"] == expected_placement
    assert (
        receipt["vm_id"] == vm_id
        and receipt["pid"] == pid
        and receipt["start_time_ticks"] == start_ticks
    )
    assert receipt["launch_generation"] > 0 and receipt["exited"] is True


# ========================================================================
# TestVMListEmpty — MUST run before any VM is created
# ========================================================================


class TestVMListEmpty:
    """Test vm ls behavior when no VMs exist — runs before any VM creation."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    def test_list_empty(self, runner_vm):
        """A clean runner returns a versioned empty list without deleting any VM."""
        assert _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == []


# ========================================================================
# TestVMAdvancedCreateFlags
# ========================================================================


# Every launch consumes the preseeded assets; this module never pulls or changes defaults.
_REMAINING_VM_ARGS = (
    "--image",
    "alpine:3.23",
    "--disk-size",
    "512M",
    "--mem",
    "256M",
    "--vcpu",
    "1",
    "--skip-deblob",
    "--no-pci",
)
_VM_OWNERS: dict[str, "_LifecycleOwner"] = {}


def _lifecycle_inventory(runner_vm: str, domain: str) -> list:
    rows = _json_success_data(_run_mvm(runner_vm, domain, "ls", "--json").stdout, list)
    # Kernel listing may refresh this observational timestamp.
    if domain == "kernel":
        rows = [
            {key: value for key, value in row.items() if key != "updated_at"}
            for row in rows
        ]
    return sorted(rows, key=lambda row: json.dumps(row, sort_keys=True))


def _require_lifecycle_assets(runner_vm: str) -> None:
    assert os.geteuid() != 0, "the installed public CLI must run as the runner operator"
    mirror = os.environ.get("MVM_ASSET_MIRROR")
    assert mirror and Path(mirror).expanduser().is_dir(), (
        "preseeded read-only mirror required"
    )
    images = [
        row
        for row in _lifecycle_inventory(runner_vm, "image")
        if (row["type"], row["version"]) == ("alpine", "3.23") and row["is_present"]
    ]
    assert len(images) == 1 and 0 < images[0]["minimum_rootfs_size_mib"] <= 512, images
    kernels = [
        row for row in _lifecycle_inventory(runner_vm, "kernel") if row["is_default"]
    ]
    assert len(kernels) == 1 and kernels[0]["is_present"], kernels
    binaries = _lifecycle_inventory(runner_vm, "bin")
    for kind in ("firecracker", "jailer"):
        selected = [
            row for row in binaries if row["type"] == kind and row["is_default"]
        ]
        assert (
            len(selected) == 1
            and selected[0]["is_present"]
            and selected[0]["version"] == "1.16.2"
        ), selected
    settings = _json_success_data(
        _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
    )
    guestfs = settings["settings"]["guestfs_enabled"]
    assert (
        guestfs["override"] if guestfs["override"] is not None else guestfs["default"]
    ) is False, settings
    status = _json_success_data(
        _run_mvm(runner_vm, "host", "status", "--json").stdout, dict
    )
    assert status["resources"]["nftables_available"] is True, status


class _LifecycleOwner:
    """Admit only this function's attempted names and retain exact generations."""

    def __init__(self, runner_vm: str):
        self.runner_vm = runner_vm
        self.baseline = {
            domain: _lifecycle_inventory(runner_vm, domain)
            for domain in (
                "vm",
                "network",
                "volume",
                "key",
                "snapshot",
                "image",
                "kernel",
                "bin",
            )
        }
        self.settings = _json_success_data(
            _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
        )
        self.attempts: dict[str, dict[str, tuple | None]] = {
            domain: {} for domain in ("vm", "network", "volume", "key")
        }
        self.retired: dict[str, list[tuple]] = {}
        self.key_files: dict[str, dict[Path, tuple[int, int]]] = {}
        assert self.baseline["vm"] == [], (
            "lifecycle gate requires an empty VM inventory; none are removed"
        )

    def _vm_rows(self) -> list:
        return _lifecycle_inventory(self.runner_vm, "vm")

    def _network_rows(self) -> list:
        return _lifecycle_inventory(self.runner_vm, "network")

    def _volume_rows(self) -> list:
        return _lifecycle_inventory(self.runner_vm, "volume")

    def _key_rows(self) -> list:
        return _lifecycle_inventory(self.runner_vm, "key")

    @staticmethod
    def _identity(domain: str, row: dict) -> tuple:
        resource_id = row["id"]
        assert isinstance(resource_id, str), row
        fields = ("id", "name", "created_at")
        if domain in ("vm", "network", "volume"):
            length = 32 if domain == "vm" else 64
            assert re.fullmatch(rf"[0-9a-f]{{{length}}}", resource_id), row
        elif domain == "key":
            assert resource_id == row["fingerprint"], row
            assert re.fullmatch(r"SHA256:[A-Za-z0-9+/]{43}", resource_id), row
            digest = base64.b64decode(resource_id[7:] + "=", validate=True)
            assert (
                len(digest) == 32
                and base64.b64encode(digest).decode().rstrip("=") == resource_id[7:]
            ), row
            fields += (
                "fingerprint",
                "algorithm",
                "public_key_path",
                "private_key_path",
            )
        else:
            raise AssertionError(domain)
        if domain == "network":
            fields += ("subnet", "bridge", "ipv4_gateway", "nat_enabled")
        elif domain == "volume":
            fields += (
                "format",
                "path",
                "size_bytes",
                "is_read_only",
                "is_shareable",
                "cache_type",
            )
        assert isinstance(row["created_at"], str) and row["created_at"], row
        return tuple(row[key] for key in fields)

    def _before(self, domain: str, names: tuple, rows: list) -> None:
        for name in names:
            assert all(row["name"] != name for row in self.baseline[domain] + rows), (
                domain,
                name,
                "collision",
            )
            if name in self.attempts[domain]:
                # Only a removed VM may be replaced (cold snapshot restore).
                prior = self.attempts[domain][name]
                assert domain == "vm" and prior is not None, (
                    domain,
                    name,
                    "unresolved repeated attempt",
                )
                assert all(row["id"] != prior[0] for row in rows), (name, prior)
                self.retired.setdefault(name, []).append(prior)
            self.attempts[domain][name] = None
            if domain == "vm":
                assert name not in _VM_OWNERS or _VM_OWNERS[name] is self
                _VM_OWNERS[name] = self

    def before_vm_create(self, *names: str) -> None:
        self._before("vm", names, self._vm_rows())

    def before_network_create(self, name: str) -> None:
        self._before("network", (name,), self._network_rows())

    def before_volume_create(self, name: str) -> None:
        self._before("volume", (name,), self._volume_rows())

    def before_key_create(self, name: str) -> None:
        self._before("key", (name,), self._key_rows())

    def _remember(self, domain: str, names: tuple, first: list, second: list) -> None:
        for name in names:
            assert name in self.attempts[domain], (
                domain,
                name,
                "unattempted publication",
            )
            matches = [row for row in first if row["name"] == name]
            current = [row for row in second if row["name"] == name]
            assert len(matches) == len(current) == 1, (domain, name, first, second)
            identity = self._identity(domain, matches[0])
            assert all(row["id"] != identity[0] for row in self.baseline[domain]), (
                domain,
                identity,
                "baseline ID",
            )
            assert self._identity(domain, current[0]) == identity, (
                domain,
                name,
                "generation changed",
            )
            retained = self.attempts[domain][name]
            assert retained is None or retained == identity, (
                domain,
                name,
                "receipt changed",
            )
            if domain == "vm":
                assert identity[0] not in {
                    item[0] for item in self.retired.get(name, [])
                }, (name, identity)
            self.attempts[domain][name] = identity
            if domain == "key":
                files = {}
                baseline_paths = {
                    row[key]
                    for row in self.baseline["key"]
                    for key in ("public_key_path", "private_key_path")
                }
                for key in ("public_key_path", "private_key_path"):
                    raw = matches[0][key]
                    assert isinstance(raw, str) and raw and raw not in baseline_paths, (
                        matches[0]
                    )
                    path = Path(raw)
                    metadata = path.lstat()
                    assert (
                        stat.S_ISREG(metadata.st_mode)
                        and metadata.st_uid == os.geteuid()
                    ), path
                    files[path] = metadata.st_dev, metadata.st_ino
                assert len(files) == 2 and self.key_files.get(name, files) == files, (
                    files
                )
                self.key_files[name] = files

    def vm_created(self, *names: str, published: list | None = None) -> None:
        self._remember(
            "vm",
            names,
            published if published is not None else self._vm_rows(),
            self._vm_rows(),
        )

    def network_created(self, name: str, *, published: dict) -> None:
        self._remember("network", (name,), [published], self._network_rows())

    def volume_created(self, name: str, *, published: dict | None = None) -> None:
        self._remember(
            "volume",
            (name,),
            [published] if published is not None else self._volume_rows(),
            self._volume_rows(),
        )

    def key_created(self, name: str, *, published: dict) -> None:
        self._remember("key", (name,), [published], self._key_rows())

    def owned_vm_id(self, name: str) -> str:
        self._remember("vm", (name,), self._vm_rows(), self._vm_rows())
        receipt = self.attempts["vm"][name]
        assert receipt is not None
        return receipt[0]

    def _owned_id(
        self, domain: str, name: str, first: list, second: list
    ) -> str | None:
        retained = self.attempts[domain].get(name)
        matches = [
            row
            for row in first
            if row["name"] == name or (retained and row["id"] == retained[0])
        ]
        current = [
            row
            for row in second
            if row["name"] == name or (retained and row["id"] == retained[0])
        ]
        if name not in self.attempts[domain]:
            assert not matches and not current, (
                domain,
                name,
                "unattempted resource appeared",
            )
            return None
        if not matches and not current:
            return None
        # Partial publication is eligible only after this case's admitted create attempt.
        self._remember(domain, (name,), matches, current)
        return self.attempts[domain][name][0]

    def remove_vm(self, name: str) -> None:
        identifier = self._owned_id("vm", name, self._vm_rows(), self._vm_rows())
        if identifier is not None:
            _run_mvm(self.runner_vm, "vm", "rm", identifier, "--force", timeout=120)
        assert all(
            row["name"] != name and row["id"] != identifier for row in self._vm_rows()
        ), ("vm", name)

    def remove_volume(self, name: str) -> None:
        identifier = self._owned_id(
            "volume", name, self._volume_rows(), self._volume_rows()
        )
        if identifier is not None:
            _run_mvm(self.runner_vm, "volume", "rm", identifier, "--force")
        assert all(
            row["name"] != name and row["id"] != identifier
            for row in self._volume_rows()
        ), ("volume", name)

    def remove_network(self, name: str) -> None:
        identifier = self._owned_id(
            "network", name, self._network_rows(), self._network_rows()
        )
        if identifier is not None:
            _run_mvm(self.runner_vm, "network", "rm", identifier, "--force")
        assert all(
            row["name"] != name and row["id"] != identifier
            for row in self._network_rows()
        ), ("network", name)

    def remove_key(self, name: str) -> None:
        identifier = self._owned_id("key", name, self._key_rows(), self._key_rows())
        if identifier is not None:
            for path, identity in self.key_files[name].items():
                metadata = path.lstat()
                assert (
                    stat.S_ISREG(metadata.st_mode) and metadata.st_uid == os.geteuid()
                ), path
                assert (metadata.st_dev, metadata.st_ino) == identity, path
            _run_mvm(self.runner_vm, "key", "rm", identifier)
        assert all(
            row["name"] != name and row["id"] != identifier for row in self._key_rows()
        ), ("key", name)
        assert all(
            not os.path.lexists(path) for path in self.key_files.get(name, {})
        ), name

    def cleanup(self) -> None:
        failures = []
        # VM removal releases volume and network dependencies before their removal.
        for domain, remove in (
            ("vm", self.remove_vm),
            ("volume", self.remove_volume),
            ("network", self.remove_network),
            ("key", self.remove_key),
        ):
            for name in reversed(list(self.attempts[domain])):
                try:
                    remove(name)
                except Exception as error:
                    failures.append(f"{domain}/{name}: {error}")
        for domain, baseline in self.baseline.items():
            try:
                assert _lifecycle_inventory(self.runner_vm, domain) == baseline, domain
            except Exception as error:
                failures.append(f"{domain} baseline: {error}")
        try:
            assert (
                _json_success_data(
                    _run_mvm(self.runner_vm, "config", "ls", "--json").stdout, dict
                )
                == self.settings
            )
        except Exception as error:
            failures.append(f"config baseline: {error}")
        for name in self.attempts["vm"]:
            assert _VM_OWNERS.pop(name, None) is self
        assert not failures, "\n".join(failures)


@pytest.fixture(autouse=True)
def lifecycle_owner(runner_vm) -> Generator[_LifecycleOwner, None, None]:
    """One exact ownership and baseline guard for every retained case."""
    _require_lifecycle_assets(runner_vm)
    owner = _LifecycleOwner(runner_vm)
    try:
        yield owner
    finally:
        owner.cleanup()


@pytest.fixture
def remaining_lifecycle_case(runner_vm, lifecycle_owner) -> Generator[dict, None, None]:
    """One no-NAT network and planned names; the owner admits each create separately."""
    suffix = uuid.uuid4().hex[:12]
    name = f"sys-remaining-{suffix}"
    case = {
        "name": name,
        "vm_names": [name, f"{name}-2", f"{name}-3"],
        "network": f"{name}-net",
        "key": f"{name}-key",
        "volume": f"{name}-vol",
        "owner": lifecycle_owner,
    }
    lifecycle_owner.before_network_create(case["network"])
    network = _json_success_data(
        _run_mvm(
            runner_vm,
            "network",
            "create",
            case["network"],
            "--subnet",
            _unique_subnet(case["network"]),
            "--no-nat",
            "--json",
        ).stdout,
        dict,
    )
    lifecycle_owner.network_created(case["network"], published=network)
    case["network_id"] = network["id"]
    yield case


class TestVMAdvancedCreateFlags:
    """Advanced vm create flags: --ssh-key <filepath>, --user,
    --lsm-flags, --skip-cleanup, --skip-deblob."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_ssh_key_filepath(
        self, runner_vm, remaining_lifecycle_case, tmp_path
    ):
        """Pass the generated public key's actual file path, rather than its name."""
        case = remaining_lifecycle_case
        case["owner"].before_key_create(case["key"])
        key = _json_success_data(
            _run_mvm(
                runner_vm,
                "key",
                "create",
                case["key"],
                "--algorithm",
                "ed25519",
                "--out",
                str(tmp_path / "ssh-key"),
                "--json",
            ).stdout,
            dict,
        )
        case["owner"].key_created(case["key"], published=key)
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--ssh-key",
                key["public_key_path"],
                "--user",
                "root",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["name"] == case["name"]
        assert created[0]["status"] == "running"
        assert wait_for_ssh(runner_vm, case["name"], "root", timeout=60)
        assert (
            _run_mvm(
                runner_vm,
                "ssh",
                case["name"],
                "--key",
                key["private_key_path"],
                "-u",
                "root",
                "--cmd",
                "id -u",
            ).stdout.strip()
            == "0"
        )

    def test_create_with_user_flag(self, runner_vm, remaining_lifecycle_case):
        """The explicit user survives inspect and exists in the running guest."""
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--cloud-init-mode",
            "off",
            "--user",
            "customuser",
            timeout=180,
        )
        case["owner"].vm_created(case["name"])
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["name"], "--json").stdout,
            dict,
        )
        assert data["vm"]["ssh_user"] == "customuser"
        assert _run_mvm(
            runner_vm, "exec", case["name"], "--", "id", "-un", "customuser"
        ).stdout.strip() == ("customuser")

    def test_create_with_lsm_flags(self, runner_vm, remaining_lifecycle_case):
        """The LSM flag reaches the guest kernel command line."""
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--cloud-init-mode",
            "off",
            "--lsm-flags",
            "landlock",
            timeout=180,
        )
        case["owner"].vm_created(case["name"])
        vm = next(
            row
            for row in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if row["name"] == case["name"]
        )
        assert vm["lsm_flags"] == "landlock"
        assert (
            "lsm=landlock"
            in _run_mvm(
                runner_vm, "exec", case["name"], "--", "cat", "/proc/cmdline"
            ).stdout.split()
        )

    def test_create_with_skip_cleanup(self, runner_vm, remaining_lifecycle_case):
        """Answer the real CLI confirmation using exact installed binary."""
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        result = subprocess.run(
            [
                "/usr/local/bin/mvm",
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--skip-cleanup",
            ],
            capture_output=True,
            text=True,
            timeout=210,
            input="y\n",
            env={**os.environ, "NO_COLOR": "1"},
        )
        if result.returncode == 0:
            case["owner"].vm_created(case["name"])
        assert result.returncode == 0, (result.stdout, result.stderr)
        assert "Continue?" in result.stderr
        created = [
            row
            for row in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if row["name"] == case["name"]
        ]
        assert len(created) == 1 and created[0]["status"] == "running"

    def test_create_with_skip_deblob(self, runner_vm, remaining_lifecycle_case):
        """The public skip-deblob option launches the pre-seeded Alpine image."""
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["name"] == case["name"]
        assert created[0]["status"] == "running"
        assert (
            created[0]["vcpu_count"],
            created[0]["mem_size_mib"],
            created[0]["disk_size_mib"],
        ) == (1, 256, 512)


# ========================================================================
# TestVMListInspect
# ========================================================================


@pytest.fixture
def listing_vm(runner_vm, lifecycle_owner) -> Generator[dict, None, None]:
    """Return one receipted managed VM; the function owner tears down dependencies."""
    suffix = uuid.uuid4().hex[:16]
    name, network = f"sys-list-{suffix}", f"sys-list-net-{suffix}"
    lifecycle_owner.before_network_create(network)
    published = _json_success_data(
        _run_mvm(
            runner_vm,
            "network",
            "create",
            network,
            "--subnet",
            _unique_subnet(network),
            "--no-nat",
            "--json",
        ).stdout,
        dict,
    )
    lifecycle_owner.network_created(network, published=published)
    lifecycle_owner.before_vm_create(name)
    created = _json_success_data(
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            name,
            *_REMAINING_VM_ARGS,
            "--network",
            published["id"],
            "--cloud-init-mode",
            "off",
            "--json",
            timeout=180,
        ).stdout,
        list,
    )
    lifecycle_owner.vm_created(name, published=created)
    assert len(created) == 1 and created[0]["name"] == name, created
    yield created[0]


class TestVMListInspect:
    """Each public listing and inspection output boundary is exercised once."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    @pytest.mark.parametrize("command", ["ls", "ps"])
    def test_listing_json(self, runner_vm, listing_vm, command):
        """The real running VM and explicit allocation survive schema-1 serialization."""
        vms = _parse_vm_list(_run_mvm(runner_vm, "vm", command, "--json").stdout)
        matches = [vm for vm in vms if vm["id"] == listing_vm["id"]]
        assert len(matches) == 1, vms
        vm = matches[0]
        assert vm["name"] == listing_vm["name"]
        assert vm["status"] == "running"
        assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (
            1,
            256,
            512,
        )
        assert vm["ipv4"] == listing_vm["ipv4"]
        assert vm["pid"] > 0
        assert vm["process_start_time"] > 0

    @pytest.mark.parametrize("command", ["ls", "ps"])
    def test_listing_text(self, runner_vm, listing_vm, command):
        """Human output is checked directly, not through a second JSON invocation."""
        output = _run_mvm(runner_vm, "vm", command).stdout
        assert listing_vm["name"] in output
        assert listing_vm["ipv4"] in output
        for header in ("name", "status", "ipv4"):
            assert header in output.lower(), output

    @pytest.mark.parametrize("selector", ["name", "ipv4", "id_prefix"])
    def test_inspect_json(self, runner_vm, listing_vm, selector):
        """Inspect exposes the same selected VM, explicit resources, and public sections."""
        identifier = {
            "name": listing_vm["name"],
            "ipv4": listing_vm["ipv4"],
            "id_prefix": listing_vm["id"][:8],
        }[selector]
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", identifier, "--json").stdout,
            dict,
        )
        for section in (
            "vm",
            "resources",
            "networking",
            "assets",
            "filesystem",
            "console",
        ):
            assert isinstance(data[section], dict), data
        assert data["vm"]["id"] == listing_vm["id"]
        assert data["vm"]["name"] == listing_vm["name"]
        assert data["vm"]["status"] == "running"
        assert data["vm"]["pid"] == listing_vm["pid"]
        assert (
            data["resources"]["vcpu"],
            data["resources"]["mem"],
            data["resources"]["disk"],
        ) == (
            1,
            256,
            512,
        )
        assert data["networking"]["ipv4"] == listing_vm["ipv4"]
        assert data["networking"]["mac"] == listing_vm["mac"]
        assert data["filesystem"]["vm_dir"]
        assert data["console"]["relay_running"] is False

    def test_inspect_tree(self, runner_vm, listing_vm):
        """The real text inspection route identifies the selected VM."""
        output = _run_mvm(runner_vm, "vm", "inspect", listing_vm["name"]).stdout
        assert listing_vm["name"] in output
        assert listing_vm["id"] in output
        assert listing_vm["ipv4"] in output
        assert "running" in output

    def test_console_state_nonexistent_vm(self, runner_vm):
        """A nonexistent VM retains its structured domain error through the console route."""
        nonexistent = f"nonexistent-vm-{uuid.uuid4().hex[:8]}"
        result = _run_mvm(
            runner_vm, "console", nonexistent, "--state", "--json", check=False
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1
        assert data["status"] == "error"
        assert data["data"] is None
        assert data["error"]["code"] == "vm.not_found"


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

    def test_ssh_available(self, runner_vm, remaining_lifecycle_case, timing_targets):
        """An injected named key authenticates as root after Alpine boots."""
        case = remaining_lifecycle_case
        case["owner"].before_key_create(case["key"])
        key = _json_success_data(
            _run_mvm(
                runner_vm,
                "key",
                "create",
                case["key"],
                "--algorithm",
                "ed25519",
                "--json",
            ).stdout,
            dict,
        )
        case["owner"].key_created(case["key"], published=key)
        case["owner"].before_vm_create(case["name"])
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--cloud-init-mode",
            "off",
            "--ssh-key",
            case["key"],
            "--user",
            "root",
            timeout=180,
        )
        case["owner"].vm_created(case["name"])
        assert wait_for_ssh(
            runner_vm,
            case["name"],
            "root",
            timeout=max(60, timing_targets["alpine:3.23"]),
        )
        assert (
            _run_mvm(
                runner_vm,
                "ssh",
                case["name"],
                "--key",
                key["private_key_path"],
                "-u",
                "root",
                "--cmd",
                "id -u",
            ).stdout.strip()
            == "0"
        )


# ========================================================================
# TestVMConfigOptions
# ========================================================================


@pytest.fixture(scope="class")
def config_vm_deps(runner_vm) -> None:
    """Admit existing assets without pulls or configuration changes."""
    _require_lifecycle_assets(runner_vm)


def _remove_config_vm(runner_vm: str, name: str) -> None:
    """Use the function's retained full-ID receipt, including cold-restore generations."""
    owner = _VM_OWNERS[name]
    assert owner.runner_vm == runner_vm
    owner.remove_vm(name)


@pytest.fixture
def config_vm_name(
    runner_vm, config_vm_deps, lifecycle_owner
) -> Generator[str, None, None]:
    """Admit both names before the configured/explicit or invalid-create journey."""
    name = f"sys-config-{uuid.uuid4().hex[:16]}"
    lifecycle_owner.before_vm_create(name, f"{name}-cli")
    yield name


def _create_config_vm(
    runner_vm: str,
    name: str,
    *options: str,
    memory: str = "256M",
    vcpu: int | None = 1,
    pci_enabled: bool = False,
) -> dict:
    """Launch a small isolated VM through the public CLI, not a test-only backend."""
    args = [
        "vm",
        "create",
        name,
        "--image",
        "alpine:3.23",
        "--no-network",
        "--cloud-init-mode",
        "off",
        "--skip-deblob",
        "--disk-size",
        "512M",
        "--mem",
        memory,
        "--json",
    ]
    if vcpu is not None:
        args.extend(["--vcpu", str(vcpu)])
    if not pci_enabled:
        args.append("--no-pci")
    created = _json_success_data(
        _run_mvm(runner_vm, *args, *options, timeout=180).stdout,
        list,
    )
    _VM_OWNERS[name].vm_created(name, published=created)
    assert len(created) == 1, created
    assert created[0]["name"] == name
    assert created[0]["status"] == "running"
    return created[0]


class TestVMConfigOptions:
    """Real allocation and feature effects, plus typed refusal before VM creation."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    @pytest.mark.parametrize(
        "memory", ["256", "256M"], ids=["numeric", "human-readable"]
    )
    def test_resource_allocation(self, runner_vm, config_vm_name, memory):
        """CPU/RAM/root-disk flags reach the guest, not merely the stored input."""
        vm = _create_config_vm(runner_vm, config_vm_name, memory=memory, vcpu=2)
        assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (
            2,
            256,
            512,
        )
        assert (
            _run_mvm(runner_vm, "exec", config_vm_name, "--", "nproc").stdout.strip()
            == "2"
        )
        memory_info = _run_mvm(
            runner_vm,
            "exec",
            config_vm_name,
            "--",
            "cat",
            "/proc/meminfo",
        ).stdout
        total_kib = int(
            next(
                line.split()[1]
                for line in memory_info.splitlines()
                if line.startswith("MemTotal:")
            )
        )
        # Kernel reservations reduce guest-visible RAM; the bound still rejects an ignored 256 MiB flag.
        assert (256 - 64) * 1024 < total_kib <= 256 * 1024, memory_info
        sectors = _run_mvm(
            runner_vm,
            "exec",
            config_vm_name,
            "--",
            "cat",
            "/sys/class/block/vda/size",
        ).stdout.strip()
        assert int(sectors) * 512 == 512 * 1024 * 1024
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", config_vm_name, "--json").stdout,
            dict,
        )
        assert inspected["resources"]["vcpu"] == 2
        assert inspected["resources"]["mem"] == 256
        assert inspected["resources"]["disk"] == 512
        assert inspected["vm"]["enable_console"] is False
        assert inspected["console"]["relay_running"] is False
        assert inspected["console"]["relay_pid"] is None

    @pytest.mark.parametrize(
        ("flag", "value", "code"),
        [
            ("--vcpu", "0", "vm.create.failed"),
            ("--vcpu", "-1", "vm.create.failed"),
            ("--mem", "0", "vm.create.failed"),
            ("--disk-size", "0", "vm.create.failed"),
            ("--disk-size", "abc", "validation.failed"),
        ],
        ids=["zero-cpu", "negative-cpu", "zero-memory", "zero-disk", "malformed-disk"],
    )
    def test_invalid_resource_rejected_before_vm_creation(
        self,
        runner_vm,
        config_vm_name,
        flag,
        value,
        code,
    ):
        """An invalid allocation is a validation error, not an incidental launch failure."""
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            config_vm_name,
            "--image",
            "alpine:3.23",
            "--no-network",
            flag,
            value,
            "--json",
            check=False,
        )
        assert result.returncode != 0
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["data"] is None
        assert envelope["error"]["code"] == code
        assert envelope["error"]["class"] == 1
        assert (
            _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before
        )

    def test_config_chain_precedence(self, runner_vm, config_vm_name):
        """Configured CPU count and explicit CLI precedence are observed inside two sequential guests."""

        def cpu_setting() -> dict:
            return _json_success_data(
                _run_mvm(runner_vm, "config", "get", "defaults.vm", "--json").stdout,
                dict,
            )["vcpu_count"]

        original = cpu_setting()
        assert original["type"] == "int"
        try:
            _run_mvm(runner_vm, "config", "set", "defaults.vm", "vcpu_count", "2")
            inherited = _create_config_vm(runner_vm, config_vm_name, vcpu=None)
            assert inherited["vcpu_count"] == 2
            assert (
                _run_mvm(
                    runner_vm,
                    "exec",
                    config_vm_name,
                    "--",
                    "nproc",
                ).stdout.strip()
                == "2"
            )
            _remove_config_vm(runner_vm, config_vm_name)

            explicit_name = f"{config_vm_name}-cli"
            explicit = _create_config_vm(runner_vm, explicit_name, vcpu=1)
            assert explicit["vcpu_count"] == 1
            assert (
                _run_mvm(
                    runner_vm,
                    "exec",
                    explicit_name,
                    "--",
                    "nproc",
                ).stdout.strip()
                == "1"
            )
        finally:
            if original["override"] is None:
                _run_mvm(runner_vm, "config", "reset", "defaults.vm", "vcpu_count")
            else:
                _run_mvm(
                    runner_vm,
                    "config",
                    "set",
                    "defaults.vm",
                    "vcpu_count",
                    str(original["override"]),
                )
            assert cpu_setting() == original

    def test_boot_args_reach_guest(self, runner_vm, config_vm_name):
        """A distinct custom argument reaches the booted kernel command line."""
        marker = "mvm_qa_resource=1"
        _create_config_vm(
            runner_vm,
            config_vm_name,
            "--boot-args",
            f"rw quiet loglevel=5 {marker}",
        )
        command_line = _run_mvm(
            runner_vm,
            "exec",
            config_vm_name,
            "--",
            "cat",
            "/proc/cmdline",
        ).stdout.split()
        assert marker in command_line
        assert "loglevel=5" in command_line

    @pytest.mark.parametrize(
        "pci_enabled", [True, False], ids=["default-pci", "no-pci"]
    )
    def test_pci_transport(self, runner_vm, config_vm_name, pci_enabled):
        """Both transport choices boot; only the PCI choice enumerates guest PCI devices."""
        vm = _create_config_vm(runner_vm, config_vm_name, pci_enabled=pci_enabled)
        assert vm["pci_enabled"] is pci_enabled
        devices = _run_mvm(
            runner_vm,
            "exec",
            config_vm_name,
            "--",
            "ls",
            "/sys/bus/pci/devices",
        ).stdout.split()
        assert bool(devices) is pci_enabled, devices

    @pytest.mark.parametrize("enabled", [True, False], ids=["enabled", "disabled"])
    def test_logging_output(self, runner_vm, config_vm_name, enabled):
        """The logging flag controls Firecracker log output in a fresh VM directory."""
        flag = "--enable-logging" if enabled else "--disable-logging"
        vm = _create_config_vm(runner_vm, config_vm_name, flag)
        assert vm["enable_logging"] is enabled
        _run_mvm(runner_vm, "exec", config_vm_name, "--", "uname", "-s")
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", config_vm_name, "--json").stdout,
            dict,
        )
        log_path = Path(inspected["filesystem"]["log_path"])
        assert log_path.is_file()
        assert (log_path.stat().st_size > 0) is enabled

    @pytest.mark.parametrize("enabled", [True, False], ids=["enabled", "disabled"])
    def test_metrics_output(self, runner_vm, config_vm_name, enabled):
        """Enabled metrics produce complete JSON records; disabling leaves no metrics file."""
        flag = "--enable-metrics" if enabled else "--disable-metrics"
        vm = _create_config_vm(runner_vm, config_vm_name, flag)
        assert vm["enable_metrics"] is enabled
        inspected = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", config_vm_name, "--json").stdout,
            dict,
        )
        path = Path(inspected["filesystem"]["vm_dir"]) / "firecracker.metrics"
        if not enabled:
            assert not path.exists()
            return
        deadline = time.monotonic() + 10
        while True:
            text = path.read_text()
            if text.endswith("\n"):
                records = [json.loads(line) for line in text.splitlines()]
                assert records and all(
                    isinstance(record, dict) and record for record in records
                )
                break
            assert time.monotonic() < deadline, text
            time.sleep(0.1)


# ========================================================================
# TestVMStateTransitions
# ========================================================================


@pytest.fixture(scope="class")
def lifecycle_vm_deps(runner_vm) -> None:
    """Read-only admission of the current preseeded runner assets."""
    _require_lifecycle_assets(runner_vm)


@pytest.fixture
def lifecycle_vm(
    runner_vm, lifecycle_vm_deps, lifecycle_owner
) -> Generator[dict, None, None]:
    """Retain one managed VM/network generation through each public state journey."""
    name = f"sys-lifecycle-{uuid.uuid4().hex[:12]}"
    network = f"{name}-net"
    lifecycle_owner.before_network_create(network)
    published = _json_success_data(
        _run_mvm(
            runner_vm,
            "network",
            "create",
            network,
            "--subnet",
            _unique_subnet(network),
            "--no-nat",
            "--json",
        ).stdout,
        dict,
    )
    lifecycle_owner.network_created(network, published=published)
    lifecycle_owner.before_vm_create(name)
    created = _json_success_data(
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            name,
            *_REMAINING_VM_ARGS,
            "--network",
            published["id"],
            "--cloud-init-mode",
            "off",
            "--json",
            timeout=180,
        ).stdout,
        list,
    )
    lifecycle_owner.vm_created(name, published=created)
    assert len(created) == 1, created
    assert created[0]["name"] == name and created[0]["status"] == "running"
    assert (
        created[0]["vcpu_count"],
        created[0]["mem_size_mib"],
        created[0]["disk_size_mib"],
    ) == (1, 256, 512)
    yield created[0]


def _lifecycle_vm_state(runner_vm: str, name: str, status: str) -> dict:
    """Require list and inspect to agree on this exact VM's public state."""
    matches = [
        vm
        for vm in _parse_vm_list(
            _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
        )
        if vm["name"] == name
    ]
    assert len(matches) == 1, matches
    vm = matches[0]
    assert vm["status"] == status, vm
    inspected = _json_success_data(
        _run_mvm(runner_vm, "vm", "inspect", name, "--json").stdout,
        dict,
    )["vm"]
    assert inspected["id"] == vm["id"]
    assert inspected["status"] == status, inspected
    if status in ("running", "paused"):
        assert inspected["pid"] == vm["pid"]
    return vm


def _lifecycle_process_identity(vm: dict) -> tuple[int, int]:
    """Match public PID/start ticks to the live task, rather than PID existence."""
    pid, ticks = int(vm["pid"]), int(vm["process_start_time"])
    assert pid > 0 and ticks > 0, vm
    fields = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
    assert len(fields) >= 20, fields
    assert int(fields[19]) == ticks, (pid, ticks, fields)
    assert fields[0] not in ("Z", "X"), fields
    return pid, ticks


@contextmanager
def _lifecycle_process_handle(
    vm: dict,
) -> Generator[tuple[tuple[int, int], int], None, None]:
    """Retain a pidfd before the CLI mutation and admit its public PID/start identity."""
    pidfd = os.pidfd_open(int(vm["pid"]))
    try:
        identity = _lifecycle_process_identity(vm)
        poller = select.poll()
        poller.register(pidfd, select.POLLIN)
        assert poller.poll(0) == [], (
            f"VM task {identity} exited before the lifecycle mutation"
        )
        yield identity, pidfd
    finally:
        os.close(pidfd)


def _assert_lifecycle_process_exited(pidfd: int) -> None:
    """The retained descriptor proves original task exit independently of PID reuse."""
    poller = select.poll()
    poller.register(pidfd, select.POLLIN)
    events = poller.poll(10_000)
    assert len(events) == 1 and events[0][0] == pidfd, events
    assert not events[0][1] & (select.POLLERR | select.POLLNVAL), events
    assert events[0][1] & (select.POLLIN | select.POLLHUP), events


def _lifecycle_boot_id(runner_vm: str, name: str) -> str:
    """A successful guest-agent read also proves vsock is reachable after launch."""
    boot_id = _run_mvm(
        runner_vm,
        "exec",
        name,
        "--",
        "cat",
        "/proc/sys/kernel/random/boot_id",
    ).stdout.strip()
    assert str(uuid.UUID(boot_id)) == boot_id, boot_id
    return boot_id


class TestVMStateTransitions:
    """Public lifecycle journeys with task identity, guest readiness, and checked cleanup."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_pause_resume_preserves_process_and_guest(self, runner_vm, lifecycle_vm):
        """Pause/resume and repeated target-state requests retain the same process and boot."""
        name = lifecycle_vm["name"]
        original = _lifecycle_process_identity(
            _lifecycle_vm_state(runner_vm, name, "running")
        )
        boot_id = _lifecycle_boot_id(runner_vm, name)

        _run_mvm(runner_vm, "vm", "resume", name)
        assert (
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
            == original
        )
        for _ in range(2):
            _run_mvm(runner_vm, "vm", "pause", name)
            assert (
                _lifecycle_process_identity(
                    _lifecycle_vm_state(runner_vm, name, "paused")
                )
                == original
            )
        _run_mvm(runner_vm, "vm", "resume", name)
        assert (
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
            == original
        )
        assert _lifecycle_boot_id(runner_vm, name) == boot_id
        _run_mvm(runner_vm, "vm", "resume", name)
        assert (
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
            == original
        )
        assert _lifecycle_boot_id(runner_vm, name) == boot_id

    def test_stop_start_three_cycles_and_idempotence(self, runner_vm, lifecycle_vm):
        """Three actual stop/start cycles include graceful, forced, and IP-selected stop."""
        name = lifecycle_vm["name"]
        running = _lifecycle_vm_state(runner_vm, name, "running")
        identity = _lifecycle_process_identity(running)
        boot_id = _lifecycle_boot_id(runner_vm, name)
        _run_mvm(runner_vm, "vm", "start", name)
        assert (
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
            == identity
        )
        assert _lifecycle_boot_id(runner_vm, name) == boot_id

        # Exactly three stops and three starts; creation already leaves the VM running.
        for selector, flags in (
            (name, ()),
            (name, ("--force",)),
            (running["ipv4"], ()),
        ):
            assert selector
            with _lifecycle_process_handle(
                _lifecycle_vm_state(runner_vm, name, "running")
            ) as (current, pidfd):
                assert current == identity
                _run_mvm(runner_vm, "vm", "stop", selector, *flags)
                stopped = _lifecycle_vm_state(runner_vm, name, "stopped")
                assert stopped["id"] == lifecycle_vm["id"]
                _assert_lifecycle_process_exited(pidfd)
                assert all(
                    vm["id"] != stopped["id"]
                    for vm in _parse_vm_list(
                        _run_mvm(runner_vm, "vm", "ps", "--json").stdout,
                    )
                )
                _run_mvm(runner_vm, "vm", "stop", selector, *flags)
                assert (
                    _lifecycle_vm_state(runner_vm, name, "stopped")["id"]
                    == stopped["id"]
                )
                _assert_lifecycle_process_exited(pidfd)

            _run_mvm(runner_vm, "vm", "start", name)
            restarted = _lifecycle_vm_state(runner_vm, name, "running")
            next_identity = _lifecycle_process_identity(restarted)
            assert next_identity != identity
            assert restarted["ipv4"] == running["ipv4"]
            next_boot = _lifecycle_boot_id(runner_vm, name)
            assert next_boot != boot_id
            assert (
                _run_mvm(
                    runner_vm,
                    "exec",
                    name,
                    "--",
                    "printf",
                    "restarted-after-stop",
                ).stdout
                == "restarted-after-stop"
            )
            identity, boot_id = next_identity, next_boot

    @pytest.mark.parametrize("force", [False, True], ids=["graceful", "force"])
    def test_reboot_changes_process_and_boot_id(self, runner_vm, lifecycle_vm, force):
        """Both reboot routes replace the process and boot a reachable new guest."""
        name = lifecycle_vm["name"]
        boot_id = _lifecycle_boot_id(runner_vm, name)
        with _lifecycle_process_handle(
            _lifecycle_vm_state(runner_vm, name, "running")
        ) as (identity, pidfd):
            _run_mvm(runner_vm, "vm", "reboot", name, *(("--force",) if force else ()))
            _assert_lifecycle_process_exited(pidfd)
            rebooted = _lifecycle_vm_state(runner_vm, name, "running")
            assert rebooted["id"] == lifecycle_vm["id"]
            assert _lifecycle_process_identity(rebooted) != identity
            assert _lifecycle_boot_id(runner_vm, name) != boot_id

    @pytest.mark.destructive
    def test_paused_forced_removal_exits_process(self, runner_vm, lifecycle_vm):
        """Forced removal of a paused VM exits its exact process and removes its public record."""
        name = lifecycle_vm["name"]
        _lifecycle_boot_id(runner_vm, name)
        with _lifecycle_process_handle(
            _lifecycle_vm_state(runner_vm, name, "running")
        ) as (identity, pidfd):
            _run_mvm(runner_vm, "vm", "pause", name)
            assert (
                _lifecycle_process_identity(
                    _lifecycle_vm_state(runner_vm, name, "paused")
                )
                == identity
            )
            _run_mvm(runner_vm, "vm", "rm", name, "--force", timeout=120)
            _assert_lifecycle_process_exited(pidfd)
            assert all(
                vm["id"] != lifecycle_vm["id"]
                for vm in _parse_vm_list(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
                )
            )

    @pytest.mark.destructive
    def test_stopped_snapshot_captures_and_restores_disk(self, runner_vm, lifecycle_vm):
        """A stopped VM yields a cold disk archive that restores guest data and vsock."""
        name = lifecycle_vm["name"]
        snapshot_name = f"{name}-cold"
        snapshot_id = None
        marker = uuid.uuid4().hex
        try:
            boot_id = _lifecycle_boot_id(runner_vm, name)
            _run_mvm(
                runner_vm,
                "exec",
                name,
                "--",
                f"printf '%s' '{marker}' > /root/lifecycle-cold-marker && sync",
            )
            with _lifecycle_process_handle(
                _lifecycle_vm_state(runner_vm, name, "running")
            ) as (_, pidfd):
                _run_mvm(runner_vm, "vm", "stop", name)
                _assert_lifecycle_process_exited(pidfd)
            _lifecycle_vm_state(runner_vm, name, "stopped")
            capture = _run_mvm(
                runner_vm,
                "snapshot",
                "create",
                name,
                "--name",
                snapshot_name,
                "--json",
                check=False,
                timeout=240,
            )
            # Keep the root request identity even if capture fails before its local projection.
            intents = [
                line.removeprefix("Snapshot request ID: ")
                for line in capture.stderr.splitlines()
                if line.startswith("Snapshot request ID: ")
            ]
            assert len(intents) <= 1, capture.stderr
            if intents:
                snapshot_id = intents[0]
                assert len(snapshot_id) == 64 and all(
                    c in "0123456789abcdef" for c in snapshot_id
                )
            assert capture.returncode == 0, (capture.stdout, capture.stderr)
            captured = _json_success_data(capture.stdout, dict)
            assert captured["id"] == snapshot_id
            assert captured["name"] == snapshot_name
            assert captured["source_vm_id"] == lifecycle_vm["id"]
            assert captured["memory_file"] == ""
            assert captured["state_file"] == ""
            assert captured["rootfs_file"]
            _lifecycle_vm_state(runner_vm, name, "stopped")
            _remove_config_vm(runner_vm, name)

            _VM_OWNERS[name].before_vm_create(name)
            restored = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "snapshot",
                    "restore",
                    snapshot_id,
                    name,
                    "--resume",
                    "--json",
                    timeout=180,
                ).stdout,
                list,
            )
            _VM_OWNERS[name].vm_created(name, published=restored)
            assert len(restored) == 1
            assert restored[0]["name"] == name and restored[0]["status"] == "running"
            assert restored[0]["id"] != lifecycle_vm["id"]
            assert restored[0]["network_id"] == lifecycle_vm["network_id"]
            assert restored[0]["ipv4"] == lifecycle_vm["ipv4"]
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
            assert _lifecycle_boot_id(runner_vm, name) != boot_id
            assert (
                _run_mvm(
                    runner_vm,
                    "exec",
                    name,
                    "--",
                    "cat",
                    "/root/lifecycle-cold-marker",
                ).stdout
                == marker
            )
        finally:
            # A restore retains captured resources until VM removal; keep this ordering on failure too.
            _remove_config_vm(runner_vm, name)
            if snapshot_id is not None:
                _run_mvm(
                    runner_vm, "snapshot", "rm", snapshot_id, "--force", timeout=120
                )
            assert all(
                item["name"] != snapshot_name
                for item in _json_success_data(
                    _run_mvm(runner_vm, "snapshot", "ls", "--json").stdout,
                    list,
                )
            )

    @pytest.mark.system
    @pytest.mark.requires_kvm
    @pytest.mark.slow
    @pytest.mark.destructive
    def test_crashed_vm_status_and_restart(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
        lifecycle_owner,
    ):
        """A confirmed Firecracker exit is crashed in public status and can relaunch."""
        net_name = unique_network_name
        lifecycle_owner.before_network_create(net_name)
        network = _json_success_data(
            _run_mvm(
                runner_vm,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--no-nat",
                "--json",
            ).stdout,
            dict,
        )
        lifecycle_owner.network_created(net_name, published=network)
        try:
            lifecycle_owner.before_vm_create(unique_vm_name)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                unique_vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                net_name,
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--no-pci",
                "-s",
                "512M",
                "--mem",
                "256M",
                "--vcpu",
                "1",
                timeout=180,
            )
            lifecycle_owner.vm_created(unique_vm_name)
            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", unique_vm_name, "--json").stdout,
                dict,
            )
            vm_data = next(
                vm
                for vm in _parse_vm_list(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
                )
                if vm["name"] == unique_vm_name
            )
            assert inspected["vm"]["status"] == "running"
            assert inspected["vm"]["pid"] == vm_data["pid"]
            assert vm_data["status"] == "running"
            assert int(vm_data["pid"]) > 0
            assert int(vm_data["process_start_time"]) > 0
            _kill_exact_test_firecracker(
                int(vm_data["pid"]),
                int(vm_data["process_start_time"]),
                vm_data["id"],
            )

            listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            entry = next(vm for vm in listed if vm["id"] == vm_data["id"])
            assert entry["status"] == "crashed"
            running = _parse_vm_list(_run_mvm(runner_vm, "vm", "ps", "--json").stdout)
            assert all(vm["id"] != vm_data["id"] for vm in running)
            inspected = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", unique_vm_name, "--json").stdout,
                dict,
            )
            assert inspected["vm"]["status"] == "crashed"

            _run_mvm(runner_vm, "vm", "stop", unique_vm_name, "--force", timeout=120)
            _run_mvm(runner_vm, "vm", "start", unique_vm_name, timeout=120)
            assert (
                _run_mvm(
                    runner_vm,
                    "exec",
                    unique_vm_name,
                    "--",
                    "printf",
                    "restarted-after-crash",
                ).stdout
                == "restarted-after-crash"
            )
        finally:
            lifecycle_owner.remove_vm(unique_vm_name)
            lifecycle_owner.remove_network(net_name)
            assert all(
                vm["name"] != unique_vm_name
                for vm in _parse_vm_list(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
                )
            )
            assert all(
                network["name"] != net_name
                for network in _json_success_data(
                    _run_mvm(runner_vm, "network", "ls", "--json").stdout,
                    list,
                )
            )


# ========================================================================
# TestVMStateTransitionErrors (from CLI edge cases)
# ========================================================================


class TestVMStateTransitionErrors:
    """Invalid transitions expose typed refusal and retain the original state."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_pause_and_resume_stopped_vm_are_rejected(self, runner_vm, lifecycle_vm):
        """Neither wrong-direction command relaunches or mutates a stopped VM."""
        name = lifecycle_vm["name"]
        with _lifecycle_process_handle(
            _lifecycle_vm_state(runner_vm, name, "running")
        ) as (identity, pidfd):
            _run_mvm(runner_vm, "vm", "stop", name)
            _assert_lifecycle_process_exited(pidfd)
            for operation in ("pause", "resume"):
                result = _run_mvm(
                    runner_vm, "vm", operation, name, "--json", check=False
                )
                self._assert_invalid_state(result, operation)
                assert (
                    _lifecycle_vm_state(runner_vm, name, "stopped")["id"]
                    == lifecycle_vm["id"]
                )
                _assert_lifecycle_process_exited(pidfd)
                assert all(
                    vm["id"] != lifecycle_vm["id"]
                    for vm in _parse_vm_list(
                        _run_mvm(runner_vm, "vm", "ps", "--json").stdout,
                    )
                )
        _run_mvm(runner_vm, "vm", "start", name)
        assert (
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
            != identity
        )
        _lifecycle_boot_id(runner_vm, name)

    def test_start_paused_vm_is_rejected_without_relaunch(
        self, runner_vm, lifecycle_vm
    ):
        """Start refuses paused state; resume recovers the same process and guest boot."""
        name = lifecycle_vm["name"]
        identity = _lifecycle_process_identity(
            _lifecycle_vm_state(runner_vm, name, "running")
        )
        boot_id = _lifecycle_boot_id(runner_vm, name)
        _run_mvm(runner_vm, "vm", "pause", name)
        assert (
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "paused"))
            == identity
        )
        result = _run_mvm(runner_vm, "vm", "start", name, "--json", check=False)
        self._assert_invalid_state(result, "start")
        assert (
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "paused"))
            == identity
        )
        _run_mvm(runner_vm, "vm", "resume", name)
        assert (
            _lifecycle_process_identity(_lifecycle_vm_state(runner_vm, name, "running"))
            == identity
        )
        assert _lifecycle_boot_id(runner_vm, name) == boot_id

    @staticmethod
    def _assert_invalid_state(
        result: subprocess.CompletedProcess[str], operation: str
    ) -> None:
        """Batch output retains both the CLI failure and the controller's typed state error."""
        assert result.returncode != 0, (result.stdout, result.stderr)
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1
        assert envelope["status"] == "error"
        assert envelope["error"]["code"] == "cli.batch.failed"
        assert envelope["error"]["class"] == 4
        items = envelope["data"]["items"]
        assert len(items) == 1, items
        assert items[0]["result"]["status"] == "error"
        assert items[0]["result"]["code"] == f"vm.{operation}_failed"
        assert items[0]["error"]["code"] == "vm.state.invalid"
        assert items[0]["error"]["class"] == 1


# ========================================================================
# TestVMCloudInitModes (from CLI edge cases)
# ========================================================================


class TestVMCloudInitModes:
    """Test VM creation with different --cloud-init-mode values."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_create_cloud_init_mode_iso(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "iso",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["status"] == "running"
        assert created[0]["cloud_init_mode"] == "iso"
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["name"], "--json").stdout,
            dict,
        )
        assert data["vm"]["cloud_init_mode"] == "iso"

    def test_vm_create_cloud_init_mode_net(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "net",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["status"] == "running"
        assert created[0]["cloud_init_mode"] == "net"
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["name"], "--json").stdout,
            dict,
        )
        assert data["vm"]["cloud_init_mode"] == "net"
        assert data["vm"]["nocloud_net_port"] == 8000
        assert data["vm"]["nocloud_net_pid"] > 0

    def test_vm_create_cloud_init_mode_off(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["status"] == "running"
        assert created[0]["cloud_init_mode"] == "off"
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["name"], "--json").stdout,
            dict,
        )
        assert data["vm"]["cloud_init_mode"] == "off"
        assert data["vm"]["nocloud_net_pid"] is None


# ========================================================================
# TestVMNocloudNetPort (from CLI edge cases)
# ========================================================================


class TestVMNocloudNetPort:
    """Test VM creation with --nocloud-net-port values."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_create_with_nocloud_net_port_specific(
        self, runner_vm, remaining_lifecycle_case
    ):
        case = remaining_lifecycle_case
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        case["owner"].before_vm_create(case["name"])
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--nocloud-net-port",
            "12345",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1 and data["status"] == "error"
        assert data["data"] is None
        assert data["error"]["code"] == "validation.failed"
        assert "reserved gateway TCP 8000" in data["error"]["message"]
        assert (
            _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before
        )


# ========================================================================
# TestVMDestructiveRmMultiple (from CLI edge cases — destructive, last)
# ========================================================================


class TestVMDestructiveRmMultiple:
    """Destructive: Remove multiple VMs."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_rm_multiple_identifiers(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        names = case["vm_names"][:2]
        for name in names:
            case["owner"].before_vm_create(name)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                name,
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                timeout=180,
            )
            case["owner"].vm_created(name)
        rows = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        assert {row["name"] for row in rows if row["name"] in names} == set(names)
        _run_mvm(
            runner_vm,
            "vm",
            "rm",
            *(case["owner"].owned_vm_id(name) for name in names),
            "--force",
            timeout=120,
        )
        assert all(
            row["name"] not in names
            for row in _parse_vm_list(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout,
            )
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

    def test_create_with_static_ip(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        static_ip = ".".join(_unique_subnet(case["network"]).split(".")[:3]) + ".50"
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--ip",
                static_ip,
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["ipv4"] == static_ip
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["name"], "--json").stdout,
            dict,
        )
        assert data["networking"]["ipv4"] == static_ip

    def test_create_with_invalid_ip_fails(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        case["owner"].before_vm_create(case["name"])
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--ip",
            "999.999.999.999",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1 and data["status"] == "error"
        assert data["data"] is None and data["error"]["code"] == "validation.failed"
        assert (
            _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before
        )

    def test_create_with_invalid_ip_format_fails(
        self, runner_vm, remaining_lifecycle_case
    ):
        case = remaining_lifecycle_case
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        case["owner"].before_vm_create(case["name"])
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--ip",
            "not-an-ip",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1 and data["status"] == "error"
        assert data["data"] is None and data["error"]["code"] == "validation.failed"
        assert (
            _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before
        )

    def test_create_with_custom_mac(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        custom_mac = "aa:bb:cc:dd:ee:ff"
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--mac",
                custom_mac,
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["mac"] == custom_mac
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["name"], "--json").stdout,
            dict,
        )
        assert data["networking"]["mac"] == custom_mac

    def test_create_with_named_network(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["network_id"] == case["network_id"]
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["name"], "--json").stdout,
            dict,
        )
        assert data["networking"]["network"]["id"] == case["network_id"]
        assert data["networking"]["network"]["name"] == case["network"]


# ========================================================================
# TestVMCloudInit (from lifecycle - remaining cloud-init tests)
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

    def test_create_with_user_data(self, runner_vm, remaining_lifecycle_case, tmp_path):
        case = remaining_lifecycle_case
        path = tmp_path / "user-data.cfg"
        path.write_text(
            "#cloud-config\nhostname: custom-hostname-test\n", encoding="utf-8"
        )
        case["owner"].before_vm_create(case["name"])
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--cloud-init-mode",
            "inject",
            "--cloudinit-config",
            str(path),
            timeout=180,
        )
        case["owner"].vm_created(case["name"])
        seed = _run_mvm(
            runner_vm,
            "exec",
            case["name"],
            "--",
            "cat",
            "/var/lib/cloud/seed/nocloud-net/user-data",
        ).stdout
        assert seed.startswith("#cloud-config\n")
        assert "hostname: custom-hostname-test" in seed

    def test_create_with_cloud_init_mode(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "inject",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["cloud_init_mode"] == "inject"
        seed = _run_mvm(
            runner_vm,
            "exec",
            case["name"],
            "--",
            "cat",
            "/var/lib/cloud/seed/nocloud-net/meta-data",
        ).stdout
        assert seed.splitlines() == [
            f"instance-id: {created[0]['name']}",
            f"local-hostname: {created[0]['name']}",
        ]

    def test_create_with_nocloud_net_port(self, runner_vm, remaining_lifecycle_case):
        """Explicit zero is rejected too; omitting the flag selects the reserved port."""
        case = remaining_lifecycle_case
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        case["owner"].before_vm_create(case["name"])
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--nocloud-net-port",
            "0",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1 and data["status"] == "error"
        assert data["data"] is None and data["error"]["code"] == "validation.failed"
        assert "reserved gateway TCP 8000" in data["error"]["message"]
        assert (
            _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before
        )

    def test_create_cloud_init_net_mode_with_port(
        self, runner_vm, remaining_lifecycle_case
    ):
        case = remaining_lifecycle_case
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        case["owner"].before_vm_create(case["name"])
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--cloud-init-mode",
            "net",
            "--nocloud-net-port",
            "9999",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1 and data["status"] == "error"
        assert data["data"] is None and data["error"]["code"] == "validation.failed"
        assert "reserved gateway TCP 8000" in data["error"]["message"]
        assert (
            _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before
        )

    def test_user_data_script_executes(
        self, runner_vm, remaining_lifecycle_case, tmp_path
    ):
        """Retain the existing script seed check; Alpine client execution is not asserted."""
        case = remaining_lifecycle_case
        path = tmp_path / "user-data.sh"
        script = "#!/bin/sh\ntouch /tmp/user-data-sentinel\n"
        path.write_text(script, encoding="utf-8")
        case["owner"].before_vm_create(case["name"])
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--cloud-init-mode",
            "inject",
            "--cloudinit-config",
            str(path),
            timeout=180,
        )
        case["owner"].vm_created(case["name"])
        assert (
            _run_mvm(
                runner_vm,
                "exec",
                case["name"],
                "--",
                "cat",
                "/var/lib/cloud/seed/nocloud-net/user-data",
            ).stdout
            == script
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

    def test_create_with_volume(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_volume_create(case["volume"])
        volume = _json_success_data(
            _run_mvm(
                runner_vm, "volume", "create", case["volume"], "512M", "--json"
            ).stdout,
            dict,
        )
        case["owner"].volume_created(case["volume"], published=volume)
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--volume",
                case["volume"],
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and volume["id"] in created[0]["volume_ids"]
        data = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", case["volume"], "--json").stdout,
            dict,
        )
        assert data["volume"]["status"] == "attached"
        assert data["attachment"]["vm_id"] == created[0]["id"]
        sectors = _run_mvm(
            runner_vm, "exec", case["name"], "--", "cat", "/sys/class/block/vdb/size"
        ).stdout
        assert int(sectors.strip()) * 512 == 512 * 1024 * 1024

    def test_attach_detach_volume(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_volume_create(case["volume"])
        _run_mvm(runner_vm, "volume", "create", case["volume"], "512M")
        case["owner"].volume_created(case["volume"])
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        _run_mvm(runner_vm, "vm", "stop", case["name"], "--force")
        _run_mvm(runner_vm, "volume", "attach", case["name"], case["volume"])
        attached = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", case["volume"], "--json").stdout,
            dict,
        )
        assert attached["volume"]["status"] == "attached"
        assert attached["attachment"]["vm_id"] == created[0]["id"]
        _run_mvm(runner_vm, "vm", "start", case["name"])
        sectors = _run_mvm(
            runner_vm, "exec", case["name"], "--", "cat", "/sys/class/block/vdb/size"
        ).stdout
        assert int(sectors.strip()) * 512 == 512 * 1024 * 1024
        _run_mvm(runner_vm, "vm", "stop", case["name"], "--force")
        _run_mvm(runner_vm, "volume", "detach", case["name"], case["volume"])
        detached = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", case["volume"], "--json").stdout,
            dict,
        )
        assert detached["volume"]["status"] == "available"
        assert detached["attachment"]["vm_id"] is None

    def test_attach_volume_to_running_vm_succeeds(
        self, runner_vm, remaining_lifecycle_case
    ):
        """Live attach needs PCI transport and must expose a real guest block device."""
        case = remaining_lifecycle_case
        case["owner"].before_volume_create(case["volume"])
        _run_mvm(runner_vm, "volume", "create", case["volume"], "512M")
        case["owner"].volume_created(case["volume"])
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *(arg for arg in _REMAINING_VM_ARGS if arg != "--no-pci"),
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert created[0]["pci_enabled"] is True
        _run_mvm(runner_vm, "volume", "attach", case["name"], case["volume"])
        data = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", case["volume"], "--json").stdout,
            dict,
        )
        assert data["volume"]["status"] == "attached"
        assert data["attachment"]["vm_id"] == created[0]["id"]
        deadline = time.monotonic() + 15
        while True:
            sectors = _run_mvm(
                runner_vm,
                "exec",
                case["name"],
                "--",
                "cat",
                "/sys/class/block/vdb/size",
                check=False,
            )
            if sectors.returncode == 0:
                assert int(sectors.stdout.strip()) * 512 == 512 * 1024 * 1024
                break
            assert time.monotonic() < deadline, (sectors.stdout, sectors.stderr)
            time.sleep(0.5)

    def test_detach_volume_from_running_vm_succeeds(
        self, runner_vm, remaining_lifecycle_case
    ):
        """Detach a boot-attached PCI volume while the owned VM remains running."""
        case = remaining_lifecycle_case
        case["owner"].before_volume_create(case["volume"])
        _run_mvm(runner_vm, "volume", "create", case["volume"], "512M")
        case["owner"].volume_created(case["volume"])
        case["owner"].before_vm_create(case["name"])
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *(arg for arg in _REMAINING_VM_ARGS if arg != "--no-pci"),
            "--network",
            case["network"],
            "--cloud-init-mode",
            "off",
            "--volume",
            case["volume"],
            timeout=180,
        )
        case["owner"].vm_created(case["name"])
        assert (
            _run_mvm(
                runner_vm, "exec", case["name"], "--", "test", "-b", "/dev/vdb"
            ).returncode
            == 0
        )
        _run_mvm(runner_vm, "volume", "detach", case["name"], case["volume"])
        data = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", case["volume"], "--json").stdout,
            dict,
        )
        assert data["volume"]["status"] == "available"
        assert data["attachment"]["vm_id"] is None
        deadline = time.monotonic() + 15
        while True:
            removed = _run_mvm(
                runner_vm,
                "exec",
                case["name"],
                "--",
                "test",
                "!",
                "-e",
                "/sys/class/block/vdb",
                check=False,
            )
            if removed.returncode == 0:
                break
            assert time.monotonic() < deadline, (removed.stdout, removed.stderr)
            time.sleep(0.5)
        vm = next(
            row
            for row in _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
            if row["name"] == case["name"]
        )
        assert vm["status"] == "running"

    def test_create_volume_by_id_prefix(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_volume_create(case["volume"])
        volume = _json_success_data(
            _run_mvm(
                runner_vm, "volume", "create", case["volume"], "512M", "--json"
            ).stdout,
            dict,
        )
        case["owner"].volume_created(case["volume"], published=volume)
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--volume",
                volume["id"][:6],
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and volume["id"] in created[0]["volume_ids"]
        data = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", case["volume"], "--json").stdout,
            dict,
        )
        assert data["volume"]["status"] == "attached"
        assert data["attachment"]["vm_id"] == created[0]["id"]

    @pytest.mark.destructive
    def test_rm_transitions_volume_to_available(
        self, runner_vm, remaining_lifecycle_case
    ):
        case = remaining_lifecycle_case
        case["owner"].before_volume_create(case["volume"])
        _run_mvm(runner_vm, "volume", "create", case["volume"], "512M")
        case["owner"].volume_created(case["volume"])
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--volume",
                case["volume"],
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        attached = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", case["volume"], "--json").stdout,
            dict,
        )
        assert attached["volume"]["status"] == "attached"
        assert attached["attachment"]["vm_id"] == created[0]["id"]
        _run_mvm(
            runner_vm,
            "vm",
            "rm",
            case["owner"].owned_vm_id(case["name"]),
            "--force",
            timeout=120,
        )
        detached = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", case["volume"], "--json").stdout,
            dict,
        )
        assert detached["volume"]["status"] == "available"
        assert detached["attachment"]["vm_id"] is None


# ========================================================================
# TestVMCreateConsole — L2: --console flag enables console relay
# ========================================================================


class TestVMCreateConsole:
    """Create VM with --console flag — enable_console=true in ls JSON."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_console(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--console",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["enable_console"] is True
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["name"], "--json").stdout,
            dict,
        )
        assert data["console"]["relay_running"] is True
        assert data["console"]["relay_pid"] > 0
        assert data["console"]["relay_socket_path"]


# ========================================================================
# TestVMCreateCount — L2: --count N creates N VMs
# ========================================================================


class TestVMCreateCount:
    """Create N VMs with --count flag — verify N VMs in ls --json."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_count(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(*case["vm_names"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--count",
                "3",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(*case["vm_names"], published=created)
        assert len(created) == 3
        assert {row["name"] for row in created} == set(case["vm_names"])
        listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        cohort = [row for row in listed if row["name"] in case["vm_names"]]
        assert {row["id"] for row in cohort} == {row["id"] for row in created}
        assert len(cohort) == 3
        for vm in cohort:
            assert vm["status"] == "running"
            assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (
                1,
                256,
                512,
            )
            assert (
                _run_mvm(
                    runner_vm, "exec", vm["name"], "--", "printf", vm["name"]
                ).stdout
                == vm["name"]
            )


# ========================================================================
# TestVMCreateAtomicCount — L2: --atomic --count N all-or-nothing
# ========================================================================


class TestVMCreateAtomicCount:
    """Create VMs with --atomic --count N — all running (happy path)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_atomic_count(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        expected = set(case["vm_names"][:2])
        case["owner"].before_vm_create(*case["vm_names"][:2])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--atomic",
                "--count",
                "2",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(*case["vm_names"][:2], published=created)
        assert len(created) == 2
        assert {row["name"] for row in created} == expected
        listed = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        cohort = [row for row in listed if row["name"] in expected]
        assert len(cohort) == 2
        assert {row["id"] for row in cohort} == {row["id"] for row in created}
        for vm in cohort:
            assert vm["status"] == "running"
            assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (
                1,
                256,
                512,
            )
            assert (
                _run_mvm(
                    runner_vm, "exec", vm["name"], "--", "printf", vm["name"]
                ).stdout
                == vm["name"]
            )


# ========================================================================
# TestVMCreateCountIP — L1: --count with --ip rejected
# ========================================================================


class TestVMCreateCountIP:
    """Reject --count with --ip — mutually exclusive."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    def test_count_with_ip_rejected(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        ip = ".".join(_unique_subnet(case["network"]).split(".")[:3]) + ".50"
        case["owner"].before_vm_create(*case["vm_names"][:2])
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--count",
            "2",
            "--ip",
            ip,
            "--json",
            check=False,
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1 and data["status"] == "error"
        assert data["data"] is None and data["error"]["code"] == "vm.create.failed"
        assert data["error"]["class"] == 1
        assert data["error"]["message"] == "Cannot specify --ip with --count > 1"
        assert (
            _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before
        )


# ========================================================================
# TestVMCreateCountMAC — L1: --count with --mac rejected
# ========================================================================


class TestVMCreateCountMAC:
    """Reject --count with --mac — mutually exclusive."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    def test_count_with_mac_rejected(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        case["owner"].before_vm_create(*case["vm_names"][:2])
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--count",
            "2",
            "--mac",
            "aa:bb:cc:dd:ee:ff",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1 and data["status"] == "error"
        assert data["data"] is None and data["error"]["code"] == "vm.create.failed"
        assert data["error"]["class"] == 1
        assert data["error"]["message"] == "Cannot specify --mac with --count > 1"
        assert (
            _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before
        )


# ========================================================================
# TestVMCreateCountNegative — L1: --count -1 rejected
# ========================================================================


class TestVMCreateCountNegative:
    """Reject --count -1 — must be at least 1."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
    ]

    def test_count_negative_rejected(self, runner_vm, remaining_lifecycle_case):
        case = remaining_lifecycle_case
        before = _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout)
        case["owner"].before_vm_create(case["name"])
        result = _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--count",
            "-1",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        data = json.loads(result.stdout)
        assert data["schema_version"] == 1 and data["status"] == "error"
        assert data["data"] is None and data["error"]["code"] == "vm.create.failed"
        assert data["error"]["class"] == 1
        assert data["error"]["message"] == "--count must be at least 1"
        assert (
            _parse_vm_list(_run_mvm(runner_vm, "vm", "ls", "--json").stdout) == before
        )


# ========================================================================
# TestVMCreateForce — L2: --force skips confirmation on create
# ========================================================================


class TestVMCreateForce:
    """Create VM with --force flag — skip confirmation prompts."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_force(self, runner_vm, remaining_lifecycle_case):
        """Force suppresses the skip-cleanup prompt on the installed public route."""
        case = remaining_lifecycle_case
        case["owner"].before_vm_create(case["name"])
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                case["name"],
                *_REMAINING_VM_ARGS,
                "--network",
                case["network"],
                "--cloud-init-mode",
                "off",
                "--force",
                "--skip-cleanup",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        case["owner"].vm_created(case["name"], published=created)
        assert len(created) == 1 and created[0]["name"] == case["name"]
        assert created[0]["status"] == "running"


# ========================================================================
# TestVMCreateVsockPort — L2: --vsock-port creates VM with custom port
# ========================================================================


class TestVMCreateVsockPort:
    """Create VM with --vsock-port flag — verify VM running."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_create_with_vsock_port(self, runner_vm, remaining_lifecycle_case):
        """The custom agent port survives inspect and handles a real exec request."""
        case = remaining_lifecycle_case
        port = "54321"
        case["owner"].before_vm_create(case["name"])
        _run_mvm(
            runner_vm,
            "vm",
            "create",
            case["name"],
            *_REMAINING_VM_ARGS,
            "--network",
            case["network"],
            "--cloud-init-mode",
            "off",
            "--vsock-port",
            port,
            timeout=180,
        )
        case["owner"].vm_created(case["name"])
        data = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["name"], "--json").stdout,
            dict,
        )
        assert data["vsock"]["port"] == int(port)
        assert (
            _run_mvm(
                runner_vm,
                "exec",
                case["name"],
                "--port",
                port,
                "--",
                "printf",
                "custom-port-ready",
            ).stdout
            == "custom-port-ready"
        )
