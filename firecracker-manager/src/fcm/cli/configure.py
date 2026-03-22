"""Guided onboarding wizard — collapses first-time setup into a single flow."""

from __future__ import annotations


import typer

from fcm.core.binary_manager import (
    fetch_binary,
    list_local_versions,
    list_remote_versions,
)
from fcm.core.host import check_kvm_access, get_host_state, init_host
from fcm.core.image import fetch_image, load_images_config
from fcm.core.kernel import build_kernel_pipeline
from fcm.core.key_manager import add_key, create_key, list_keys
from fcm.exceptions import BinaryError, HostError
from fcm.exceptions import KeyError as FCMKeyError
from fcm.utils.console import print_info, print_success, print_warning
from fcm.utils.fs import get_assets_dir, get_cache_dir, get_images_dir, get_kernels_dir

app = typer.Typer(help="Guided onboarding")


def _step_host(skip: bool, non_interactive: bool) -> None:
    """Step 1: Host initialisation."""
    print_info("\n[1/6] Host configuration")
    if skip:
        print_info("  Skipped (--skip-host)")
        return

    cache_dir = get_cache_dir()
    state = None
    try:
        state = get_host_state(cache_dir)
    except HostError:
        pass

    kvm_ok = check_kvm_access()

    if state and kvm_ok:
        print_success("  Already configured")
        return

    if not kvm_ok:
        print_warning("  /dev/kvm is not accessible")

    if non_interactive:
        try:
            init_host(cache_dir)
            print_success("  Host initialized")
        except HostError as e:
            print_warning(f"  Host init failed: {e}")
        return

    print_info("  This will enable IP forwarding and other host settings.")
    if typer.confirm("  Proceed with host init?", default=True):
        try:
            changes = init_host(cache_dir)
            if changes:
                print_success(f"  Host initialized ({len(changes)} change(s))")
            else:
                print_success("  Already configured")
        except HostError as e:
            print_warning(f"  Host init failed: {e}")
            print_info("  Run 'fcm host init' manually when ready.")
    else:
        print_info("  Skipped. Run 'fcm host init' manually when ready.")


def _step_binary(non_interactive: bool) -> None:
    """Step 2: Binary download."""
    print_info("\n[2/6] Firecracker binary")

    local = list_local_versions()
    if local:
        active = [v for v in local if v.is_active]
        label = active[0].version if active else local[0].version
        print_success(f"  Binary available (v{label})")
        return

    if non_interactive:
        try:
            versions = list_remote_versions(limit=1)
            if versions:
                bv = fetch_binary(versions[0])
                print_success(f"  Downloaded v{bv.version}")
            else:
                print_warning("  No remote versions found")
        except BinaryError as e:
            print_warning(f"  Download failed: {e}")
        return

    print_info("  No Firecracker binary found in cache.")
    try:
        versions = list_remote_versions(limit=5)
    except BinaryError:
        print_warning("  Could not list remote versions.")
        print_info("  Run 'fcm asset bin fetch <version>' manually.")
        return

    if not versions:
        print_warning("  No remote versions available.")
        return

    print_info(f"  Latest available: {versions[0]}")
    if typer.confirm(f"  Download v{versions[0]}?", default=True):
        try:
            bv = fetch_binary(versions[0])
            print_success(f"  Downloaded v{bv.version}")
        except BinaryError as e:
            print_warning(f"  Download failed: {e}")
    else:
        print_info("  Skipped. Run 'fcm asset bin fetch <version>' manually.")


def _step_kernel(non_interactive: bool) -> None:
    """Step 3: Kernel download."""
    print_info("\n[3/6] Kernel")

    kernels_dir = get_kernels_dir()
    if kernels_dir.exists() and any(kernels_dir.glob("vmlinux*")):
        print_success("  Kernel available")
        return

    if non_interactive:
        print_info("  Building default kernel (6.1.102)...")
        _build_default_kernel()
        return

    print_info("  No kernel found in cache.")
    if typer.confirm("  Build the default minimal kernel (v6.1.102)?", default=True):
        _build_default_kernel()
    else:
        print_info("  Skipped. Run 'fcm asset kernel build' manually.")


def _build_default_kernel() -> None:
    version = "6.1.102"
    out = get_kernels_dir() / "vmlinux"
    out.parent.mkdir(parents=True, exist_ok=True)
    source_url = f"https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-{version}.tar.xz"
    success = build_kernel_pipeline(
        version=version,
        source_url=source_url,
        output_path=out,
        build_dir=get_cache_dir() / "kernel-build",
        jobs=None,
    )
    if success:
        print_success(f"  Kernel built: {out}")
    else:
        print_warning("  Kernel build failed. Run 'fcm asset kernel build' manually.")


def _step_image(non_interactive: bool) -> None:
    """Step 4: Image download."""
    print_info("\n[4/6] Root filesystem image")

    images_dir = get_images_dir()
    if images_dir.exists() and any(images_dir.glob("*.ext4")) or (
        images_dir.exists() and any(images_dir.glob("*.btrfs"))
    ):
        print_success("  Image available")
        return

    config_path = get_assets_dir() / "images.yaml"
    try:
        images = load_images_config(config_path)
    except Exception:
        print_warning("  Could not load images config.")
        print_info("  Run 'fcm asset image fetch <id>' manually.")
        return

    if not images:
        print_warning("  No images defined in images.yaml")
        return

    if non_interactive:
        # Use the first available image
        spec = images[0]
        print_info(f"  Downloading {spec.id}...")
        images_dir.mkdir(parents=True, exist_ok=True)
        result = fetch_image(spec, images_dir, force=False)
        if result:
            print_success(f"  Image ready: {result}")
        else:
            print_warning("  Image download failed")
        return

    print_info("  No image found in cache. Available images:")
    for i, img in enumerate(images, 1):
        print_info(f"    {i}. {img.id} — {img.name}")

    choice = typer.prompt("  Select image number (or 0 to skip)", default="1")
    try:
        idx = int(choice)
    except ValueError:
        idx = 0

    if idx < 1 or idx > len(images):
        print_info("  Skipped. Run 'fcm asset image fetch <id>' manually.")
        return

    spec = images[idx - 1]
    images_dir.mkdir(parents=True, exist_ok=True)
    result = fetch_image(spec, images_dir, force=False)
    if result:
        print_success(f"  Image ready: {result}")
    else:
        print_warning("  Image download failed")


def _step_ssh_key(non_interactive: bool) -> None:
    """Step 5: SSH key setup."""
    print_info("\n[5/6] SSH key")

    keys = list_keys()
    if keys:
        print_success(f"  Key available: {keys[0].name}")
        return

    if non_interactive:
        try:
            info, priv_path = create_key("fcm-default")
            print_success(f"  Key created: {info.name} ({priv_path})")
        except FCMKeyError as e:
            print_warning(f"  Key creation failed: {e}")
        return

    print_info("  No SSH keys in cache.")
    print_info("    1. Generate a new ED25519 keypair")
    print_info("    2. Import an existing public key")
    print_info("    0. Skip")

    choice = typer.prompt("  Choice", default="1")

    if choice == "1":
        name = typer.prompt("  Key name", default="fcm-default")
        try:
            info, priv_path = create_key(name)
            print_success(f"  Key created: {info.name}")
            print_info(f"  Private key: {priv_path}")
        except FCMKeyError as e:
            print_warning(f"  Key creation failed: {e}")
    elif choice == "2":
        path_str = typer.prompt("  Path to public key")
        name = typer.prompt("  Name for this key", default="default")
        try:
            info = add_key(name, path_str)
            print_success(f"  Key added: {info.name}")
        except FCMKeyError as e:
            print_warning(f"  Key import failed: {e}")
    else:
        print_info("  Skipped. Run 'fcm key add' or 'fcm key create' manually.")


def _step_summary() -> None:
    """Step 6: Print summary."""
    print_info("\n[6/6] Summary")

    cache_dir = get_cache_dir()

    # Check each component
    checks: list[tuple[str, bool]] = []

    try:
        state = get_host_state(cache_dir)
        checks.append(("Host init", state is not None))
    except HostError:
        checks.append(("Host init", False))

    local_bins = list_local_versions()
    checks.append(("Firecracker binary", len(local_bins) > 0))

    kernels_dir = get_kernels_dir()
    has_kernel = kernels_dir.exists() and any(kernels_dir.glob("vmlinux*"))
    checks.append(("Kernel", has_kernel))

    images_dir = get_images_dir()
    has_image = images_dir.exists() and (
        any(images_dir.glob("*.ext4")) or any(images_dir.glob("*.btrfs"))
    )
    checks.append(("Image", has_image))

    keys = list_keys()
    checks.append(("SSH key", len(keys) > 0))

    all_ok = True
    for label, ok in checks:
        status = "[green]ready[/green]" if ok else "[yellow]missing[/yellow]"
        from rich.console import Console
        Console().print(f"  {label}: {status}")
        if not ok:
            all_ok = False

    if all_ok:
        print_success("\nAll set! Create your first VM with:")
        key_flag = f" --ssh-key {keys[0].name}" if keys else ""
        print_info(f"  fcm vm create --name my-vm --image <image-id>{key_flag}")
    else:
        print_warning("\nSome components are missing. Fix them and run 'fcm configure' again.")


@app.callback(invoke_without_command=True)
def configure(
    non_interactive: bool = typer.Option(
        False, "--non-interactive", help="Use defaults, skip prompts"
    ),
    skip_host: bool = typer.Option(
        False, "--skip-host", help="Skip host init step"
    ),
) -> None:
    """Guided setup wizard — run this to get started."""
    print_info("Firecracker Manager — Setup Wizard")
    print_info("=" * 40)

    _step_host(skip=skip_host, non_interactive=non_interactive)
    _step_binary(non_interactive=non_interactive)
    _step_kernel(non_interactive=non_interactive)
    _step_image(non_interactive=non_interactive)
    _step_ssh_key(non_interactive=non_interactive)
    _step_summary()
