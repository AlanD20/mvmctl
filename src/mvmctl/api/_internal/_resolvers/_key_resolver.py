"""SSH key resolution helpers."""

from __future__ import annotations

from pathlib import Path

__all__ = [
    "resolve_default_public_keys",
]


def resolve_default_public_keys(
    ssh_key: str | list[str] | None,
) -> str | list[str] | None:
    """Resolve SSH key specification to key content.

    Args:
        ssh_key: Can be:
            - None: Returns None (use VM default)
            - String "default": Fetch default keys
            - Single key path: Read and return content
            - List of key paths: Read and return list of contents

    Returns:
        Resolved key content or None
    """
    if ssh_key is None:
        return None

    if ssh_key == "default":
        from mvmctl.core.key_manager import get_default_keys

        return get_default_keys()

    if isinstance(ssh_key, list):
        resolved: list[str] = []
        for key in ssh_key:
            if key == "default":
                from mvmctl.core.key_manager import get_default_keys

                default_keys = get_default_keys()
                if isinstance(default_keys, list):
                    resolved.extend(default_keys)
                elif default_keys:
                    resolved.append(default_keys)
            else:
                key_path = Path(key)
                if not key_path.exists():
                    from mvmctl.exceptions import VMCreateError

                    raise VMCreateError(f"SSH key file not found: {key}")
                resolved.append(key_path.read_text().strip())
        return resolved

    # Single key path
    key_path = Path(ssh_key)
    if not key_path.exists():
        from mvmctl.exceptions import VMCreateError

        raise VMCreateError(f"SSH key file not found: {ssh_key}")

    return key_path.read_text().strip()
