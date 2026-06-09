"""Shell completion helpers for mvmctl CLI commands.

Each function returns ALL identifier types for its entity, filtered by the
`incomplete` prefix the user has already typed.

All data is fetched through the public API layer — CLI never touches core directly.
"""

from __future__ import annotations


def _complete_remote_image_ids(incomplete: str) -> list[str]:
    """Complete with remote image IDs (via API, not direct asset read)."""
    try:
        from mvmctl.api import ImageOperation

        images = ImageOperation.list_all(remote=True)
        results: list[str] = []
        for img in images:
            if (
                hasattr(img, "id")
                and img.id
                and img.id.startswith(incomplete)
                and img.id not in results
            ):
                results.append(img.id)
        return results
    except Exception:
        return []


def _complete_local_image_ids(incomplete: str) -> list[str]:
    """Complete with local image short IDs and OS slugs."""
    try:
        from mvmctl.api import ImageOperation

        images = ImageOperation.list_all(remote=False)
        results: list[str] = []
        for img in images:
            if hasattr(img, "id") and img.id:
                short = img.id[:6] if len(img.id) >= 6 else img.id
                if short.startswith(incomplete) and short not in results:
                    results.append(short)
            if (
                hasattr(img, "type")
                and img.type
                and img.type.startswith(incomplete)
                and img.type not in results
            ):
                results.append(img.type)
        return results
    except Exception:
        return []


def _complete_vm_names(incomplete: str) -> list[str]:
    """Complete with VM names, short IDs, IPv4 addresses, and MAC addresses."""
    try:
        from mvmctl.api import VMInput, VMOperation

        vms = VMOperation.list_all(VMInput())
        results: list[str] = []
        for vm in vms:
            if (
                vm.name
                and vm.name.startswith(incomplete)
                and vm.name not in results
            ):
                results.append(vm.name)
            if vm.id:
                short = vm.id[:6] if len(vm.id) >= 6 else vm.id
                if short.startswith(incomplete) and short not in results:
                    results.append(short)
            if (
                vm.ipv4
                and vm.ipv4.startswith(incomplete)
                and vm.ipv4 not in results
            ):
                results.append(vm.ipv4)
            if (
                vm.mac
                and vm.mac.startswith(incomplete)
                and vm.mac not in results
            ):
                results.append(vm.mac)
        return results
    except Exception:
        return []


def _complete_network_names(incomplete: str) -> list[str]:
    """Complete with network names and short IDs."""
    try:
        from mvmctl.api import NetworkInput, NetworkOperation

        networks = NetworkOperation.list_all(NetworkInput())
        results: list[str] = []
        for net in networks:
            if (
                net.name
                and net.name.startswith(incomplete)
                and net.name not in results
            ):
                results.append(net.name)
            if net.id:
                short = net.id[:6] if len(net.id) >= 6 else net.id
                if short.startswith(incomplete) and short not in results:
                    results.append(short)
        return results
    except Exception:
        return []


def _complete_kernel_ids(incomplete: str) -> list[str]:
    """Complete with kernel type:version combos and short IDs."""
    try:
        from mvmctl.api import KernelOperation

        kernels = KernelOperation.list_all()
        results: list[str] = []
        for k in kernels:
            if k.type and k.version:
                combo = f"{k.type}:{k.version}"
                if combo.startswith(incomplete) and combo not in results:
                    results.append(combo)
            if k.id:
                short = k.id[:6] if len(k.id) >= 6 else k.id
                if short.startswith(incomplete) and short not in results:
                    results.append(short)
        return results
    except Exception:
        return []


def _complete_binary_versions(incomplete: str) -> list[str]:
    """Complete with binary versions and short IDs."""
    try:
        from mvmctl.api import BinaryOperation

        binaries = BinaryOperation.list_all()
        results: list[str] = []
        for b in binaries:
            if (
                b.version
                and b.version.startswith(incomplete)
                and b.version not in results
            ):
                results.append(b.version)
            if b.id:
                short = b.id[:6] if len(b.id) >= 6 else b.id
                if short.startswith(incomplete) and short not in results:
                    results.append(short)
        return results
    except Exception:
        return []


def _complete_key_names(incomplete: str) -> list[str]:
    """Complete with key names and fingerprints (with and without SHA256: prefix)."""
    try:
        from mvmctl.api import KeyInput, KeyOperation

        keys = KeyOperation.list_all(KeyInput())
        results: list[str] = []
        for k in keys:
            if (
                k.name
                and k.name.startswith(incomplete)
                and k.name not in results
            ):
                results.append(k.name)
            if (
                k.fingerprint
                and k.fingerprint.startswith(incomplete)
                and k.fingerprint not in results
            ):
                results.append(k.fingerprint)
            if k.fingerprint:
                bare = k.fingerprint.removeprefix("SHA256:")
                if (
                    bare != k.fingerprint
                    and bare.startswith(incomplete)
                    and bare not in results
                ):
                    results.append(bare)
        return results
    except Exception:
        return []


def _complete_cache_resources(incomplete: str) -> list[str]:
    """Complete with cache resource names."""
    resources = ["vm", "network", "image", "kernel", "binary", "misc"]
    return [r for r in resources if r.startswith(incomplete)]


def _complete_config_categories(incomplete: str) -> list[str]:
    """Complete with config category names."""
    try:
        from mvmctl.constants import OVERRIDABLE_DEFAULTS

        return [c for c in OVERRIDABLE_DEFAULTS if c.startswith(incomplete)]
    except Exception:
        return []


def _complete_config_keys(incomplete: str) -> list[str]:
    """Complete with config key names from all categories."""
    try:
        from mvmctl.constants import OVERRIDABLE_DEFAULTS

        keys: list[str] = []
        for category_keys in OVERRIDABLE_DEFAULTS.values():
            for key in category_keys:
                if key.startswith(incomplete) and key not in keys:
                    keys.append(key)
        return keys
    except Exception:
        return []


def _complete_volume_names(incomplete: str) -> list[str]:
    """Complete with volume names and short IDs."""
    try:
        from mvmctl.api import VolumeInput, VolumeOperation

        volumes = VolumeOperation.list_all(VolumeInput())
        results: list[str] = []
        for v in volumes:
            if (
                v.name
                and v.name.startswith(incomplete)
                and v.name not in results
            ):
                results.append(v.name)
            if v.id:
                short = v.id[:6] if len(v.id) >= 6 else v.id
                if short.startswith(incomplete) and short not in results:
                    results.append(short)
        return results
    except Exception:
        return []
