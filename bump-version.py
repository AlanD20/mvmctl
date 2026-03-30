#!/usr/bin/env python3
"""Bump version across all project files consistently.

This script updates version numbers in:
- pyproject.toml (main project version)
- src/mvmctl/__init__.py (__version__)
- packaging/PKGBUILD (pkgver)
- packaging/mvmctl.spec (Version)
- packaging/debian/changelog (new entry)
- packaging/debian/control (version if present)
- docs/mvm.1 (version in header)
- docs/RELEASE.md (version references)

Usage:
    ./bump-version.py 0.2.0
    ./bump-version.py 1.0.0-alpha.1
    ./bump-version.py 0.2.0 --dry-run  # Preview changes
    ./bump-version.py 0.2.0 --commit   # Auto-commit after bump

The script validates version format (PEP 440 compatible) and ensures
all files are updated atomically.
"""

import argparse
import re
import sys
from datetime import datetime
from pathlib import Path


def validate_version(version: str) -> bool:
    """Validate version follows PEP 440 or semantic versioning."""
    # Allow: 0.1.0, 1.0.0, 1.2.3-alpha, 1.0.0-rc.1, etc.
    pattern = r"^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$"
    return bool(re.match(pattern, version))


def update_pyproject_toml(root: Path, new_version: str, dry_run: bool) -> None:
    """Update version in pyproject.toml."""
    file_path = root / "pyproject.toml"
    content = file_path.read_text()
    old_version = re.search(r'^version = "([^"]+)"', content, re.M)

    if old_version:
        old = old_version.group(1)
        new_content = re.sub(
            r'^version = "[^"]+"', f'version = "{new_version}"', content, flags=re.M
        )
        if not dry_run:
            file_path.write_text(new_content)
        print(f"  pyproject.toml: {old} → {new_version}")
    else:
        print(f"  ⚠️  pyproject.toml: version not found")


def update_init_py(root: Path, new_version: str, dry_run: bool) -> None:
    """Update __version__ in src/mvmctl/__init__.py."""
    file_path = root / "src" / "mvmctl" / "__init__.py"
    if not file_path.exists():
        print(f"  ⚠️  src/mvmctl/__init__.py: file not found")
        return

    content = file_path.read_text()
    old_version = re.search(r'__version__ = "([^"]+)"', content)

    if old_version:
        old = old_version.group(1)
        new_content = re.sub(r'__version__ = "[^"]+"', f'__version__ = "{new_version}"', content)
        if not dry_run:
            file_path.write_text(new_content)
        print(f"  src/mvmctl/__init__.py: {old} → {new_version}")
    else:
        print(f"  ⚠️  src/mvmctl/__init__.py: __version__ not found")


def update_pkgbuild(root: Path, new_version: str, dry_run: bool) -> None:
    """Update pkgver in packaging/PKGBUILD."""
    file_path = root / "packaging" / "PKGBUILD"
    if not file_path.exists():
        print(f"  ⚠️  packaging/PKGBUILD: file not found")
        return

    content = file_path.read_text()
    old_version = re.search(r"^pkgver=([0-9.]+)", content, re.M)

    if old_version:
        old = old_version.group(1)
        # Extract base version (without pre-release suffix for pkgver)
        base_version = new_version.split("-")[0]
        new_content = re.sub(r"^pkgver=[0-9.]+", f"pkgver={base_version}", content, flags=re.M)
        if not dry_run:
            file_path.write_text(new_content)
        print(f"  packaging/PKGBUILD: {old} → {base_version}")
    else:
        print(f"  ⚠️  packaging/PKGBUILD: pkgver not found")


def update_rpm_spec(root: Path, new_version: str, dry_run: bool) -> None:
    """Update Version in packaging/mvmctl.spec."""
    file_path = root / "packaging" / "mvmctl.spec"
    if not file_path.exists():
        print(f"  ⚠️  packaging/mvmctl.spec: file not found")
        return

    content = file_path.read_text()
    old_version = re.search(r"^Version:\s+([0-9.]+)", content, re.M)

    if old_version:
        old = old_version.group(1)
        base_version = new_version.split("-")[0]
        new_content = re.sub(
            r"^Version:\s+[0-9.]+", f"Version:        {base_version}", content, flags=re.M
        )
        if not dry_run:
            file_path.write_text(new_content)
        print(f"  packaging/mvmctl.spec: {old} → {base_version}")
    else:
        print(f"  ⚠️  packaging/mvmctl.spec: Version not found")


def update_debian_changelog(root: Path, new_version: str, dry_run: bool) -> None:
    """Add new entry to packaging/debian/changelog."""
    file_path = root / "packaging" / "debian" / "changelog"
    if not file_path.exists():
        print(f"  ⚠️  packaging/debian/changelog: file not found")
        return

    content = file_path.read_text()
    old_version_match = re.search(r"mvmctl \(([0-9.]+)\)", content)
    old_version = old_version_match.group(1) if old_version_match else "unknown"

    # Get current date in Debian format
    date_str = datetime.now().strftime("%a, %d %b %Y %H:%M:%S %z")

    # Create new changelog entry
    new_entry = f"""mvmctl ({new_version}) unstable; urgency=medium

  * New upstream release {new_version}

 -- AlanD20 <aland20@pm.me>  {date_str}

"""

    if not dry_run:
        new_content = new_entry + content
        file_path.write_text(new_content)
    print(f"  packaging/debian/changelog: added entry for {new_version} (was {old_version})")


def update_man_page(root: Path, new_version: str, dry_run: bool) -> None:
    """Update version in docs/mvm.1 header."""
    file_path = root / "docs" / "mvm.1"
    if not file_path.exists():
        print(f"  ⚠️  docs/mvm.1: file not found")
        return

    content = file_path.read_text()
    old_version_match = re.search(r'"mvmctl ([^"]+)"', content)
    old_version = old_version_match.group(1) if old_version_match else "unknown"

    # Update version in .TH line
    new_content = re.sub(r'("mvmctl) [^"]+(")', rf"\1 {new_version}\2", content)

    if not dry_run:
        file_path.write_text(new_content)
    print(f"  docs/mvm.1: {old_version} → {new_version}")


def update_root_changelog(root: Path, new_version: str, dry_run: bool) -> None:
    """Update CHANGELOG.md with new version entry."""
    file_path = root / "CHANGELOG.md"
    if not file_path.exists():
        print(f"  ⚠️  CHANGELOG.md: file not found")
        return

    content = file_path.read_text()

    # Check if Unreleased section has content
    unreleased_match = re.search(
        r"## \[Unreleased\]\n\n(### [a-zA-Z]+\n- .*?)?\n## \[", content, re.DOTALL
    )

    date_str = datetime.now().strftime("%Y-%m-%d")

    # Create new version entry
    new_entry = f"""## [Unreleased]

## [{new_version}] - {date_str}

### Added
- (Add changes here)

### Changed
- (Add changes here)

### Fixed
- (Add changes here)

"""

    # Replace Unreleased header with new version entry
    new_content = re.sub(r"## \[Unreleased\]\n\n", new_entry, content)

    # Add new version to link references at bottom
    link_pattern = (
        r"(\[Unreleased\]: https://github\.com/AlanD20/mvmctl/compare/v)([0-9.]+)(\.\.\.HEAD)"
    )

    def replace_link(match: re.Match) -> str:
        return f"{match.group(1)}{new_version}{match.group(3)}\n[{new_version}]: https://github.com/AlanD20/mvmctl/releases/tag/v{new_version}"

    new_content = re.sub(link_pattern, replace_link, new_content)

    if not dry_run:
        file_path.write_text(new_content)
    print(f"  CHANGELOG.md: added section for v{new_version}")


def main():
    parser = argparse.ArgumentParser(
        description="Bump version across all project files",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s 0.2.0              # Bump to version 0.2.0
  %(prog)s 1.0.0 --dry-run  # Preview changes
  %(prog)s 0.2.0 --commit   # Bump and auto-commit
        """,
    )
    parser.add_argument("version", help="New version number (e.g., 0.2.0)")
    parser.add_argument(
        "--dry-run", action="store_true", help="Show what would change without modifying files"
    )
    parser.add_argument("--commit", action="store_true", help="Automatically commit after bumping")
    parser.add_argument(
        "--no-commit-msg", action="store_true", help="Skip the commit message prompt (use default)"
    )

    args = parser.parse_args()

    # Validate version format
    if not validate_version(args.version):
        print(f"Error: Invalid version format: {args.version}")
        print("Version must follow semantic versioning: X.Y.Z or X.Y.Z-prerelease")
        sys.exit(1)

    # Find project root
    script_dir = Path(__file__).parent.absolute()
    root = script_dir  # Script is at root

    print(f"\n{'=' * 60}")
    print(f"Bumping version to: {args.version}")
    if args.dry_run:
        print("(DRY RUN - no files will be modified)")
    print(f"{'=' * 60}\n")

    # Update all files
    print("Updating files:")
    update_pyproject_toml(root, args.version, args.dry_run)
    update_init_py(root, args.version, args.dry_run)
    update_pkgbuild(root, args.version, args.dry_run)
    update_rpm_spec(root, args.version, args.dry_run)
    update_debian_changelog(root, args.version, args.dry_run)
    update_man_page(root, args.version, args.dry_run)
    update_root_changelog(root, args.version, args.dry_run)

    print(f"\n{'=' * 60}")

    if args.dry_run:
        print("Dry run complete. No files were modified.")
        print(f"Run without --dry-run to apply changes:")
        print(f"  ./bump-version.py {args.version}")
    else:
        print(f"Version bumped to {args.version} successfully!")

        if args.commit:
            # Auto-commit
            import subprocess

            try:
                subprocess.run(["git", "add", "-A"], check=True, cwd=root)
                commit_msg = f"chore: bump version to {args.version}"
                subprocess.run(["git", "commit", "-m", commit_msg], check=True, cwd=root)
                print(f"\nCommitted: {commit_msg}")
                print("\nNext steps:")
                print(f"  git tag v{args.version}")
                print(f"  git push origin main")
                print(f"  git push origin v{args.version}")
            except subprocess.CalledProcessError as e:
                print(f"\n⚠️  Git commit failed: {e}")
                print("You may need to commit manually:")
                print(f"  git add -A")
                print(f'  git commit -m "chore: bump version to {args.version}"')
        else:
            print("\nNext steps:")
            print("  1. Review the changes:")
            print("     git diff")
            print("  2. Commit the version bump:")
            print(f'     git commit -am "chore: bump version to {args.version}"')
            print(f"  3. Create a tag:")
            print(f"     git tag v{args.version}")
            print(f"  4. Push:")
            print(f"     git push origin main")
            print(f"     git push origin v{args.version}")

    print(f"{'=' * 60}\n")


if __name__ == "__main__":
    main()
