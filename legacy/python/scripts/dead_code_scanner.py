#!/usr/bin/env python3
"""Scan for dead code using AST analysis.

An expression (function, class, or top-level variable) is considered dead
when it has **zero references** in the scanned source directories.  A
reference is any use of the name as an expression (``ast.Name``), import
alias, ``__dir__()`` export string, ``__all__`` entry, or implicit
registration via a Typer/Click CLI decorator.

Two categories of dead code are reported:

  **test-only** — the definition is referenced in ``tests/`` (or another
  ignored directory) but nowhere in the production source tree.

  **only-declared** — the definition is never referenced anywhere, not even
  in the same file or in tests.

Limitations
-----------
- Local variables that shadow a global name may produce a **false positive**
  (the local's ``ast.Name`` node is counted as a reference to the global).
- Names referenced exclusively through dotted attribute access
  (``module.ClassName`` without a prior ``from module import ClassName``)
  are only caught when the attribute name also appears as a bare ``Name``
  elsewhere.
- Names that appear only in string annotations (e.g. ``x: "SomeClass"``)
  are not detected.

Usage
-----
    python scripts/dead_code_scanner.py                   # default: scan src/
    python scripts/dead_code_scanner.py --verbose          # show progress
    python scripts/dead_code_scanner.py --json             # machine-readable output
    python scripts/dead_code_scanner.py path/to/dir        # scan extra directories
"""

from __future__ import annotations

import ast
import json
import sys
from collections import defaultdict
from pathlib import Path
from typing import NamedTuple

# ── Reuse common.py infrastructure ────────────────────────────────
# Add the project root to ``sys.path`` so ``from scripts.common import ...``
# works regardless of how this script is invoked (``python scripts/*.py``
# puts only ``scripts/`` on the path, not the project root).
_SCRIPT_DIR = Path(__file__).resolve().parent
_PROJECT_ROOT = _SCRIPT_DIR.parent
if str(_PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(_PROJECT_ROOT))

from scripts.common import BOLD, GREEN, PROJECT_ROOT, RED, RESET, YELLOW

# ---------------------------------------------------------------------------
# Configurable paths — extend these lists to scan/ignore more directories.
# ---------------------------------------------------------------------------

SCAN_DIRS: list[Path] = [PROJECT_ROOT / "src"]
IGNORE_DIRS: list[Path] = [PROJECT_ROOT / "tests"]

# ---------------------------------------------------------------------------
# Configurable name filters — definitions matching these are excluded from
# the dead-code report.
# ---------------------------------------------------------------------------

# Exact names to skip (dunder methods that are called implicitly by Python).
SKIP_NAMES: set[str] = {
    "__getattr__",
    "__dir__",
    "__init__",
    "__str__",
    "__repr__",
    "__hash__",
    "__eq__",
    "__ne__",
    "__lt__",
    "__le__",
    "__gt__",
    "__ge__",
    "__enter__",
    "__exit__",
    "__aenter__",
    "__aexit__",
    "__iter__",
    "__next__",
    "__len__",
    "__contains__",
    "__getitem__",
    "__setitem__",
    "__delitem__",
    "__call__",
    "__bool__",
    "__int__",
    "__float__",
    "__del__",
    "__new__",
    "__reduce__",
    "__reduce_ex__",
    "__sizeof__",
    "__copy__",
    "__deepcopy__",
    "__format__",
}

# Names that start with any of these prefixes are skipped.
SKIP_NAME_PREFIXES: tuple[str, ...] = ()

# Names that end with any of these suffixes are skipped.
SKIP_NAME_SUFFIXES: tuple[str, ...] = ()


# ---------------------------------------------------------------------------
# Utility
# ---------------------------------------------------------------------------


def _log(*args: object, **kwargs: object) -> None:
    """Print to stderr (all progress/info output, never stdout)."""
    print(*args, file=sys.stderr, **kwargs)


def _print_banner(text: str) -> None:
    """Print a prominent blue banner around *text* to stderr."""
    _log(f"\n\033[94m{'=' * 60}\033[0m")
    _log(f"  \033[1m\033[94m{text}\033[0m")
    _log(f"\033[94m{'=' * 60}\033[0m\n")


# ---------------------------------------------------------------------------
# AST helpers
# ---------------------------------------------------------------------------


def _is_hidden(path: Path) -> bool:
    """Return ``True`` if *path* or any component starts with ``.``."""
    return any(part.startswith(".") for part in path.parts)


def _walk_py_files(root: Path) -> list[Path]:
    """Return all ``.py`` files under *root*, skipping hidden directories."""
    return sorted(path for path in root.rglob("*.py") if not _is_hidden(path))


def _should_skip(name: str) -> bool:
    """Return ``True`` if *name* should be excluded from the dead-code report."""
    if name in SKIP_NAMES:
        return True
    if SKIP_NAME_PREFIXES and name.startswith(SKIP_NAME_PREFIXES):
        return True
    if SKIP_NAME_SUFFIXES and name.endswith(SKIP_NAME_SUFFIXES):
        return True
    return False


# ---------------------------------------------------------------------------
# Name counting
# ---------------------------------------------------------------------------


def _count_name_nodes(tree: ast.AST) -> dict[str, int]:
    """Count every ``ast.Name`` occurrence in the tree, keyed by ``.id``.

    This includes both definition targets (left-hand side of assignments)
    and actual references.  Callers must subtract definition-target
    counts to obtain "pure reference" names.
    """
    counts: dict[str, int] = defaultdict(int)
    for node in ast.walk(tree):
        if isinstance(node, ast.Name):
            counts[node.id] += 1
    return dict(counts)


def _count_def_targets(tree: ast.AST) -> dict[str, int]:
    """Count names that appear as **definition targets** — these are NOT
    references and must be subtracted from the raw ``Name`` counts.

    Handles::
        x = ...          # simple assignment
        x: int = ...     # annotated assignment
        x, y = ...       # tuple unpacking
        for x in ...     # for-loop target
        async for x in ...  # async for-loop target
        with ... as x:   # context-manager target
    """
    counts: dict[str, int] = defaultdict(int)

    for node in ast.walk(tree):
        # -- assignments (Assign, AnnAssign) --
        if isinstance(node, ast.Assign):
            for t in node.targets:
                if isinstance(t, ast.Name):
                    counts[t.id] += 1
                elif isinstance(t, ast.Tuple):
                    for elt in t.elts:
                        if isinstance(elt, ast.Name):
                            counts[elt.id] += 1
        elif isinstance(node, ast.AnnAssign):
            if isinstance(node.target, ast.Name):
                counts[node.target.id] += 1
        # -- for / async for targets --
        elif isinstance(node, (ast.For, ast.AsyncFor)):
            for child in ast.walk(node.target):
                if isinstance(child, ast.Name):
                    counts[child.id] += 1
        # -- with ... as targets --
        elif isinstance(node, ast.With):
            for item in node.items:
                if item.optional_vars is not None:
                    for child in ast.walk(item.optional_vars):
                        if isinstance(child, ast.Name):
                            counts[child.id] += 1

    return dict(counts)


# ---------------------------------------------------------------------------
# Reference collectors
# ---------------------------------------------------------------------------


def _collect_import_names(tree: ast.AST) -> set[str]:
    """Names brought into scope via ``import`` / ``from ... import``.

    For alias imports (``from X import Y as Z``) both the *original*
    name (``Y``) and the alias (``Z``) are tracked — the definition
    named ``Y`` in its source module is still referenced even though
    it is renamed locally.
    """
    names: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                top = alias.name.split(".", 1)[0]
                names.add(alias.asname or top)
                if alias.asname:
                    names.add(alias.name)
        elif isinstance(node, ast.ImportFrom):
            for alias in node.names:
                names.add(alias.asname or alias.name)
                if alias.asname:
                    names.add(alias.name)
    return names


def _collect_attr_names(tree: ast.AST) -> set[str]:
    """Collect every ``ast.Attribute.attr`` in the tree.

    This catches references made via dotted access, e.g.
    ``constants.DETECTOR_SCORES`` — the ``DETECTOR_SCORES`` part is
    an ``ast.Attribute.attr``, NOT an ``ast.Name.id``.

    These are **unfiltered** at collection time; the caller should
    intersect them with the known definition set before treating them
    as references.
    """
    return {
        node.attr
        for node in ast.walk(tree)
        if isinstance(node, ast.Attribute)
    }


def _collect_export_strings(tree: ast.AST) -> set[str]:
    """Collect names from ``__dir__()`` return lists and ``__all__``
    assignments — these serve as re-export declarations in lazy-import
    packages."""
    strings: set[str] = set()

    for node in ast.walk(tree):
        # -- __dir__() return lists --
        if isinstance(node, ast.FunctionDef) and node.name == "__dir__":
            for child in ast.walk(node):
                if isinstance(child, ast.List):
                    for elt in child.elts:
                        if isinstance(elt, ast.Constant) and isinstance(
                            elt.value, str
                        ):
                            strings.add(elt.value)
        # -- __all__ = [...] --
        if isinstance(node, ast.Assign):
            for target in node.targets:
                if isinstance(target, ast.Name) and target.id == "__all__":
                    if isinstance(node.value, (ast.List, ast.Tuple)):
                        for elt in node.value.elts:
                            if isinstance(elt, ast.Constant) and isinstance(
                                elt.value, str
                            ):
                                strings.add(elt.value)

    return strings


def _collect_implicit_cli_refs(tree: ast.AST) -> set[str]:
    """Detect names that are implicitly referenced through Typer/Click CLI
    decorator registration (``@app.command()``, ``@app.callback()``)."""
    refs: set[str] = set()

    for node in ast.iter_child_nodes(tree):
        if isinstance(
            node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)
        ):
            for deco in node.decorator_list:
                if _is_cli_registration_decorator(deco):
                    refs.add(node.name)
                    break

    return refs


def _is_cli_registration_decorator(deco: ast.expr) -> bool:
    """Check if *deco* looks like ``<name>.command(...)`` or
    ``<name>.callback(...)`` (Typer/Click registration)."""
    if isinstance(deco, ast.Call):
        func = deco.func
        if isinstance(func, ast.Attribute):
            return func.attr in {"command", "callback", "error_handler"}
    return False


# ---------------------------------------------------------------------------
# Definition collectors
# ---------------------------------------------------------------------------


def _collect_top_level_defs(tree: ast.AST) -> list[tuple[str, str, int]]:
    """Return ``(name, kind, line)`` for every top-level definition.

    Detected forms::

        def name(...):          # "def"
        async def name(...):    # "async def"
        class Name(...):        # "class"
        name = ...              # "var"
        name: type = ...        # "var"
    """
    defs: list[tuple[str, str, int]] = []
    for node in ast.iter_child_nodes(tree):
        if isinstance(node, ast.FunctionDef):
            defs.append((node.name, "def", node.lineno))
        elif isinstance(node, ast.AsyncFunctionDef):
            defs.append((node.name, "async def", node.lineno))
        elif isinstance(node, ast.ClassDef):
            defs.append((node.name, "class", node.lineno))
        elif isinstance(node, ast.Assign):
            for target in node.targets:
                if isinstance(target, ast.Name):
                    defs.append((target.id, "var", node.lineno))
                elif isinstance(target, ast.Tuple):
                    for elt in target.elts:
                        if isinstance(elt, ast.Name):
                            defs.append(
                                (elt.id, "var", elt.lineno or node.lineno)
                            )
        elif isinstance(node, ast.AnnAssign):
            if isinstance(node.target, ast.Name):
                defs.append((node.target.id, "var", node.lineno))
    return defs


# ---------------------------------------------------------------------------
# File analysis
# ---------------------------------------------------------------------------


class FileInfo(NamedTuple):
    """Parsed information from a single Python file."""

    path: Path
    defs: list[tuple[str, str, int]]       # (name, kind, lineno)
    name_refs: set[str]                     # Name nodes minus definition targets
    import_names: set[str]                  # imported names
    export_strings: set[str]                # __dir__() + __all__ strings
    implicit_refs: set[str]                 # CLI decorator registrations
    attr_refs: set[str]                     # attribute-access names (unfiltered)


def analyze_file(path: Path) -> FileInfo | None:
    """Parse *path* and return its analysis, or ``None`` on syntax error."""
    try:
        tree = ast.parse(path.read_text(), filename=str(path))
    except SyntaxError:
        return None

    name_counts = _count_name_nodes(tree)
    def_counts = _count_def_targets(tree)

    # A name is "really referenced" when it appears as a Name node more
    # often than it appears as a definition target.  The extra occurrences
    # are true references (e.g. ``x = x + 1`` references ``x`` on the RHS).
    refs: set[str] = set()
    for name, total in name_counts.items():
        if total > def_counts.get(name, 0):
            refs.add(name)

    return FileInfo(
        path=path,
        defs=_collect_top_level_defs(tree),
        name_refs=refs,
        import_names=_collect_import_names(tree),
        export_strings=_collect_export_strings(tree),
        implicit_refs=_collect_implicit_cli_refs(tree),
        attr_refs=_collect_attr_names(tree),
    )


# ---------------------------------------------------------------------------
# Report formatting helpers
# ---------------------------------------------------------------------------


def _emit_report(
    dead_items: list[dict],
    out: object = None,
) -> None:
    """Print a human-readable dead-code report."""
    if not dead_items:
        _log(f"\n  {GREEN}No dead code found!{RESET}\n")
        return

    test_only = [d for d in dead_items if d["category"] == "test-only"]
    only_declared = [d for d in dead_items if d["category"] == "only-declared"]

    _log(f"\n  Found {BOLD}{len(dead_items)}{RESET} dead item(s):\n")

    if test_only:
        _log(f"  {YELLOW}── Used only in ignored directories (tests/){RESET}")
        _log(f"     ({len(test_only)} item(s))\n")
        for item in test_only:
            loc = f"{item['file']}:{item['line']}"
            tag = item["kind"]
            _log(f"    {loc}  {tag} {BOLD}{item['name']}{RESET}")
            refs_shown = item["referenced_in"][:3]
            if refs_shown:
                _log(f"      referenced in: {', '.join(refs_shown)}")
            if len(item["referenced_in"]) > 3:
                _log(f"      ... and {len(item['referenced_in']) - 3} more")
            _log()

    if only_declared:
        _log(f"  {RED}── Only declared (never referenced anywhere){RESET}")
        _log(f"     ({len(only_declared)} item(s))\n")
        for item in only_declared:
            loc = f"{item['file']}:{item['line']}"
            tag = item["kind"]
            _log(f"    {loc}  {tag} {BOLD}{item['name']}{RESET}")
        _log()

    _log(f"  {'─' * 50}")
    _log(f"  Total dead items:    {len(dead_items)}")
    _log(f"    test-only:         {len(test_only)}")
    _log(f"    only-declared:     {len(only_declared)}")
    _log()


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> None:  # noqa: C901
    import argparse

    parser = argparse.ArgumentParser(
        description="Scan for dead code (functions, classes, variables)."
    )
    parser.add_argument(
        "extra_dirs",
        nargs="*",
        metavar="DIR",
        help="Additional directories to scan (in addition to SCAN_DIRS)",
    )
    parser.add_argument(
        "--verbose",
        "-v",
        action="store_true",
        help="Show files being scanned and skipped",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="Output results as JSON to stdout (all progress goes to stderr)",
    )
    args = parser.parse_args()

    # All progress/info output goes to stderr so that --json can safely
    # pipe to stdout without contamination.
    _print_banner("Dead Code Scanner")

    # ── Resolve directories ────────────────────────────────────────
    scan_dirs: list[Path] = list(SCAN_DIRS)
    for d in args.extra_dirs:
        scan_dirs.append(Path(d).resolve())
    ignore_dirs: list[Path] = list(IGNORE_DIRS)

    # ── Collect files ──────────────────────────────────────────────
    scan_files: list[Path] = []
    for d in scan_dirs:
        if d.is_dir():
            scan_files.extend(_walk_py_files(d))
            if args.verbose:
                _log(f"  scan: {d}")
        else:
            _log(f"  {YELLOW}scan dir not found: {d}{RESET}")

    ignore_files: list[Path] = []
    for d in ignore_dirs:
        if d.is_dir():
            ignore_files.extend(_walk_py_files(d))
            if args.verbose:
                _log(f"  scan (usages only): {d}")
        else:
            _log(f"  {YELLOW}ignore dir not found: {d}{RESET}")

    if args.verbose:
        _log(
            f"\n  {len(scan_files)} scan files, {len(ignore_files)} ignore files"
        )

    if not scan_files:
        _log(f"  {RED}No Python files found in scan directories{RESET}")
        sys.exit(1)

    # ── Parse all files ────────────────────────────────────────────
    analyses: list[FileInfo] = []
    for f in scan_files + ignore_files:
        info = analyze_file(f)
        if info is not None:
            analyses.append(info)
        elif args.verbose:
            _log(f"  {YELLOW}skipped (syntax error): {f}{RESET}")

    # ── Build definition index ─────────────────────────────────────
    # def_key = (file_path, name) -> (kind, lineno)
    def_index: dict[tuple[Path, str], tuple[str, int]] = {}
    scan_def_keys: set[tuple[Path, str]] = set()

    for info in analyses:
        for name, kind, lineno in info.defs:
            key = (info.path, name)
            def_index[key] = (kind, lineno)
            if any(info.path.is_relative_to(d) for d in scan_dirs):
                scan_def_keys.add(key)

    # ── Build reference index ──────────────────────────────────────
    # refs[name] -> set of Paths that reference that name
    refs: dict[str, set[Path]] = defaultdict(set)

    # Build a set of known definition names for attribute-access filtering.
    known_def_names: set[str] = {name for (_, name) in scan_def_keys}

    for info in analyses:
        all_refs = (
            info.name_refs
            | info.import_names
            | info.export_strings
            | info.implicit_refs
            # Attribute-access names are filtered against known definitions
            # to avoid noise from every ``obj.method()`` call adding ``method``
            # as a reference.
            | {a for a in info.attr_refs if a in known_def_names}
        )
        for name in all_refs:
            refs[name].add(info.path)

    # ── Find dead definitions ──────────────────────────────────────
    dead_items: list[dict] = []

    for def_file, name in sorted(scan_def_keys, key=lambda x: (x[0], x[1])):
        # Skip names that match the filter list.
        if _should_skip(name):
            continue

        kind, lineno = def_index[(def_file, name)]

        referencing_files = refs.get(name, set())

        # Split references by whether they originate in scan or ignore dirs.
        scan_refs = {
            f
            for f in referencing_files
            if any(f.is_relative_to(d) for d in scan_dirs)
        }
        ignore_refs = {
            f
            for f in referencing_files
            if any(f.is_relative_to(d) for d in ignore_dirs)
        }

        if len(scan_refs) == 0 and len(ignore_refs) > 0:
            category = "test-only"
        elif len(referencing_files) == 0:
            category = "only-declared"
        else:
            continue  # alive — referenced in scan dirs

        dead_items.append(
            {
                "file": str(def_file.relative_to(PROJECT_ROOT)),
                "line": lineno,
                "name": name,
                "kind": kind,
                "category": category,
                "referenced_in": sorted(
                    str(f.relative_to(PROJECT_ROOT)) for f in ignore_refs
                ),
            }
        )

    # ── Report ─────────────────────────────────────────────────────
    if args.json:
        # stdout: only JSON
        print(json.dumps(dead_items, indent=2))
        return

    # stderr: human-readable report
    _emit_report(dead_items)


if __name__ == "__main__":
    main()
