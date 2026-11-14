"""Filesystem checks for administrator-only mvmctl uninstallation."""

from __future__ import annotations

import contextlib
import ctypes
import json
import os
from pathlib import Path
import stat
from typing import Iterator


class Refusal(RuntimeError):
    """Ownership or quiescence could not be established; retain remaining state."""


DIRECTORY_FLAGS = os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW


def identity(info: os.stat_result) -> tuple[int, int]:
    return info.st_dev, info.st_ino


def fingerprint(info: os.stat_result) -> tuple:
    """Reads may update atime; content, ownership and namespace identity may not change."""
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


@contextlib.contextmanager
def directory(path: Path, *, trusted: bool = False) -> Iterator[int]:
    """Open every component without following a symlink, including intermediate ones."""
    if not path.is_absolute() or ".." in path.parts:
        raise Refusal(f"not a canonical absolute path: {path}")
    fd = os.open("/", DIRECTORY_FLAGS)
    try:
        if trusted:
            private_root(fd)
        for component in path.parts[1:]:
            child = os.open(component, DIRECTORY_FLAGS, dir_fd=fd)
            os.close(fd)
            fd = child
            if trusted:
                private_root(fd)
        yield fd
    finally:
        os.close(fd)


def capture(parent: int, source: str, destination: str) -> None:
    """Linux atomic rename without replacing another uninstall generation."""
    libc = ctypes.CDLL(None, use_errno=True)
    rename = libc.renameat2
    rename.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
    rename.restype = ctypes.c_int
    if rename(parent, os.fsencode(source), parent, os.fsencode(destination), 1) != 0:
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error), destination)


def private_root(fd: int) -> None:
    info = os.fstat(fd)
    if info.st_uid != 0 or info.st_mode & 0o022:
        raise Refusal("administrator directory is not root-owned and protected")


def read_record(parent: int, name: str) -> dict:
    fd = os.open(name, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
    try:
        before = os.fstat(fd)
        if (not stat.S_ISREG(before.st_mode) or before.st_uid != 0 or before.st_nlink != 1
                or before.st_mode & 0o022 or not 0 < before.st_size <= 65536):
            raise Refusal(f"unsafe administrator record: {name}")
        with os.fdopen(os.dup(fd), "rb") as source:
            raw = source.read(65537)
        if len(raw) > 65536 or fingerprint(os.fstat(fd)) != fingerprint(before):
            raise Refusal(f"administrator record changed: {name}")
        def unique(pairs):
            result = {}
            for key, value in pairs:
                if key in result:
                    raise Refusal(f"duplicate record field: {key}")
                result[key] = value
            return result
        record = json.loads(raw, object_pairs_hook=unique)
        if not isinstance(record, dict):
            raise Refusal(f"not an object record: {name}")
        return record
    finally:
        os.close(fd)


def mount_id(fd: int) -> str:
    for line in Path(f"/proc/self/fdinfo/{fd}").read_text().splitlines():
        if line.startswith("mnt_id:"):
            return line.split()[1]
    raise Refusal("kernel did not report a mount identity")


def reject_mounts_beneath(path: Path) -> None:
    for line in Path("/proc/self/mountinfo").read_text().splitlines():
        raw = line.split()[4]
        for escaped, value in (("\\040", " "), ("\\011", "\t"), ("\\012", "\n"), ("\\134", "\\")):
            raw = raw.replace(escaped, value)
        mount = Path(raw)
        if mount == path or path in mount.parents:
            raise Refusal(f"mounted data must be handled explicitly before uninstall: {mount}")


def inspect_tree(fd: int, owner: int, expected_mount: str) -> None:
    """Preflight the complete selected tree before deleting any of it."""
    current = os.fstat(fd)
    if current.st_uid != owner or mount_id(fd) != expected_mount:
        raise Refusal("directory owner or mount identity changed")
    for name in os.listdir(fd):
        info = os.stat(name, dir_fd=fd, follow_symlinks=False)
        if info.st_uid != owner:
            raise Refusal(f"foreign-owned entry: {name}")
        if stat.S_ISDIR(info.st_mode):
            child = os.open(name, DIRECTORY_FLAGS, dir_fd=fd)
            try:
                if identity(os.fstat(child)) != identity(info):
                    raise Refusal(f"directory changed: {name}")
                inspect_tree(child, owner, expected_mount)
            finally:
                os.close(child)
        elif not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise Refusal(f"unexpected symlink, shared inode or special entry: {name}")


def remove_contents(fd: int, owner: int, expected_mount: str) -> None:
    """Remove relative to pinned descriptors; never traverse a replacement or another mount."""
    if os.fstat(fd).st_uid != owner or mount_id(fd) != expected_mount:
        raise Refusal("removal directory identity is unsafe")
    for name in os.listdir(fd):
        before = os.stat(name, dir_fd=fd, follow_symlinks=False)
        if before.st_uid != owner:
            raise Refusal(f"foreign-owned entry during removal: {name}")
        if stat.S_ISDIR(before.st_mode):
            child = os.open(name, DIRECTORY_FLAGS, dir_fd=fd)
            try:
                if identity(os.fstat(child)) != identity(before):
                    raise Refusal(f"directory replaced before removal: {name}")
                remove_contents(child, owner, expected_mount)
                if identity(os.stat(name, dir_fd=fd, follow_symlinks=False)) != identity(before):
                    raise Refusal(f"directory replaced during removal: {name}")
                os.rmdir(name, dir_fd=fd)
            finally:
                os.close(child)
        elif stat.S_ISREG(before.st_mode) and before.st_nlink == 1:
            after = os.stat(name, dir_fd=fd, follow_symlinks=False)
            if fingerprint(after) != fingerprint(before):
                raise Refusal(f"file replaced during removal: {name}")
            os.unlink(name, dir_fd=fd)
        else:
            raise Refusal(f"unsafe file during removal: {name}")
    os.fsync(fd)


def remove_tree(path: Path, owner: int) -> None:
    """Call only inside a root-controlled namespace or after dropping to the owning UID."""
    reject_mounts_beneath(path)
    with directory(path.parent) as parent:
        try:
            fd = os.open(path.name, DIRECTORY_FLAGS, dir_fd=parent)
        except FileNotFoundError:
            return
        try:
            before = os.fstat(fd)
            mount = mount_id(fd)
            inspect_tree(fd, owner, mount)
            remove_contents(fd, owner, mount)
            if identity(os.stat(path.name, dir_fd=parent, follow_symlinks=False)) != identity(before):
                raise Refusal(f"tree replaced before final removal: {path}")
            os.rmdir(path.name, dir_fd=parent)
            os.fsync(parent)
        finally:
            os.close(fd)
