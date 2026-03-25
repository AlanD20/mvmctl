"""Guided onboarding wizard — collapses first-time setup into a single flow."""

from __future__ import annotations

from pathlib import Path

import typer

from mvmctl.api.assets import (
    build_kernel_pipeline,
    fetch_binary,
    fetch_image,
    list_local_versions,
    list_remote_versions,
    load_images_config,
)
from mvmctl.api.host import check_kvm_access, get_host_state, init_host
from mvmctl.api.keys import add_key, create_key, list_keys
from mvmctl.constants import (
    DEFAULT_KERNEL_VERSION,
    KERNEL_TARBALL_URL_TEMPLATE,
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.core.config_state import initialize_default_config
from mvmctl.exceptions import BinaryError, HostError, KernelError, MVMError, MVMKeyError
from mvmctl.utils.console import print_info, print_success, print_warning
from mvmctl.utils.fs import get_assets_dir, get_cache_dir, get_images_dir, get_kernels_dir

app = typer.Typer(
    help="Guided onboarding",
    rich_markup_mode=None,
    add_completion=False,
)


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the configure command."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


def _run_host_init_noninteractive(cache_dir: Path) -> None:
    """Run host initialisation without prompts (for --non-interactive mode)."""
    try:
        init_host(cache_dir)
        print_success("  Host initialized")
    except HostError as e:
        print_warning(f"  Host init failed: {e}")
    try:
        from mvmctl.api.network import ensure_default_network

        ensure_default_network()
        print_success("  Default network ready")
    except MVMError:
        pass


def _run_host_init_interactive() -> None:
    """Prompt the user and run host initialisation interactively."""
    if typer.confirm("  Run 'sudo mvm host init' now?", default=True):
        import shutil
        import subprocess
        import sys

        mvm_bin = shutil.which("mvm") or sys.argv[0]
        result = subprocess.run(["sudo", mvm_bin, "host", "init"])
        if result.returncode == 0:
            print_success("  Host initialized.")
            print_warning(
                "  ACTION REQUIRED: Log out and back in for group membership to take effect."
            )
            print_info("  Or run immediately: newgrp mvm")
            print_info("  Then re-run: mvm configure")
        else:
            print_warning("  Host init failed. Run 'sudo mvm host init' manually.")
    else:
        print_info("  Skipped. Run 'sudo mvm host init' manually when ready.")


def _step_host(skip: bool, non_interactive: bool) -> None:
    """Step 1: Privilege setup and host initialisation."""
    print_info("\n[1/6] Privilege setup")
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

    print_info("  This requires sudo once to create the 'mvm' group and sudoers drop-in.")
    print_info("  After this, you won't need sudo for any mvm commands.")

    if non_interactive:
        _run_host_init_noninteractive(cache_dir)
    else:
        _run_host_init_interactive()


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
        print_info("  Run 'mvm bin fetch <version>' manually.")
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
        print_info("  Skipped. Run 'mvm bin fetch <version>' manually.")


def _step_kernel(non_interactive: bool) -> None:
    """Step 3: Kernel download."""
    print_info("\n[3/6] Kernel")

    kernels_dir = get_kernels_dir()
    if kernels_dir.exists() and any(kernels_dir.glob("vmlinux*")):
        print_success("  Kernel available")
        return

    if non_interactive:
        print_info(f"  Building default kernel ({DEFAULT_KERNEL_VERSION})...")
        _build_default_kernel()
        return

    print_info("  No kernel found in cache.")
    if typer.confirm(
        f"  Build the default minimal kernel (v{DEFAULT_KERNEL_VERSION})?", default=True
    ):
        _build_default_kernel()
    else:
        print_info("  Skipped. Run 'mvm kernel build' manually.")


def _build_default_kernel() -> None:
    """Build the default minimal kernel (v6.1.102) for Firecracker.

    Downloads the Linux 6.1.102 source tarball from kernel.org, applies the
    Firecracker microvm kernel configuration, compiles ``vmlinux``, and copies
    it to ``<cache-dir>/kernels/vmlinux``. Intermediate build artifacts are
    kept in ``<cache-dir>/kernel-build/``.

    Prints a success or warning message depending on the outcome. Callers
    should check for the presence of the output file afterwards if they need
    to handle failure programmatically.
    """
    version = DEFAULT_KERNEL_VERSION
    out = get_kernels_dir() / "vmlinux"
    out.parent.mkdir(parents=True, exist_ok=True)
    source_url = KERNEL_TARBALL_URL_TEMPLATE.format(version=version)
    try:
        build_kernel_pipeline(
            version=version,
            source_url=source_url,
            output_path=out,
            build_dir=get_cache_dir() / "kernel-build",
            jobs=None,
        )
    except KernelError as exc:
        print_warning(f"  Kernel build failed: {exc}. Run 'mvm kernel build' manually.")
    else:
        print_success(f"  Kernel built: {out}")


def _step_image(non_interactive: bool) -> None:
    """Step 4: Image download."""
    print_info("\n[4/6] Root filesystem image")

    images_dir = get_images_dir()
    if images_dir.exists() and any(
        images_dir.glob(f"*{ext}") for ext in SUPPORTED_IMAGE_EXTENSIONS
    ):
        print_success("  Image available")
        return

    config_path = get_assets_dir() / "images.yaml"
    try:
        images = load_images_config(config_path)
    except MVMError:
        print_warning("  Could not load images config.")
        print_info("  Run 'mvm image fetch <id>' manually.")
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
        print_info("  Skipped. Run 'mvm image fetch <id>' manually.")
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
            info, priv_path = create_key("mvm-default")
            print_success(f"  Key created: {info.name} ({priv_path})")
        except MVMKeyError as e:
            print_warning(f"  Key creation failed: {e}")
        return

    print_info("  No SSH keys in cache.")
    print_info("    1. Generate a new ED25519 keypair")
    print_info("    2. Import an existing public key")
    print_info("    0. Skip")

    choice = typer.prompt("  Choice", default="1")

    if choice == "1":
        name = typer.prompt("  Key name", default="mvm-default")
        try:
            info, priv_path = create_key(name)
            print_success(f"  Key created: {info.name}")
            print_info(f"  Private key: {priv_path}")
        except MVMKeyError as e:
            print_warning(f"  Key creation failed: {e}")
    elif choice == "2":
        path_str = typer.prompt("  Path to public key")
        name = typer.prompt("  Name for this key", default="default")
        try:
            info = add_key(name, path_str)
            print_success(f"  Key added: {info.name}")
        except MVMKeyError as e:
            print_warning(f"  Key import failed: {e}")
    else:
        print_info("  Skipped. Run 'mvm key add' or 'mvm key create' manually.")


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
    has_image = images_dir.exists() and any(
        images_dir.glob(f"*{ext}") for ext in SUPPORTED_IMAGE_EXTENSIONS
    )
    checks.append(("Image", has_image))

    keys = list_keys()
    checks.append(("SSH key", len(keys) > 0))

    all_ok = True
    for label, ok in checks:
        status = "ready" if ok else "missing"
        print_info(f"{label}: {status}")
        if not ok:
            all_ok = False

    if all_ok:
        print_success("\nAll set! Create your first VM with:")
        key_flag = f" --ssh-key {keys[0].name}" if keys else ""
        print_info(f"  mvm vm create --name my-vm --image <image-id>{key_flag}")
    else:
        print_warning("\nSome components are missing. Fix them and run 'mvm configure' again.")


@app.callback(invoke_without_command=True)
def configure(
    non_interactive: bool = typer.Option(
        False, "--non-interactive", help="Use defaults, skip prompts"
    ),
    skip_host: bool = typer.Option(False, "--skip-host", help="Skip host init step"),
) -> None:
    """Guided setup wizard -- run this to get started.

    Walks through six steps: host privilege setup, Firecracker binary
    download, kernel build, root filesystem image download, SSH key
    creation, and a final readiness summary.

    In interactive mode (the default), each step prompts before making
    changes. Use --non-interactive to accept all defaults for headless or
    CI environments.

    Examples:
        mvm configure
        mvm configure --non-interactive
        mvm configure --skip-host --non-interactive
    """
    print_info("mvm — Setup Wizard")
    print_info("=" * 40)

    initialize_default_config()

    _step_host(skip=skip_host, non_interactive=non_interactive)
    _step_binary(non_interactive=non_interactive)
    _step_kernel(non_interactive=non_interactive)
    _step_image(non_interactive=non_interactive)
    _step_ssh_key(non_interactive=non_interactive)
    _step_summary()
