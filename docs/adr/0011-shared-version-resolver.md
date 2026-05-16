# 0011 — Shared VersionResolver: unified version spec parsing across domains

**Status:** accepted

The project previously had ad-hoc version string parsing scattered across three domains (binary, image, kernel), each handling `type:version` selectors and partial version resolution independently. This ADR documents the unified `VersionResolver` utility that consolidates that logic into a single shared module.

## What changed

Introduced `core/_shared/_version_resolver.py` with three exports:

- **`VersionSpec`** — A `@dataclass` with explicit `major`, `minor`, `patch`, `is_latest` fields. The `is_partial` property signals incomplete specs.
- **`VersionResolver`** — A pure static-method utility class with four methods, all side-effect-free: `parse_spec`, `parse_selector`, `resolve`, `semver_key`.
- **`VersionError`** (inherits `MVMError`) — raised when resolution fails.

### Removed

| Location | Removed | Replaced by |
|----------|---------|-------------|
| `utils/common.py` — `CommonUtils.semver_key()` | Entire method | `VersionResolver.semver_key()` |
| `core/binary/_service.py` — `BinaryService._semver_key()` | Entire method | `VersionResolver.semver_key()` inline |
| `core/image/_resolver.py` — inline `split(":")` | Ad-hoc selector parsing | `VersionResolver.parse_selector()` |
| `core/kernel/_resolver.py` — inline `split(":")` | Ad-hoc selector parsing | `VersionResolver.parse_selector()` |
| `api/inputs/_binary_input.py` — inline `name:version` split | Ad-hoc selector parsing | `VersionResolver.parse_selector()` |

### CLI consistency

`mvm bin pull` was changed to match the existing `image pull` / `kernel pull` pattern:

```diff
-    version: str = typer.Argument(..., help="Version to download (e.g. 1.15.0)")
+    name: str = typer.Argument(..., help="Binary name (e.g. firecracker)")
+    version: str | None = typer.Option(None, "--version", help="Version to download")
```

All `--default` CLI options now also accept `-d` shorthand.

### Field rename

`BinaryPullInput.set_as_default` renamed to `set_default` for consistency with the rest of the codebase.

## Design

### VersionResolver — pure utility, zero I/O

```
VersionResolver (static methods)
  ├── parse_spec(spec: str) -> VersionSpec
  │     "" / "latest" -> VersionSpec(is_latest=True)
  │     "1" -> VersionSpec(major=1)
  │     "1.15" -> VersionSpec(major=1, minor=15)
  │     "v1.15.1" -> VersionSpec(major=1, minor=15, patch=1)
  │
  ├── parse_selector(selector: str) -> tuple[str | None, str]
  │     "firecracker:1.15" -> ("firecracker", "1.15")
  │     "1.15" -> (None, "1.15")
  │     "firecracker" -> ("firecracker", "")
  │
  ├── resolve(versions: list[str], spec: VersionSpec) -> str
  │     Sorts a COPY of versions descending.
  │     is_latest=True -> versions[0]
  │     Exact (all 3 parts) -> verify existence, return early — no scan
  │     Partial -> prefix match on sorted list, return highest
  │     No match -> raise VersionError
  │
  └── semver_key(v: str) -> tuple[int, ...]
        Strip "v", split ".", int() each part.
```

### Key rules

1. **Works on a copy** — `resolve()` sorts a copy of the input list, never mutates the caller's data.
2. **Exact match short-circuit** — When major+minor+patch are all set, `resolve()` checks existence in the list and returns immediately instead of iterating.
3. **Strict prefix match** for partials — `"1.15"` matches `[1, 15, x]` only. Not `[1, 16, x]` or `[2, 0, 0]`.
4. **No I/O** — The class never makes network calls, reads files, or queries databases. The caller fetches versions, then calls `resolve()`.

## Consequences

Positive:

- Single source of truth for version parsing logic across three domains
- Pure utility is trivially unit-testable (no mocking needed for parse_spec, parse_selector, semver_key; resolve needs a version list)
- Removed 3 separate inline implementations of `type:version` splitting
- Removed delegation antipattern (`_semver_key` on BinaryService that just called CommonUtils)

Negative:

- CLI signature change for `bin pull` breaks any scripts using the old positional version argument

## Related decisions

- ADR-0008: Image type/version listing — established the `type:version` selector convention that this ADR consolidates.
