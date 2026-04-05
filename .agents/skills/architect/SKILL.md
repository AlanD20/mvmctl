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

I guide you in making architecture-aligned design decisions:

- **Boundary consciousness** — Every layer has a sacred purpose; respect the walls between them
- **Flow fidelity** — Data MUST travel the correct path; never skip layers
- **Import integrity** — Prevent circular dependencies before they breed
- **Configuration hierarchy** — Know which source of truth dominates
- **Responsibility clarity** — Each module answers to exactly one master

## When to use me

Use me when designing new features, planning refactors, or deciding where to place new code.

I am NOT for code review — use `@.agents/skills/code-review/` skill for that.

## Core Principles

### Principle 1: RESPECT THE LAYER WALLS

Each layer has a SACRED purpose. You MUST know which layer owns what:

| Layer | It IS | It IS NOT |
|-------|-------|-----------|
| **cli/** | The voice — speaks to the user | The brain — does not compute |
| **api/** | The gatekeeper — checks privileges | The executor — does not perform |
| **core/** | The workhorse — all business logic | The speaker — does not print |
| **models/** | The data — pure containers | The actor — has no side effects |
| **utils/** | The tools — shared helpers | The decider — has no domain knowledge |

**MEMO**: "Ask not what the code does — ask which LAYER owns it."

### Principle 2: DATA MUST FLOW DOWNWARD

The path of least resistance is NOT the correct path. Data MUST travel:

```
User → mvm → main.py → cli/*.py → api/*.py → core/*.py → models/ + utils/
```

Skipping layers is architectural debt. If you see:
- `cli/` importing from `core/` → VIOLATION
- `core/` printing to console → VIOLATION
- `models/` making network calls → VIOLATION

**MEMO**: "Downward only. Never climb upstream."

### Principle 3: IMPORT BOUNDARIES ARE FENCES, NOT SUGGESTIONS

Circular dependencies are the root of all architectural evil:

- `cli/` can ONLY import from `api/`
- `api/` can ONLY import from `core/`, `models/`, `utils/`
- `core/` can ONLY import from `models/`, `utils/`
- `models/` and `utils/` are LEAF NODES — they import nothing

**MEMO**: "Import only from below. Never from beside."

### Principle 4: TRUST THE CONFIGURATION HIERARCHY

When multiple sources claim to define a default, KNOW WHICH WINS:

```
1. (lowest) constants.py FALLBACK_* — the desperate last resort
2. State files — config.json and metadata.json in user space
3. MVM_* environment variables — user override
4. (highest) CLI flags — explicit user intent
```

**MEMO**: "Higher authority always wins. The user knows best."

### Principle 5: EACH MODULE HAS ONE JOB

When placing code, ask: "What is this module's SOLE PURPOSE?"

**cli/ SOLE PURPOSE**: Parse args, format output, call api/
- Typer app with `no_args_is_help=True`, `rich_markup_mode=None`, `add_completion=False`
- Runtime defaults: `_defaults = _get_vm_defaults()` — NOT typer defaults
- NO business logic. NO print statements. NO subprocess.

**api/ SOLE PURPOSE**: Add privilege checks, delegate to core/, return results
- `check_privileges(binary_path)` before ANY privileged operation
- `__all__` exports only
- NO output formatting. NO business logic.

**core/ SOLE PURPOSE**: Execute business logic, raise typed exceptions
- Return data OR raise MVMError subclasses
- Subprocess calls ONLY here (list form, NO shell=True)
- NO console output. NO privilege checks.

**models/ SOLE PURPOSE**: Contain data
- `@dataclass` ONLY
- `__post_init__` for validation
- NO subprocess, NO I/O, NO side effects

**utils/ SOLE PURPOSE**: Provide pure helpers
- No domain knowledge whatsoever
- Shared across all layers

**MEMO**: "One purpose. One reason to exist. If it needs two reasons, it doesn't belong."

## Architecture Decision Protocol

### Before placing ANY code, answer:

1. **What is the user trying to do?** → cli/
2. **Does it need privilege verification?** → api/
3. **What is the actual operation?** → core/
4. **Does it hold state or represent domain data?** → models/
5. **Is it a reusable pure function?** → utils/

### Checklist (verify before committing):

- [ ] Which layer owns this logic? (cli/api/core/models/utils)
- [ ] Does data flow follow cli → api → core → models?
- [ ] cli/ imports ONLY from api/?
- [ ] api/ adds `check_privileges()` before privileged ops?
- [ ] core/ raises typed exceptions, never prints?
- [ ] models/ is @dataclass ONLY, no side effects?
- [ ] NO hardcoded defaults (use FALLBACK_* in constants.py)?
- [ ] Env vars use `MVM_` prefix?
- [ ] Subprocess calls ONLY in core/?
- [ ] New exceptions extend MVMError hierarchy?

## Known Violations (Documented)

These exist and are tolerated — they are NOT patterns to follow:

- `cli/asset.py` imports from `core/` directly
- `cli/configure.py` imports from `core/` directly

**MEMO**: "The exception proves the rule. Do not make new exceptions."

## Entry Point Mental Model

```
main.py:LazyMVMGroup (click.Group)
├── _COMMAND_SPECS dict — deferred loading
├── get_command() — imports module only when called
└── Sub-apps via typer.main.get_command()
```

Think of it as a librarian who does not fetch books until you ask for them.

## Quick Reference

| Question | Answer |
|----------|--------|
| New command? | cli/ calls api/ |
| Privileged op? | api/ checks privileges → calls core/ |
| Data container? | models/ @dataclass |
| Helper function? | utils/ (pure, no domain) |
| Default value? | constants.py FALLBACK_* |
| Config resolution? | `None` default → runtime resolution |

