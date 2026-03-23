# Agent Instructions — Firecracker Manager CLI

## Your Task

> [!IMPORTANT]
> This project is already partially implemented. Before writing any code, you must first
> audit what exists. For every requirement in every phase document, locate the relevant
> code, read it, and determine whether the requirement is already met.
>
> **DO NOT reinvent or rewrite working code.**
>
> If a requirement is **not yet implemented** — implement it.
>
> If a requirement is **already implemented** — review the logic line by line and verify it
> correctly satisfies the requirement as written. Fix only what does not meet the spec.
>
> Work through each phase document requirement by requirement, in order. For each one:
> locate the code path, read it, compare it against the requirement, then act. Do not skim.
> Do not assume something works because it exists.

The `firecracker-manager` Python CLI project implementation to completion across all phases.
The work is defined in a set of phase requirement documents (`python-cli-phase-*.md`).
You must read and implement every requirement in every phase document before considering
the project done.

---

## Repository Layout

The repository contains two distinct things:

1. **`firecracker-manager/`** — the Python CLI project you are building. This is your
   working directory. All code, tests, configuration, and documentation go here.

2. **Everything else** (`assets/`, `multi-vm/`, `single-vm/`, `custom-images/`,
   `environment_setup.sh`, `ssh/`) — bash proof-of-concept scripts. These are **read-only
   reference material**. Study them to understand what each operation does at the system
   level (how networking is configured, how cloud-init is embedded, how Firecracker is
   launched, etc.), then implement the equivalent behaviour in Python inside
   `firecracker-manager/`. Do not modify the bash scripts. Do not call them from Python at
   runtime.

---

## Phase Documents

Requirements are split across multiple phase documents, named `python-cli-phase-1.md`,
`python-cli-phase-2.md`, `python-cli-phase-3.md`, and so on.

**Precedence rule:** if the same requirement is defined in more than one phase document,
the highest-numbered phase document wins. A requirement in phase 3 overrides the same
requirement in phase 2, which overrides phase 1. Always read all phase documents before
starting implementation so you have a complete picture of the final state.

Read all phase documents in order before writing any code.

---

## Status Tracking

You must maintain two status files as you work:

### `phase-status.md` — top-level index (at the repo root)

This file provides a single-glance view of all phases. Update it whenever a phase
transitions state.

Format:

```markdown
# Phase Status

| Phase | Document | Status | Last Updated |
|---|---|---|---|
| Phase 1 | python-cli-phase-1.md | Complete | 2025-01-01 |
| Phase 2 | python-cli-phase-2.md | In Progress | 2025-01-02 |
| Phase 3 | python-cli-phase-3.md | Not Started | — |
```

Valid status values: `Not Started`, `In Progress`, `Complete`, `Blocked` (with a note
explaining what is blocking).

### `phase-N-status.md` — per-phase detail file

Each phase gets its own status file. Create it when you begin that phase. Update it as
you complete individual requirements within the phase.

Format:

```markdown
# Phase N Status

## Summary
Status: In Progress
Started: 2025-01-02
Completed: —

## Requirements

| # | Requirement | Status | Notes |
|---|---|---|---|
| 1 | Project identity and build flags | Complete | |
| 2 | Cache directory layout | Complete | |
| 3 | vm create command | In Progress | Flags done, cloud-init pending |
| 4 | Network auto-setup | Not Started | |
```

The requirement descriptions in the table must match the section headings in the phase
document closely enough that a reader can find the corresponding spec without ambiguity.

---

## Implementation Order

Work through the phases in order: complete phase 1 before starting phase 2, and so on.
Within a phase, implement requirements in the order they appear in the document unless a
dependency forces a different order (e.g. models and exceptions must exist before API
functions that use them).

After completing each phase:

1. Update `phase-N-status.md` to `Complete` with a completion date
2. Update `phase-status.md` to reflect the new state
3. Verify the test suite passes in full before moving to the next phase
4. Do not carry forward known broken behaviour into the next phase

---

## General Rules

- **The phase documents are the source of truth.** If something is not specified in a
  phase document, use your best judgement and document the decision in the relevant
  `phase-N-status.md` under a Notes column or a separate Decisions section.
- **Precedence is strict.** Before implementing any requirement, check whether a later
  phase document overrides it. Implementing a requirement that a later phase removes or
  changes is wasted work and creates churn.
- **Do not modify the proof-of-concept bash scripts.** They are reference only.
- **Tests are not optional.** Each phase's requirements include testing. Do not mark a
  phase complete if its tests are missing or failing.
- **Status files must stay current.** An outdated status file is worse than no status
  file — it causes confusion about what is actually done. Update them continuously as you
  work, not in a single batch at the end.
- Ensure every unused files are added to gitignore including __pycache__ directory under every sub-directory if this pycache directory is not needed! 
