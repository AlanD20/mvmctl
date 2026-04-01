# mvmctl — Claude Code Context

**Project:** Production-grade Python CLI for managing microVMs  
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.  
**Stack:** Python 3.13, Typer, Rich, uv  
**CLI Entry:** `mvm` console script (defined in `pyproject.toml`)

> **Legacy bash scripts** are preserved in `legacy/` for reference.

### ⚠️ ABSOLUTE RULES
 
1. **NEVER read files yourself** — spawn a subagent to do it
2. **NEVER edit/create code yourself** — spawn a subagent to do it
3. **ALWAYS use default subagent** — NEVER use `agentName: "Plan"` (omit `agentName` entirely)

### User Confirmation Required

**NEVER implement changes immediately without user confirmation.**

Before making any code changes:
1. Present your proposed approach to the user
2. Explain what you intend to do and why
3. Wait for explicit user approval
4. Only proceed with implementation after receiving confirmation

This applies to all edits, fixes, features, and refactoring. No exceptions.

---

## Quick Start

```bash
uv sync --group dev            # Install all deps
uv run pytest tests/ -x -q    # Run tests (stop at first failure)
uv run ruff check src/ && uv run mypy src/  # Lint + type check

# Build standalone binary
pip install -e ".[dev]" pyinstaller
pyinstaller --onefile --name mvm src/mvmctl/main.py
# Output: dist/mvm
```

## Project Structure

```
src/mvmctl/
├── main.py          # LazyMVMGroup (click.Group) — lazy-loads sub-apps via importlib
├── constants.py     # Single source of truth — CLI name, env prefix, all defaults
├── exceptions.py    # Custom exception hierarchy (MVMError → domain subclasses)
├── cli/             # Thin Typer command definitions (no business logic)
├── api/             # Stable public Python API; adds privilege checks before core
├── core/            # All business logic, subprocess, Firecracker interaction
├── models/          # Pure dataclasses (VMInstance, VMConfig, ImageSpec, etc.)
├── utils/           # Shared helpers: console, process, fs, http, audit, validation
├── assets/          # Bundled YAML configs (images.yaml, kernels.yaml, defaults.yaml)
└── services/        # Runtime subprocess services (console_relay, nocloud_server)
tests/               # Unit + integration test files (64 total)
docs/                # API and release docs
legacy/              # Archived bash scripts (single-vm, multi-vm, assets)
pyproject.toml       # Build, ruff, mypy strict, pytest (80% branch coverage gate)
```

## Data Flow

```
User → mvm → main.py → cli/*.py → api/*.py → core/*.py → models/ + utils/
```

## Key Files

| Task | Location |
|------|----------|
| VM lifecycle | `src/mvmctl/core/vm_lifecycle.py` |
| Network setup | `src/mvmctl/core/network.py`, `core/network_manager.py` |
| Host init | `src/mvmctl/core/host_setup.py` |
| Privilege checks | `src/mvmctl/core/host_privilege.py` |
| Asset metadata | `src/mvmctl/core/metadata.py` |
| Firecracker HTTP API | `src/mvmctl/core/firecracker.py` |
| CLI commands | `src/mvmctl/cli/` |
| Tests | `tests/unit/` |
| CI/CD | `.github/workflows/ci.yml`, `.github/workflows/release.yml` |

## Configuration

- **Cache:** `~/.cache/mvmctl/` (`MVM_CACHE_DIR`)
- **Config:** `~/.config/mvmctl/config.json` (`MVM_CONFIG_DIR`) — JSON, not YAML
- **Metadata:** `~/.cache/mvmctl/metadata.json` (`MVM_CACHE_DIR`) — images/kernels/binaries + `is_default` markers
- **Env prefix:** `MVM_` (e.g. `MVM_CACHE_DIR`, `MVM_KERNEL`)
- **Priority:** constants.py fallbacks → state files (config.json + metadata.json) → MVM_* env vars → CLI flags

## Architecture Constraints

- **cli/** — arg parsing + output formatting ONLY; call `api/`
- **api/** — privilege checks + delegation to `core/`; stable public API with `__all__`
- **core/** — subprocess, filesystem, business logic; raises typed exceptions
- **models/** — `@dataclass` only; no methods with side effects
- **utils/** — pure helpers with no domain knowledge

## Code Quality Gates (CI-enforced)

**ALL code changes MUST pass CI checks before completion.**

```bash
uv run ruff check src/          # Must be clean
uv run ruff format --check src/ # Must be clean
uv run mypy src/                # Strict mode — no type: ignore allowed
uv run pytest tests/ -q         # 80% branch coverage minimum
```

**If checks fail:**
- Fix linting/formatting issues with `uv run ruff check src/ --fix` and `uv run ruff format src/`
- Fix type errors with proper type annotations
- Fix failing tests — NEVER delete tests to make them pass

Tests must NOT require root, KVM, or real network. Mock all subprocess calls.

## Commit Authorship (MANDATORY)

**DO NOT add `Co-authored-by` trailers unless the co-author actually contributed to that specific change.**

- Only add co-authors when they **directly contributed code, review, or significant input** to that specific commit
- Do NOT add co-authors as a blanket practice on every commit  
- Do NOT add co-authors just because they are part of the project or team
- When in doubt, **omit the co-author trailer entirely**

**Correct:**
```
feat: add new VM snapshot feature

Co-authored-by: Alice <alice@example.com>  # Alice wrote part of this feature
```

**Incorrect:**
```
style: fix formatting

Co-authored-by: Adam <adam@example.com>  # WRONG - no contribution to this change
```

## Related Files

- `AGENTS.md` — Full architecture reference for AI agents
- `src/mvmctl/core/AGENTS.md` — Core module inventory
- `src/mvmctl/cli/AGENTS.md` — CLI wiring, Typer patterns
- `src/mvmctl/api/AGENTS.md` — API layer pattern
- `src/mvmctl/services/AGENTS.md` — Runtime services (console_relay, nocloud_server)
- `tests/AGENTS.md` — Test fixtures, mock conventions
