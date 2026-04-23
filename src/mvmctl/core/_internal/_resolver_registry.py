"""Lazy resolver registry to prevent circular imports."""

from __future__ import annotations

from typing import Callable

_factories: dict[str, Callable[[], type]] = {}


def register(name: str, factory: Callable[[], type]) -> None:
    """Register a resolver by name.

    factory is a zero-argument callable that returns the resolver CLASS.
    Using a lambda delays the actual import until call time.
    """
    _factories[name] = factory


def get(name: str) -> type:
    """Get a resolver class by name. Raises KeyError if unknown."""
    if name not in _factories:
        available = ", ".join(sorted(_factories.keys()))
        raise KeyError(f"Unknown resolver '{name}'. Registered: {available}")
    return _factories[name]()
