"""Public Python API for firecracker-manager.

Every CLI command maps 1:1 to a function in this package.  The CLI is a thin
presentation layer on top of these modules.  Import directly for scripting or
automation without going through the CLI.

Example::

    from mvmctl.api import vms, network, assets, keys, host
"""

from mvmctl.api import assets, host, keys, network, vms

__all__ = ["assets", "host", "keys", "network", "vms"]
