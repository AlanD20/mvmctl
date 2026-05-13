"""Lazy resolver registry to prevent circular imports."""

from __future__ import annotations

import importlib
from collections.abc import Callable

_factories: dict[str, Callable[[], type]] = {}

# Mapping of known resolver names to their module paths.
# When get() is called for an unregistered name, the registry
# auto-imports the module to trigger its module-level register()
# side-effect before falling back to the KeyError.
_RESOLVER_MODULE_PATHS: dict[str, str] = {
    "binary": "mvmctl.core.binary._resolver",
    "image": "mvmctl.core.image._resolver",
    "kernel": "mvmctl.core.kernel._resolver",
    "key": "mvmctl.core.key._resolver",
    "network": "mvmctl.core.network._resolver",
    "network_lease": "mvmctl.core.network._lease_resolver",
    "vm": "mvmctl.core.vm._resolver",
    "volume": "mvmctl.core.volume._resolver",
    "iptables_rule": "mvmctl.core._shared._iptables_tracker._resolver",
}


def register(name: str, factory: Callable[[], type]) -> None:
    """
    Register a resolver by name.

    factory is a zero-argument callable that returns the resolver CLASS.
    Using a lambda delays the actual import until call time.
    """
    _factories[name] = factory


def get(name: str) -> type:
    """Get a resolver class by name. Raises KeyError if unknown."""
    if name not in _factories:
        # Auto-discovery: try importing the expected module to trigger
        # its module-level register() side-effect.
        module_path = _RESOLVER_MODULE_PATHS.get(name)
        if module_path is not None:
            importlib.import_module(module_path)
        # Check again after attempted import
        if name not in _factories:
            available = ", ".join(sorted(_factories.keys()))
            raise KeyError(
                f"Unknown resolver '{name}'. Registered: {available}"
            )
    return _factories[name]()
