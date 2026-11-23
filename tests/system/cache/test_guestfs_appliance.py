"""Qualify the enabled GuestFS appliance with real tools in its own T2 runner."""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import stat
import subprocess
from contextlib import ExitStack

import pytest

from tests.system.cache.test_cache import (
    selected_cache_case as selected_cache_case,
    selected_cache_preservation as selected_cache_preservation,
)
from tests.system.conftest import _json_success_data, _run_mvm

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_guestfs,
    pytest.mark.tier2,
    pytest.mark.slow,
    pytest.mark.destructive,
]


def _identity(info):
    return info.st_dev, info.st_ino, info.st_mode


def _file_receipt(directory_fd, name):
    fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=directory_fd)
    with os.fdopen(fd, "rb") as source:
        info = os.fstat(source.fileno())
        assert stat.S_ISREG(info.st_mode) and info.st_size > 0, (name, info)
        digest = hashlib.file_digest(source, "sha256").hexdigest()
    return (
        *_identity(info),
        info.st_uid,
        info.st_gid,
        info.st_size,
        info.st_mtime_ns,
        info.st_ctime_ns,
        digest,
    )


def _appliance_receipt(appliance_fd):
    return {
        name: _file_receipt(appliance_fd, name) for name in ("kernel", "initrd", "root")
    }


def _init_appliance(case, phase, record_property):
    result = _run_mvm(
        case.runner_vm,
        "cache",
        "init",
        "--json",
        "--debug",
        timeout=90,
    )
    data = _json_success_data(result.stdout, dict)
    record_property(f"{phase}_init", json.dumps(data, sort_keys=True))
    record_property(f"{phase}_diagnostics", result.stderr[-8192:])
    assert data["cache_dir"] == str(case.cache_dir)
    assert data["guestfs_appliance"] == str(case.cache_dir / "appliance"), (
        "CLI success does not qualify a skipped or failed appliance build",
        data,
        result.stderr,
    )
    return data


def _launch_appliance(guestfish, appliance, disk, phase, record_property):
    # Pin this launch to the just-built fixed appliance, not supermin's global cache.
    environment = {
        **os.environ,
        "LIBGUESTFS_PATH": str(appliance),
        "LIBGUESTFS_BACKEND": "direct",
        "LIBGUESTFS_BACKEND_SETTINGS": "force_kvm",
        "LIBGUESTFS_MEMSIZE": "256",
    }
    result = subprocess.run(
        [guestfish, "--ro", "--format=raw", "-a", str(disk)],
        input=(
            "run\nlist-filesystems\nmount-ro /dev/sda /\n"
            "is-dir /lost+found\numount-all\n"
        ),
        capture_output=True,
        text=True,
        timeout=120,
        env=environment,
    )
    record_property(f"{phase}_launch_stdout", result.stdout)
    record_property(f"{phase}_launch_stderr", result.stderr[-8192:])
    assert result.returncode == 0, (result.stdout, result.stderr)
    assert result.stdout.splitlines() == ["/dev/sda: ext4", "true"], result.stdout


def test_enabled_build_reuse_and_incomplete_rebuild(
    runner_vm,
    selected_cache_case,
    selected_cache_preservation,
    monkeypatch,
    record_property,
):
    """Build, launch, reuse, and repair one creation-owned fixed appliance."""
    tools = {
        name: shutil.which(name)
        for name in (
            "libguestfs-make-fixed-appliance",
            "guestfish",
            "supermin",
            "qemu-system-x86_64",
            "file",
            "mkfs.ext4",
        )
    }
    assert all(tools.values()), (
        "Preinstall GuestFS prerequisites in the runner",
        tools,
    )
    case = selected_cache_case
    appliance = case.cache_dir / "appliance"
    disk_name = "guestfs-smoke.raw"
    disk = case.cache_dir / disk_name
    cache_identity = _identity(case.cache_dir.lstat())
    assert case.initialize()["guestfs_appliance"] == ""
    with pytest.raises(FileNotFoundError):
        appliance.lstat()

    for variable, child in (
        ("LIBGUESTFS_CACHEDIR", "guestfs-cache"),
        ("LIBGUESTFS_TMPDIR", "guestfs-tmp"),
    ):
        directory = case.cache_dir / child
        directory.mkdir(mode=0o700)
        monkeypatch.setenv(variable, str(directory))

    with ExitStack() as descriptors:
        cache_fd = os.open(case.cache_dir, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        descriptors.callback(os.close, cache_fd)
        kvm_fd = os.open("/dev/kvm", os.O_RDWR)
        descriptors.callback(os.close, kvm_fd)
        disk_fd = os.open(
            disk_name,
            os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
            0o600,
            dir_fd=cache_fd,
        )
        descriptors.callback(os.close, disk_fd)
        disk_identity = _identity(os.fstat(disk_fd))
        appliance_identity = None
        try:
            os.ftruncate(disk_fd, 32 * 1024 * 1024)
            formatted = subprocess.run(
                [tools["mkfs.ext4"], "-F", "-q", str(disk)],
                capture_output=True,
                text=True,
                timeout=60,
            )
            assert formatted.returncode == 0, (formatted.stdout, formatted.stderr)
            disk_before = _file_receipt(cache_fd, disk_name)
            _run_mvm(
                runner_vm,
                "config",
                "set",
                "settings",
                "guestfs_enabled",
                "true",
                "--json",
            )
            enabled = _json_success_data(
                _run_mvm(
                    runner_vm, "config", "get", "settings", "guestfs_enabled", "--json"
                ).stdout,
                bool,
            )
            assert enabled is True

            _init_appliance(case, "cold", record_property)
            appliance_fd = os.open(
                "appliance",
                os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                dir_fd=cache_fd,
            )
            descriptors.callback(os.close, appliance_fd)
            appliance_identity = _identity(os.fstat(appliance_fd))
            built = _appliance_receipt(appliance_fd)
            record_property("cold_appliance", json.dumps(built, sort_keys=True))
            _launch_appliance(
                tools["guestfish"], appliance, disk, "cold", record_property
            )
            assert _appliance_receipt(appliance_fd) == built

            _init_appliance(case, "warm", record_property)
            assert _identity(appliance.lstat()) == appliance_identity
            assert _appliance_receipt(appliance_fd) == built
            _launch_appliance(
                tools["guestfish"], appliance, disk, "warm", record_property
            )
            assert _appliance_receipt(appliance_fd) == built

            # Only the exact built initrd in the retained private directory may be removed.
            initrd_fd = os.open(
                "initrd", os.O_RDONLY | os.O_NOFOLLOW, dir_fd=appliance_fd
            )
            descriptors.callback(os.close, initrd_fd)
            assert _identity(appliance.lstat()) == appliance_identity
            assert _identity(os.fstat(initrd_fd)) == tuple(built["initrd"][:3])
            assert _identity(
                os.stat("initrd", dir_fd=appliance_fd, follow_symlinks=False)
            ) == (_identity(os.fstat(initrd_fd)))
            os.unlink("initrd", dir_fd=appliance_fd)
            with pytest.raises(FileNotFoundError):
                os.stat("initrd", dir_fd=appliance_fd, follow_symlinks=False)
            _init_appliance(case, "repair", record_property)
            assert _identity(appliance.lstat()) == appliance_identity
            repaired = _appliance_receipt(appliance_fd)
            assert repaired["initrd"][:2] != built["initrd"][:2]
            record_property("repaired_appliance", json.dumps(repaired, sort_keys=True))
            _launch_appliance(
                tools["guestfish"], appliance, disk, "repair", record_property
            )
            assert _appliance_receipt(appliance_fd) == repaired
            assert _file_receipt(cache_fd, disk_name) == disk_before
            selected_cache_preservation.assert_preserved()
        finally:
            failures = []
            try:
                assert _identity(case.cache_dir.lstat()) == cache_identity
                assert _identity(os.fstat(cache_fd)) == cache_identity
                try:
                    current = appliance.lstat()
                except FileNotFoundError:
                    pass
                else:
                    assert stat.S_ISDIR(current.st_mode), current
                    if appliance_identity is not None:
                        assert _identity(current) == appliance_identity
                    result = _json_success_data(
                        _run_mvm(
                            runner_vm, "cache", "prune", "misc", "--force", "--json"
                        ).stdout,
                        dict,
                    )
                    assert result["appliance"] is True, result
                    assert result["guestfs_state"] is False, result
                with pytest.raises(FileNotFoundError):
                    appliance.lstat()
                assert (
                    _identity(
                        os.stat(disk_name, dir_fd=cache_fd, follow_symlinks=False)
                    )
                    == disk_identity
                )
                assert _identity(os.fstat(disk_fd)) == disk_identity
                os.unlink(disk_name, dir_fd=cache_fd)
                with pytest.raises(FileNotFoundError):
                    os.stat(disk_name, dir_fd=cache_fd, follow_symlinks=False)
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"selected appliance/disk cleanup: {error!r}")
            try:
                selected_cache_preservation.assert_preserved()
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"external preservation: {error!r}")
            assert not failures, "GuestFS teardown failed:\n" + "\n".join(failures)
