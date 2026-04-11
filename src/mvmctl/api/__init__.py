"""Public Python API for mvmctl.

Every CLI command maps 1:1 to a function in this package.  The CLI is a thin
presentation layer on top of these modules.  Import directly for scripting or
automation without going through the CLI.

Example::

    from mvmctl.api import keys, host, vm, network, assets
"""

# Lazy imports - do not eagerly load all modules to maintain fast startup time
__all__ = ["assets", "host", "keys", "metadata", "network", "vm"]
