"""Installed-candidate hotplug journeys with function-owned resources and checked cleanup."""

from __future__ import annotations

import json
import os
import stat
import time
import uuid
from pathlib import Path
from typing import Generator

import pytest

from tests.system.conftest import _json_success_data, _run_mvm, _unique_subnet
from tests.system.vm.test_vm_lifecycle import _LifecycleOwner, _require_lifecycle_assets

pytestmark = [pytest.mark.system, pytest.mark.domain_volume]


def _wait_for_vdb(runner_vm: str, vm_name: str, timeout: float = 10.0) -> bool:
    """Poll guest /proc/partitions via vsock exec until vdb appears or *timeout* expires.

    Uses ``mvm exec`` (vsock agent) instead of ``mvm ssh`` because the
    Alpine cloud image's SSHD configuration rejects publickey auth in nested
    virtualization (PAM + StrictModes). The vsock agent is always available.
    Uses ``cat /proc/partitions`` because Alpine lacks ``lsblk``.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = _run_mvm(
            runner_vm, "exec", vm_name, "--user", "root", "--timeout",
            "10", "--", "cat /proc/partitions",
            check=False, timeout=15,
        )
        if result.returncode == 0 and "vdb" in result.stdout:
            return True
        time.sleep(0.5)
    return False


def _wait_for_no_vdb(
    runner_vm: str, vm_name: str, timeout: float = 10.0
) -> bool:
    """Poll guest /proc/partitions via vsock exec until vdb disappears or *timeout* expires."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = _run_mvm(
            runner_vm, "exec", vm_name, "--user", "root", "--timeout",
            "10", "--", "cat /proc/partitions",
            check=False, timeout=15,
        )
        if result.returncode == 0:
            vdb_found = "vdb" in result.stdout
            if not vdb_found:
                return True
        time.sleep(0.5)
    return False


def _require_firecracker_hotplug(runner_vm: str) -> None:
    """Fail the test if the default Firecracker binary doesn't support hotplug.

    Hotplug requires Firecracker v1.5+ (version gate in the API layer).
    This helper is called at the start of every hotplug test to ensure
    the environment is capable.
    """
    result = _run_mvm(
        runner_vm, "bin", "ls", "--json", timeout=120
    )
    if result.returncode != 0:
        pytest.fail("Cannot check Firecracker version (bin ls failed)")

    bins = _json_success_data(result.stdout, list)
    fc_default = next(
        (b for b in bins
         if b.get("type") == "firecracker" and b.get("is_default")),
        None,
    )
    if fc_default is None:
        pytest.fail("No default Firecracker binary set")
    version = fc_default.get("version", "")
    clean = version.lstrip("v").split("-")[0].split(".")[:2]
    try:
        major, minor = int(clean[0]), int(clean[1])
    except (ValueError, IndexError):
        pytest.fail(f"Firecracker version '''{version}''' is not parseable")
    if major < 1 or (major == 1 and minor < 5):
        pytest.fail(
            f"Hotplug tests require Firecracker v1.5+ "
            f"(current: v{version}). "
            f"Install a newer binary with: mvm bin pull firecracker <version>"
        )


def _count_virtio_block_devices(runner_vm: str, vm_name: str) -> int:
    result = _run_mvm(
        runner_vm, "exec", vm_name, "--user", "root", "--",
        "sh -c \"ls /sys/block | grep '^vd[b-z]' | wc -l\"",
    )
    return int(result.stdout.strip())


def _owned_hotplug_case(
    runner_vm: str, *, pci_enabled: bool
) -> Generator[dict, None, None]:
    """Use the lifecycle owner's exact create receipts and cleanup for one guest."""
    _require_lifecycle_assets(runner_vm)
    owner = _LifecycleOwner(runner_vm)
    suffix = uuid.uuid4().hex[:12]
    names = {
        "vm": f"sys-hp-{suffix}",
        "network": f"sys-hp-net-{suffix}",
        "key": f"sys-hp-key-{suffix}",
        "volume": f"sys-hp-vol-{suffix}",
    }
    source: Path | None = None
    try:
        owner.before_key_create(names["key"])
        key = _json_success_data(
            _run_mvm(
                runner_vm, "key", "create", names["key"],
                "--algorithm", "ed25519", "--json",
            ).stdout, dict,
        )
        owner.key_created(names["key"], published=key)
        owner.before_network_create(names["network"])
        network = _json_success_data(
            _run_mvm(
                runner_vm, "network", "create", names["network"],
                "--subnet", _unique_subnet(names["network"]), "--no-nat", "--json",
            ).stdout, dict,
        )
        owner.network_created(names["network"], published=network)
        image = next(
            row for row in owner.baseline["image"]
            if row["type"] == "alpine" and row["version"] == "3.23" and row["is_present"]
        )
        kernel = next(row for row in owner.baseline["kernel"] if row["is_default"])
        owner.before_vm_create(names["vm"])
        created = _json_success_data(
            _run_mvm(
                runner_vm, "vm", "create", names["vm"],
                "--image", image["id"], "--kernel", kernel["id"],
                "--network", network["id"], "--ssh-key", key["id"], "--user", "root",
                "--disk-size", "512M", "--mem", "256M", "--vcpu", "1",
                *(("--no-pci",) if not pci_enabled else ()),
                "--json", timeout=180,
            ).stdout, list,
        )
        owner.vm_created(names["vm"], published=created)
        assert len(created) == 1 and created[0]["status"] == "running", created
        assert created[0]["pci_enabled"] is pci_enabled, created
        owner.before_volume_create(names["volume"])
        volume = _json_success_data(
            _run_mvm(
                runner_vm, "volume", "create", names["volume"], "512M", "--json",
            ).stdout, dict,
        )
        owner.volume_created(names["volume"], published=volume)
        source = Path(volume["path"])
        metadata = source.lstat()
        assert (
            source.name == f"{volume['id']}.raw"
            and stat.S_ISREG(metadata.st_mode)
            and metadata.st_uid == os.geteuid()
            and metadata.st_size == 512 * 1024 * 1024
        ), source
        yield {
            "vm": created[0]["id"], "volume": volume["id"],
        }
    finally:
        owner.cleanup()
        if source is not None:
            assert not os.path.lexists(source), ("removed volume retained raw source", source)


@pytest.fixture
def hotplug_case(runner_vm: str) -> Generator[dict, None, None]:
    yield from _owned_hotplug_case(runner_vm, pci_enabled=True)


@pytest.fixture
def hotplug_no_pci_case(runner_vm: str) -> Generator[dict, None, None]:
    yield from _owned_hotplug_case(runner_vm, pci_enabled=False)


class TestVolumeHotplug:
    """Hotplug, guest I/O, detach and reattach on a fresh owned PCI guest."""

    pytestmark = [pytest.mark.slow, pytest.mark.requires_firecracker_116]

    @pytest.fixture(autouse=True)
    def _ensure_firecracker_ge_1_16(self, runner_vm: str) -> None:
        _require_firecracker_hotplug(runner_vm)

    def test_hotplug_volume_appears_in_guest(self, runner_vm: str, hotplug_case: dict) -> None:
        vm_id, vol_id = hotplug_case["vm"], hotplug_case["volume"]
        _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
        assert _wait_for_vdb(runner_vm, vm_id), "hotplugged vdb did not appear"
        driver = _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--",
            "readlink /sys/block/vdb/device/driver",
        )
        assert "virtio" in driver.stdout.lower(), driver.stdout
        volumes = _json_success_data(
            _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
        )
        volume = next(row for row in volumes if row["id"] == vol_id)
        assert volume["status"] == "attached" and volume["vm_id"] == vm_id, volume

    def test_hotplug_volume_format_and_mount(self, runner_vm: str, hotplug_case: dict) -> None:
        vm_id, vol_id = hotplug_case["vm"], hotplug_case["volume"]
        _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
        ready = _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--",
            "set -e; test -b /dev/vdb; cat /sys/block/vdb/size; "
            "readlink /sys/block/vdb/device/driver",
        ).stdout.splitlines()
        assert len(ready) == 2 and ready[0] == "1048576", ready
        assert Path(ready[1]).name == "virtio_blk", ready
        _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--",
            "mkfs.ext4 -F /dev/vdb", timeout=120,
        )
        _run_mvm(runner_vm, "exec", vm_id, "--user", "root", "--", "mount /dev/vdb /mnt")
        _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--",
            "printf '%s\\n' 'hotplug test data' > /mnt/test-hotplug.txt",
        )
        readback = _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--", "cat /mnt/test-hotplug.txt",
        )
        assert readback.stdout.strip() == "hotplug test data", readback.stdout
        _run_mvm(runner_vm, "exec", vm_id, "--user", "root", "--", "umount /mnt")

    def test_hotplug_paused_attach_defers_until_resume_retry(
        self, runner_vm: str, hotplug_case: dict
    ) -> None:
        """Paused attachment retains ownership; success after retry means usable guest I/O."""
        vm_id, vol_id = hotplug_case["vm"], hotplug_case["volume"]
        # Establish guest-agent startup before pausing, not after claimed attach readiness.
        _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--timeout", "30",
            "--", "cat /proc/partitions",
        )
        initial = _json_success_data(
            _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
        )
        selected = [row for row in initial if row["id"] == vm_id]
        assert len(selected) == 1 and selected[0]["status"] == "running", selected
        identity = (selected[0]["pid"], selected[0]["process_start_time"])
        assert identity[0] > 0 and identity[1] > 0, selected
        _run_mvm(runner_vm, "vm", "pause", vm_id)
        deferred = _run_mvm(
            runner_vm, "volume", "attach", vm_id, vol_id, "--json", check=False,
        )
        assert deferred.returncode != 0, (deferred.stdout, deferred.stderr)
        response = json.loads(deferred.stdout)
        assert set(response) == {"schema_version", "status", "data", "error"}, response
        assert response["schema_version"] == 1 and response["status"] == "error", response
        assert response["data"] is None, response
        error = response["error"]
        assert error["code"] == "volume.guest_not_ready" and error["class"] == 3, error
        assert error["entity"] == vol_id, error
        assert error["details"] == {
            "vm_id": vm_id, "vmm_attached": True,
            "guest_ready": False, "readiness_deferred": True,
        }, error
        paused = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", vm_id, "--json").stdout, dict,
        )
        listed = _json_success_data(
            _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list,
        )
        selected = [row for row in listed if row["id"] == vm_id]
        assert len(selected) == 1, selected
        for row in (selected[0], paused["vm"]):
            assert row["id"] == vm_id and row["status"] == "paused", row
            assert (row["pid"], row["process_start_time"]) == identity, row
        assert sum(row["id"] == vol_id for row in paused["volumes"]) == 1, paused
        volume = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", vol_id, "--json").stdout, dict,
        )
        assert volume["volume"]["id"] == vol_id and volume["volume"]["status"] == "attached", volume
        assert volume["attachment"]["vm_id"] == vm_id, volume

        _run_mvm(runner_vm, "vm", "resume", vm_id)
        completed = _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id, "--json")
        assert json.loads(completed.stdout) == {
            "schema_version": 1, "status": "success", "data": None, "error": None,
        }, completed.stdout
        ready = _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--",
            "set -e; test -b /dev/vdb; cat /sys/block/vdb/size; "
            "readlink /sys/block/vdb/device/driver",
        ).stdout.splitlines()
        assert len(ready) == 2 and ready[0] == "1048576", ready
        assert Path(ready[1]).name == "virtio_blk", ready
        assert _count_virtio_block_devices(runner_vm, vm_id) == 1
        _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--",
            "mkfs.ext4 -F /dev/vdb", timeout=120,
        )
        _run_mvm(runner_vm, "exec", vm_id, "--user", "root", "--", "mount /dev/vdb /mnt")
        marker = f"paused-ready-{uuid.uuid4().hex}"
        written = _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--",
            f"set -e; printf %s {marker} > /mnt/test-hotplug.txt; sync; cat /mnt/test-hotplug.txt",
        )
        assert written.stdout.strip() == marker, written.stdout
        repeated = _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id, "--json")
        assert json.loads(repeated.stdout) == {
            "schema_version": 1, "status": "success", "data": None, "error": None,
        }, repeated.stdout
        assert _count_virtio_block_devices(runner_vm, vm_id) == 1
        readback = _run_mvm(
            runner_vm, "exec", vm_id, "--user", "root", "--", "cat /mnt/test-hotplug.txt",
        )
        assert readback.stdout.strip() == marker, readback.stdout
        vm = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", vm_id, "--json").stdout, dict,
        )
        assert vm["vm"]["id"] == vm_id and vm["vm"]["status"] == "running", vm
        assert (vm["vm"]["pid"], vm["vm"]["process_start_time"]) == identity, vm
        assert sum(row["id"] == vol_id for row in vm["volumes"]) == 1, vm
        volume = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", vol_id, "--json").stdout, dict,
        )
        assert volume["volume"]["id"] == vol_id and volume["volume"]["status"] == "attached", volume
        assert volume["attachment"]["vm_id"] == vm_id, volume
        _run_mvm(runner_vm, "exec", vm_id, "--user", "root", "--", "umount /mnt")

    def test_hotplug_volume_hot_unplug(self, runner_vm: str, hotplug_case: dict) -> None:
        vm_id, vol_id = hotplug_case["vm"], hotplug_case["volume"]
        _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
        assert _wait_for_vdb(runner_vm, vm_id), "hotplugged vdb did not appear"
        count_before = _count_virtio_block_devices(runner_vm, vm_id)
        assert count_before == 1, count_before
        _run_mvm(runner_vm, "volume", "detach", vm_id, vol_id)
        assert _wait_for_no_vdb(runner_vm, vm_id), "vdb did not disappear after hot-unplug"
        assert _count_virtio_block_devices(runner_vm, vm_id) == 0
        volumes = _json_success_data(
            _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
        )
        volume = next(row for row in volumes if row["id"] == vol_id)
        assert volume["status"] == "available" and volume.get("vm_id") is None, volume

    def test_hotplug_no_pci_guest_does_not_see_device(
        self, runner_vm: str, hotplug_no_pci_case: dict
    ) -> None:
        case = hotplug_no_pci_case
        result = _run_mvm(
            runner_vm, "volume", "attach", case["vm"], case["volume"],
            "--json", check=False,
        )
        assert result.returncode != 0, result.stdout + result.stderr
        envelope = json.loads(result.stdout)
        assert envelope["schema_version"] == 1 and envelope["status"] == "error", envelope
        assert envelope["error"]["code"] == "vm.create.failed", envelope
        assert type(envelope["error"]["class"]) is int, envelope
        assert envelope["error"]["class"] == 1, envelope
        assert "pci is not enabled" in envelope["error"]["message"].lower(), envelope
        partitions = _run_mvm(
            runner_vm, "exec", case["vm"], "--user", "root", "--", "cat /proc/partitions",
        )
        assert not any(
            line.split() and line.split()[-1] == "vdb" for line in partitions.stdout.splitlines()
        ), partitions.stdout
        volume = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", case["volume"], "--json").stdout, dict,
        )
        assert volume["volume"]["status"] == "available"
        assert volume["attachment"]["vm_id"] is None
        vm = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", case["vm"], "--json").stdout, dict,
        )
        assert vm["vm"]["status"] == "running" and vm["vm"]["pci_enabled"] is False
        assert all(row["id"] != case["volume"] for row in vm["volumes"])

    @pytest.mark.xfail(
        reason="Firecracker v1.16 dev-preview hot-unplug does not fully reset PCI BARs; "
               "re-attaching the same volume to the same running VM fails virtio-blk probe."
    )
    def test_hotplug_re_attach_after_hot_unplug(self, runner_vm: str, hotplug_case: dict) -> None:
        vm_id, vol_id = hotplug_case["vm"], hotplug_case["volume"]
        _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
        assert _wait_for_vdb(runner_vm, vm_id), "initial hotplugged vdb did not appear"
        _run_mvm(runner_vm, "volume", "detach", vm_id, vol_id)
        assert _wait_for_no_vdb(runner_vm, vm_id), "vdb did not disappear after hot-unplug"
        detached = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", vol_id, "--json").stdout, dict,
        )
        assert detached["volume"]["id"] == vol_id
        assert detached["volume"]["status"] == "available"
        assert detached["attachment"]["vm_id"] is None
        _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
        # PCI remove/re-add may allocate vdc rather than reuse vdb. This fresh
        # guest has one data volume; find its exact 512MiB extent, excluding vda.
        deadline = time.monotonic() + 10
        while True:
            devices = _run_mvm(
                runner_vm, "exec", vm_id, "--user", "root", "--",
                'for block in /sys/block/vd[b-z]*; do '
                '[ -f "$block/size" ] || continue; '
                '[ "$(cat "$block/size")" = 1048576 ] || continue; '
                'basename "$block"; done',
            )
            if devices.stdout.strip():
                assert len(devices.stdout.splitlines()) == 1, devices.stdout
                assert _count_virtio_block_devices(runner_vm, vm_id) == 1
                break
            assert time.monotonic() < deadline, "same volume did not reappear after reattach"
            time.sleep(0.5)
        attached = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", vol_id, "--json").stdout, dict,
        )
        assert attached["volume"]["id"] == vol_id
        assert attached["volume"]["status"] == "attached"
        assert attached["attachment"]["vm_id"] == vm_id


class TestVolumeHotplugVersionGate:
    """Stopped-VM attachment bypasses the live hotplug path."""

    pytestmark = [pytest.mark.requires_kvm, pytest.mark.slow]

    def test_attach_volume_to_stopped_vm_succeeds(
        self, runner_vm: str, hotplug_case: dict
    ) -> None:
        vm_id, vol_id = hotplug_case["vm"], hotplug_case["volume"]
        _run_mvm(runner_vm, "vm", "stop", vm_id, "--force")
        _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
        volumes = _json_success_data(
            _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
        )
        volume = next(row for row in volumes if row["id"] == vol_id)
        assert volume["status"] == "attached" and volume["vm_id"] == vm_id, volume
        vm = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", vm_id, "--json").stdout, dict,
        )
        assert vm["vm"]["status"] == "stopped"
        assert sum(row["id"] == vol_id for row in vm["volumes"]) == 1

    def test_detach_volume_from_stopped_vm_succeeds(
        self, runner_vm: str, hotplug_case: dict
    ) -> None:
        vm_id, vol_id = hotplug_case["vm"], hotplug_case["volume"]
        _run_mvm(runner_vm, "vm", "stop", vm_id, "--force")
        _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
        _run_mvm(runner_vm, "volume", "detach", vm_id, vol_id)
        volumes = _json_success_data(
            _run_mvm(runner_vm, "volume", "ls", "--json").stdout, list,
        )
        volume = next(row for row in volumes if row["id"] == vol_id)
        assert volume["status"] == "available" and volume.get("vm_id") is None, volume
        vm = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", vm_id, "--json").stdout, dict,
        )
        assert vm["vm"]["status"] == "stopped"
        assert all(row["id"] != vol_id for row in vm["volumes"])


class TestVolumeHotplugDestructive:
    """Stopped force-removal and exact duplicate-attachment semantics."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_volume,
        pytest.mark.slow,
        pytest.mark.requires_firecracker_116,
    ]

    @pytest.fixture(autouse=True)
    def _ensure_firecracker_ge_1_16(self, runner_vm: str) -> None:
        """Fail all hotplug destructive tests if Firecracker is too old."""
        _require_firecracker_hotplug(runner_vm)

    @pytest.mark.destructive
    def test_hotplug_force_remove_attached_volume(
        self,
        runner_vm: str,
    ) -> None:
        """Refuse running force-removal, then remove after stop without detach."""
        suffix = uuid.uuid4().hex[:8]
        names = {
            "vm": f"sys-hp-rm-vm-{suffix}",
            "volume": f"sys-hp-rm-vol-{suffix}",
            "key": f"sys-hp-rm-key-{suffix}",
            "network": f"sys-hp-rm-net-{suffix}",
        }

        def inventory(domain: str) -> list[dict]:
            rows = _json_success_data(
                _run_mvm(runner_vm, domain, "ls", "--json").stdout, list
            )
            if domain == "kernel":
                # Listing missing recorded kernels refreshes updated_at.
                rows = [
                    {key: value for key, value in row.items() if key != "updated_at"}
                    for row in rows
                ]
            return sorted(rows, key=lambda row: json.dumps(row, sort_keys=True))

        baseline = {
            domain: inventory(domain)
            for domain in ("vm", "volume", "key", "network", "image", "kernel", "bin")
        }
        config = _json_success_data(
            _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
        )
        for domain, name in names.items():
            assert all(row["name"] != name for row in baseline[domain])
        alpine = [
            row for row in baseline["image"]
            if row["type"] == "alpine" and row["version"] == "3.23" and row["is_present"]
        ]
        assert len(alpine) == 1, alpine
        assert 0 < alpine[0]["minimum_rootfs_size_mib"] <= 512, alpine
        kernels = [
            row for row in baseline["kernel"] if row["is_default"] and row["is_present"]
        ]
        assert len(kernels) == 1, kernels
        for binary_type in ("firecracker", "jailer"):
            assert any(
                row["type"] == binary_type and row["version"] == "1.16.2"
                and row["is_default"] and row["is_present"]
                for row in baseline["bin"]
            ), (binary_type, baseline["bin"])

        attempted: set[str] = set()
        owned_ids: dict[str, str] = {}
        source: Path | None = None
        try:
            attempted.add("key")
            _run_mvm(runner_vm, "key", "create", names["key"], "--algorithm", "ed25519")
            owned_ids["key"] = next(
                row["id"] for row in inventory("key") if row["name"] == names["key"]
            )
            attempted.add("network")
            _run_mvm(
                runner_vm, "network", "create", names["network"],
                "--subnet", _unique_subnet(names["network"]),
            )
            owned_ids["network"] = next(
                row["id"] for row in inventory("network") if row["name"] == names["network"]
            )
            attempted.add("vm")
            _run_mvm(
                runner_vm, "vm", "create", names["vm"],
                "--image", alpine[0]["id"], "--kernel", kernels[0]["id"],
                "--network", owned_ids["network"], "--ssh-key", owned_ids["key"],
                "--user", "root", "--mem", "256M", "--vcpu", "1", "--disk-size", "512M",
                timeout=180,
            )
            owned_ids["vm"] = next(
                row["id"] for row in inventory("vm") if row["name"] == names["vm"]
            )
            attempted.add("volume")
            _run_mvm(runner_vm, "volume", "create", names["volume"], "512M")
            volume = next(
                row for row in inventory("volume") if row["name"] == names["volume"]
            )
            owned_ids["volume"] = volume["id"]
            source = Path(volume["path"])
            assert source.name == f"{owned_ids['volume']}.raw", source
            vm_id, vol_id = owned_ids["vm"], owned_ids["volume"]
            _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
            assert _wait_for_vdb(runner_vm, vm_id), "hotplugged vdb did not appear"

            attached = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", vol_id, "--json").stdout, dict
            )
            assert attached["volume"]["status"] == "attached"
            assert attached["attachment"]["vm_id"] == vm_id
            source_before = source.lstat()
            assert stat.S_ISREG(source_before.st_mode), source
            assert source_before.st_size == 512 * 1024 * 1024
            result = _run_mvm(
                runner_vm, "volume", "rm", vol_id, "--force", check=False
            )
            assert result.returncode != 0, result.stdout + result.stderr
            assert "stop the vm" in (result.stdout + result.stderr).lower()

            retained = _json_success_data(
                _run_mvm(runner_vm, "volume", "inspect", vol_id, "--json").stdout, dict
            )
            assert retained["volume"] == attached["volume"]
            assert retained["attachment"] == attached["attachment"]
            assert retained["disk_info"] == attached["disk_info"]
            source_after = source.lstat()
            for field in ("st_dev", "st_ino", "st_size", "st_ctime_ns", "st_mtime_ns"):
                assert getattr(source_after, field) == getattr(source_before, field), field
            running = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_id, "--json").stdout, dict
            )
            assert running["vm"]["id"] == vm_id
            assert running["vm"]["status"] == "running"
            assert any(row["id"] == vol_id for row in running["volumes"])
            sectors = _run_mvm(
                runner_vm, "exec", vm_id, "--user", "root", "--timeout", "10", "--",
                "cat /sys/block/vdb/size",
            )
            assert int(sectors.stdout.strip()) == 512 * 1024 * 1024 // 512

            _run_mvm(runner_vm, "vm", "stop", vm_id, "--force")
            stopped = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_id, "--json").stdout, dict
            )
            assert stopped["vm"]["status"] == "stopped"
            assert any(row["id"] == vol_id for row in stopped["volumes"])
            _run_mvm(runner_vm, "volume", "rm", vol_id, "--force")
            assert all(row["id"] != vol_id for row in inventory("volume"))
            assert not os.path.lexists(source), "removed volume retained its raw source"
            after = _json_success_data(
                _run_mvm(runner_vm, "vm", "inspect", vm_id, "--json").stdout, dict
            )
            assert after["vm"]["id"] == vm_id
            assert after["vm"]["status"] == "stopped"
            assert all(row["id"] != vol_id for row in after["volumes"])
        finally:
            failures = []
            for domain, name in names.items():
                try:
                    rows = [row for row in inventory(domain) if row["name"] == name]
                    assert len(rows) <= 1, rows
                    if rows:
                        assert domain in attempted, ("unattempted resource exists", domain, rows)
                        resource_id = rows[0]["id"]
                        assert owned_ids.get(domain, resource_id) == resource_id
                        assert all(row["id"] != resource_id for row in baseline[domain])
                        flags = () if domain == "key" else ("--force",)
                        _run_mvm(
                            runner_vm, domain, "rm", resource_id, *flags,
                            timeout=120 if domain == "vm" else 60,
                        )
                    assert all(
                        row["name"] != name and row["id"] != owned_ids.get(domain)
                        for row in inventory(domain)
                    ), (domain, name)
                    if domain == "volume" and source is not None:
                        assert not os.path.lexists(source), source
                except Exception as error:
                    failures.append(f"cleanup {domain}/{name}: {error}")
            for domain, before in baseline.items():
                try:
                    assert inventory(domain) == before, domain
                except Exception as error:
                    failures.append(f"baseline {domain}: {error}")
            try:
                assert _json_success_data(
                    _run_mvm(runner_vm, "config", "ls", "--json").stdout, dict
                ) == config, "configuration changed"
            except Exception as error:
                failures.append(f"baseline config: {error}")
            assert not failures, "\n".join(failures)

    def test_hotplug_double_attach_is_idempotent(
        self, runner_vm: str, hotplug_case: dict
    ) -> None:
        """An exact attachment retry keeps one drive and one VM reference."""
        vm_id, vol_id = hotplug_case["vm"], hotplug_case["volume"]
        _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
        assert _wait_for_vdb(runner_vm, vm_id), "initial hotplugged vdb did not appear"
        assert _count_virtio_block_devices(runner_vm, vm_id) == 1
        _run_mvm(runner_vm, "volume", "attach", vm_id, vol_id)
        assert _wait_for_vdb(runner_vm, vm_id), "retry lost the attached device"
        assert _count_virtio_block_devices(runner_vm, vm_id) == 1
        volume = _json_success_data(
            _run_mvm(runner_vm, "volume", "inspect", vol_id, "--json").stdout, dict,
        )
        assert volume["volume"]["id"] == vol_id
        assert volume["volume"]["status"] == "attached"
        assert volume["attachment"]["vm_id"] == vm_id
        vm = _json_success_data(
            _run_mvm(runner_vm, "vm", "inspect", vm_id, "--json").stdout, dict,
        )
        assert vm["vm"]["status"] == "running"
        assert vm["vm"]["id"] == vm_id
        assert sum(row["id"] == vol_id for row in vm["volumes"]) == 1
