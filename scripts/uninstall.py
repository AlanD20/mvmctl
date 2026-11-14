#!/usr/bin/env python3
"""Remove an admitted mvmctl 0.3 installation using its existing typed cleanup first."""

from __future__ import annotations

import argparse
from dataclasses import dataclass, field
import fcntl
import json
import os
from pathlib import Path
import pwd
import re
import stat
import subprocess
import sys

sys.dont_write_bytecode = True

from uninstall_files import (  # noqa: E402
    Refusal, capture, directory, fingerprint, identity, inspect_tree, mount_id,
    private_root, read_record, reject_mounts_beneath, remove_tree,
)

STATE = Path("/var/lib/mvmctl")
QUARANTINE = Path("/var/lib/.mvmctl-uninstall")
BINARY = Path("/usr/local/bin/mvm")
RUNTIME = Path("/run/mvmctl")
CGROUP = Path("/sys/fs/cgroup/mvmctl")
WARM_ROOT = Path("/tmp")
ROOT_CHILDREN = frozenset({
    "host", "instances", "networks", "firewall", "binaries", "jailer", "loopmount",
    "allocations", "execution-identities", "protected-backing", "launch-devices",
    "vm-rootfs-export-data",
})
SAFE_ENV = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8", "HOME": "/root"}


@dataclass
class Operator:
    name: str
    uid: int
    gid: int
    home: Path
    caches: set[Path] = field(default_factory=set)
    configs: set[Path] = field(default_factory=set)


@dataclass
class Plan:
    state: Path
    initial_operator: str
    operators: dict[int, Operator]
    binary: tuple[int, int] | None
    reset_complete: bool
    package_command: tuple[str, ...] | None
    reset_started: bool = False


def command(args: list[str], *, env: dict | None = None, timeout: int = 3600) -> str:
    result = subprocess.run(args, env=env or SAFE_ENV, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise Refusal(f"command failed ({result.returncode}): {args[0]}\n{result.stderr.strip()}\n{result.stdout.strip()}")
    return result.stdout


def native(args: list[str], operator: Operator | None = None, cache: Path | None = None) -> dict:
    env = dict(SAFE_ENV)
    argv = [str(BINARY), *args, "--json"]
    if operator is not None:
        env.update(HOME=str(operator.home), USER=operator.name, LOGNAME=operator.name)
        if cache is not None:
            env["MVM_CACHE_DIR"] = str(cache)
        argv = ["/usr/sbin/runuser", "-u", operator.name, "--", *argv]
    value = json.loads(command(argv, env=env))
    if value.get("schema_version") != 1 or value.get("status") != "success":
        raise Refusal(f"mvm cleanup did not report success: {value}")
    return value.get("data")


def file_identity(path: Path) -> tuple[int, int] | None:
    try:
        with directory(path.parent, trusted=True) as parent:
            info = os.stat(path.name, dir_fd=parent, follow_symlinks=False)
    except FileNotFoundError:
        return None
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022 or info.st_nlink != 1:
        raise Refusal(f"not a root-owned regular installation file: {path}")
    return identity(info)


def package_removal() -> tuple[str, ...] | None:
    """Do not remove a packaged /usr/bin/mvm behind its package manager's back."""
    if not os.path.lexists("/usr/bin/mvm"):
        return None
    file_identity(Path("/usr/bin/mvm"))
    for query, remove in (
        (("/usr/bin/dpkg-query", "-S", "/usr/bin/mvm"), ("/usr/bin/dpkg", "--purge")),
        (("/usr/bin/rpm", "-qf", "--qf", "%{NAME}\\n", "/usr/bin/mvm"), ("/usr/bin/rpm", "--erase")),
        (("/usr/bin/pacman", "-Qqo", "/usr/bin/mvm"), ("/usr/bin/pacman", "--remove", "--noconfirm")),
    ):
        if not Path(query[0]).is_file():
            continue
        result = subprocess.run(query, env=SAFE_ENV, capture_output=True, text=True, timeout=30)
        if result.returncode:
            continue
        owner = result.stdout.strip().split(":", 1)[0]
        if owner not in {"mvmctl", "mvmctl-bin"}:
            raise Refusal(f"/usr/bin/mvm belongs to an unexpected package: {owner}")
        return (*remove, owner)
    raise Refusal("/usr/bin/mvm exists without a recognized mvmctl package; resolve it before uninstall")


def check_cache(path: Path, operator: Operator) -> None:
    forbidden = {Path("/"), Path("/home"), operator.home, operator.home / ".cache", operator.home / ".config"}
    if (not path.is_absolute() or ".." in path.parts or len(path.parts) < 3 or path in forbidden
            or path in operator.home.parents):
        raise Refusal(f"unsafe cache selection: {path}")
    if any(path == root or root in path.parents for root in map(Path, ("/etc", "/usr", "/proc", "/sys", "/dev", "/run", "/var/lib/mvmctl"))):
        raise Refusal(f"cache overlaps system state: {path}")
    reject_mounts_beneath(path)
    try:
        with directory(path) as fd:
            if os.fstat(fd).st_uid != operator.uid:
                raise Refusal(f"cache does not belong to {operator.name}: {path}")
    except FileNotFoundError:
        pass


def inspect_root_inventory(path: Path) -> None:
    reject_mounts_beneath(path)
    with directory(path, trusted=True) as fd:
        private_root(fd)
        unknown = set(os.listdir(fd)) - ROOT_CHILDREN
        if unknown:
            raise Refusal(f"unrecognized administrator entries; preserved: {sorted(unknown)}")


def make_plan(extra_caches: list[str], extra_configs: list[str] = ()) -> Plan:
    if os.geteuid() != 0:
        raise Refusal("run this administrator script with sudo (including --dry-run)")
    if os.path.lexists(STATE) and os.path.lexists(QUARANTINE):
        raise Refusal("both installation and uninstall quarantine exist; refusing an ambiguous generation")
    state = QUARANTINE if os.path.lexists(QUARANTINE) else STATE
    inspect_root_inventory(state)
    operators: dict[int, Operator] = {}
    with directory(state / "host", trusted=True) as host:
        private_root(host)
        journal = read_record(host, "journal.json")
        if journal.get("version") != 2 or journal.get("pending") is not False:
            raise Refusal("complete interrupted host initialization or use a compatible uninstaller")
        for name in os.listdir(host):
            match = re.fullmatch(r"operator-([1-9][0-9]*)\.json", name)
            if not match:
                if name not in {"journal.json", "admission.json", "lock"}:
                    raise Refusal(f"unknown administrator entry: {name}")
                continue
            record = read_record(host, name)
            account = pwd.getpwuid(int(match[1]))
            if (record.get("version") != 1 or record.get("uid") != str(account.pw_uid)
                    or record.get("gid") != str(account.pw_gid) or record.get("operator") != account.pw_name
                    or record.get("completed") is not True):
                raise Refusal(f"operator enrollment changed: {name}")
            operator = Operator(account.pw_name, account.pw_uid, account.pw_gid, Path(account.pw_dir))
            if not operator.home.is_absolute() or len(operator.home.parts) < 3:
                raise Refusal(f"unsafe home for enrolled operator: {operator.name}")
            operator.caches.add(operator.home / ".cache/mvmctl")
            operator.configs.add(operator.home / ".config/mvmctl")
            operators[operator.uid] = operator
    if not operators or journal.get("operator") not in {op.name for op in operators.values()}:
        raise Refusal("initial operator enrollment is unavailable")
    backing = state / "protected-backing"
    retired_locators: set[tuple[int, Path]] = set()
    active_locators: set[tuple[int, Path]] = set()
    if backing.exists():
        with directory(backing, trusted=True) as owners:
            private_root(owners)
            for owner in os.listdir(owners):
                if not owner.isdecimal() or int(owner) not in operators:
                    raise Refusal(f"protected cache has an unenrolled owner: {owner}")
                with directory(backing / owner, trusted=True) as caches:
                    private_root(caches)
                    for key in os.listdir(caches):
                        if not re.fullmatch(r"[0-9]+-[0-9]+-[0-9]+", key):
                            continue  # Logical resource records are checked by native cleanup/reset.
                        with directory(backing / owner / key, trusted=True) as anchor:
                            private_root(anchor)
                            record = read_record(anchor, "store.json")
                            if record.get("version") != 2 or record.get("owner") != int(owner):
                                raise Refusal("unsupported protected-cache binding")
                            retirement = record.get("retirement")
                            if retirement is not None and not isinstance(retirement, dict):
                                raise Refusal("invalid protected-cache retirement")
                            if retirement and retirement.get("phase") == "removed":
                                if isinstance(record.get("locator"), str):
                                    retired_locators.add((int(owner), Path(record["locator"])))
                                continue  # A retired locator can now hold unrelated files.
                            if not isinstance(record.get("locator"), str):
                                raise Refusal("invalid protected-cache locator")
                            locator = Path(record["locator"])
                            if not os.path.lexists(locator):
                                continue
                            op = operators[int(owner)]
                            check_cache(locator, op)
                            info = locator.lstat()
                            expected = record["cache"]
                            if not isinstance(expected, dict):
                                raise Refusal("invalid protected-cache identity")
                            if (os.major(info.st_dev), os.minor(info.st_dev), info.st_ino) != (
                                expected["major"], expected["minor"], expected["inode"],
                            ):
                                raise Refusal(f"protected cache locator was replaced: {locator}")
                            op.caches.add(locator)
                            active_locators.add((int(owner), locator))
    for selections, attribute in ((extra_caches, "caches"), (extra_configs, "configs")):
        for selection in selections:
            name, separator, raw = selection.partition("=")
            candidates = [op for op in operators.values() if op.name == name]
            if not separator or len(candidates) != 1:
                raise Refusal(f"--{attribute[:-1]} requires ENROLLED_USER=/absolute/path")
            getattr(candidates[0], attribute).add(Path(raw))
    selected = []
    for op in operators.values():
        for path in op.caches | op.configs:
            if (os.path.lexists(path) and (op.uid, path) in retired_locators
                    and (op.uid, path) not in active_locators):
                raise Refusal(f"selected path reuses a retired cache without current authority: {path}")
            check_cache(path, op)
            for prior in selected:
                if path == prior or path in prior.parents or prior in path.parents:
                    raise Refusal(f"overlapping removal paths: {path}, {prior}")
            for other in operators.values():
                mirror = other.home / ".cache/mvm-asset-mirror"
                if path == mirror or path in mirror.parents or mirror in path.parents:
                    raise Refusal(f"removal overlaps a preserved asset mirror: {path}")
            selected.append(path)
        for config in op.configs | {WARM_ROOT / f"mvmctl-{op.uid}"}:
            if os.path.lexists(config):
                with directory(config) as fd:
                    inspect_tree(fd, op.uid, mount_id(fd))
        personal_binaries(op, remove=False)
    reset = journal.get("reset_state") == "complete" and journal.get("reset_step") == 4
    if state == QUARANTINE and not reset:
        raise Refusal("uninstall quarantine does not contain completed reset authority")
    binary = file_identity(BINARY)
    if binary is None and not reset:
        raise Refusal("compatible trusted system mvm is required for typed cleanup")
    return Plan(state, journal["operator"], operators, binary, reset, package_removal(),
                journal.get("reset_state") == "restoring")


def personal_binaries(operator: Operator, *, remove: bool) -> None:
    if remove and os.geteuid() != operator.uid:
        raise Refusal("personal launchers must be removed using their owner's credentials")
    for name in (operator.home / ".local/bin/mvm", operator.home / "bin/mvm"):
        if not os.path.lexists(name):
            continue
        with directory(name.parent) as parent:
            info = os.stat(name.name, dir_fd=parent, follow_symlinks=False)
            if info.st_uid != operator.uid:
                raise Refusal(f"personal binary ownership changed: {name}")
            if stat.S_ISLNK(info.st_mode):
                if os.readlink(name.name, dir_fd=parent) not in {str(BINARY), "/usr/bin/mvm"}:
                    raise Refusal(f"unrecognized personal mvm symlink: {name}")
            elif stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_size <= 128 * 1024 * 1024:
                fd = os.open(name.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
                with os.fdopen(fd, "rb") as source:
                    if fingerprint(os.fstat(source.fileno())) != fingerprint(info):
                        raise Refusal(f"personal executable changed before reading: {name}")
                    raw = source.read(info.st_size + 1)
                    if len(raw) != info.st_size or b"path\tmvmctl/cmd/mvm\n" not in raw:
                        raise Refusal(f"unrecognized or changed personal mvm executable: {name}")
            else:
                raise Refusal(f"unsafe personal binary: {name}")
            if fingerprint(os.stat(name.name, dir_fd=parent, follow_symlinks=False)) != fingerprint(info):
                raise Refusal(f"personal binary changed: {name}")
            if remove:
                os.unlink(name.name, dir_fd=parent)
                os.fsync(parent)


def user_config_cleanup(operator: Operator) -> None:
    """Filesystem recursion for caller data never runs with administrator credentials."""
    pid = os.fork()
    if pid == 0:
        try:
            os.setgroups([])
            os.setgid(operator.gid)
            os.setuid(operator.uid)
            for config in operator.configs | {WARM_ROOT / f"mvmctl-{operator.uid}"}:
                if os.path.lexists(config):
                    remove_tree(config, operator.uid)
            personal_binaries(operator, remove=True)
            os._exit(0)
        except Exception as exc:
            print(f"User-data cleanup refused: {exc}", file=sys.stderr, flush=True)
            os._exit(1)
    _, status = os.waitpid(pid, 0)
    if status != 0:
        raise Refusal(f"caller-owned cleanup incomplete for {operator.name}")


def finalize(plan: Plan) -> None:
    with directory(plan.state / "host", trusted=True) as host:
        private_root(host)
        lock = os.open("lock", os.O_RDWR | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=host)
        try:
            info = os.fstat(lock)
            if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1
                    or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size != 0):
                raise Refusal("host admission lock identity is unsafe")
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            journal = read_record(host, "journal.json")
            admission = read_record(host, "admission.json")
            if (journal.get("reset_state") != "complete" or journal.get("reset_step") != 4
                    or journal.get("initialized") is not False or admission != {"version": 1, "enabled": False}):
                raise Refusal("native host reset did not complete with admission disabled")
            for op in plan.operators.values():
                if any(os.path.lexists(cache) for cache in op.caches):
                    raise Refusal(f"cache remains after typed cleanup for {op.name}")
            inspect_root_inventory(plan.state)
            with directory(plan.state, trusted=True) as state:
                inspect_tree(state, 0, mount_id(state))
                pinned = identity(os.fstat(state))
            if os.path.lexists(RUNTIME):
                remove_tree(RUNTIME, 0)
            if os.path.lexists(CGROUP):
                with directory(CGROUP, trusted=True) as cgroup:
                    private_root(cgroup)
                    if (Path(CGROUP / "cgroup.procs").read_text().strip()
                            or any(stat.S_ISDIR(os.stat(name, dir_fd=cgroup, follow_symlinks=False).st_mode)
                                   for name in os.listdir(cgroup))):
                        raise Refusal("mvm cgroup still contains processes or child cgroups")
                CGROUP.rmdir()
            if plan.state == STATE:
                with directory(STATE.parent, trusted=True) as parent:
                    private_root(parent)
                    if QUARANTINE.exists() or identity(os.stat(STATE.name, dir_fd=parent, follow_symlinks=False)) != pinned:
                        raise Refusal("administrator state changed before uninstall capture")
                    capture(parent, STATE.name, QUARANTINE.name)
                    if identity(os.stat(QUARANTINE.name, dir_fd=parent, follow_symlinks=False)) != pinned:
                        raise Refusal("administrator capture identity mismatch")
                    os.fsync(parent)
            if plan.package_command is not None:
                command(list(plan.package_command))
            current = file_identity(BINARY)
            if current is not None:
                if current != plan.binary:
                    raise Refusal("system binary changed during uninstall; preserved")
                with directory(BINARY.parent, trusted=True) as parent:
                    if identity(os.stat(BINARY.name, dir_fd=parent, follow_symlinks=False)) != current:
                        raise Refusal("system binary was replaced before removal")
                    os.unlink(BINARY.name, dir_fd=parent)
                    os.fsync(parent)
            remove_tree(QUARANTINE, 0)
        finally:
            os.close(lock)


def uninstall(plan: Plan) -> None:
    if not plan.reset_complete:
        if file_identity(BINARY) != plan.binary:
            raise Refusal("system binary changed after preview")
        for op in plan.operators.values():
            for cache in sorted(op.caches):
                if not os.path.lexists(cache):
                    continue
                if plan.reset_started:
                    raise Refusal("host reset was interrupted while a selected cache still exists; recover host setup first")
                check_cache(cache, op)
                print(f"Cleaning {op.name}: {cache}", flush=True)
                result = native(["cache", "clean", "--force"], op, cache)
                if not isinstance(result, dict) or result.get("cache_dir_removed") is not True or os.path.lexists(cache):
                    raise Refusal(f"typed cache cleanup incomplete: {cache}")
        print("Restoring recorded host settings and disabling admission", flush=True)
        native(["host", "reset", "--operator", plan.initial_operator, "--force"])
    for op in plan.operators.values():
        print(f"Removing managed configuration for {op.name}", flush=True)
        user_config_cleanup(op)
    finalize(plan)
    if any(os.path.lexists(path) for path in (STATE, QUARANTINE, RUNTIME, CGROUP, BINARY, Path("/usr/bin/mvm"))):
        raise Refusal("uninstallation incomplete; retained paths require recovery")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dry-run", action="store_true", help="Show the full removal plan without invoking mvm or changing state")
    parser.add_argument("--yes", action="store_true", help="Confirm permanent deletion of every enrolled operator's managed data")
    parser.add_argument("--cache", action="append", default=[], metavar="USER=PATH", help="Include another explicitly selected operator cache")
    parser.add_argument("--config", action="append", default=[], metavar="USER=PATH", help="Include another explicitly selected operator config directory")
    args = parser.parse_args()
    try:
        if os.geteuid() != 0:
            raise Refusal("run this administrator script with sudo (including --dry-run)")
        if not any(os.path.lexists(path) for path in (STATE, QUARANTINE, RUNTIME, CGROUP, BINARY, Path("/usr/bin/mvm"))):
            print("No system mvmctl installation remains. Unenrolled user files are not selected for deletion.")
            return 0
        plan = make_plan(args.cache, args.config)
        print("SYSTEM-WIDE UNINSTALL: permanently removes all managed VMs, snapshots, volumes, images and kernels.")
        for op in plan.operators.values():
            print(f"  {op.name}: caches={', '.join(map(str, sorted(op.caches)))}; configs={', '.join(map(str, sorted(op.configs)))}")
            print(f"    Personal mvm launchers, when recognized: {op.home / '.local/bin/mvm'}, {op.home / 'bin/mvm'}")
            print(f"    Temporary warm-image directory: {WARM_ROOT / f'mvmctl-{op.uid}'}")
        print(f"  Host authority: {plan.state}; runtime: {RUNTIME}; cgroup: {CGROUP}; binary: {BINARY}")
        if plan.package_command:
            print(f"  Package removal: {' '.join(plan.package_command)}")
        print("External asset mirrors, original SSH files and shared dependency packages are preserved.")
        if args.dry_run:
            print("DRY RUN: no mvm commands or filesystem mutations performed.")
            return 0
        if not args.yes:
            if not sys.stdin.isatty() or input("Type UNINSTALL MVMCTL to continue: ") != "UNINSTALL MVMCTL":
                raise Refusal("uninstall not confirmed")
        uninstall(plan)
        print("UNINSTALL COMPLETE: reinstall with a fresh host init and managed cache.")
        return 0
    except (Refusal, OSError, ValueError, KeyError, subprocess.TimeoutExpired) as exc:
        print(f"UNINSTALL INCOMPLETE: {exc}\nCompleted deletions are not rolled back. Resolve the refusal before retrying.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
