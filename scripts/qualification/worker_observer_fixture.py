#!/usr/bin/python3
"""Explicit administrator install/remove for one named-QA worker-stall fixture.

This tool never runs tests or the observer and never changes the normal mvm policy.
Run a reviewed root-owned copy with Python -I and explicit expected hashes.
"""

import argparse
import grp
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import socket
import stat
import subprocess
import uuid

HOST = "mvmctl-030-test"
EXECUTABLE = Path("/usr/local/libexec/mvmctl-qa-worker-stall")
STATE = Path("/var/lib/mvmctl-qa-worker-observer")
MANIFEST = STATE / "manifest.json"
POLICY = Path("/etc/sudoers.d/mvmctl-qa-worker-observer")
ENROLLMENT = Path("/etc/sudoers.d/mvm")
PREFIX = b"#!/usr/bin/python3 -I\n"
MAXIMUM = 128 * 1024


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def unique(pairs):
    value = {}
    for key, item in pairs:
        require(key not in value, "duplicate fixture manifest field")
        value[key] = item
    return value


def fingerprint(row):
    return (row.st_dev, row.st_ino, row.st_mode, row.st_uid, row.st_gid,
            row.st_nlink, row.st_size, row.st_mtime_ns, row.st_ctime_ns)


def directory(path, *, root_owned=True):
    """Inspect each component without following symlinks."""
    path = Path(path)
    require(path.is_absolute() and ".." not in path.parts, "absolute canonical directory required")
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        for component in path.parts[1:]:
            child = os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            os.close(fd)
            fd = child
            row = os.fstat(fd)
            if root_owned:
                require(row.st_uid == row.st_gid == 0 and not row.st_mode & 0o022,
                        "fixture parent must be root-owned and not writable by operators")
        return os.fstat(fd)
    finally:
        os.close(fd)


def read(path, *, root_owned=False, mode=None, links=(1,)):
    path = Path(path)
    directory(path.parent, root_owned=root_owned)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK)
    try:
        row = os.fstat(fd)
        require(stat.S_ISREG(row.st_mode) and row.st_nlink in links and row.st_size <= MAXIMUM,
                "expected bounded single-link regular file")
        if root_owned:
            require(row.st_uid == row.st_gid == 0 and not row.st_mode & 0o022, "unsafe root fixture file")
        if mode is not None:
            require(stat.S_IMODE(row.st_mode) == mode, "fixture mode differs")
        raw = os.read(fd, MAXIMUM + 1)
        require(len(raw) == row.st_size and fingerprint(row) == fingerprint(os.fstat(fd)), "file changed during read")
        require(fingerprint(row) == fingerprint(path.lstat()), "file replaced during read")
        return raw
    finally:
        os.close(fd)


def policy_bytes(uid, gid):
    require(type(uid) is int and type(gid) is int and uid > 0 and gid > 0, "non-root numeric operator required")
    # Sudo's anchored argument regex admits three exact fields, never an interpreter.
    return (f"#{uid} ALL=(root) NOPASSWD: {EXECUTABLE} ^[0-9a-f]{{32}} {uid} {gid}$\n").encode()


def validate_namespace(args):
    require(os.getuid() == os.geteuid() == 0, "explicit root administrator invocation required")
    require(socket.gethostname() == HOST, "fixture is restricted to the named QA VM")
    require(str(uuid.UUID(args.boot_id)) == args.boot_id, "canonical expected boot UUID required")
    require(Path("/proc/sys/kernel/random/boot_id").read_text().strip() == args.boot_id, "QA boot changed")
    account = pwd.getpwnam(args.operator_user)
    require((account.pw_uid, account.pw_gid, account.pw_dir) ==
            (args.operator_uid, args.operator_gid, args.operator_home), "operator passwd identity differs")
    require(pwd.getpwuid(args.operator_uid).pw_name == args.operator_user, "operator reverse lookup differs")
    require(args.operator_uid > 0 and args.operator_gid > 0, "non-root enrolled operator required")
    home = directory(args.operator_home, root_owned=False)
    require(home.st_uid == args.operator_uid and home.st_gid == args.operator_gid and not home.st_mode & 0o022,
            "operator home identity or permissions differ")
    require(grp.getgrnam("mvm").gr_gid in os.getgrouplist(args.operator_user, args.operator_gid),
            "operator is not enrolled in mvm group")
    enrollment = read(ENROLLMENT, root_owned=True, mode=0o440)
    lines = [line.strip() for line in enrollment.decode().splitlines() if line.strip() and not line.startswith("#")]
    require(lines == ["%mvm ALL=(root) NOPASSWD: /usr/local/bin/mvm __mvm_privileged_v1 *"],
            "normal typed mvm enrollment differs")
    for value in (args.source_sha256, args.tool_sha256):
        require(re.fullmatch(r"[0-9a-f]{64}", value), "explicit lowercase SHA256 required")
    require(digest(read(Path(__file__).absolute(), root_owned=True)) == args.tool_sha256,
            "reviewed root-owned administrator tool hash differs")
    return dict(user=args.operator_user, uid=args.operator_uid, gid=args.operator_gid, home=args.operator_home), enrollment


def create(path, raw, mode):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, mode)
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, "wb", closefd=False) as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(fd)
    except BaseException:
        if os.path.samestat(os.fstat(fd), path.lstat()):
            path.unlink()
        raise
    finally:
        os.close(fd)


def visudo(path=None):
    command = ["/usr/sbin/visudo", "-c"]
    if path is not None:
        command += ["-f", str(path)]
    subprocess.run(command, check=True, capture_output=True, text=True,
                   env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"}, timeout=15)


def remove_exact(path, expected, mode, *, links=(1,)):
    require(read(path, root_owned=True, mode=mode, links=links) == expected, "refusing changed/foreign fixture during rollback")
    path.unlink()


def install(args, operator, enrollment):
    for parent in (EXECUTABLE.parent, STATE.parent, POLICY.parent):
        directory(parent)
    require(not any(os.path.lexists(path) for path in (EXECUTABLE, STATE, POLICY)), "refusing existing/foreign fixture")
    source = read(args.source)
    require(digest(source) == args.source_sha256, "observer source SHA256 differs")
    require(not source.startswith(b"#!"), "observer source must be the exact repository Python module")
    payload = PREFIX + source
    policy = policy_bytes(operator["uid"], operator["gid"])
    manifest = dict(schema_version=1, hostname=HOST, boot_id=args.boot_id, operator=operator,
                    source_sha256=args.source_sha256, payload_sha256=digest(payload), tool_sha256=args.tool_sha256,
                    enrollment_sha256=digest(enrollment), policy_sha256=digest(policy))
    raw_manifest = (json.dumps(manifest, sort_keys=True) + "\n").encode()
    owned = []
    made_directory = False
    try:
        STATE.mkdir(mode=0o755)
        made_directory = True
        os.chmod(STATE, 0o755)
        for path, raw, mode in ((EXECUTABLE, payload, 0o555), (MANIFEST, raw_manifest, 0o644),
                                (STATE / "sudoers.pending", policy, 0o440)):
            create(path, raw, mode)
            owned.append((path, raw, mode))
        visudo(STATE / "sudoers.pending")
        os.link(STATE / "sudoers.pending", POLICY, follow_symlinks=False)
        owned.append((POLICY, policy, 0o440))
        # Do not retain a second hard link to the admitted policy.
        (STATE / "sudoers.pending").unlink()
        owned.pop(-2)
        visudo()
        require(read(ENROLLMENT, root_owned=True, mode=0o440) == enrollment, "normal enrollment changed")
        require(read(EXECUTABLE, root_owned=True, mode=0o555) == payload, "installed payload differs")
    except BaseException:
        for path, raw, mode in reversed(owned):
            remove_exact(path, raw, mode, links=(1, 2))
        if made_directory:
            STATE.rmdir()
        raise
    return manifest


def uninstall(args, operator, enrollment):
    directory(STATE)
    require(stat.S_IMODE(STATE.lstat().st_mode) == 0o755, "fixture state mode differs")
    raw = read(MANIFEST, root_owned=True, mode=0o644)
    manifest = json.loads(raw, object_pairs_hook=unique)
    expected_keys = {"schema_version", "hostname", "boot_id", "operator", "source_sha256", "payload_sha256",
                     "tool_sha256", "enrollment_sha256", "policy_sha256"}
    require(set(manifest) == expected_keys and type(manifest["schema_version"]) is int and
            manifest["schema_version"] == 1, "fixture manifest schema differs")
    require(manifest["hostname"] == HOST and manifest["boot_id"] == args.boot_id and manifest["operator"] == operator,
            "fixture namespace differs")
    require(manifest["source_sha256"] == args.source_sha256 and manifest["tool_sha256"] == args.tool_sha256 and
            manifest["enrollment_sha256"] == digest(enrollment), "fixture provenance/enrollment differs")
    payload = read(EXECUTABLE, root_owned=True, mode=0o555)
    policy = read(POLICY, root_owned=True, mode=0o440)
    require(digest(payload) == manifest["payload_sha256"] and payload.startswith(PREFIX) and
            digest(payload[len(PREFIX):]) == args.source_sha256, "fixture payload differs")
    require(policy == policy_bytes(operator["uid"], operator["gid"]) and digest(policy) == manifest["policy_sha256"],
            "fixture policy differs")
    require(set(os.listdir(STATE)) == {MANIFEST.name}, "foreign entries in fixture state")
    # Revoke admission before removing code; existing observer retains its pidfd
    # and unconditional continuation path independently of these installed files.
    remove_exact(POLICY, policy, 0o440)
    visudo()
    remove_exact(EXECUTABLE, payload, 0o555)
    remove_exact(MANIFEST, raw, 0o644)
    STATE.rmdir()
    require(read(ENROLLMENT, root_owned=True, mode=0o440) == enrollment, "normal enrollment changed")
    return dict(removed=True, boot_id=args.boot_id, source_sha256=args.source_sha256)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "remove"))
    parser.add_argument("--boot-id", required=True)
    parser.add_argument("--operator-user", required=True)
    parser.add_argument("--operator-uid", required=True, type=int)
    parser.add_argument("--operator-gid", required=True, type=int)
    parser.add_argument("--operator-home", required=True)
    parser.add_argument("--source", type=Path)
    parser.add_argument("--source-sha256", required=True)
    parser.add_argument("--tool-sha256", required=True)
    args = parser.parse_args()
    require(args.action != "install" or args.source is not None, "install requires exact observer source path")
    require(args.action != "remove" or args.source is None, "remove uses installed provenance, not caller source")
    operator, enrollment = validate_namespace(args)
    print(json.dumps(install(args, operator, enrollment) if args.action == "install" else
                     uninstall(args, operator, enrollment), sort_keys=True))


if __name__ == "__main__":
    main()
