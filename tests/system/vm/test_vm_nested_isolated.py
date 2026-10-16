"""Tests executed DIRECTLY ON THE HOST using a function-owned fenv VM.

No runner VM -- this test creates a Firecracker VM on the host directly
(double-nested: host -> fenv VM -> nested VM), then runs every test command
via vsock exec into the fenv VM from the host.

Architecture::

    Host (runs mvm directly)
        |
        v  Enrolled runner + fixture-owned second operator; root administrator setup
        +-- mvm host status --json               - isolated state
        +-- mvm config set/get/reset              - isolated config
        +-- mvm key create/ls/rm                  - isolated keys
        +-- mvm vol create/resize/inspect         - isolated disk
        +-- mvm network create/ls/rm              - isolated network
        +-- mvm vm create ... --kernel firecracker - 3rd level VM
        +-- unprivileged user tests               - non-root boundaries
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import shlex
import subprocess
import uuid
from pathlib import Path
from typing import Generator

import pytest

from tests.system.conftest import _json_success_data, _run_mvm_host, _unique_subnet
from tests.system.host.test_host_admin import _require_error, _STATE_PROBE

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
    pytest.mark.slow,
    pytest.mark.requires_kvm,
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

IMAGE_SELECTOR = "mvm-test-runner"
KERNEL_SELECTOR = "official:7.0.11"
KERNEL_FEATURES = "kvm,nftables,tuntap,btrfs"

SSH_TIMEOUT = 300.0
VM_CREATE_TIMEOUT = 600

FENV_VCPUS = 4
FENV_MEM = "4g"
FENV_DISK = "30g"

# ---------------------------------------------------------------------------
# Host-level helpers
# ---------------------------------------------------------------------------


def _check_nested_virt_support() -> bool:
    """Check if the HOST has nested VMX/SVM enabled."""
    for module in ("kvm_intel", "kvm_amd"):
        param_file = Path(f"/sys/module/{module}/parameters/nested")
        if param_file.exists():
            value = param_file.read_text().strip().lower()
            if value in ("1", "y"):
                return True
    return False


def _resolve_image_id(selector: str, candidate_version: str) -> str | None:
    """Resolve the prepared base prefix at the exact candidate version."""
    result = _run_mvm_host("image", "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        return None
    images = _json_success_data(result.stdout, list)
    sel_type = selector.split(":")[0] if ":" in selector else selector
    matches = [
        i
        for i in images
        if i.get("type", "").startswith(sel_type)
        and i.get("version") == candidate_version
        and i.get("is_present")
    ]
    if not matches:
        return None
    assert len(matches) == 1, ("ambiguous current runner base", matches)
    return matches[0]["id"]


def _resolve_official_kernel_id() -> str | None:
    """Check if the official:7.0.11 kernel is already cached on the host."""
    result = _run_mvm_host("kernel", "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        return None
    kernels = _json_success_data(result.stdout, list)
    matches = [
        k
        for k in kernels
        if k.get("type") == "official"
        and k.get("version") == "7.0.11"
        and k.get("is_present")
    ]
    if not matches:
        return None
    return matches[0]["id"]


def _wait_for_ssh_host(
    vm_name: str, user: str = "root", timeout: float = 300
) -> bool:
    """Poll vsock exec until available or timeout."""
    import time as _time

    start = _time.monotonic()
    while _time.monotonic() - start < timeout:
        try:
            result = _run_mvm_host(
                "exec", vm_name, "--user", user,
                "--timeout", "10", "--", "exit",
                check=False, timeout=15,
            )
            if result.returncode == 0:
                return True
        except subprocess.TimeoutExpired:
            pass
        _time.sleep(0.5)
    return False


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def built_binary() -> Path:
    """The candidate must already be installed on this qualification host."""
    binary = Path(os.environ.get("MVM_CANDIDATE_BINARY", "/usr/local/bin/mvm")).resolve()
    assert binary.is_file() and os.access(binary, os.X_OK), binary
    assert hashlib.sha256(binary.read_bytes()).digest() == hashlib.sha256(
        Path("/usr/local/bin/mvm").read_bytes()
    ).digest(), "candidate and canonical qualification-host binary differ"
    return binary


@pytest.fixture
def fenv_vm(built_binary: Path) -> Generator[tuple[str, str, str], None, None]:
    """One function-owned child; ordinary operations use enrolled non-root accounts."""
    assert _check_nested_virt_support(), (
        "Nested virtualization not supported on host "
        "(kvm_intel/kvm_amd nested=Y/1 not detected)"
    )
    operator = "runner"
    second_operator = f"fenvop{uuid.uuid4().hex[:12]}"
    vm_name = f"sys-fenv-{uuid.uuid4().hex[:8]}"
    net_name = f"sys-fenv-net-{uuid.uuid4().hex[:6]}"
    key_name = f"sys-fenv-key-{uuid.uuid4().hex[:6]}"
    baseline = {
        domain: _json_success_data(_run_mvm_host(domain, "ls", "--json").stdout, list)
        for domain in ("vm", "network", "key", "kernel", "volume", "image")
    }
    for domain, name in (("vm", vm_name), ("network", net_name), ("key", key_name)):
        assert all(row["name"] != name for row in baseline[domain]), (domain, name)
    version_output = _run_mvm_host("--version").stdout.strip()
    assert version_output.startswith("mvm "), version_output
    candidate_version = version_output.removeprefix("mvm ")
    candidate_sha = hashlib.sha256(built_binary.read_bytes()).hexdigest()
    image_id = _resolve_image_id(IMAGE_SELECTOR, candidate_version)
    assert image_id is not None, (
        f"Prepare a current {IMAGE_SELECTOR}:{candidate_version} base; "
        "a stale imported base is not a downloadable upstream image"
    )
    base_image = next(row for row in baseline["image"] if row["id"] == image_id)
    mirror_rows = [row for row in baseline["volume"] if row["name"] == "asset-mirror"]
    assert len(mirror_rows) == 1, mirror_rows
    mirror_volume = mirror_rows[0]
    assert mirror_volume["is_read_only"] and mirror_volume["is_shareable"], mirror_volume
    mirror_id = mirror_volume["id"]
    mirror_inspect = _json_success_data(
        _run_mvm_host("volume", "inspect", mirror_id, "--json").stdout, dict
    )
    assert mirror_inspect["volume"]["id"] == mirror_id
    assert mirror_inspect["volume"]["is_read_only"] is True

    expected_receipt = None
    if base_image["type"] != IMAGE_SELECTOR:
        imported_id = os.environ.get("MVM_RUNNER_KERNEL_ID")
        imported_sha = os.environ.get("MVM_RUNNER_KERNEL_SHA256")
        assert imported_id and imported_sha, "imported base requires its existing explicit kernel binding"
        assert base_image["type"] == f"{IMAGE_SELECTOR}-kernel-{imported_id}", base_image
        imported = _json_success_data(
            _run_mvm_host("kernel", "inspect", imported_id, "--json").stdout, dict
        )
        kernel = imported["kernel"]
        assert kernel["id"] == imported_id and kernel["type"] == "custom"
        assert kernel["version"] == "7.0.11" and kernel["arch"] == "x86_64"
        kernel_path = Path(imported["storage"]["path"])
        assert hashlib.sha256(kernel_path.read_bytes()).hexdigest() == imported_sha
        elf_notes = subprocess.run(
            ["readelf", "--wide", "-n", str(kernel_path)],
            capture_output=True, text=True, check=True,
        ).stdout
        build_id = re.search(r"Build ID:\s+([0-9a-f]+)", elf_notes)
        assert build_id is not None, elf_notes
        expected_receipt = {
            "receipt_schema": 1, "candidate_version": candidate_version,
            "candidate_sha256": candidate_sha, "kernel_id": imported_id,
            "kernel_sha256": imported_sha, "kernel_version": kernel["version"],
            "kernel_arch": kernel["arch"], "kernel_build_id": build_id.group(1),
        }

    attempted = {"vm": False, "network": False, "key": False}
    receipts = {}
    try:
        kernel_id = _resolve_official_kernel_id()
        if kernel_id is None:
            mirror = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser()
            assert (mirror / "linux-7.0.11.tar.xz").is_file(), "official kernel source must already be mirrored"
            pull = _run_mvm_host(
                "kernel", "pull", KERNEL_SELECTOR, "--features", KERNEL_FEATURES,
                check=False, timeout=300,
            )
            assert pull.returncode == 0, pull.stderr
            output = pull.stdout + pull.stderr
            assert "Mirror checksum mismatch" not in output and "Copied to asset mirror" not in output
            assert "Using local mirror for download" in output or "Using cached kernel build" in output
            kernel_id = _resolve_official_kernel_id()
            assert kernel_id is not None, "official:7.0.11 was not registered after the genuine pull"

        attempted["network"] = True
        receipts["network"] = _json_success_data(_run_mvm_host(
            "network", "create", net_name, "--subnet", _unique_subnet(net_name), "--json",
        ).stdout, dict)["id"]
        attempted["key"] = True
        receipts["key"] = _json_success_data(_run_mvm_host(
            "key", "create", key_name, "--algorithm", "ed25519", "--json",
        ).stdout, dict)["id"]

        # Record creation intent before the CLI can publish a partial VM.
        attempted["vm"] = True
        created = _json_success_data(_run_mvm_host(
            "vm", "create", vm_name, "--image", image_id, "--kernel", kernel_id,
            "--vcpu", str(FENV_VCPUS), "--mem", FENV_MEM, "-s", FENV_DISK,
            "--nested-virt", "--network", receipts["network"], "--ssh-key", receipts["key"],
            "--user", operator, "--volume", mirror_id, "--json", timeout=VM_CREATE_TIMEOUT,
        ).stdout, list)
        assert len(created) == 1 and created[0]["name"] == vm_name, created
        receipts["vm"] = created[0]["id"]
        vms = _json_success_data(_run_mvm_host("vm", "ls", "--json").stdout, list)
        vm_entry = next((row for row in vms if row["id"] == receipts["vm"]), None)
        assert vm_entry is not None and vm_entry["status"] == "running", vms
        assert _wait_for_ssh_host(vm_name, operator, timeout=SSH_TIMEOUT), vm_name

        # Check provenance and exact installed bytes before administrator/user initialization.
        if expected_receipt is not None:
            receipt_text = _guest_run(
                vm_name, "head -c 4097 /opt/mvmctl-test/base-inputs.json", user=operator,
            ).stdout
            assert len(receipt_text.encode()) <= 4096
            receipt = json.loads(receipt_text)
            assert type(receipt.get("receipt_schema")) is int and receipt == expected_receipt, receipt
        installed_sha = _guest_run(
            vm_name, "sha256sum /usr/local/bin/mvm", user=operator,
        ).stdout.split()[0]
        assert installed_sha == candidate_sha
        assert _guest_run(
            vm_name, "stat -c '%u:%g:%a:%h' /usr/local/bin/mvm", user=operator,
        ).stdout.strip() == "0:0:755:1"
        assert _guest_run(
            vm_name, "/usr/local/bin/mvm --version", user=operator,
        ).stdout.strip() == version_output

        _guest_run(
            vm_name,
            "set -eu; mkdir -p /mnt; mount -o ro /dev/vdb /mnt; "
            "findmnt -n -o SOURCE,OPTIONS /mnt",
            user="root",
        )
        mount = _guest_run(vm_name, "findmnt -n -o SOURCE,OPTIONS /mnt", user=operator).stdout.split()
        assert len(mount) == 2 and mount[0] == "/dev/vdb" and "ro" in mount[1].split(","), mount

        # useradd selects the real OS UID/GID; no fixed UID pool or inherited wildcard sudoers.
        identity = _guest_run(
            vm_name,
            f"set -eu; if getent passwd {second_operator}; then exit 1; fi; "
            f"useradd --create-home --user-group --shell /bin/sh {second_operator}; "
            f"passwd --lock {second_operator}; getent passwd {second_operator}",
            user="root",
        ).stdout.strip().splitlines()[-1].split(":")
        assert identity[0] == second_operator and int(identity[2]) > 0 and int(identity[3]) > 0
        assert identity[5] == f"/home/{second_operator}"
        second_mirror = _prepare_second_operator_mirror(
            vm_name, second_operator, int(identity[2]), int(identity[3]),
        )
        for account in (operator, second_operator):
            enrolled = _json_success_data(_guest_run(
                vm_name, f"/usr/local/bin/mvm host init --operator {account} --json",
                user="root", timeout=180,
            ).stdout, dict)
            assert isinstance(enrolled["changes"], list)
            mirror_assignment = (
                f"MVM_ASSET_MIRROR={shlex.quote(second_mirror)} "
                if account == second_operator else ""
            )
            initialized = _guest_run(
                vm_name,
                mirror_assignment +
                "/usr/local/bin/mvm init --non-interactive --skip-host --skip-network "
                "--binary-version 1.17.0 --debug",
                user=account, timeout=180,
            )
            output = initialized.stdout + initialized.stderr
            assert "Downloading trusted release" not in output
            if account == second_operator:
                assert "Using local mirror for trusted release" in output, output
            pairs = _json_success_data(_guest_run(
                vm_name, "/usr/local/bin/mvm bin ls --json", user=account,
            ).stdout, list)
            assert all(any(
                row["type"] == member and row["version"] == "1.17.0"
                and row["is_present"] and row["is_default"]
                for row in pairs
            ) for member in ("firecracker", "jailer")), pairs

        yield (vm_name, operator, second_operator)
    finally:
        failures = []
        for domain, name in (("vm", vm_name), ("network", net_name), ("key", key_name)):
            if not attempted[domain]:
                continue
            try:
                rows = _json_success_data(_run_mvm_host(domain, "ls", "--json").stdout, list)
                owned = [row for row in rows if row["name"] == name]
                assert len(owned) <= 1, owned
                if owned:
                    row = owned[0]
                    assert all(row["id"] != prior["id"] for prior in baseline[domain]), row
                    assert receipts.get(domain, row["id"]) == row["id"], row
                    flags = () if domain == "key" else ("--force",)
                    _run_mvm_host(domain, "rm", row["id"], *flags, timeout=300)
                remaining = _json_success_data(_run_mvm_host(domain, "ls", "--json").stdout, list)
                assert all(row["name"] != name for row in remaining), remaining
                assert all(any(
                    row["id"] == prior["id"] and row["created_at"] == prior["created_at"]
                    for row in remaining
                ) for prior in baseline[domain]), (domain, remaining)
            except Exception as error:
                failures.append((domain, repr(error)))
        # Kernel assets and the exact shared RO mirror are borrowed, never selected for removal.
        for domain, expected in (("kernel", baseline["kernel"]), ("volume", [mirror_volume])):
            try:
                current = _json_success_data(_run_mvm_host(domain, "ls", "--json").stdout, list)
                for prior in expected:
                    assert next(row for row in current if row["id"] == prior["id"]) == prior, (domain, prior)
            except Exception as error:
                failures.append((domain + " preservation", repr(error)))
        assert not failures, failures


# ---------------------------------------------------------------------------
# Guest-level vsock helper (from host)
# ---------------------------------------------------------------------------


def _guest_run(
    vm_name: str,
    guest_cmd: str,
    *,
    check: bool = True,
    timeout: int = 30,
    user: str = "runner",
) -> subprocess.CompletedProcess[str]:
    """Run once as the selected account, with the attached read-only asset mirror."""
    command = shlex.join([
        "/usr/bin/env", "MVM_ASSET_MIRROR=/mnt", "NO_COLOR=1",
        "/bin/sh", "-c", guest_cmd,
    ])
    return _run_mvm_host(
        "exec", vm_name, "--user", user, "--timeout", "10", "--",
        command, check=check, timeout=timeout,
    )


def _prepare_second_operator_mirror(
    vm_name: str, account: str, uid: int, gid: int,
) -> str:
    """Copy one borrowed release into private, fixture-owned second-user state."""
    mirror = f"/home/{account}/.mvm-test-asset-mirror"
    prepare = r'''
import hashlib,os,shutil,stat,sys
home,uid,gid=sys.argv[1],int(sys.argv[2]),int(sys.argv[3])
assert uid>0 and gid>0
archive="firecracker-v1.17.0-x86_64.tgz"
source_path="/mnt/"+archive
fields=("st_dev","st_ino","st_uid","st_gid","st_mode","st_nlink","st_size","st_mtime_ns","st_ctime_ns")
def identity(info):
 return tuple(getattr(info,field) for field in fields)
with os.fdopen(os.open(source_path,os.O_RDONLY|os.O_NOFOLLOW),"rb") as source:
 before=os.fstat(source.fileno())
 assert stat.S_ISREG(before.st_mode) and before.st_nlink==1 and 0<before.st_size<=128*1024*1024
 receipt=identity(before)
 digest=hashlib.file_digest(source,"sha256").hexdigest()
 home_fd=os.open(home,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
 try:
  home_info=os.fstat(home_fd)
  assert home_info.st_uid==uid and home_info.st_gid==gid
  os.mkdir(".mvm-test-asset-mirror",0o700,dir_fd=home_fd)
  mirror_fd=os.open(".mvm-test-asset-mirror",os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=home_fd)
  try:
   os.fchmod(mirror_fd,0o700)
   fd=os.open(archive,os.O_RDWR|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=mirror_fd)
   with os.fdopen(fd,"w+b") as destination:
    source.seek(0)
    shutil.copyfileobj(source,destination,64*1024)
    destination.flush()
    os.fchmod(destination.fileno(),0o600)
    os.fchown(destination.fileno(),uid,gid)
    copied=os.fstat(destination.fileno())
    assert stat.S_ISREG(copied.st_mode) and copied.st_nlink==1 and copied.st_size==before.st_size
    assert (copied.st_uid,copied.st_gid,stat.S_IMODE(copied.st_mode))==(uid,gid,0o600)
    destination.seek(0)
    assert hashlib.file_digest(destination,"sha256").hexdigest()==digest
    assert identity(os.fstat(destination.fileno()))==identity(copied)
    assert identity(os.stat(archive,dir_fd=mirror_fd,follow_symlinks=False))==identity(copied)
   os.fchown(mirror_fd,uid,gid)
   directory=os.fstat(mirror_fd)
   assert (directory.st_uid,directory.st_gid,stat.S_IMODE(directory.st_mode))==(uid,gid,0o700)
   assert os.listdir(mirror_fd)==[archive]
  finally:
   os.close(mirror_fd)
 finally:
  os.close(home_fd)
 assert identity(os.fstat(source.fileno()))==receipt
 assert identity(os.stat(source_path,follow_symlinks=False))==receipt
 source.seek(0)
 assert hashlib.file_digest(source,"sha256").hexdigest()==digest
'''
    command = shlex.join(["python3", "-c", prepare, f"/home/{account}", str(uid), str(gid)])
    _guest_run(vm_name, command, user="root")
    return mirror


# ========================================================================
# TestNestedIsolated
# ========================================================================


class TestNestedIsolated:
    """Run mvm commands inside a nested VM for fully isolated testing."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
        pytest.mark.slow,
        pytest.mark.requires_kvm,
    ]

    def test_inside_guest_host_status(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Verify ``mvm host status --json`` inside the guest."""
        vm_name, _operator, _unpriv_user = fenv_vm
        result = _guest_run(vm_name, "/usr/local/bin/mvm host status --json")
        status = _json_success_data(result.stdout, dict)
        assert "kvm_accessible" in status
        assert "missing_binaries" in status
        if not status.get("kvm_accessible"):
            print("  [info] KVM not accessible inside guest")

    def test_inside_guest_config_roundtrip(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Config set/get/reset roundtrip as the enrolled non-root operator."""
        vm_name, _operator, _unpriv_user = fenv_vm
        _guest_run(vm_name, "/usr/local/bin/mvm config set defaults.vm vcpu_count 4")
        get_result = _guest_run(
            vm_name, "/usr/local/bin/mvm config get defaults.vm vcpu_count",
        )
        assert "4" in get_result.stdout

        _guest_run(vm_name, "/usr/local/bin/mvm config reset defaults.vm vcpu_count")
        get_result2 = _guest_run(
            vm_name, "/usr/local/bin/mvm config get defaults.vm vcpu_count",
        )
        assert "1" in get_result2.stdout

    def test_inside_guest_key_operations(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Key create/list/remove lifecycle inside the guest."""
        vm_name, _operator, _unpriv_user = fenv_vm
        key_name = f"test-key-{uuid.uuid4().hex[:6]}"
        try:
            _guest_run(
                vm_name,
                f"/usr/local/bin/mvm key create {key_name} --algorithm ed25519 --force",
            )
            ls_result = _guest_run(vm_name, "/usr/local/bin/mvm key ls --json")
            keys = _json_success_data(ls_result.stdout, list)
            assert any(k.get("name") == key_name for k in keys)
        finally:
            _guest_run(
                vm_name, f"/usr/local/bin/mvm key rm {key_name}", check=False,
            )
        ls_result2 = _guest_run(vm_name, "/usr/local/bin/mvm key ls --json")
        keys2 = _json_success_data(ls_result2.stdout, list)
        assert not any(k.get("name") == key_name for k in keys2)

    def test_inside_guest_volume_create_and_resize(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Volume create, list, resize, inspect, and remove inside the guest."""
        vm_name, _operator, _unpriv_user = fenv_vm
        vol_name = f"test-vol-{uuid.uuid4().hex[:6]}"
        try:
            _guest_run(vm_name, f"/usr/local/bin/mvm vol create {vol_name} 512M")
            ls_result = _guest_run(vm_name, "/usr/local/bin/mvm vol ls --json")
            vols = _json_success_data(ls_result.stdout, list)
            vol_entry = next(
                (v for v in vols if v.get("name") == vol_name), None
            )
            assert vol_entry is not None
            size_bytes = vol_entry.get("size_bytes", 0)
            assert size_bytes == 536870912

            _guest_run(vm_name, f"/usr/local/bin/mvm vol resize {vol_name} 1G")
            inspect_result = _guest_run(
                vm_name, f"/usr/local/bin/mvm vol inspect {vol_name} --json",
            )
            vol_info = _json_success_data(inspect_result.stdout, dict)
            vol_data = vol_info["volume"]
            new_size = vol_data.get("size_bytes", 0)
            assert new_size == 1073741824
        finally:
            _guest_run(
                vm_name, f"/usr/local/bin/mvm vol rm {vol_name} --force", check=False,
            )

    def test_inside_guest_network_operations(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Network create/list/remove inside the guest."""
        vm_name, _operator, _unpriv_user = fenv_vm
        net_name = f"test-net-{uuid.uuid4().hex[:6]}"
        try:
            create_result = _guest_run(
                vm_name,
                f"/usr/local/bin/mvm network create {net_name} "
                "--subnet 10.200.0.0/24",
                check=False, timeout=120,
            )
            assert create_result.returncode == 0, (
                "Guest network creation failed: "
                f"{create_result.stderr.strip()}"
            )

            ls_result = _guest_run(vm_name, "/usr/local/bin/mvm network ls --json")
            nets = _json_success_data(ls_result.stdout, list)
            assert any(n.get("name") == net_name for n in nets)
        finally:
            _guest_run(
                vm_name, f"/usr/local/bin/mvm network rm {net_name} --force", check=False,
            )

    def test_inside_guest_nested_vm_lifecycle(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Create a nested VM INSIDE the fenv guest (triple nesting)."""
        vm_name, _operator, _unpriv_user = fenv_vm
        nested_vm_name = f"nested-{uuid.uuid4().hex[:6]}"

        kvm_check = _guest_run(
            vm_name,
            "test -c /dev/kvm && echo KVM_OK || echo KVM_MISSING",
            check=False,
        )
        assert "KVM_OK" in kvm_check.stdout, (
            "/dev/kvm not available inside fenv guest -- "
            "cannot test nested VM creation"
        )

        nested_net = f"nestnet-{uuid.uuid4().hex[:6]}"
        try:
            images = _json_success_data(_guest_run(
                vm_name, "/usr/local/bin/mvm image ls --json",
            ).stdout, list)
            cached = [row for row in images if row["type"] == "ubuntu"
                      and row["version"] == "24.04" and row["is_present"]]
            assert len(cached) <= 1, cached
            cached_image = cached[0] if cached else None
            cache_receipt = None
            if cached_image is not None:
                cache_probe = r'''
import hashlib,json,os,stat,sys
from pathlib import Path
path=Path(sys.argv[1]);assert path.is_absolute()
with os.fdopen(os.open(path,os.O_RDONLY|os.O_NOFOLLOW),"rb") as source:
 before=os.fstat(source.fileno())
 assert stat.S_ISREG(before.st_mode) and before.st_nlink==1 and before.st_size>0
 digest=hashlib.file_digest(source,"sha256").hexdigest()
 fields=("st_dev","st_ino","st_uid","st_gid","st_mode","st_nlink","st_size","st_mtime_ns","st_ctime_ns")
 identity=[getattr(before,field) for field in fields]
 assert identity==[getattr(os.fstat(source.fileno()),field) for field in fields]
 assert identity==[getattr(path.lstat(),field) for field in fields]
print(json.dumps({"identity":identity,"sha256":digest}))
'''
                cache_command = shlex.join(["python3", "-c", cache_probe, cached_image["path"]])
                cache_receipt = json.loads(_guest_run(vm_name, cache_command).stdout)
            img_pull = _guest_run(
                vm_name, "/usr/local/bin/mvm image pull ubuntu:24.04 --json --debug",
                timeout=300, check=False,
            )
            assert img_pull.returncode == 0, (
                f"Image pull inside guest failed (exit "
                f"{img_pull.returncode}): "
                f"{img_pull.stderr.strip()[:300]}"
            )
            image_output = img_pull.stdout + img_pull.stderr
            assert "Mirror checksum mismatch" not in image_output and "Copied to asset mirror" not in image_output
            pulled_image = _json_success_data(img_pull.stdout, dict)
            assert pulled_image["type"] == "ubuntu" and pulled_image["version"] == "24.04", pulled_image
            assert pulled_image["is_present"] is True, pulled_image
            if "Using local mirror for download" not in image_output:
                # ImagePull returns the existing record before invoking the downloader.
                assert cached_image is not None and cache_receipt is not None, image_output
                assert pulled_image["id"] == cached_image["id"], (cached_image, pulled_image)
                assert pulled_image["path"] == cached_image["path"], (cached_image, pulled_image)
                assert json.loads(_guest_run(vm_name, cache_command).stdout) == cache_receipt

            kernel_pull = _guest_run(
                vm_name,
                "/usr/local/bin/mvm kernel pull --type firecracker --version v1.15 --default --debug",
                timeout=60,
            )
            kernel_output = kernel_pull.stdout + kernel_pull.stderr
            assert "Using local mirror for download" in kernel_output, kernel_output
            assert "Mirror checksum mismatch" not in kernel_output and "Copied to asset mirror" not in kernel_output

            _guest_run(
                vm_name,
                f"/usr/local/bin/mvm network create {nested_net} "
                "--subnet 10.199.0.0/24",
                timeout=60,
            )

            create_result = _guest_run(
                vm_name,
                f"/usr/local/bin/mvm vm create {nested_vm_name} "
                f"--vcpu 1 --mem 256m "
                f"--image ubuntu:24.04 "
                f"--kernel firecracker "
                f"--network {nested_net} ",
                check=False, timeout=300,
            )
            assert create_result.returncode == 0, (
                f"Nested VM creation failed (exit "
                f"{create_result.returncode}): "
                f"{create_result.stderr.strip()[:1000]}"
            )

            ls_result = _guest_run(vm_name, "/usr/local/bin/mvm vm ls --json")
            vms = _json_success_data(ls_result.stdout, list)
            assert any(v.get("name") == nested_vm_name for v in vms)
        finally:
            _guest_run(
                vm_name, f"/usr/local/bin/mvm vm rm {nested_vm_name} --force", check=False,
            )
            _guest_run(
                vm_name, f"/usr/local/bin/mvm network rm {nested_net} --force", check=False,
            )

    def test_inside_guest_unprivileged_read_only(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Read-only mvm commands work as an unprivileged user."""
        vm_name, _operator, unpriv_user = fenv_vm
        mvm_for_user = "/usr/local/bin/mvm"

        ls_result = _guest_run(
            vm_name, f"{mvm_for_user} vm ls --json",
            check=False, user=unpriv_user,
        )
        assert ls_result.returncode == 0
        data = _json_success_data(ls_result.stdout, list)
        assert isinstance(data, list)

        host_result = _guest_run(
            vm_name, f"{mvm_for_user} host status --json",
            check=False, user=unpriv_user,
        )
        assert host_result.returncode == 0
        host_data = _json_success_data(host_result.stdout, dict)
        assert "kvm_accessible" in host_data

        key_result = _guest_run(
            vm_name, f"{mvm_for_user} key ls --json",
            check=False, user=unpriv_user,
        )
        assert key_result.returncode == 0
        key_data = _json_success_data(key_result.stdout, list)
        assert isinstance(key_data, list)

    def test_inside_guest_host_reset(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """An active managed network refuses reset without changing administrator state."""
        vm_name, operator, _unpriv_user = fenv_vm
        net_name = f"reset-net-{uuid.uuid4().hex[:12]}"
        probe = _STATE_PROBE.replace('pwd.getpwnam("hostqa")', f'pwd.getpwnam("{operator}")')
        probe_cmd = shlex.join(["python3", "-c", probe])
        network_id = None
        try:
            network = _json_success_data(_guest_run(
                vm_name, f"/usr/local/bin/mvm network create {net_name} "
                f"--subnet {_unique_subnet(net_name)} --json",
            ).stdout, dict)
            network_id = network["id"]
            before = json.loads(_guest_run(vm_name, probe_cmd, user="root").stdout)
            result = _guest_run(
                vm_name, f"/usr/local/bin/mvm host reset --operator {operator} --force --json",
                user="root", check=False, timeout=180,
            )
            error = _require_error(result, "host.reset.failed")
            assert error["details"]["journal_retained"] is True, error
            assert error["details"]["reset_failure_stage"] == "acquire network and firewall reset guard", error
            assert "host reset requires all managed networks to be removed" in error["details"]["reset_failure_cause"], error
            assert json.loads(_guest_run(vm_name, probe_cmd, user="root").stdout) == before
            nets = _json_success_data(_guest_run(vm_name, "/usr/local/bin/mvm network ls --json").stdout, list)
            assert any(row["id"] == network_id and row["name"] == net_name for row in nets), nets
        finally:
            if network_id is not None:
                _guest_run(vm_name, f"/usr/local/bin/mvm network rm {network_id} --force")
                nets = _json_success_data(_guest_run(vm_name, "/usr/local/bin/mvm network ls --json").stdout, list)
                assert all(row["id"] != network_id for row in nets), nets
            _json_success_data(_guest_run(
                vm_name, f"/usr/local/bin/mvm host init --operator {operator} --json",
                user="root", timeout=180,
            ).stdout, dict)
