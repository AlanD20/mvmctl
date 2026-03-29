---
name: architect
version: 1.0.0
description: Guide architecture-aligned design decisions for mvmctl
author: mvmctl team
license: MIT
compatibility: opencode
metadata:
  audience: developers
  tags: ["python", "firecracker", "mvmctl", "architecture", "design"]
  workflow: development
---

## What I do

I guide you in making architecture-aligned design decisions when implementing new features or refactoring:

- **Layer placement** — Determine where code belongs (cli/api/core/models/utils)
- **Data flow** — Ensure User → cli → api → core → models flow
- **Import boundaries** — Prevent circular dependencies and layer violations
- **Configuration patterns** — Choose correct default resolution strategy
- **Module responsibilities** — Clarify what each layer should/shouldn't do

## When to use me

Use me when designing new features, planning refactors, or deciding where to place new code.

I am NOT for code review — use `@.agents/skills/code-review/` skill for that.

## Layer Architecture

**Data Flow**: User → mvm → main.py → cli/*.py → api/*.py → core/*.py → models/ + utils/

| Layer | Responsibility | Can Import | Must NOT Import |
|-------|---------------|------------|-----------------|
| **cli/** | Typer commands, arg parsing, output | api/ | core/, models/ |
| **api/** | Privilege checks, delegate to core/ | core/, models/, utils/ | cli/ |
| **core/** | Business logic, subprocess, Firecracker | models/, utils/ | cli/, api/ |
| **models/** | Pure @dataclass, no side effects | None (leaf node) | All other layers |
| **utils/** | Pure helpers, no domain knowledge | None (leaf node) | All other layers |

**Known Violations**: `cli/asset.py` and `cli/configure.py` import from core/ directly (documented exceptions).

## Module Responsibilities

### cli/
- Typer app with `no_args_is_help=True`, `rich_markup_mode=None`, `add_completion=False`
- Runtime default resolution: `_defaults = _get_vm_defaults()`
- Call api/ only (except known violations)
- NO business logic here

### api/
- Add `check_privileges(binary_path)` before privileged ops
- Delegate to core/, return results directly
- Export with `__all__`
- NO output formatting

### core/
- Return data or raise typed exceptions
- NO `print()` or console output
- Subprocess calls only here (list form, NO shell=True)
- Raise MVMError subclasses

### models/
- @dataclass only
- `__post_init__` for validation only
- NO subprocess, I/O, or side effects
- VMInstance uses 64-char SHA256 hash (6-char prefix display)

## Configuration Priority

1. `constants.py` FALLBACK_*
2. State files (`config.json` + `metadata.json`)
3. `MVM_*` environment variables
4. CLI flags

## Design Decision Checklist

When adding new functionality:

- [ ] Which layer? (cli/api/core/models/utils)
- [ ] Does data flow follow cli→api→core→models?
- [ ] cli/ imports only from api/?
- [ ] api/ adds privilege checks before privileged ops?
- [ ] core/ raises typed exceptions (never prints)?
- [ ] models/ uses @dataclass only?
- [ ] NO hardcoded defaults (use FALLBACK_*)?
- [ ] Env vars use `MVM_` prefix?
- [ ] Subprocess calls only in core/?
- [ ] New exceptions extend MVMError hierarchy?

## Entry Point

```
main.py:LazyMVMGroup (click.Group)
├── _COMMAND_SPECS dict for lazy loading
├── get_command() imports module on first access
└── Sub-apps via `typer.main.get_command()`
```

## Quick Reference

| Decision | Answer |
|----------|--------|
| New command? | cli/ → call api/ |
| Privileged operation? | api/ adds `check_privileges()` → calls core/ |
| Data container? | models/ @dataclass |
| Helper function? | utils/ (pure, no domain knowledge) |
| Default value? | constants.py FALLBACK_* |
| Config resolution? | None default → runtime resolution |
