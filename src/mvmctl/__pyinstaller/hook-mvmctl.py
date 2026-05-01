"""
PyInstaller hook for mvmctl.

This hook ensures PyInstaller collects all CLI submodules that are lazy-loaded
by LazyMVMGroup. The imports are declared as hiddenimports so PyInstaller
includes them in the frozen build without requiring eager runtime imports.
"""

from PyInstaller.utils.hooks import collect_all  # type: ignore[import-untyped]

# Collect all mvmctl package data (assets, YAML configs)
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
