# tests/layer_compliance/ — Architectural Compliance Tests

**Scope:** AST-based enforcement of architecture rules; all 6 tests run in CI
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Tests here use AST parsing or subprocess isolation — NOT runtime imports — to avoid side effects
**Parent:** See `tests/AGENTS.md` for fixtures and mocking patterns

## STRUCTURE

```
tests/layer_compliance/
├── test_imports.py       # CLI→API→Core import boundary enforcement
├── test_constants.py     # constants.py single-source-of-truth validation
├── test_privilege.py     # API privilege check presence verification
├── test_startup_time.py  # <200ms cold-start enforcement
├── test_cleanup.py       # Pytest temp directory cleanup behavior
└── test_constants_new.py # Additional constants validation
```

## TEST FILES

### `test_imports.py` — Import Boundary Enforcement

Uses `ast.parse()` to scan all `cli/*.py` files at import time (no Python execution).

**Rule enforced:** CLI may only import from `api/`, `models/`, `exceptions`, `constants`, `utils/` — NOT from `core/` directly.

**Known violations (allowlisted in test docstring):**
- `cli/bin.py` imports `core/metadata` directly (bulk asset operations need it)
- `cli/init.py` imports `core/config_state` directly (onboarding wizard)

**Key symbols:**
- `_is_core_violation(import_path)` — returns True if import violates the rule
- `TestCLILayerImports.test_cli_no_direct_core_imports` — main gate

---

### `test_constants.py` — Hardcoded Value Detection

Uses `ast.parse()` to enforce no hardcoded paths, large numbers (≥100), or list/dict literals in `core/`, `api/`, or `cli/`.

**Rules enforced:**
- No absolute paths (`/usr/...`, `/etc/...`) in core — must use `constants.*` or `DEFAULT_*`
- No large integers (≥100) in core/api without `constants.` / `FALLBACK_` / `DEFAULT_` on the same line
- List/dict constants in `constants.py` must use `_require_str_list()` / `_require_str_dict()` — no hardcoded literals

**`_require_*` functions (must exist in `constants.py`):**
`_require_str`, `_require_int`, `_require_bool`, `_require_str_list`, `_require_str_dict`, `_require_str_float_dict`, `_require_chain_list`

**File exceptions:** `constants.py` itself, `core/rootfs_injector.py` (constant holder)

**Line exceptions:** `vm_lifecycle.py` chmod/chown lines; `rootfs_injector.py` set_memsize lines

---

### `test_privilege.py` — API Privilege Check Presence

Uses `ast.parse()` to verify that specific API functions call `check_privileges()` or `check_privileges_interactive()` before delegating to core.

**Privileged functions tracked:**

| File | Functions |
|------|-----------|
| `api/vms.py` | `create_vm`, `remove_vm`, `cleanup_vms` |
| `api/network.py` | `create_network`, `remove_network` |

**Detection:** walks function AST body looking for `ast.Call` nodes matching either privilege function name (direct or attribute form).

---

### `test_startup_time.py` — Cold-Start Enforcement

Spawns a **subprocess** (not import) to measure true cold-start time. Strips `COVERAGE*`, `COV_CORE_*`, `PYTEST_*` env vars to prevent instrumentation inflation.

**Limit:** 200ms for all modules and main CLI (`--help` roundtrip)

**Exempting a slow module:**
```python
STARTUP_ALLOWLIST: dict[str, str] = {
    "mvmctl.some.module": "Reason here (Issue #X)",
}
```

Module path format: `mvmctl.<relative.path.without.extension>` (e.g., `mvmctl.core.firecracker`)

CLI modules (`cli/`) are excluded from per-module parametrization — they are lazy-loaded.

---

### `test_cleanup.py` — Pytest Temp Dir Cleanup

Verifies that `tests/conftest.py:pytest_sessionfinish` hook exists and only targets `pytest-*` directories under `tempfile.gettempdir()`. Prevents accidental temp dir accumulation.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Runtime imports to test architecture | Use `ast.parse()` — no side effects |
| Deleting allowlist entries to "fix" failures | Fix the underlying violation; allowlists are documentation |
| Adding blanket file exceptions to test_constants | Fix the hardcoded value; exceptions are named explicitly |
| Running layer_compliance in watch mode | These are CI gate tests; run via `uv run pytest tests/layer_compliance/` |

## COMMANDS

```bash
# Run all layer compliance tests
uv run pytest tests/layer_compliance/ -v

# Run single file
uv run pytest tests/layer_compliance/test_imports.py -v
uv run pytest tests/layer_compliance/test_startup_time.py -v  # slow — spawns subprocesses
```

## NOTES

- **6 test files**: Covering imports, constants, privileges, startup time, and cleanup behavior
- All tests are **AST-based or subprocess-isolated** — they do NOT import `mvmctl.*` at collection time (except `test_constants.py` which imports `mvmctl.constants` for functional validation)
- Layer compliance failures in CI indicate real architectural regressions — never skip or xfail without documented justification
