"""Root-owned system binary installation tests — Tier 1 runner VM."""

from __future__ import annotations

import hashlib
import json
import os
import shlex
import stat
import tempfile
import uuid
from pathlib import Path
from typing import Iterator

import pytest

from tests.system.conftest import (
    SYSTEM_MVM_BINARY,
    _guest_run,
    _json_success_data,
    _run_mvm,
)
from tests.system.init.test_init import _init_inventory, _retained_rows

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_system_install,
]

_INSTALL_CANDIDATE = "/opt/mvmctl-test/mvm-candidate"
_SYSTEM_DIRECTORY_CHAIN = ("/", "/usr", "/usr/local", "/usr/local/bin")
_CANDIDATE_DIRECTORY_CHAIN = ("/opt", "/opt/mvmctl-test")
_LATEST_RELEASE_URL = "https://api.github.com/repos/AlanD20/mvmctl/releases/latest"


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


def _file_receipt(path: str) -> tuple:
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as file:
        metadata = os.fstat(file.fileno())
        assert stat.S_ISREG(metadata.st_mode) and metadata.st_nlink == 1, path
        return (
            metadata.st_dev,
            metadata.st_ino,
            metadata.st_uid,
            metadata.st_gid,
            metadata.st_mode,
            metadata.st_size,
            hashlib.file_digest(file, "sha256").hexdigest(),
        )


def _symlink_receipt(path: str) -> tuple:
    metadata = Path(path).lstat()
    assert stat.S_ISLNK(metadata.st_mode), path
    return (
        metadata.st_dev,
        metadata.st_ino,
        metadata.st_uid,
        metadata.st_gid,
        os.readlink(path),
    )


def _absent(path: str) -> bool:
    try:
        Path(path).lstat()
    except FileNotFoundError:
        return True
    return False


def _mount_records() -> list[tuple[str, ...]]:
    records = []
    for line in Path("/proc/self/mountinfo").read_text().splitlines():
        fields = line.split()
        separator = fields.index("-")
        # Remount toggles ro/rw; retain the mount ID, parent, device, root,
        # target, propagation fields, filesystem type, and source instead.
        records.append(tuple(fields[:5] + fields[6 : separator + 3]))
    return records


def _mount_receipt() -> tuple[str, ...] | None:
    records = [record for record in _mount_records() if record[4] == "/usr/local/bin"]
    assert len(records) <= 1, records
    return records[0] if records else None


def _marker_identity(metadata: os.stat_result) -> tuple[int, ...]:
    assert stat.S_ISREG(metadata.st_mode) and metadata.st_nlink == 1
    return (
        metadata.st_dev,
        metadata.st_ino,
        metadata.st_uid,
        metadata.st_gid,
        metadata.st_mode,
        metadata.st_nlink,
    )


def _selected_cache_dir() -> Path:
    cache_dir = Path(
        os.environ.get("MVM_CACHE_DIR", str(Path.home() / ".cache/mvmctl"))
    ).expanduser()
    assert cache_dir.is_dir() and not cache_dir.is_symlink(), cache_dir
    return cache_dir.absolute()


@pytest.fixture(autouse=True)
def installed_release_case(runner_vm: str) -> Iterator[None]:
    """Admit the staged artifact and retain every public inventory/default set."""
    assert os.geteuid() != 0, "installer tests must use the ordinary runner operator"
    for path in (SYSTEM_MVM_BINARY, _INSTALL_CANDIDATE):
        assert _stat(runner_vm, path) == (0, 0, 0o755, "regular file")
    for directory in _SYSTEM_DIRECTORY_CHAIN + _CANDIDATE_DIRECTORY_CHAIN:
        owner, group, mode, file_type = _stat(runner_vm, directory)
        assert (owner, group, file_type) == (0, 0, "directory")
        assert mode & 0o022 == 0
    candidate = _file_receipt(_INSTALL_CANDIDATE)
    assert _file_receipt(SYSTEM_MVM_BINARY)[-1] == candidate[-1]
    before = _init_inventory(runner_vm)
    try:
        yield
    finally:
        failures = []
        canonical_ready = True
        for path in (SYSTEM_MVM_BINARY, _INSTALL_CANDIDATE):
            try:
                assert _stat(runner_vm, path) == (0, 0, 0o755, "regular file")
                assert _file_receipt(path)[-1] == candidate[-1]
            except Exception as error:
                failures.append(f"installed candidate {path}: {error}")
                if path == SYSTEM_MVM_BINARY:
                    canonical_ready = False
        try:
            assert _file_receipt(_INSTALL_CANDIDATE) == candidate
        except Exception as error:
            failures.append(f"staged candidate identity: {error}")
        assert canonical_ready, (
            "unsafe canonical CLI; preserve state and do not execute readbacks:\n"
            + "\n".join(failures)
        )
        for domain, expected in before.items():
            try:
                if domain.startswith("policy."):
                    actual = _json_success_data(
                        _run_mvm(
                            runner_vm, "policy", domain.split(".")[1], "ls", "--json"
                        ).stdout,
                        type(expected),
                    )
                else:
                    actual = _json_success_data(
                        _run_mvm(runner_vm, domain, "ls", "--json").stdout,
                        type(expected),
                    )
                if isinstance(expected, list):
                    assert _retained_rows(domain, actual) == _retained_rows(
                        domain, expected
                    )
                else:
                    assert actual == expected
            except Exception as error:
                failures.append(f"retained {domain} baseline: {error}")
        assert not failures, "\n".join(failures)


@pytest.fixture
def self_update_release_cache() -> Iterator[None]:
    """Seed a newer release response and restore the exact prior cache state."""
    temp_root = Path(
        os.environ.get("MVM_TEMP_DIR", str(_selected_cache_dir() / "tmp"))
    ).expanduser()
    temp_root_existed = temp_root.exists()
    cache_dir = temp_root / "http"
    assert not cache_dir.is_symlink(), (
        f"release cache directory is a symlink: {cache_dir}"
    )
    cache_dir_existed = cache_dir.exists()
    cache_key = hashlib.sha256(_LATEST_RELEASE_URL.encode()).hexdigest()
    cache_path = cache_dir / cache_key
    assert not cache_path.is_symlink(), (
        f"release cache entry is a symlink: {cache_path}"
    )
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
        assert (
            "accepts no flags or arguments"
            in (rejected.stdout + rejected.stderr).lower()
        )
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
        self, runner_vm: str, monkeypatch: pytest.MonkeyPatch
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

        mirror = os.environ.get("MVM_ASSET_MIRROR")
        assert mirror and Path(mirror).expanduser().is_dir(), (
            "existing asset mirror is required"
        )
        cache_dir = _selected_cache_dir()
        binaries = _json_success_data(
            _run_mvm(runner_vm, "bin", "ls", "--json").stdout, list
        )
        for member in ("firecracker", "jailer"):
            assert any(
                row["type"] == member
                and row["version"] == "1.16.2"
                and row["is_present"]
                and row["is_default"]
                for row in binaries
            ), "preseeded 1.16.2 pair is required; init must not download"
        with tempfile.TemporaryDirectory(
            prefix="system-install-init-", dir=cache_dir
        ) as private_dir:
            config_dir = Path(private_dir) / "config"
            with monkeypatch.context() as selection:
                selection.setenv("MVM_CONFIG_DIR", str(config_dir))
                selection.setenv("MVM_TEMP_DIR", str(Path(private_dir) / "tmp"))
                user_initialized = _run_mvm(
                    runner_vm,
                    "init",
                    "--non-interactive",
                    "--skip-host",
                    "--skip-network",
                    "--binary-version",
                    "1.16.2",
                    check=False,
                    timeout=180,
                )
                assert user_initialized.returncode == 0, (
                    user_initialized.stdout + user_initialized.stderr
                )
                assert "all set" in user_initialized.stdout + user_initialized.stderr
                for directory in (config_dir, cache_dir):
                    metadata = directory.lstat()
                    assert stat.S_ISDIR(metadata.st_mode)
                    assert (metadata.st_uid, metadata.st_gid) == (
                        os.getuid(),
                        os.getgid(),
                    )

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
        original_receipt = _file_receipt(SYSTEM_MVM_BINARY)
        expected_stat = "0:0:755:regular file"
        bind_receipt = None
        bind_attempted = False
        directory = Path("/usr/local/bin").stat()
        filesystem_receipt = (directory.st_dev, directory.st_ino)
        mount_records = _mount_records()
        mount_ids = {record[0] for record in mount_records}
        source_mount = max(
            (
                record
                for record in mount_records
                if Path("/usr/local/bin").is_relative_to(record[4])
            ),
            key=lambda record: len(record[4]),
        )
        expected_bind_root = os.path.normpath(
            os.path.join(
                source_mount[3], os.path.relpath("/usr/local/bin", source_mount[4])
            )
        )

        def admit_bind_effect():
            nonlocal bind_receipt
            observed = _mount_receipt()
            if observed is None:
                return
            assert observed[0] not in mount_ids, observed
            assert observed[1:5] == (
                source_mount[0],
                source_mount[2],
                expected_bind_root,
                "/usr/local/bin",
            ), observed
            assert observed[-2:] == source_mount[-2:], observed
            assert source_mount in _mount_records(), "source mount metadata changed"
            directory = Path("/usr/local/bin").stat()
            assert (directory.st_dev, directory.st_ino) == filesystem_receipt
            assert _mount_receipt() == observed, "created mount is not stable"
            assert bind_receipt is None or bind_receipt == observed
            bind_receipt = observed

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
            assert _mount_receipt() is None, "bind target already has a mount"
            assert source_mount in _mount_records()
            bind_attempted = True
            try:
                mounted = _guest_run(
                    runner_vm,
                    "sudo -n mount --bind /usr/local/bin /usr/local/bin",
                    check=False,
                )
            finally:
                admit_bind_effect()
            assert mounted.returncode == 0, mounted.stderr
            assert bind_receipt is not None, (
                "successful bind mount must have an identity receipt"
            )
            assert _mount_receipt() == bind_receipt
            assert _file_receipt(SYSTEM_MVM_BINARY) == original_receipt

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
                f"sudo -n {shlex.quote(_INSTALL_CANDIDATE)} host install-system",
                check=False,
            )

            assert rejected.returncode != 0
            assert (
                "read-only file system" in (rejected.stdout + rejected.stderr).lower()
            )
            assert _sha256(runner_vm, SYSTEM_MVM_BINARY) == before_hash
            assert _stat(runner_vm, SYSTEM_MVM_BINARY) == before_metadata
            assert _file_receipt(SYSTEM_MVM_BINARY) == original_receipt
            leftovers = _guest_run(
                runner_vm,
                "find /usr/local/bin -maxdepth 1 -name '.mvm-install-*.tmp' -print",
            )
            assert leftovers.stdout.strip() == ""
        finally:
            restoration_failures: list[str] = []
            if bind_attempted:
                try:
                    admit_bind_effect()
                except Exception as error:
                    restoration_failures.append(
                        f"admit attempted bind; preserve ambiguous mount: {error}"
                    )
            if bind_receipt is not None:
                for label, command in (
                    (
                        "restore writable bind mount",
                        "sudo -n mount -o remount,bind,rw /usr/local/bin",
                    ),
                    ("unmount installer fault target", "sudo -n umount /usr/local/bin"),
                ):
                    try:
                        directory = Path("/usr/local/bin").stat()
                        assert _mount_receipt() == bind_receipt, (
                            "created mount identity changed"
                        )
                        assert (
                            directory.st_dev,
                            directory.st_ino,
                        ) == filesystem_receipt
                        result = _guest_run(runner_vm, command, check=False)
                        assert result.returncode == 0, result.stdout + result.stderr
                    except Exception as error:
                        restoration_failures.append(f"{label}: {error}")

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
                f'{shlex.quote(SYSTEM_MVM_BINARY)})" = '
                f"{shlex.quote(expected_stat)} && "
                f'test "$(sha256sum {shlex.quote(SYSTEM_MVM_BINARY)} '
                f"| cut -d ' ' -f 1)\" = {shlex.quote(before_hash)} && "
                'test -z "$(find /usr/local/bin -maxdepth 1 '
                "-name '.mvm-install-*.tmp' -print -quit)\"",
                check=False,
            )
            if restored.returncode != 0:
                restoration_failures.append(
                    "verify canonical restoration: "
                    f"rc={restored.returncode}, stdout={restored.stdout!r}, "
                    f"stderr={restored.stderr!r}"
                )
            try:
                assert _file_receipt(SYSTEM_MVM_BINARY) == original_receipt
            except Exception as error:
                restoration_failures.append(f"canonical identity restoration: {error}")
            if not restoration_failures:
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
        original_receipt = _file_receipt(SYSTEM_MVM_BINARY)
        case_id = uuid.uuid4().hex
        backup = f"/usr/local/bin/.mvm-system-install-l2-{case_id}.backup"
        target = str(_selected_cache_dir() / f"system-install-symlink-{case_id}")
        backup_created = False
        rename_attempted = False
        link_attempted = False
        marker_receipt = None
        link_receipt = None

        def admit_backup_effect():
            nonlocal backup_created
            if _absent(backup):
                return
            assert _file_receipt(backup) == original_receipt, (
                "moved backup identity changed"
            )
            if not link_attempted:
                assert _absent(SYSTEM_MVM_BINARY), "rename source is not absent"
            assert _file_receipt(backup) == original_receipt, (
                "moved backup is not stable"
            )
            backup_created = True

        def admit_link_effect():
            nonlocal link_receipt
            if _absent(SYSTEM_MVM_BINARY):
                return
            observed = _symlink_receipt(SYSTEM_MVM_BINARY)
            assert observed[2:4] == (0, 0), observed
            assert observed[-1] == target, "attempted symlink has another payload"
            assert _marker_identity(Path(target).lstat()) == marker_receipt
            assert _symlink_receipt(SYSTEM_MVM_BINARY) == observed, (
                "created symlink is not stable"
            )
            assert link_receipt is None or link_receipt == observed
            link_receipt = observed

        try:
            absent = _guest_run(
                runner_vm,
                f"sudo -n test ! -e {shlex.quote(backup)} && "
                f"sudo -n test ! -L {shlex.quote(backup)}",
                check=False,
            )
            assert absent.returncode == 0, (
                f"stale system-install backup exists: {absent.stderr}"
            )
            with Path(target).open("xb") as marker:
                marker_receipt = _marker_identity(os.fstat(marker.fileno()))
                marker.write(b"must-not-change\n")
            assert _file_receipt(SYSTEM_MVM_BINARY) == original_receipt
            assert _absent(backup)
            rename_attempted = True
            try:
                moved = _guest_run(
                    runner_vm,
                    f"sudo -n mv -T --no-clobber {shlex.quote(SYSTEM_MVM_BINARY)} "
                    f"{shlex.quote(backup)}",
                    check=False,
                )
            finally:
                admit_backup_effect()
            assert moved.returncode == 0, moved.stderr
            assert backup_created and _absent(SYSTEM_MVM_BINARY)
            assert _absent(SYSTEM_MVM_BINARY)
            link_attempted = True
            try:
                linked = _guest_run(
                    runner_vm,
                    f"sudo -n ln -s {shlex.quote(target)} {shlex.quote(SYSTEM_MVM_BINARY)}",
                    check=False,
                )
            finally:
                admit_link_effect()
            assert linked.returncode == 0, linked.stderr
            assert link_receipt is not None

            rejected = _guest_run(
                runner_vm,
                f"sudo -n {shlex.quote(_INSTALL_CANDIDATE)} host install-system",
                check=False,
            )

            assert rejected.returncode != 0
            combined = (rejected.stdout + rejected.stderr).lower()
            assert "system binary" in combined
            actual_target = _guest_run(
                runner_vm,
                f"readlink {shlex.quote(SYSTEM_MVM_BINARY)}",
            )
            assert actual_target.stdout.strip() == target
            assert _symlink_receipt(SYSTEM_MVM_BINARY) == link_receipt
            marker = _guest_run(
                runner_vm,
                f"cat {shlex.quote(target)}",
            )
            assert marker.stdout == "must-not-change\n"
            assert _file_receipt(backup) == original_receipt
        finally:
            restoration_failures: list[str] = []
            if rename_attempted:
                try:
                    admit_backup_effect()
                except Exception as error:
                    restoration_failures.append(
                        f"admit attempted rename; preserve {backup}: {error}"
                    )
            if link_attempted:
                try:
                    admit_link_effect()
                except Exception as error:
                    restoration_failures.append(
                        f"admit attempted symlink; preserve canonical target: {error}"
                    )
            if backup_created:
                try:
                    assert _file_receipt(backup) == original_receipt, (
                        "backup identity changed"
                    )
                    if not _absent(SYSTEM_MVM_BINARY):
                        assert link_receipt is not None, (
                            "canonical target has no owned symlink receipt"
                        )
                        assert _symlink_receipt(SYSTEM_MVM_BINARY) == link_receipt
                        try:
                            _guest_run(
                                runner_vm,
                                f"sudo -n rm -- {shlex.quote(SYSTEM_MVM_BINARY)}",
                            )
                        except Exception as error:
                            restoration_failures.append(
                                f"remove admitted symlink: {error}"
                            )
                        # A timed-out unlink may have completed. Proceed only
                        # when absence and the retained backup independently prove it.
                    assert _absent(SYSTEM_MVM_BINARY), "canonical target is not absent"
                    assert _file_receipt(backup) == original_receipt
                    _guest_run(
                        runner_vm,
                        f"sudo -n mv -T --no-clobber {shlex.quote(backup)} "
                        f"{shlex.quote(SYSTEM_MVM_BINARY)}",
                    )
                except Exception as error:
                    restoration_failures.append(
                        f"restore admitted backup {backup}; preserve ambiguous state: {error}"
                    )
            if marker_receipt is not None:
                try:
                    # Retain the marker when it is still an ambiguous canonical target.
                    assert (
                        _absent(SYSTEM_MVM_BINARY)
                        or not Path(SYSTEM_MVM_BINARY).is_symlink()
                    )
                    assert _marker_identity(Path(target).lstat()) == marker_receipt, (
                        "marker identity changed"
                    )
                    Path(target).unlink()
                    assert _absent(target)
                except Exception as error:
                    restoration_failures.append(
                        f"remove owned marker {target}: {error}"
                    )

            restored = _guest_run(
                runner_vm,
                f"test ! -L {shlex.quote(SYSTEM_MVM_BINARY)} && "
                f"test ! -e {shlex.quote(backup)} && "
                f"test ! -L {shlex.quote(backup)} && "
                f"test \"$(stat -c '%u:%g:%a' "
                f"{shlex.quote(SYSTEM_MVM_BINARY)})\" = '0:0:755' && "
                f'test "$(sha256sum {shlex.quote(SYSTEM_MVM_BINARY)} '
                f"| cut -d ' ' -f 1)\" = {shlex.quote(before)}",
                check=False,
            )
            if restored.returncode != 0:
                restoration_failures.append(
                    f"verify canonical restoration: rc={restored.returncode}, "
                    f"stdout={restored.stdout!r}, stderr={restored.stderr!r}"
                )
            try:
                assert _file_receipt(SYSTEM_MVM_BINARY) == original_receipt
                assert _absent(backup)
            except Exception as error:
                restoration_failures.append(f"canonical identity restoration: {error}")
            # Execute only the admitted original, never an ambiguous replacement.
            if not restoration_failures:
                executable = _run_mvm(runner_vm, "--version", check=False)
                if executable.returncode != 0:
                    restoration_failures.append(
                        f"execute restored binary: rc={executable.returncode}, "
                        f"stdout={executable.stdout!r}, stderr={executable.stderr!r}"
                    )
            if restoration_failures:
                restoration_failures.append(
                    f"manual runner recovery required; backup={backup}, marker={target}"
                )
            assert not restoration_failures, (
                "system binary restoration failed:\n" + "\n".join(restoration_failures)
            )
