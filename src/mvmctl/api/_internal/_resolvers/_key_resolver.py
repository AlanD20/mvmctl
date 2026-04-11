"""SSH key resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.exceptions import KeyNotFoundError

__all__ = [
    "KeyResolver",
    "KeyResolveResult",
]


@dataclass
class KeyResolveResult:
    items: list[str]
    errors: list[str]
    exit_code: int


class KeyResolver:
    """Resolver for SSH key resources."""

    def by_name(self, name: str) -> str:
        """Resolve key by name (reads from ~/.mvmctl/keys/ directory)."""
        from mvmctl.core.key_manager import resolve_key_input

        return resolve_key_input(name)

    def resolve(self, value: str) -> str:
        """Resolve key by name."""
        return self.by_name(value)

    def resolve_many(self, identifiers: list[str]) -> KeyResolveResult:
        """Resolve multiple key identifiers by name."""
        items: list[str] = []
        errors: list[str] = []

        for identifier in identifiers:
            try:
                item = self.resolve(identifier)
                if item not in items:
                    items.append(item)
            except KeyNotFoundError as e:
                errors.append(f"{identifier}: {e}")

        exit_code = 1 if errors and not items else 0
        return KeyResolveResult(items=items, errors=errors, exit_code=exit_code)
