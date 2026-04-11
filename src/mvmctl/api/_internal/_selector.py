"""VM selector resolution — resolves name/ID prefix to VM name."""

from __future__ import annotations

import re
from dataclasses import dataclass

__all__ = [
    "resolve_vm_selector",
    "resolve_vm_targets",
    "ResolveVMTargetsResult",
]


@dataclass
class ResolveVMTargetsResult:
    """Result of resolving VM targets."""

    targets: list[str]
    errors: list[str]
    exit_code: int


def resolve_vm_selector(selector: str) -> str:
    """Resolve a VM selector (name or ID prefix) to full VM name.

    If selector contains a slash, it's treated as a regex pattern.
    Otherwise, it's treated as an exact name match or ID prefix.

    Args:
        selector: VM name, ID prefix, or regex pattern

    Returns:
        Full VM name

    Raises:
        VMNotFoundError: If no matching VM found
    """
    from mvmctl.core.vm_manager import get_vm_manager

    manager = get_vm_manager()

    # Check if it's a regex pattern
    if "/" in selector:
        pattern = re.compile(selector)
        all_vms = manager.list_all()
        matches = [vm.name for vm in all_vms if pattern.match(vm.name)]
        if not matches:
            from mvmctl.exceptions import VMNotFoundError

            raise VMNotFoundError(f"No VMs match pattern: {selector}")
        if len(matches) > 1:
            from mvmctl.exceptions import VMNotFoundError

            raise VMNotFoundError(f"Pattern {selector} matches multiple VMs: {matches}")
        return matches[0]

    # Check exact name match first
    try:
        vm = manager.get(selector)
        if vm is not None:
            return vm.name
    except Exception:
        pass

    # Try ID prefix match
    prefix_matches = manager.find_by_id_prefix(selector)
    if not prefix_matches:
        from mvmctl.exceptions import VMNotFoundError

        raise VMNotFoundError(f"VM not found: {selector}")
    if len(prefix_matches) > 1:
        from mvmctl.exceptions import VMNotFoundError

        names = ", ".join(vm.name for vm in prefix_matches)
        raise VMNotFoundError(f"ID prefix {selector} matches multiple VMs: {names}")

    return prefix_matches[0].name


def resolve_vm_targets(ids: list[str], names: list[str]) -> ResolveVMTargetsResult:
    """Resolve multiple VM selectors to full names.

    Args:
        ids: List of VM ID prefixes
        names: List of VM names

    Returns:
        ResolveVMTargetsResult with resolved names and any errors
    """
    targets: list[str] = []
    errors: list[str] = []

    for name in names:
        targets.append(name)

    for selector in ids:
        try:
            resolved = resolve_vm_selector(selector)
            if resolved not in targets:
                targets.append(resolved)
        except Exception as e:
            errors.append(f"{selector}: {e}")

    exit_code = 1 if errors else 0
    return ResolveVMTargetsResult(targets=targets, errors=errors, exit_code=exit_code)
