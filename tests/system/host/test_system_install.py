"""Root-owned system binary installation tests — Tier 1 runner VM."""

from __future__ import annotations

import hashlib
import json
import os
import shlex
import stat
from pathlib import Path
from typing import Iterator

import pytest

from tests.system.conftest import SYSTEM_MVM_BINARY, _guest_run, _run_mvm

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_system_install,
]

_INSTALL_CANDIDATE = "/opt/mvmctl-test/mvm-candidate"
_SYSTEM_DIRECTORY_CHAIN = ("/", "/usr", "/usr/local", "/usr/local/bin")
_CANDIDATE_DIRECTORY_CHAIN = ("/opt", "/opt/mvmctl-test")
_SYMLINK_BACKUP = "/usr/local/bin/.mvm-system-install-l2.backup"
_SYMLINK_TARGET = "/home/runner/.mvm-system-install-symlink-target"
_LATEST_RELEASE_URL = (
    "https://api.github.com/repos/AlanD20/mvmctl/releases/latest"
)


def _sha256(runner_vm: str, path: str) -> str:
    result = _guest_run(runner_vm, f"sha256sum {shlex.quote(path)}")
    return result.stdout.split()[0]


def _stat(runner_vm: str, path: str) -> tuple[int, int, int, str]:
    result = _guest_run(
        runner_vm,
        f"stat -c '%u %g %a %F' {shlex.quote(path)}",
    )
    owner, group, mode, file_type = result.stdout.strip().split(maxsplit=3)
    return int(owner), int(group), int(mode, 8), file_type


@pytest.fixture
def self_update_release_cache() -> Iterator[None]:
    """Seed a newer release response and restore the exact prior cache state."""
    temp_root = Path(os.environ.get("MVM_TEMP_DIR", "/tmp/mvmctl"))
    temp_root_existed = temp_root.exists()
    cache_dir = temp_root / "http"
    assert not cache_dir.is_symlink(), f"release cache directory is a symlink: {cache_dir}"
    cache_dir_existed = cache_dir.exists()
    cache_key = hashlib.sha256(_LATEST_RELEASE_URL.encode()).hexdigest()
    cache_path = cache_dir / cache_key
    assert not cache_path.is_symlink(), f"release cache entry is a symlink: {cache_path}"
    original = cache_path.read_bytes() if cache_path.exists() else None
    original_stat = cache_path.stat() if original is not None else None
    cache_dir.mkdir(parents=True, exist_ok=True)
    cache_path.write_text(
        json.dumps({"tag_name": "v999.0.0", "assets": []}),
        encoding="utf-8",
    )
    try:
        yield
    finally:
        failures: list[str] = []
        try:
            if original is None:
                cache_path.unlink(missing_ok=True)
            else:
                cache_path.write_bytes(original)
                if cache_path.read_bytes() != original:
                    failures.append("release cache content was not restored exactly")
                assert original_stat is not None
                os.chmod(cache_path, stat.S_IMODE(original_stat.st_mode))
                os.utime(
                    cache_path,
                    ns=(original_stat.st_atime_ns, original_stat.st_mtime_ns),
                )
        except (OSError, AssertionError) as error:
            failures.append(f"restore release cache file: {error}")
        if not cache_dir_existed:
            try:
                cache_dir.rmdir()
            except OSError as error:
                failures.append(f"remove created release cache directory: {error}")
        if not temp_root_existed:
            try:
                temp_root.rmdir()
            except OSError as error:
                failures.append(f"remove created temp directory: {error}")

        if original is None:
            if cache_path.exists():
                failures.append(f"release cache entry still exists: {cache_path}")
        assert not failures, "self-update cache restoration failed:\n" + "\n".join(
            failures
        )


class TestSystemBinaryInstall:
    def test_tests_execute_the_canonical_installed_cli(self, runner_vm: str) -> None:
        resolved = _guest_run(runner_vm, "command -v mvm").stdout.strip()
        assert resolved == SYSTEM_MVM_BINARY

        version = _run_mvm(runner_vm, "--version")
        assert version.stdout.startswith("mvm ")

        owner, group, mode, file_type = _stat(runner_vm, SYSTEM_MVM_BINARY)
        assert (owner, group, mode) == (0, 0, 0o755)
        assert file_type == "regular file"

        for directory in _SYSTEM_DIRECTORY_CHAIN + _CANDIDATE_DIRECTORY_CHAIN:
            dir_owner, dir_group, dir_mode, dir_type = _stat(runner_vm, directory)
            assert dir_owner == 0, f"{directory} is not root-owned"
            assert dir_group == 0, f"{directory} is not root-group-owned"
            assert dir_mode & 0o022 == 0, f"{directory} is group/world writable"
            assert dir_type == "directory"

        candidate_owner, candidate_group, candidate_mode, candidate_type = _stat(
            runner_vm, _INSTALL_CANDIDATE
        )
        assert (candidate_owner, candidate_group, candidate_mode) == (0, 0, 0o755)
        assert candidate_type == "regular file"
        assert _sha256(runner_vm, SYSTEM_MVM_BINARY) == _sha256(
            runner_vm, _INSTALL_CANDIDATE
        )
        leftovers = _guest_run(
            runner_vm,
            "find /usr/local/bin -maxdepth 1 -name '.mvm-install-*.tmp' -print",
        )
        assert leftovers.stdout.strip() == ""

    def test_unprivileged_install_cannot_replace_the_system_binary(
        self, runner_vm: str
    ) -> None:
        before = _sha256(runner_vm, SYSTEM_MVM_BINARY)

        rejected = _guest_run(
            runner_vm,
            f"{_INSTALL_CANDIDATE} host install-system",
            check=False,
        )

        assert rejected.returncode != 0
        assert "requires root" in (rejected.stdout + rejected.stderr).lower()
        assert _sha256(runner_vm, SYSTEM_MVM_BINARY) == before

    @pytest.mark.parametrize(
        "extra_args",
        [
            ("host", "install-system", "--force"),
            ("--debug", "host", "install-system"),
            ("host", "--verbose", "install-system"),
        ],
    )
    def test_malformed_install_route_fails_without_replacing_the_binary(
        self, runner_vm: str, extra_args: tuple[str, ...]
    ) -> None:
        before = _sha256(runner_vm, SYSTEM_MVM_BINARY)
        command = " ".join(
            ["sudo", "-n", shlex.quote(_INSTALL_CANDIDATE)]
            + [shlex.quote(arg) for arg in extra_args]
        )

        rejected = _guest_run(runner_vm, command, check=False)

        assert rejected.returncode != 0
        assert "accepts no flags or arguments" in (
            rejected.stdout + rejected.stderr
        ).lower()
        assert _sha256(runner_vm, SYSTEM_MVM_BINARY) == before

    def test_install_system_is_idempotent_from_the_installed_cli(
        self, runner_vm: str
    ) -> None:
        before = _sha256(runner_vm, SYSTEM_MVM_BINARY)

        installed = _guest_run(
            runner_vm,
            f"sudo -n {SYSTEM_MVM_BINARY} host install-system",
            check=False,
        )
        assert installed.returncode == 0, installed.stderr

        assert _sha256(runner_vm, SYSTEM_MVM_BINARY) == before
        assert _sha256(runner_vm, _INSTALL_CANDIDATE) == before
        assert _run_mvm(runner_vm, "--version").returncode == 0

    def test_host_init_then_unprivileged_init_use_the_installed_cli(
        self, runner_vm: str
    ) -> None:
        initialized = _guest_run(
            runner_vm,
            f"sudo -n {SYSTEM_MVM_BINARY} host init",
            check=False,
            timeout=180,
        )
        assert initialized.returncode == 0, initialized.stderr

        status = _run_mvm(runner_vm, "host", "status", "--json", check=False)
        assert status.returncode == 0, status.stderr
        sudoers = _guest_run(
            runner_vm,
            "stat -c '%u:%g:%a:%F' /etc/sudoers.d/mvm",
            check=False,
        )
        assert sudoers.returncode == 0, sudoers.stderr
        assert sudoers.stdout.strip() == "0:0:440:regular file"
        group = _guest_run(runner_vm, "getent group mvm", check=False)
        assert group.returncode == 0, group.stderr

        user_initialized = _guest_run(
            runner_vm,
            f"MVM_ASSET_MIRROR=/mnt {SYSTEM_MVM_BINARY} init --non-interactive",
            check=False,
            timeout=180,
        )
        assert user_initialized.returncode == 0, user_initialized.stderr

        user_state = _guest_run(
            runner_vm,
            "stat -c '%U:%G:%F' /home/runner/.config/mvmctl "
            "/home/runner/.cache/mvmctl",
            check=False,
        )
        assert user_state.returncode == 0, user_state.stderr
        assert user_state.stdout.splitlines() == [
            "runner:runner:directory",
            "runner:runner:directory",
        ]

    @pytest.mark.destructive
    def test_self_update_cannot_replace_the_system_installation(
        self, runner_vm: str, self_update_release_cache: None
    ) -> None:
        before = _sha256(runner_vm, SYSTEM_MVM_BINARY)

        rejected = _run_mvm(
            runner_vm,
            "self-update",
            "apply",
            "--force",
            check=False,
            timeout=90,
        )

        assert rejected.returncode != 0
        combined = (rejected.stdout + rejected.stderr).lower()
        assert "cannot replace the system installation" in combined
        assert "sudo <new-mvm-binary> host install-system" in combined
        assert _sha256(runner_vm, SYSTEM_MVM_BINARY) == before

    @pytest.mark.destructive
    def test_read_only_install_directory_preserves_system_binary(
        self, runner_vm: str
    ) -> None:
        before_hash = _sha256(runner_vm, SYSTEM_MVM_BINARY)
        before_metadata = _stat(runner_vm, SYSTEM_MVM_BINARY)
        assert before_metadata == (0, 0, 0o755, "regular file")
        expected_stat = "0:0:755:regular file"
        bind_mounted = False
        try:
            existing_mount = _guest_run(
                runner_vm,
                f"findmnt -rn --mountpoint {shlex.quote('/usr/local/bin')} "
                "-o TARGET,OPTIONS",
                check=False,
            )
            assert existing_mount.returncode == 1, (
                "installer fault target is already a mountpoint: "
                f"rc={existing_mount.returncode}, "
                f"stdout={existing_mount.stdout!r}, "
                f"stderr={existing_mount.stderr!r}"
            )

            mounted = _guest_run(
                runner_vm,
                "sudo -n mount --bind /usr/local/bin /usr/local/bin",
                check=False,
            )
            assert mounted.returncode == 0, mounted.stderr
            bind_mounted = True

            read_only = _guest_run(
                runner_vm,
                "sudo -n mount -o remount,bind,ro /usr/local/bin",
                check=False,
            )
            assert read_only.returncode == 0, read_only.stderr
            mount_state = _guest_run(
                runner_vm,
                "findmnt -rn --mountpoint /usr/local/bin -o OPTIONS",
                check=False,
            )
            assert mount_state.returncode == 0, mount_state.stderr
            assert "ro" in mount_state.stdout.strip().split(",")

            rejected = _guest_run(
                runner_vm,
                f"sudo -n {shlex.quote(_INSTALL_CANDIDATE)} "
                "host install-system",
                check=False,
            )

            assert rejected.returncode != 0
            assert "read-only file system" in (
                rejected.stdout + rejected.stderr
            ).lower()
            assert _sha256(runner_vm, SYSTEM_MVM_BINARY) == before_hash
            assert _stat(runner_vm, SYSTEM_MVM_BINARY) == before_metadata
            leftovers = _guest_run(
                runner_vm,
                "find /usr/local/bin -maxdepth 1 "
                "-name '.mvm-install-*.tmp' -print",
            )
            assert leftovers.stdout.strip() == ""
        finally:
            restoration_failures: list[str] = []
            if bind_mounted:
                writable = _guest_run(
                    runner_vm,
                    "sudo -n mount -o remount,bind,rw /usr/local/bin",
                    check=False,
                )
                if writable.returncode != 0:
                    restoration_failures.append(
                        "restore writable bind mount: "
                        f"rc={writable.returncode}, "
                        f"stdout={writable.stdout!r}, stderr={writable.stderr!r}"
                    )
                unmounted = _guest_run(
                    runner_vm,
                    "sudo -n umount /usr/local/bin",
                    check=False,
                )
                if unmounted.returncode != 0:
                    restoration_failures.append(
                        "unmount installer fault target: "
                        f"rc={unmounted.returncode}, "
                        f"stdout={unmounted.stdout!r}, "
                        f"stderr={unmounted.stderr!r}"
                    )

            leaked_mount = _guest_run(
                runner_vm,
                "findmnt -rn --mountpoint /usr/local/bin -o TARGET,OPTIONS",
                check=False,
            )
            if leaked_mount.returncode != 1:
                restoration_failures.append(
                    "installer fault mount leaked: "
                    f"rc={leaked_mount.returncode}, "
                    f"stdout={leaked_mount.stdout!r}, "
                    f"stderr={leaked_mount.stderr!r}"
                )
            restored = _guest_run(
                runner_vm,
                f"test ! -L {shlex.quote(SYSTEM_MVM_BINARY)} && "
                f"test \"$(stat -c '%u:%g:%a:%F' "
                f"{shlex.quote(SYSTEM_MVM_BINARY)})\" = "
                f"{shlex.quote(expected_stat)} && "
                f"test \"$(sha256sum {shlex.quote(SYSTEM_MVM_BINARY)} "
                f"| cut -d ' ' -f 1)\" = {shlex.quote(before_hash)} && "
                "test -z \"$(find /usr/local/bin -maxdepth 1 "
                "-name '.mvm-install-*.tmp' -print -quit)\"",
                check=False,
            )
            if restored.returncode != 0:
                restoration_failures.append(
                    "verify canonical restoration: "
                    f"rc={restored.returncode}, stdout={restored.stdout!r}, "
                    f"stderr={restored.stderr!r}"
                )
            executable = _run_mvm(runner_vm, "--version", check=False)
            if executable.returncode != 0:
                restoration_failures.append(
                    "execute restored binary: "
                    f"rc={executable.returncode}, "
                    f"stdout={executable.stdout!r}, "
                    f"stderr={executable.stderr!r}"
                )
            assert not restoration_failures, (
                "read-only installer fault restoration failed:\n"
                + "\n".join(restoration_failures)
            )

    @pytest.mark.destructive
    def test_existing_target_symlink_is_rejected_and_restored(
        self, runner_vm: str
    ) -> None:
        before = _sha256(runner_vm, SYSTEM_MVM_BINARY)
        backup_created = False
        try:
            absent = _guest_run(
                runner_vm,
                f"sudo -n test ! -e {shlex.quote(_SYMLINK_BACKUP)} && "
                f"sudo -n test ! -L {shlex.quote(_SYMLINK_BACKUP)}",
                check=False,
            )
            assert absent.returncode == 0, (
                f"stale system-install backup exists: {absent.stderr}"
            )
            _guest_run(
                runner_vm,
                f"printf 'must-not-change\\n' > {shlex.quote(_SYMLINK_TARGET)}",
            )
            moved = _guest_run(
                runner_vm,
                f"sudo -n mv {shlex.quote(SYSTEM_MVM_BINARY)} "
                f"{shlex.quote(_SYMLINK_BACKUP)}",
                check=False,
            )
            assert moved.returncode == 0, moved.stderr
            backup_created = True
            linked = _guest_run(
                runner_vm,
                f"sudo -n ln -s {shlex.quote(_SYMLINK_TARGET)} "
                f"{shlex.quote(SYSTEM_MVM_BINARY)}",
                check=False,
            )
            assert linked.returncode == 0, linked.stderr

            rejected = _guest_run(
                runner_vm,
                f"sudo -n {shlex.quote(_INSTALL_CANDIDATE)} host install-system",
                check=False,
            )

            assert rejected.returncode != 0
            combined = (rejected.stdout + rejected.stderr).lower()
            assert "system binary" in combined
            target = _guest_run(
                runner_vm,
                f"readlink {shlex.quote(SYSTEM_MVM_BINARY)}",
            )
            assert target.stdout.strip() == _SYMLINK_TARGET
            marker = _guest_run(
                runner_vm,
                f"cat {shlex.quote(_SYMLINK_TARGET)}",
            )
            assert marker.stdout == "must-not-change\n"
            assert _sha256(runner_vm, _SYMLINK_BACKUP) == before
        finally:
            restoration_failures: list[str] = []
            if backup_created:
                for label, command in (
                    (
                        "remove test target",
                        f"sudo -n rm -f {shlex.quote(SYSTEM_MVM_BINARY)}",
                    ),
                    (
                        "restore canonical binary",
                        f"sudo -n mv {shlex.quote(_SYMLINK_BACKUP)} "
                        f"{shlex.quote(SYSTEM_MVM_BINARY)}",
                    ),
                    (
                        "restore canonical metadata",
                        f"sudo -n chown root:root {shlex.quote(SYSTEM_MVM_BINARY)} && "
                        f"sudo -n chmod 0755 {shlex.quote(SYSTEM_MVM_BINARY)}",
                    ),
                ):
                    result = _guest_run(runner_vm, command, check=False)
                    if result.returncode != 0:
                        restoration_failures.append(
                            f"{label}: rc={result.returncode}, "
                            f"stdout={result.stdout!r}, stderr={result.stderr!r}"
                        )
            cleanup = _guest_run(
                runner_vm,
                f"rm -f {shlex.quote(_SYMLINK_TARGET)}",
                check=False,
            )
            if cleanup.returncode != 0:
                restoration_failures.append(
                    f"remove test marker: rc={cleanup.returncode}, "
                    f"stdout={cleanup.stdout!r}, stderr={cleanup.stderr!r}"
                )

            restored = _guest_run(
                runner_vm,
                f"test ! -L {shlex.quote(SYSTEM_MVM_BINARY)} && "
                f"test ! -e {shlex.quote(_SYMLINK_BACKUP)} && "
                f"test ! -L {shlex.quote(_SYMLINK_BACKUP)} && "
                f"test \"$(stat -c '%u:%g:%a' "
                f"{shlex.quote(SYSTEM_MVM_BINARY)})\" = '0:0:755' && "
                f"test \"$(sha256sum {shlex.quote(SYSTEM_MVM_BINARY)} "
                f"| cut -d ' ' -f 1)\" = {shlex.quote(before)}",
                check=False,
            )
            if restored.returncode != 0:
                restoration_failures.append(
                    f"verify canonical restoration: rc={restored.returncode}, "
                    f"stdout={restored.stdout!r}, stderr={restored.stderr!r}"
                )
            executable = _run_mvm(runner_vm, "--version", check=False)
            if executable.returncode != 0:
                restoration_failures.append(
                    f"execute restored binary: rc={executable.returncode}, "
                    f"stdout={executable.stdout!r}, stderr={executable.stderr!r}"
                )
            assert not restoration_failures, (
                "system binary restoration failed:\n"
                + "\n".join(restoration_failures)
            )
