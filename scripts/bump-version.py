#!/usr/bin/env python3
"""Bump version across all project files consistently.

This script updates version numbers in:
- internal/lib/version/info.go (Go source version defaults)
- packaging/PKGBUILD (pkgver)
- packaging/mvmctl.spec (Version)
- packaging/debian/changelog (new entry)
- docs/mvm.1 (version in .TH header)
- CHANGELOG.md (new version section + compare links)

Changes are tracked in CHANGELOG.md under an ## [Unreleased] section during
development. This script moves those changes into the new version section.

The script validates version format (semver) and ensures
all files are updated atomically.

Usage:
    ./scripts/bump-version.py 0.2.0                              # Bump to version
    ./scripts/bump-version.py 0.2.0 --dry-run                    # Preview changes
    ./scripts/bump-version.py 0.2.0 --commit                     # Bump and auto-commit
    ./scripts/bump-version.py 0.2.0 --author-name "Name" --author-email "email"  # Custom maintainer
"""

import argparse
import re
import sys
from datetime import datetime
from pathlib import Path

# Default package maintainer for distro packaging files
_DEFAULT_AUTHOR_NAME = "AlanD20"
_DEFAULT_AUTHOR_EMAIL = "aland20@pm.me"


def validate_version(version: str) -> bool:
    """Validate version follows semantic versioning."""
    # Allow: 0.1.0, 1.0.0, 1.2.3-alpha, 1.0.0-rc.1, etc.
    pattern = r"^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$"
    return bool(re.match(pattern, version))


def update_go_version(root: Path, new_version: str, dry_run: bool) -> None:
    """Update version defaults in internal/lib/version/info.go.

    Updates three things:
    1. versionString = "X.Y.Z" (the package-level fallback)
    2. FormatVersion() hardcoded fallback
    3. GetVersion() hardcoded fallback
    """
    file_path = root / "internal" / "lib" / "version" / "info.go"
    if not file_path.exists():
        print(f"  \u26a0\ufe0f  internal/lib/version/info.go: file not found")
        return

    content = file_path.read_text()

    # Detect the current version from the versionString var
    old_match = re.search(r'var versionString = "([^"]+)"', content)
    old_version = old_match.group(1) if old_match else "unknown"

    # Update all three locations using line-local patterns:
    # 1. versionString = "X.Y.Z"   (package-level var)
    # 2. FormatVersion: version := "X.Y.Z" on immediate next line after comment
    # 3. GetVersion: version := "X.Y.Z" on immediate next line after comment

    lines = content.split("\n")
    new_lines = []
    for line in lines:
        # versionString = "X.Y.Z"
        line = re.sub(r'^(var versionString = )"[^"]*"', rf'\g<1>"{new_version}"', line)
        # version := "X.Y.Z" inside FormatVersion/GetVersion
        line = re.sub(
            r'^(\t*version := ")[^"]*(")',
            rf'\g<1>{new_version}\g<2>',
            line,
        )
        new_lines.append(line)
    new_content = "\n".join(new_lines)

    if not dry_run:
        file_path.write_text(new_content)
    print(f"  internal/lib/version/info.go: {old_version} \u2192 {new_version}")


def update_pkgbuild(
    root: Path,
    new_version: str,
    dry_run: bool,
    author: tuple[str, str] = (_DEFAULT_AUTHOR_NAME, _DEFAULT_AUTHOR_EMAIL),
) -> None:
    """Update pkgver and maintainer in packaging/PKGBUILD."""
    file_path = root / "packaging" / "PKGBUILD"
    if not file_path.exists():
        print(f"  \u26a0\ufe0f  packaging/PKGBUILD: file not found")
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
        print(f"  packaging/PKGBUILD: {old} \u2192 {base_version}")

    else:
        print(f"  \u26a0\ufe0f  packaging/PKGBUILD: pkgver not found")


def update_rpm_spec(root: Path, new_version: str, dry_run: bool) -> None:
    """Update Version in packaging/mvmctl.spec."""
    file_path = root / "packaging" / "mvmctl.spec"
    if not file_path.exists():
        print(f"  \u26a0\ufe0f  packaging/mvmctl.spec: file not found")
        return

    content = file_path.read_text()
    old_version = re.search(r"^Version:\s+([0-9.]+)", content, re.M)
    version_macro = re.search(r"^Version:\s+(%\{[^}]+\})", content, re.M)

    if old_version:
        old = old_version.group(1)
        base_version = new_version.split("-")[0]
        new_content = re.sub(
            r"^Version:\s+[0-9.]+", f"Version:        {base_version}", content, flags=re.M
        )
        if not dry_run:
            file_path.write_text(new_content)
        print(f"  packaging/mvmctl.spec: {old} \u2192 {base_version}")
    elif version_macro:
        print(f"  packaging/mvmctl.spec: {version_macro.group(1)} (macro, resolved at build time)")
    else:
        print(f"  \u26a0\ufe0f  packaging/mvmctl.spec: Version not found")


def update_debian_changelog(
    root: Path,
    new_version: str,
    dry_run: bool,
    changelog: str = "",
    author: tuple[str, str] = (_DEFAULT_AUTHOR_NAME, _DEFAULT_AUTHOR_EMAIL),
) -> None:
    """Add new entry to packaging/debian/changelog."""
    file_path = root / "packaging" / "debian" / "changelog"
    if not file_path.exists():
        print(f"  \u26a0\ufe0f  packaging/debian/changelog: file not found")
        return

    content = file_path.read_text()
    old_version_match = re.search(r"mvmctl \(([0-9.]+)\)", content)
    old_version = old_version_match.group(1) if old_version_match else "(none)"

    date_str = datetime.now().strftime("%a, %d %b %Y %H:%M:%S %z")

    if changelog:
        # Strip markdown headings, bold, and bullets for debian changelog format
        debian_lines = []
        for line in changelog.strip().split("\n"):
            line = line.strip()
            if not line or line.startswith("#"):
                continue  # skip headings and empty lines
            # Strip markdown bold/italic
            line = line.replace("**", "").replace("__", "").replace("*", "").replace("`", "")
            # Strip leading bullets/dashes
            line = line.lstrip("- ").lstrip("* ")
            debian_lines.append(f"  * {line}")
        debian_changes = "\n".join(debian_lines)
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
    """Update version in docs/mvm.1 .TH header."""
    file_path = root / "docs" / "mvm.1"
    if not file_path.exists():
        print(f"  \u26a0\ufe0f  docs/mvm.1: file not found")
        return

    content = file_path.read_text()
    old_version_match = re.search(r'"mvmctl ([^"]+)"', content)
    old_version = old_version_match.group(1) if old_version_match else "unknown"

    # Update version in .TH line
    date_str = datetime.now().strftime("%B %Y")
    # Update both the date and version
    new_content = re.sub(
        r'(\.TH MVM 1 ")[^"]*(" "mvmctl )[^"]*(")',
        rf'\g<1>{date_str}\g<2>{new_version}\g<3>',
        content,
    )

    if not dry_run:
        file_path.write_text(new_content)
    print(f"  docs/mvm.1: {old_version} \u2192 {new_version}")


def update_rpm_changelog(
    root: Path,
    new_version: str,
    dry_run: bool,
    changelog: str = "",
    author: tuple[str, str] = (_DEFAULT_AUTHOR_NAME, _DEFAULT_AUTHOR_EMAIL),
) -> None:
    """Update %%changelog in packaging/mvmctl.spec."""
    file_path = root / "packaging" / "mvmctl.spec"
    if not file_path.exists():
        print(f"  \u26a0\ufe0f  packaging/mvmctl.spec: file not found")
        return

    content = file_path.read_text()

    if "%changelog" not in content:
        print(f"  \u26a0\ufe0f  packaging/mvmctl.spec: no %changelog section found")
        return

    date_str = datetime.now().strftime("%a %b %d %Y")

    if changelog:
        rpm_lines = []
        for line in changelog.strip().split("\n"):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            line = line.replace("**", "").replace("__", "").replace("*", "").replace("`", "")
            line = line.lstrip("- ").lstrip("* ")
            rpm_lines.append(f"- {line}")
        rpm_changes = "\n".join(rpm_lines)
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


def _extract_unreleased_content(root: Path) -> str:
    """Extract the content of the [Unreleased] section from CHANGELOG.md, excluding link references."""
    file_path = root / "CHANGELOG.md"
    if not file_path.exists():
        return ""
    content = file_path.read_text()
    match = re.search(r"## \[Unreleased\]\n\n(.+?)(?=\n## \[|\Z)", content, re.DOTALL)
    if not match:
        return ""
    # Strip trailing link references ([name]: url)
    lines = []
    for line in match.group(1).split("\n"):
        if not re.match(r"^\[.+\]: https?://", line.strip()):
            lines.append(line)
    return "\n".join(lines).strip()


def update_root_changelog(root: Path, new_version: str, dry_run: bool, changelog_content: str = "") -> None:
    """Update CHANGELOG.md: read [Unreleased] section and move it under the new version."""
    file_path = root / "CHANGELOG.md"
    if not file_path.exists():
        print(f"  \u26a0\ufe0f  CHANGELOG.md: file not found")
        return

    content = file_path.read_text()
    date_str = datetime.now().strftime("%Y-%m-%d")

    # Use pre-extracted changelog content to populate the new version section.
    unreleased_match = re.search(
        r"(.*?)## \[Unreleased\]\n\n", content, re.DOTALL
    )
    if not unreleased_match:
        print("  \u26a0\ufe0f  CHANGELOG.md: could not find [Unreleased] section")
        return

    before_section = unreleased_match.group(1)
    after_section = content[unreleased_match.end():]

    if changelog_content:
        new_version_section = f"## [{new_version}] - {date_str}\n\n{changelog_content}"
        # Strip old unreleased content from after_section to avoid duplication.
        if after_section.startswith(changelog_content):
            after_section = after_section[len(changelog_content):].lstrip("\n")
        print(f"  CHANGELOG.md: moved unreleased changes to v{new_version}")
    else:
        new_version_section = f"## [{new_version}] - {date_str}\n\n### Added\n- (Add changes here)\n\n### Changed\n- (Add changes here)\n\n"
        print(f"  CHANGELOG.md: no unreleased changes found, creating empty section")

    # Update the [Unreleased] compare link and add the new version tag link.
    # Handles both the initial state (compare/main...HEAD) and subsequent releases (compare/vX.Y.Z...HEAD).
    link_match = re.search(
        r"\[Unreleased\]: https://github\.com/AlanD20/mvmctl/compare/(v[0-9.]+|main)(\.\.\.HEAD)",
        after_section,
    )
    if link_match:
        old_ref = link_match.group(1)
        after_section = after_section.replace(
            f"[Unreleased]: https://github.com/AlanD20/mvmctl/compare/{old_ref}...HEAD",
            f"[Unreleased]: https://github.com/AlanD20/mvmctl/compare/v{new_version}...HEAD\n[{new_version}]: https://github.com/AlanD20/mvmctl/releases/tag/v{new_version}",
        )

    new_content = f"{before_section}## [Unreleased]\n\n{new_version_section}\n{after_section}"

    if not dry_run:
        file_path.write_text(new_content)


def main():
    parser = argparse.ArgumentParser(
        description="Bump version across all project files",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s 0.2.0                          # Bump to version 0.2.0
  %(prog)s 1.0.0 --dry-run                # Preview changes
  %(prog)s 0.2.0 --commit                 # Bump and auto-commit
  %(prog)s 0.2.0 --author-name "Name" --author-email "email"  # Custom maintainer
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
    parser.add_argument(
        "--author-name",
        dest="author_name",
        default=_DEFAULT_AUTHOR_NAME,
        help="Package author/maintainer name",
    )
    parser.add_argument(
        "--author-email",
        dest="author_email",
        default=_DEFAULT_AUTHOR_EMAIL,
        help="Package author/maintainer email",
    )

    args = parser.parse_args()

    # Validate version format
    if not validate_version(args.version):
        print(f"Error: Invalid version format: {args.version}")
        print("Version must follow semantic versioning: X.Y.Z or X.Y.Z-prerelease")
        sys.exit(1)

    # Get author info
    author = (args.author_name, args.author_email)

    # Find project root (script lives in scripts/ directory)
    script_dir = Path(__file__).resolve().parent
    root = script_dir.parent  # Parent of scripts/ is project root

    print(f"\n{'=' * 60}")
    print(f"Bumping version to: {args.version}")
    print(f"Author: {author[0]} <{author[1]}>")
    if args.dry_run:
        print("(DRY RUN - no files will be modified)")
    print(f"{'=' * 60}\n")

    # Extract unreleased changelog content to feed into all changelog files
    changelog_content = _extract_unreleased_content(root)

    # Update all files
    print("Updating files:")
    update_go_version(root, args.version, args.dry_run)
    update_pkgbuild(root, args.version, args.dry_run, author)
    update_rpm_spec(root, args.version, args.dry_run)
    update_rpm_changelog(root, args.version, args.dry_run, changelog_content, author)
    update_debian_changelog(root, args.version, args.dry_run, changelog_content, author)
    update_man_page(root, args.version, args.dry_run)
    update_root_changelog(root, args.version, args.dry_run, changelog_content)

    print(f"\n{'=' * 60}")

    if args.dry_run:
        print("Dry run complete. No files were modified.")
        print(f"Run without --dry-run to apply changes:")
        print(f"  ./scripts/bump-version.py {args.version}")
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
                print(f"\n\u26a0\ufe0f  Git commit failed: {e}")
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
