"""Explicit two-process host-reboot proof; PRE alone is not a journey pass."""

from __future__ import annotations

import hashlib
import json
import os
import re
import socket
import stat
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import _json_success_data, _run_mvm
from tests.system.host.test_host import _host_administrator_state, _host_inventory
from tests.system.network.test_network import _inventory
from tests.system.vm.test_vm_lifecycle import (
    _lifecycle_process_identity, _lifecycle_vm_state,
)

pytestmark = [pytest.mark.system, pytest.mark.tier2, pytest.mark.domain_host_reboot]
RUNNER = "mvmctl-030-test"
_SOURCE_FILES = ("tests/__init__.py", "tests/system/__init__.py", "tests/system/conftest.py",
                 "tests/system/pytest.ini", "tests/system/host/__init__.py", "tests/system/host/test_host.py",
                 "tests/system/host/test_host_reboot.py", "tests/system/network/__init__.py",
                 "tests/system/network/test_network.py", "tests/system/vm/__init__.py",
                 "tests/system/vm/test_vm_lifecycle.py")
_FIELDS = {
    "schema_version", "source", "candidate", "boot_id", "vm", "process", "guest_boot_id",
    "marker", "baseline", "physical", "administrator", "kernel", "image", "vm_paths",
}


def _boot() -> str:
    value = Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    assert str(uuid.UUID(value)) == value
    return value


def _host_reboot_guest_boot_id(name: str) -> str:
    """Allow the cold guest agent to connect before proving its canonical boot ID."""
    boot_id = _run_mvm(
        RUNNER, "exec", name, "--timeout", "30", "--", "cat", "/proc/sys/kernel/random/boot_id",
    ).stdout.strip()
    assert str(uuid.UUID(boot_id)) == boot_id, boot_id
    return boot_id


def _phase_task(phase: str) -> Path:
    assert os.geteuid() != 0 and socket.gethostname() == RUNNER
    assert os.environ.get("MVM_TEST_VM") == os.environ.get("MVM_TEST_QUALIFICATION_HOST") == RUNNER
    assert os.environ.get("MVM_HOST_REBOOT_PHASE") == phase, "explicit host_reboot phase required"
    task = Path(os.environ["MVM_HOST_REBOOT_TASK"])
    assert re.fullmatch(r"/home/runner/\.cache/mvmctl-host-reboot-[0-9a-f]{32}", str(task))
    row = task.lstat()
    assert stat.S_ISDIR(row.st_mode) and row.st_uid == os.getuid()
    assert stat.S_IMODE(row.st_mode) == 0o700
    return task


def _file(path: Path) -> dict:
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        row = os.fstat(fd)
        assert stat.S_ISREG(row.st_mode) and row.st_nlink == 1
        with os.fdopen(os.dup(fd), "rb") as stream:
            digest = hashlib.file_digest(stream, "sha256").hexdigest()
        stable = lambda info: (info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode,
                               info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)
        assert stable(os.fstat(fd)) == stable(row) == stable(path.lstat())
        return dict(identity=[row.st_dev, row.st_ino, row.st_uid, row.st_gid,
                              stat.S_IMODE(row.st_mode), row.st_nlink, row.st_size], sha256=digest)
    finally:
        os.close(fd)


def _candidate() -> dict:
    identity = _file(Path("/usr/local/bin/mvm"))
    assert identity["identity"][2:6] == [0, 0, 0o755, 1]
    assert identity["sha256"] == os.environ["MVM_HOST_REBOOT_CANDIDATE_SHA256"]
    version = _run_mvm(RUNNER, "--version").stdout.strip()
    assert version == os.environ["MVM_HOST_REBOOT_CANDIDATE_VERSION"]
    return dict(**identity, version=version)


def _source() -> dict:
    revision = os.environ["MVM_HOST_REBOOT_SOURCE_REVISION"]
    assert re.fullmatch(r"[0-9a-f]{40}", revision)
    root = Path(__file__).resolve().parents[3]
    return dict(revision=revision, test_sha256=_file(Path(__file__))["sha256"],
                payload={relative: _file(root / relative)["sha256"] for relative in _SOURCE_FILES})


def _write_receipt(task: Path, name: str, data: dict) -> None:
    encoded = (json.dumps(data, sort_keys=True) + "\n").encode()
    assert len(encoded) <= 128 * 1024
    fd = os.open(task / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(encoded)
        stream.flush()
        os.fsync(stream.fileno())
    directory = os.open(task, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def _read_pre(task: Path) -> dict:
    fd = os.open(task / "pre.json", os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as stream:
        row = os.fstat(stream.fileno())
        assert stat.S_ISREG(row.st_mode) and row.st_uid == os.getuid() and row.st_nlink == 1
        assert stat.S_IMODE(row.st_mode) == 0o600 and row.st_size <= 128 * 1024
        def unique(pairs):
            result = {}
            for key, value in pairs:
                assert key not in result, key
                result[key] = value
            return result
        data = json.loads(stream.read(128 * 1024 + 1), object_pairs_hook=unique)
        assert stream.tell() == row.st_size
        current = os.fstat(stream.fileno())
        assert (current.st_dev, current.st_ino, current.st_size, current.st_mtime_ns, current.st_ctime_ns) == (
            row.st_dev, row.st_ino, row.st_size, row.st_mtime_ns, row.st_ctime_ns)
    assert set(data) == _FIELDS and type(data["schema_version"]) is int and data["schema_version"] == 1
    assert data["source"] == _source() and data["candidate"] == _candidate()
    assert str(uuid.UUID(data["boot_id"])) == data["boot_id"]
    assert str(uuid.UUID(data["guest_boot_id"])) == data["guest_boot_id"]
    assert set(data["vm"]) == {"id", "name", "created_at"}
    assert re.fullmatch(r"[0-9a-f]{32}", data["vm"]["id"])
    assert data["vm"]["name"] == "sys-host-reboot-" + task.name.removeprefix("mvmctl-host-reboot-")
    assert re.fullmatch(r"[0-9a-f]{32}", data["marker"])
    assert len(data["process"]) == 2 and all(type(n) is int and n > 0 for n in data["process"])
    return data


def _owned_row(receipt: dict) -> dict:
    rows = [row for row in _host_inventory(RUNNER, "vm") if row["id"] == receipt["vm"]["id"]]
    assert len(rows) == 1
    assert all(rows[0][key] == value for key, value in receipt["vm"].items())
    return rows[0]


def _preserve_persistent_inventory(task: Path, before: dict, after_boot: dict, final: dict) -> None:
    # Retain literal maps before asserting so a lost policy/snapshot remains diagnosable.
    _write_receipt(task, "preservation.json", dict(before=before, after_boot=after_boot, final=final))
    for domain in ("image", "kernel", "bin", "volume", "key", "snapshot", "config", "traffic", "exec"):
        assert after_boot[domain] == final[domain] == before[domain], domain
    # Namespace inode, ifindex, links/addresses/routes/firewall/mounts are boot-physical
    # observations, not durable records. Their raw values remain in preservation.json.
    # Owned VM and original network differences are checked separately by exact ID.


def test_host_reboot_pre() -> None:
    """Retain one running owned VM; this phase cannot claim reboot success."""
    task = _phase_task("pre")
    assert not (task / "pre.json").exists()
    candidate, source = _candidate(), _source()
    baseline = {domain: _host_inventory(RUNNER, domain) for domain in
                ("vm", "network", "image", "kernel", "bin", "key", "volume", "config")}
    assert baseline["vm"] == [], "dedicated quiescent runner required"
    assert len(baseline["network"]) == len({row["id"] for row in baseline["network"]}) == 2
    defaults = [row for row in baseline["network"] if row["is_default"]]
    runner_networks = [row for row in baseline["network"] if not row["is_default"]]
    assert len(defaults) == len(runner_networks) == 1
    assert runner_networks[0]["name"] == "sys-test-net"
    assert all(row["bridge_active"] and row["is_present"] for row in baseline["network"])
    assert os.environ.get("MVM_ASSET_MIRROR") and Path(os.environ["MVM_ASSET_MIRROR"]).is_dir()
    for member in ("firecracker", "jailer"):
        assert any(row["type"] == member and row["version"] == "1.17.0" and row["is_default"]
                   and row["is_present"] for row in baseline["bin"])
    kernels = [row for row in baseline["kernel"] if row["id"] == os.environ["MVM_RUNNER_KERNEL_ID"]]
    images = [row for row in baseline["image"] if row["type"] == "alpine" and row["version"] == "3.23"
              and row["is_present"]]
    assert len(baseline["kernel"]) == len(kernels) == len(images) == 1 and kernels[0]["is_present"]
    kernel, image = _file(Path(kernels[0]["path"])), _file(Path(images[0]["path"]))
    assert kernel["sha256"] == os.environ["MVM_RUNNER_KERNEL_SHA256"]
    assert _json_success_data(_run_mvm(RUNNER, "config", "get", "settings", "guestfs_enabled", "--json").stdout, bool) is False
    physical, administrator = _inventory(RUNNER), _host_administrator_state(RUNNER)
    name = "sys-host-reboot-" + task.name.removeprefix("mvmctl-host-reboot-")
    marker = uuid.uuid4().hex
    vm = None
    try:
        rows = _json_success_data(_run_mvm(
            RUNNER, "vm", "create", name, "--image", images[0]["id"], "--kernel", kernels[0]["id"],
            "--no-network", "--cloud-init-mode", "off", "--skip-deblob", "--no-pci",
            "--mem", "256M", "--vcpu", "1", "--disk-size", "512M", "--json", timeout=180,
        ).stdout, list)
        assert len(rows) == 1 and rows[0]["name"] == name
        vm = rows[0]
        assert vm["network_placement"] == "networkless" and vm["network_id"] == ""
        assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (1, 256, 512)
        state = _lifecycle_vm_state(RUNNER, name, "running")
        guest_boot = _host_reboot_guest_boot_id(name)
        process = _lifecycle_process_identity(state)
        assert any(row["id"] == vm["id"] for row in _json_success_data(_run_mvm(RUNNER, "vm", "ps", "--json").stdout, list))
        _run_mvm(RUNNER, "exec", vm["id"], "--", f"printf %s {marker} > /root/host-reboot-marker && sync")
        inspection = _json_success_data(_run_mvm(RUNNER, "vm", "inspect", vm["id"], "--json").stdout, dict)
        receipt = dict(schema_version=1, source=source, candidate=candidate, boot_id=_boot(),
                       vm={key: vm[key] for key in ("id", "name", "created_at")}, process=list(process),
                       guest_boot_id=guest_boot, marker=marker, baseline=baseline, physical=physical,
                       administrator=administrator, kernel=kernel, image=image,
                       vm_paths={key: inspection["filesystem"][key] for key in ("vm_dir", "rootfs_path")})
        _write_receipt(task, "pre.json", receipt)
        print("PRE PHASE COMPLETE; RELEASE VERDICT INCOMPLETE", flush=True)
    except BaseException:
        # The only admissible cleanup identity is a successful exact create result.
        if vm is not None:
            _run_mvm(RUNNER, "vm", "rm", vm["id"], "--force", timeout=120)
        raise


def test_host_reboot_post() -> None:
    """Prior-boot stopped projection, same-ID restart/exec and exact owned cleanup."""
    task = _phase_task("post")
    receipt = _read_pre(task)
    assert _boot() != receipt["boot_id"], "a nested VM reboot is not a host reboot"
    before_post = _inventory(RUNNER)
    row = _owned_row(receipt)
    assert row["status"] == "stopped"
    _lifecycle_vm_state(RUNNER, row["name"], "stopped")
    assert all(vm["id"] != row["id"] for vm in _json_success_data(_run_mvm(RUNNER, "vm", "ps", "--json").stdout, list))
    denied = _run_mvm(RUNNER, "exec", row["id"], "--", "uname", "-s", check=False)
    assert denied.returncode != 0
    try:
        _run_mvm(RUNNER, "vm", "start", row["id"], timeout=120)
        started = _lifecycle_vm_state(RUNNER, row["name"], "running")
        assert started["id"] == receipt["vm"]["id"]
        process = _lifecycle_process_identity(started)
        guest_boot = _host_reboot_guest_boot_id(row["name"])
        assert guest_boot != receipt["guest_boot_id"]
        assert _run_mvm(RUNNER, "exec", row["id"], "--", "cat", "/root/host-reboot-marker").stdout == receipt["marker"]
    finally:
        _owned_row(receipt)
        _run_mvm(RUNNER, "vm", "rm", row["id"], "--force", timeout=120)
    assert all(vm["id"] != row["id"] for vm in _host_inventory(RUNNER, "vm"))
    assert all(vm["id"] != row["id"] for vm in _json_success_data(_run_mvm(RUNNER, "vm", "ps", "--json").stdout, list))
    for path in (*receipt["vm_paths"].values(), f"/run/mvmctl/runtime/{os.getuid()}/{row['id']}"):
        assert not Path(path).exists()
    original_networks = receipt["baseline"]["network"]
    originals = {item["id"]: item for item in original_networks}
    networks = _host_inventory(RUNNER, "network")
    current = {item["id"]: item for item in networks}
    assert len(networks) == len(current) == len(original_networks) == len(originals) == 2
    assert current.keys() == originals.keys()
    # Boot-derived absence only; never repair a same-boot disappearance.
    for network_id, original in originals.items():
        assert {key: value for key, value in current[network_id].items() if key not in ("bridge_active", "is_present")} == {
            key: value for key, value in original.items() if key not in ("bridge_active", "is_present")}
        assert current[network_id]["bridge_active"] is False and current[network_id]["is_present"] is False
    for network_id in originals:
        _run_mvm(RUNNER, "network", "sync", network_id)
    restored_networks = _host_inventory(RUNNER, "network")
    restored = {item["id"]: item for item in restored_networks}
    assert len(restored_networks) == len(restored) == len(originals) and restored.keys() == originals.keys()
    for network_id, original in originals.items():
        assert restored[network_id]["bridge_active"] is True and restored[network_id]["is_present"] is True
        assert {key: value for key, value in restored[network_id].items() if key != "updated_at"} == {
            key: value for key, value in original.items() if key != "updated_at"}
    final = _inventory(RUNNER)
    _preserve_persistent_inventory(task, receipt["physical"], before_post, final)
    for domain, expected in receipt["baseline"].items():
        if domain != "network":
            assert _host_inventory(RUNNER, domain) == expected, domain
    assert _host_administrator_state(RUNNER) == receipt["administrator"]
    assert _candidate() == receipt["candidate"]
    assert _file(Path(receipt["baseline"]["kernel"][0]["path"])) == receipt["kernel"]
    alpine = next(item for item in receipt["baseline"]["image"] if item["type"] == "alpine" and item["version"] == "3.23")
    assert _file(Path(alpine["path"])) == receipt["image"]
    _write_receipt(task, "post.json", dict(schema_version=1, host_boot_id=_boot(), vm_id=row["id"],
        source=_source(), candidate=_candidate(), process=list(process), guest_boot_id=guest_boot,
        owned_cleanup=True, network_before=original_networks, network_after=restored_networks,
        physical_before=receipt["physical"], physical_after_boot=before_post, physical_after=final))
