#!/usr/bin/env python3
"""Post-release tasks executed after a GitHub release is published.

Downloads release artifacts, updates distribution packaging files,
and prepares downstream packages.

Usage:
    python scripts/post-release.py --aur           # Update AUR PKGBUILD (uses latest git tag)
    python scripts/post-release.py --aur --dry-run # Preview changes
    python scripts/post-release.py --help          # Show all flags
"""

import argparse
import hashlib
import re
import subprocess
import sys
import tempfile
import urllib.request
from pathlib import Path


def validate_semver(version: str) -> bool:
    """Validate version follows strict semver format (X.Y.Z)."""
    return bool(re.match(r"^\d+\.\d+\.\d+$", version))


def get_latest_tag(root: Path) -> str:
    """Get the latest git tag matching v*.*.*, stripping the v prefix."""
    try:
        result = subprocess.run(
            ["git", "-C", str(root), "describe", "--tags", "--abbrev=0", "--match", "v*"],
            capture_output=True, text=True, check=True,
        )
        tag = result.stdout.strip()
        version = tag.lstrip("v")
        if not validate_semver(version):
            print(f"Error: Latest tag '{tag}' does not contain a valid semver version.")
            sys.exit(1)
        return version
    except subprocess.CalledProcessError:
        print("Error: No git tags found. Run bump-version.py first and create a tag.")
        sys.exit(1)


def regenerate_srcinfo(root: Path) -> None:
    """Regenerate .SRCINFO from PKGBUILD using makepkg."""
    pkgbuild_dir = root / "packaging"
    srcinfo_path = pkgbuild_dir / ".SRCINFO"

    try:
        result = subprocess.run(
            ["which", "makepkg"],
            capture_output=True, text=True, check=False,
        )
        if result.returncode != 0:
            print("  ⚠️  .SRCINFO: makepkg not found, skipping regeneration")
            print("      Install pacman-contrib or run manually: makepkg --printsrcinfo > .SRCINFO")
            return

        result = subprocess.run(
            ["makepkg", "--printsrcinfo"],
            cwd=pkgbuild_dir, capture_output=True, text=True, check=True,
        )
        srcinfo_path.write_text(result.stdout)
        print("  packaging/.SRCINFO: regenerated")
    except subprocess.CalledProcessError as e:
        print("  ⚠️  .SRCINFO: failed to regenerate")
        print(f"      Error: {e.stderr}")
    except FileNotFoundError:
        print("  ⚠️  .SRCINFO: makepkg not found, skipping regeneration")
        print("      Install pacman-contrib or run manually: makepkg --printsrcinfo > .SRCINFO")


def update_aur(version: str, root: Path, dry_run: bool) -> None:
    """Download release artifacts and update AUR PKGBUILD checksums."""
    pkgbuild_path = root / "packaging" / "PKGBUILD"
    if not pkgbuild_path.exists():
        print(f"Error: packaging/PKGBUILD not found at {pkgbuild_path}")
        sys.exit(1)

    binary_url = f"https://github.com/AlanD20/mvmctl/releases/download/v{version}/mvm"
    manpage_url = f"https://raw.githubusercontent.com/AlanD20/mvmctl/v{version}/docs/mvm.1"

    print(f"\n{'=' * 60}")
    print(f"Updating AUR PKGBUILD for v{version}")
    print(f"{'=' * 60}\n")

    if dry_run:
        print("  Would download:")
        print(f"    {binary_url}")
        print(f"    {manpage_url}")
        print("  Would update sha256sums in packaging/PKGBUILD")
        print("  Would regenerate packaging/.SRCINFO")
        print(f"\n{'=' * 60}")
        print("Dry run complete. No files were modified.")
        print(f"{'=' * 60}\n")
        return

    checksums: list[str] = []

    try:
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp_path = Path(tmpdir)

            print("  Downloading mvm binary...")
            binary_path = tmp_path / f"mvm-{version}"
            try:
                urllib.request.urlretrieve(binary_url, binary_path)
                binary_hash = hashlib.sha256(binary_path.read_bytes()).hexdigest()
                checksums.append(binary_hash)
                print(f"    ✓ mvm: {binary_hash[:16]}...")
            except Exception as e:
                print(f"    ✗ Failed to download binary: {e}")
                print(f"      Make sure GitHub release v{version} is published with the 'mvm' binary.")
                sys.exit(1)

            print("  Downloading mvm.1 man page...")
            manpage_path = tmp_path / f"mvm.1-{version}"
            try:
                urllib.request.urlretrieve(manpage_url, manpage_path)
                manpage_hash = hashlib.sha256(manpage_path.read_bytes()).hexdigest()
                checksums.append(manpage_hash)
                print(f"    ✓ mvm.1: {manpage_hash[:16]}...")
            except Exception as e:
                print(f"    ✗ Failed to download man page: {e}")
                print(f"      Make sure the docs/mvm.1 file exists in the v{version} tag.")
                sys.exit(1)

            content = pkgbuild_path.read_text()
            new_checksums_line = f"sha256sums=('{checksums[0]}' '{checksums[1]}')"
            new_content = re.sub(r"sha256sums=\([^)]+\)", new_checksums_line, content)
            pkgbuild_path.write_text(new_content)
            print("  ✓ packaging/PKGBUILD: updated sha256sums")

            regenerate_srcinfo(root)

    except Exception as e:
        print(f"  ✗ Failed to update checksums: {e}")
        print(f"      Make sure GitHub release v{version} is published with artifacts.")
        sys.exit(1)

    print(f"\n{'=' * 60}")
    print("PKGBUILD updated with checksums and .SRCINFO regenerated!")
    print("\nNext steps for AUR:")
    print("  1. Review changes: git diff packaging/")
    print(f"  2. Commit: git commit -am 'aur: update to v{version}'")
    print("  3. Push to AUR:")
    print("     cd packaging")
    print("     git push aur master")
    print(f"{'=' * 60}\n")


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Post-release tasks for mvmctl",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""Examples:
  %(prog)s --aur                        # Update AUR PKGBUILD (uses latest tag)
  %(prog)s --aur --dry-run              # Preview changes
        """,
    )
    parser.add_argument("--aur", action="store_true", help="Update AUR PKGBUILD sha256sums and regenerate .SRCINFO")
    parser.add_argument("--dry-run", action="store_true", help="Show what would change without modifying files")
    args = parser.parse_args()

    root = Path(__file__).parent.parent

    if args.aur:
        version = get_latest_tag(root)
        update_aur(version, root, args.dry_run)
    else:
        parser.print_help()
        print("\nNo task specified. Use --aur to update AUR checksums.")
        sys.exit(1)


if __name__ == "__main__":
    main()
