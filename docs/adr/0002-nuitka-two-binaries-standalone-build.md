# Nuitka Build with Two Binaries and `--onefile` Mode

mvmctl compiles two Nuitka binaries (`mvm` for the main CLI, `mvm-services` for runtime subprocesses via multidist symlink dispatch) in `--onefile` mode. Libraries with dynamic runtime registries (passlib, etc.) require manual `--include-module` flags. The `--onefile` mode produces a single ELF that self-extracts to `/tmp/onefile_{PID}_{TIME}/` on first invocation (~35 MB release, ~50 MB fast). This extraction happens once per cold start and is reused until the temp directory is cleaned (typically on reboot). Multidist services (`mvm-console-relay`, `mvm-nocloud-server`, `mvm-provision`) share a single binary dispatched via `sys.argv[0]`.

## Status

Accepted

## Context

Nuitka can compile Python in two modes: `--standalone` (a directory with the binary + all dependencies) and `--onefile` (a single ELF that extracts itself at runtime). The project originally considered `--standalone` primarily because the binaries spawned via sudo (`mvm-provision`) must resolve their own dependencies without the parent Python environment — `--standalone` and `--onefile` both solve this. `--onefile` was chosen for distribution convenience: a single file for users to download, install, and symlink.

## Decision

| Aspect | Selection | Rationale |
|--------|-----------|-----------|
| Single-file distribution | `--onefile` | A single ELF is simpler for users than a directory. |
| Tree-shaking (release) | `--lto=yes`, `--enable-plugin=anti-bloat`, `--deployment`, `--nofollow-import-to=*`, `--noinclude-default-mode=nofollow` | Reduces binary from ~150 MB to ~35 MB. Safe force-includes prevent runtime `ModuleNotFoundError` for dynamic imports (passlib, jinja2.tests, rich._unicode_data). |
| Multidist service binary | Single `mvm-services` with symlink dispatch | Avoids compiling three separate service binaries. Each service entry point is linked via a temp symlink passed via `--main=<path>`. |
| Static libpython (release) | Conditional `--static-libpython=yes` | Reduces size, improves portability. Only available with standard Python (not standalone distributions like uv's). |

## Consequences

- **Single-file distribution**: Users download one file per binary (`mvm`, `mvm-services`). The ~35 MB extraction to `/tmp` happens on the first run of each binary (warm thereafter).
- **Fast and release modes**: `--fast` skips tree-shaking for quick iteration (~50 MB). `--release` is the default and includes aggressive optimization (~35 MB).
- **Safe force-includes required**: Dynamic imports (passlib handlers, jinja2.tests, rich unicode data) must be explicitly listed — Nuitka can't auto-detect them. Missing one causes `ModuleNotFoundError` in production.
- **No PyInstaller**: The `pyinstaller` dependency remains in `pyproject.toml` build group but is entirely unused. No PyInstaller hooks exist on disk.
- **Build targets**: `python scripts/build_services.py` builds everything (default). Use `--services`, `--service <name>`, or `--mvm` to build specific targets. `--fast` skips optimization.
