from __future__ import annotations

import os
from contextlib import contextmanager
from pathlib import Path
from typing import Any

from mvmctl.exceptions import MVMError


class OptimizedGuestfs:
    def __init__(self, disk_path: Path, readonly: bool = False) -> None:
        self.disk_path = disk_path
        self.readonly = readonly
        self._g: Any = None
        self._orig_env: dict[str, str | None] = {}

    def _setup_environment(self) -> None:
        self._orig_env = {
            "LIBGUESTFS_BACKEND": os.environ.get("LIBGUESTFS_BACKEND"),
            "LIBGUESTFS_CACHEDIR": os.environ.get("LIBGUESTFS_CACHEDIR"),
        }
        os.environ["LIBGUESTFS_BACKEND"] = "direct"
        if Path("/dev/shm").exists():
            os.environ["LIBGUESTFS_CACHEDIR"] = "/dev/shm"

    def _restore_environment(self) -> None:
        for key, value in self._orig_env.items():
            if value is not None:
                os.environ[key] = value
            elif key in os.environ:
                del os.environ[key]

    def _create_handle(self) -> Any:
        import importlib

        guestfs = importlib.import_module("guestfs")
        g = guestfs.GuestFS(python_return_dict=True)

        if hasattr(g, "set_recovery_proc"):
            g.set_recovery_proc(False)
        if hasattr(g, "set_autosync"):
            g.set_autosync(False)
        if hasattr(g, "set_network"):
            g.set_network(False)
        if hasattr(g, "set_smp"):
            g.set_smp(1)
        if hasattr(g, "set_memsize"):
            g.set_memsize(256)

        g.add_drive_opts(
            str(self.disk_path),
            format="raw",
            readonly=self.readonly,
            cachemode="unsafe",
        )

        return g

    def __enter__(self) -> Any:
        self._setup_environment()
        try:
            self._g = self._create_handle()
            self._g.launch()
            return self._g
        except Exception as e:
            self._restore_environment()
            raise MVMError(f"Failed to launch guestfs: {e}") from e

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        try:
            if self._g is not None:
                try:
                    self._g.shutdown()
                except Exception:
                    pass
        finally:
            self._restore_environment()


@contextmanager
def optimized_guestfs(disk_path: Path, readonly: bool = False) -> Any:
    with OptimizedGuestfs(disk_path, readonly) as g:
        yield g


def check_libguestfs() -> bool:
    try:
        import importlib

        guestfs = importlib.import_module("guestfs")
        return hasattr(guestfs, "GuestFS")
    except ImportError:
        return False
