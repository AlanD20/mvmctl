"""
Reusable lazy import for module __init__.py re-exports.

Provides ``resolve_lazy()`` for use in module-level ``__getattr__`` functions.
Keeps package ``__init__.py`` files from eagerly importing all submodules
when the package is first loaded.

Usage in any package ``__init__.py``::

    from mvmctl.utils._lazy_import import resolve_lazy

    __all__ = [
        "SomeClass",
        "OtherClass",
    ]

    _LAZY_MAP: dict[str, tuple[str, str]] = {
        "SomeClass": ("parent_pkg._submodule", "SomeClass"),
        "OtherClass": ("parent_pkg._other", "OtherClass"),
    }

    def __getattr__(name: str) -> object:
        return resolve_lazy(name, _LAZY_MAP, __name__)

    def __dir__() -> list[str]:
        return __all__

Reference: PEP 562 — Module ``__getattr__`` and ``__dir__``.
"""

from __future__ import annotations

import importlib
from typing import Any


def resolve_lazy(
    name: str,
    mapping: dict[str, tuple[str, str]],
    module_name: str,
) -> Any:
    """
    Resolve a lazily-imported name from a module's ``__getattr__``.

    Args:
        name: The attribute name being accessed (from ``__getattr__``'s
            argument).
        mapping: Dict mapping *name* → ``(module_path, attribute_name)``.
            *module_path* is a fully-qualified module path suitable for
            ``importlib.import_module()``.
            *attribute_name* is the name to fetch from that module.
        module_name: The calling module's ``__name__``, used solely for
            ``AttributeError`` messages.

    Returns:
        The resolved object (class, function, etc.).

    Raises:
        AttributeError: If *name* is not in *mapping*.

    """
    entry = mapping.get(name)
    if entry is None:
        raise AttributeError(
            f"module {module_name!r} has no attribute {name!r}"
        )

    module_path, attr_name = entry
    module = importlib.import_module(module_path)
    return getattr(module, attr_name)
