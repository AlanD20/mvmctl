"""Tests for core/_shared/_resolver_registry.py.

The registry is a simple ``dict[str, Callable[[], type]]`` with
``register()`` and ``get()`` functions.  ``get()`` raises ``KeyError``
with a helpful message when the resolver name is not found.
"""

from __future__ import annotations

import pytest

from mvmctl.core._shared._resolver_registry import get, register


class TestResolverRegistry:
    """Tests for resolver_registry.register() and resolver_registry.get()."""

    def test_register_and_get(self) -> None:
        """A registered factory can be retrieved via get()."""
        # Use a simple type for the test
        class FakeResolver:
            pass

        register("test_resolver", lambda: FakeResolver)
        resolved = get("test_resolver")
        assert resolved is FakeResolver

    def test_get_unknown_raises_key_error(self) -> None:
        """get() raises KeyError with registered names for an unknown key."""
        register("dummy", lambda: object)

        with pytest.raises(KeyError) as excinfo:
            get("nonexistent")

        msg = str(excinfo.value)
        assert "nonexistent" in msg
        assert "dummy" in msg  # helpful message includes registered names
