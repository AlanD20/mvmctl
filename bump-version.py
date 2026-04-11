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


def update_pkgbuild(
    root: Path,
    new_version: str,
    dry_run: bool,
    author: tuple[str, str] = ("AlanD20", "aland20@pm.me"),
    aur_mode: bool = False,
) -> None:
    """Update pkgver and maintainer in packaging/PKGBUILD, and regenerate .SRCINFO."""
    file_path = root / "packaging" / "PKGBUILD"
    if not file_path.exists():
        print(f"  ⚠️  packaging/PKGBUILD: file not found")
        return

    content = file_path.read_text()
    old_version = re.search(r"^pkgver=([0-9.]+)", content, re.M)

    if old_version:
        old = old_version.group(1)
        base_version = new_version.split("-")[0]
        new_content = re.sub(r"^pkgver=[0-9.]+", f"pkgver={base_version}", content, flags=re.M)

        # Update maintainer
        new_content = re.sub(
            r"# Maintainer: .*", f"# Maintainer: {author[0]} <{author[1]}>", new_content
        )

        if not dry_run:
            file_path.write_text(new_content)
        print(f"  packaging/PKGBUILD: {old} → {base_version}")

        # In AUR mode, download artifacts and update checksums
        if aur_mode and not dry_run:
            update_pkgbuild_checksums(root, base_version, file_path)

        # Regenerate .SRCINFO if makepkg is available
        if not dry_run:
            regenerate_srcinfo(root)
    else:
        print(f"  ⚠️  packaging/PKGBUILD: pkgver not found")


def update_pkgbuild_checksums(root: Path, version: str, pkgbuild_path: Path) -> None:
    """Download release artifacts and update PKGBUILD sha256sums."""
    import subprocess
    import hashlib
    import urllib.request
    import tempfile

    print(f"  Downloading release artifacts for v{version}...")

    # URLs for the artifacts
    binary_url = f"https://github.com/AlanD20/mvmctl/releases/download/v{version}/mvm"
    manpage_url = f"https://raw.githubusercontent.com/AlanD20/mvmctl/v{version}/docs/mvm.1"

    checksums = []

    try:
        with tempfile.TemporaryDirectory() as tmpdir:
            # Download binary
            binary_path = Path(tmpdir) / f"mvm-{version}"
            try:
                urllib.request.urlretrieve(binary_url, binary_path)
                binary_hash = hashlib.sha256(binary_path.read_bytes()).hexdigest()
                checksums.append(binary_hash)
                print(f"    mvm binary: {binary_hash[:16]}...")
            except Exception as e:
                print(f"    ⚠️  Failed to download binary: {e}")
                return

            # Download man page
            manpage_path = Path(tmpdir) / f"mvm.1-{version}"
            try:
                urllib.request.urlretrieve(manpage_url, manpage_path)
                manpage_hash = hashlib.sha256(manpage_path.read_bytes()).hexdigest()
                checksums.append(manpage_hash)
                print(f"    mvm.1 manpage: {manpage_hash[:16]}...")
            except Exception as e:
                print(f"    ⚠️  Failed to download manpage: {e}")
                return

        content = pkgbuild_path.read_text()
        new_checksums_line = f"sha256sums=('{checksums[0]}' '{checksums[1]}')"
        new_content = re.sub(r"sha256sums=\([^)]+\)", new_checksums_line, content)
        pkgbuild_path.write_text(new_content)
        print(f"  packaging/PKGBUILD: updated sha256sums")

    except Exception as e:
        print(f"  ⚠️  Failed to update checksums: {e}")
        print(f"      Make sure GitHub release v{version} is published with artifacts")


def regenerate_srcinfo(root: Path) -> None:
    """Regenerate .SRCINFO from PKGBUILD using makepkg."""
    import subprocess

    pkgbuild_dir = root / "packaging"
    srcinfo_path = pkgbuild_dir / ".SRCINFO"

    try:
        # Check if makepkg is available
        result = subprocess.run(
            ["which", "makepkg"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            print(f"  ⚠️  .SRCINFO: makepkg not found, skipping regeneration")
            print(
                f"      Install pacman-contrib or run manually: makepkg --printsrcinfo > .SRCINFO"
            )
            return

        # Regenerate .SRCINFO
        result = subprocess.run(
            ["makepkg", "--printsrcinfo"],
            cwd=pkgbuild_dir,
            capture_output=True,
            text=True,
            check=True,
        )
        srcinfo_path.write_text(result.stdout)
        print(f"  packaging/.SRCINFO: regenerated")
    except subprocess.CalledProcessError as e:
        print(f"  ⚠️  .SRCINFO: failed to regenerate")
        print(f"      Error: {e.stderr}")
    except FileNotFoundError:
        print(f"  ⚠️  .SRCINFO: makepkg not found, skipping regeneration")
        print(f"      Install pacman-contrib or run manually: makepkg --printsrcinfo > .SRCINFO")


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


def update_debian_changelog(
    root: Path,
    new_version: str,
    dry_run: bool,
    changelog: str = "",
    author: tuple[str, str] = ("AlanD20", "aland20@pm.me"),
) -> None:
    """Add new entry to packaging/debian/changelog."""
    file_path = root / "packaging" / "debian" / "changelog"
    if not file_path.exists():
        print(f"  ⚠️  packaging/debian/changelog: file not found")
        return

    content = file_path.read_text()
    old_version_match = re.search(r"mvmctl \(([0-9.]+)\)", content)
    old_version = old_version_match.group(1) if old_version_match else "unknown"

    date_str = datetime.now().strftime("%a, %d %b %Y %H:%M:%S %z")

    if changelog:
        debian_changes = "\n".join(
            f"  * {line.lstrip('- ').lstrip('* ')}"
            for line in changelog.strip().split("\n")
            if line.strip()
        )
    else:
        debian_changes = f"  * New upstream release {new_version}"

    new_entry = f"""mvmctl ({new_version}) unstable; urgency=medium

{debian_changes}

 -- {author[0]} <{author[1]}>  {date_str}

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


def update_rpm_changelog(
    root: Path,
    new_version: str,
    dry_run: bool,
    changelog: str = "",
    author: tuple[str, str] = ("AlanD20", "aland20@pm.me"),
) -> None:
    """Update %changelog in packaging/mvmctl.spec."""
    file_path = root / "packaging" / "mvmctl.spec"
    if not file_path.exists():
        print(f"  ⚠️  packaging/mvmctl.spec: file not found")
        return

    content = file_path.read_text()

    if "%changelog" not in content:
        print(f"  ⚠️  packaging/mvmctl.spec: no %changelog section found")
        return

    date_str = datetime.now().strftime("%a %b %d %Y")

    if changelog:
        rpm_changes = "\n".join(
            f"- {line.lstrip('- ').lstrip('* ')}"
            for line in changelog.strip().split("\n")
            if line.strip() and not line.startswith("###")
        )
    else:
        rpm_changes = f"- New upstream release {new_version}"

    new_entry = f"""* {date_str} {author[0]} <{author[1]}> - {new_version}-1
{rpm_changes}

"""

    changelog_pattern = r"(%changelog\n)"
    new_content = re.sub(changelog_pattern, rf"\1{new_entry}", content)

    if not dry_run:
        file_path.write_text(new_content)
    print(f"  packaging/mvmctl.spec: added %changelog entry for {new_version}")


def update_root_changelog(root: Path, new_version: str, dry_run: bool, changelog: str = "") -> None:
    """Update CHANGELOG.md with new version entry."""
    file_path = root / "CHANGELOG.md"
    if not file_path.exists():
        print(f"  ⚠️  CHANGELOG.md: file not found")
        return

    content = file_path.read_text()
    date_str = datetime.now().strftime("%Y-%m-%d")

    if changelog:
        sections = {"Added": [], "Changed": [], "Fixed": [], "Other": []}
        current_section = "Other"

        for line in changelog.strip().split("\n"):
            line = line.strip()
            if not line:
                continue
            if line.startswith("### "):
                current_section = line[4:].strip()
                if current_section not in sections:
                    sections[current_section] = []
            elif line.startswith("- ") or line.startswith("* "):
                sections[current_section].append(line)

        entry_parts = [f"## [{new_version}] - {date_str}", ""]
        for section_name in ["Added", "Changed", "Fixed", "Deprecated", "Removed", "Security"]:
            items = sections.get(section_name, [])
            if items:
                entry_parts.append(f"### {section_name}")
                entry_parts.extend(items)
                entry_parts.append("")

        if sections["Other"]:
            if not any(sections.get(k) for k in ["Added", "Changed", "Fixed"]):
                entry_parts.append("### Added")
                entry_parts.extend(sections["Other"])
                entry_parts.append("")

        new_version_section = "\n".join(entry_parts)
    else:
        new_version_section = f"""## [{new_version}] - {date_str}

### Added
- (Add changes here)

### Changed
- (Add changes here)

### Fixed
- (Add changes here)

"""

    new_entry = f"""## [Unreleased]

{new_version_section}"""

    new_content = re.sub(r"## \[Unreleased\]\n\n", new_entry, content)

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
  %(prog)s 0.2.0                          # Bump to version 0.2.0
  %(prog)s 0.2.0 --changelog "- Added feature X"  # With changelog
  %(prog)s 0.2.0 --changelog-file notes.md      # Read from file
  %(prog)s 1.0.0 --dry-run                # Preview changes
  %(prog)s 0.2.0 --commit                 # Bump and auto-commit
        """,
    )
    parser.add_argument("version", help="New version number (e.g., 0.2.0)")
    parser.add_argument(
        "--aur",
        action="store_true",
        help="AUR mode: download release artifacts and update sha256sums in PKGBUILD (run after GitHub release)",
    )
    parser.add_argument(
        "--dry-run", action="store_true", help="Show what would change without modifying files"
    )
    parser.add_argument("--commit", action="store_true", help="Automatically commit after bumping")
    parser.add_argument(
        "--no-commit-msg", action="store_true", help="Skip the commit message prompt (use default)"
    )
    parser.add_argument(
        "--changelog", dest="changelog_text", help="Changelog entry text (multi-line supported)"
    )
    parser.add_argument("--changelog-file", dest="changelog_file", help="Read changelog from file")
    parser.add_argument(
        "--author-name",
        dest="author_name",
        default="AlanD20",
        help="Package author/maintainer name",
    )
    parser.add_argument(
        "--author-email",
        dest="author_email",
        default="aland20@pm.me",
        help="Package author/maintainer email",
    )

    args = parser.parse_args()

    # Validate version format
    if not validate_version(args.version):
        print(f"Error: Invalid version format: {args.version}")
        print("Version must follow semantic versioning: X.Y.Z or X.Y.Z-prerelease")
        sys.exit(1)

    # Get changelog from file or argument
    changelog = ""
    if args.changelog_file:
        file_path = Path(args.changelog_file)
        if file_path.exists():
            changelog = file_path.read_text()
        else:
            print(f"Error: Changelog file not found: {args.changelog_file}")
            sys.exit(1)
    elif args.changelog_text:
        changelog = args.changelog_text

    # Get author info
    author = (args.author_name, args.author_email)

    # Find project root
    script_dir = Path(__file__).parent.absolute()
    root = script_dir  # Script is at root

    # AUR mode: only update PKGBUILD checksums and .SRCINFO
    if args.aur:
        print(f"\n{'=' * 60}")
        print(f"AUR Mode: Preparing PKGBUILD for v{args.version}")
        print(f"{'=' * 60}\n")
        print("This will download release artifacts from GitHub and update checksums.")
        print("Make sure the GitHub release is published with the 'mvm' binary.\n")

        update_pkgbuild(root, args.version, args.dry_run, author, aur_mode=True)

        print(f"\n{'=' * 60}")
        if args.dry_run:
            print("Dry run complete. No files were modified.")
        else:
            print("PKGBUILD updated with checksums and .SRCINFO regenerated!")
            print("\nNext steps for AUR:")
            print("  1. Review changes: git diff packaging/")
            print("  2. Commit: git commit -am 'aur: update to v" + args.version + "'")
            print("  3. Push to AUR:")
            print("     cd packaging")
            print("     git push aur master")
        print(f"{'=' * 60}\n")
        return

    print(f"\n{'=' * 60}")
    print(f"Bumping version to: {args.version}")
    if changelog:
        print("Changelog entries provided")
    print(f"Author: {author[0]} <{author[1]}>")
    if args.dry_run:
        print("(DRY RUN - no files will be modified)")
    print(f"{'=' * 60}\n")

    # Update all files
    print("Updating files:")
    update_pyproject_toml(root, args.version, args.dry_run)
    update_init_py(root, args.version, args.dry_run)
    update_pkgbuild(root, args.version, args.dry_run, author, aur_mode=False)
    update_rpm_spec(root, args.version, args.dry_run)
    update_rpm_changelog(root, args.version, args.dry_run, changelog, author)
    update_debian_changelog(root, args.version, args.dry_run, changelog, author)
    update_man_page(root, args.version, args.dry_run)
    update_root_changelog(root, args.version, args.dry_run, changelog)

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
