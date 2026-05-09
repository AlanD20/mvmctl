---
name: refactor
version: 1.0.0
description: Guide clean code refactoring for mvmctl
author: mvmctl team
license: MIT
compatibility: opencode
metadata:
  audience: developers
  tags: ["python", "firecracker", "mvmctl", "refactor", "clean-code"]
  workflow: development
---

## What I do

I guide you through refactoring — transforming messy, convoluted code into clean, logical instruction flows:

- **Investigation first** — You MUST investigate the function thoroughly before touching it
- **Clean flow enforcement** — Instructions must follow a clear, logical sequence
- **Utility extraction** — Move shared logic to utils/ when appropriate
- **Behavioral preservation** — The contract MUST be honored; behavior is non-negotiable
- **Adaptive cognition** — When logic is flawed or ambiguous, you STOP and ASK

## When to use me

Use me when refactoring any function in mvmctl, especially when:
- Instructions are tangled and hard to follow
- Multiple responsibilities are mixed together
- A utility helper could be extracted
- The existing logic seems questionable

I am NOT for implementing new features — use `@.agents/skills/architect/` skill for that.

## Core Principles

### Principle 1: INVESTIGATE BEFORE TOUCHING

You MUST perform due diligence before making any changes:

1. Read the function definition THOROUGHLY
2. Use `explore` agent to find ALL call sites of this function
3. Map every input, output, and side effect
4. Understand the contract this function promises to maintain

**MEMO**: "Understand the function's complete behavior before altering its form."

### Principle 2: PRESERVE THE CONTRACT

The function's behavior is SACROSANCT:

- Identical inputs MUST produce identical outputs
- Same exceptions MUST be raised under same conditions
- Side effects MUST remain unchanged
- If the existing behavior is WRONG, you STOP and PRESENT the discrepancy to the user

**MEMO**: "Honor the contract. Behavior is sacred; only form may change."

### Principle 3: LINEARIZE THE FLOW

Instructions MUST follow a clean, logical sequence:

1. **Setup** — Prepare what is needed (inputs, resources, state)
2. **Validation** — Verify preconditions and invariants
3. **Execution** — Perform the core operation
4. **Termination** — Clean up, return results, handle final state

Scattered logic is a sign of technical debt. When you see instructions that jump around:
- Group related operations
- Order them by dependency
- Extract helper functions when steps are repeated

**MEMO**: "A function is a story. Tell it in order: beginning, middle, end."

### Principle 4: NAME WITH INTENT

Variables and functions MUST reveal their purpose:

- `$counter` tells nothing; `$retry_attempt` tells everything
- `$data` is meaningless; `$vm_instance_record` is precise
- `$process` is vague; `$validate_kernel_executable` is clear

If you rename during refactor, you are EMPOWERED to do so — the name must serve comprehension.

**MEMO**: "Name the thing for what it IS, not what it CONTAINS."

### Principle 5: EXTRACT UTILITIES

Shared logic belongs in utils/:

- Pure helper functions → utils/ (no domain knowledge)
- Repeated subprocess patterns → utils/operation_utils.py
- File operations → utils/fs.py
- HTTP operations → utils/http.py
- Network helpers → utils/network.py

**MEMO**: "Centralize tools; distribute only the orchestration."

### Principle 6: KNOW WHEN TO STOP

When you encounter ambiguity or potential flaws:

1. Do NOT assume you know better than the codebase
2. Do NOT silently "fix" behavior you think is wrong
3. STOP the refactor immediately
4. PRESENT the discrepancy to the user
5. AWAIT their decision on how to proceed

The user has vision. You have only analysis.

**MEMO**: "When in doubt, the pen stays down. Ask, do not assume."

## Refactoring Protocol

### Step 1: Investigation (MANDATORY)
```
1. Read the function completely — all paths, branches, edge cases
2. List ALL callers via explore agent
3. Document:
   - Inputs (parameters, globals, file state)
   - Outputs (return values, exceptions, side effects)
   - The "contract" in one sentence
```

### Step 2: Analysis
```
1. Identify instruction groups (what belongs together)
2. Identify instruction order issues (what depends on what)
3. Identify naming issues (misleading, vague, or absent names)
4. Identify extraction opportunities (repeated patterns, utils-worthy helpers)
```

### Step 3: Planning (for complex refactors)
```
1. Propose the new structure to user
2. Explain the flow improvement
3. AWAIT confirmation before proceeding
```

### Step 4: Execution
```
1. Implement the clean flow
2. Move helpers to utils/ if applicable
3. Rename variables for clarity
4. Verify: identical behavior, cleaner structure
```

### Step 5: Verification
```
1. Run: uv run ruff check src/
2. Run: uv run mypy src/
3. Run: uv run pytest tests/ -q --cov=src/mvmctl -n auto --cov-fail-under=80
```

## Checklist

- [ ] Function fully investigated (read + call sites found)
- [ ] Contract documented (inputs → outputs + exceptions)
- [ ] Flow is linear (setup → validate → execute → terminate)
- [ ] Variables renamed for clarity
- [ ] Utilities extracted to utils/ if applicable
- [ ] Behavioral equivalence verified
- [ ] CI gates pass (ruff, format, mypy, pytest 80%)

## Quick Reference

| Principle | Imperative |
|-----------|------------|
| Investigate first | Read completely, find all callers |
| Preserve contract | Behavior is sacred; form may change |
| Linearize flow | Setup → Validate → Execute → Terminate |
| Name with intent | Reveal purpose, not contents |
| Extract utilities | Centralize shared helpers in utils/ |
| Know when to stop | Ask when ambiguous; do not assume |

## Example Refactor

**BEFORE** (messy):
```python
def create_vm(cfg, name, img):
    # validation scattered
    if not name:
        raise ValueError("no name")
    f = get_config()
    # some setup mixed in
    vcpus = cfg.get("vcpus", 2)
    if vcpus < 1:
        raise ValueError("bad vcpus")
    img_path = resolve_image(img) # but what if img is None?
    # ... more mixed logic
```

**AFTER** (clean linear flow):
```python
def create_vm(cfg: VMConfig, name: str, img: str | None) -> VMInstance:
    # SETUP: Resolve inputs and resources
    _validate_name(name)
    _validate_vcpus(cfg.vcpu_count)
    image_path = _resolve_image(img, cfg)
    kernel_path = _resolve_kernel(cfg)
    
    # EXECUTION: Create the VM
    instance = _instantiate_vm(name, image_path, kernel_path, cfg)
    
    # TERMINATION: Finalize and return
    _persist_instance(instance)
    return instance
```

