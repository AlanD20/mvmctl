"""
PyInstaller hook for mvmctl.

This hook ensures PyInstaller collects all CLI submodules that are lazy-loaded
by LazyMVMGroup. The heavy ``collect_all`` call is deferred until PyInstaller
actually accesses the hook variables (``datas``, ``binaries``, ``hiddenimports``).
This keeps module-level import time fast for startup-time compliance checks.
"""

from __future__ import annotations

_RESULTS: dict[str, object] | None = None


def _ensure_loaded() -> None:
    """Lazily load PyInstaller hook data on first access."""
    global _RESULTS
    if _RESULTS is not None:
        return
    from PyInstaller.utils.hooks import (  # type: ignore[import-untyped]
        collect_all,
    )

    datas, binaries, hiddenimports = collect_all("mvmctl")

    # Explicitly declare CLI submodules for lazy loading
    # These are imported on-demand by LazyMVMGroup.get_command()
    hiddenimports += [
        "mvmctl.cli.bin",
        "mvmctl.cli.cache",
        "mvmctl.cli.config",
        "mvmctl.cli.console",
        "mvmctl.cli.host",
        "mvmctl.cli.init",
        "mvmctl.cli.key",
        "mvmctl.cli.network",
        "mvmctl.cli.vm",
    ]
    _RESULTS = {
        "datas": datas,
        "binaries": binaries,
        "hiddenimports": hiddenimports,
    }


def __getattr__(name: str) -> object:
    """Defer hook variable resolution until PyInstaller accesses them."""
    _ensure_loaded()
    assert _RESULTS is not None
    try:
        return _RESULTS[name]
    except KeyError:
        raise AttributeError(f"module {__name__!r} has no attribute {name!r}")


def __dir__() -> list[str]:
    """Expose hook variables for tab-completion and introspection."""
    return ["datas", "binaries", "hiddenimports"]
