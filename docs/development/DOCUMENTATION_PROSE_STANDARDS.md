# Documentation Prose Standards

Prose standards for mvmctl documentation. Every documentation file must follow the
conventions for its type. These standards exist because mvmctl's docs serve real
users and contributors — not as internal notes or AI chat logs.

## Table of Contents

- [Universal principles](#universal-principles)
- [General documentation workflow](#general-documentation-workflow)
  - [Before you start](#before-you-start)
  - [Link, do not duplicate](#link-do-not-duplicate)
  - [Verify every claim against source code](#verify-every-claim-against-source-code)
  - [Pre-submit checklist](#pre-submit-checklist)
  - [When a full documentation audit is needed](#when-a-full-documentation-audit-is-needed)
- [Doc type: README](#doc-type-readme)
- [Doc type: Tutorial](#doc-type-tutorial)
- [Doc type: Architecture / Domain Language](#doc-type-architecture--domain-language)
- [Doc type: Technical Reference](#doc-type-technical-reference)
- [Doc type: Implementation Deep Dive](#doc-type-implementation-deep-dive)
- [Doc type: Decision Record (ADR)](#doc-type-decision-record-adr)
- [Doc type: Contributor Guide](#doc-type-contributor-guide)
- [Doc type: Troubleshooting Guide](#doc-type-troubleshooting-guide)
- [Doc type: Changelog / Release Notes](#doc-type-changelog--release-notes)
- [Doc type: Spec Reference](#doc-type-spec-reference)
- [Doc type: Optimization Note](#doc-type-optimization-note)
- [Public website content](#public-website-content)
  - [Audience classification](#audience-classification)
  - [What belongs on the site vs the repo](#what-belongs-on-the-site-vs-the-repo)
  - [Five rules for all public-facing content](#five-rules-for-all-public-facing-content)
  - [Replacement pattern](#replacement-pattern)
- [Prohibited patterns](#prohibited-patterns)
- [Rewriting guide: bad → good](#rewriting-guide-bad--good)

---

## Universal principles

These apply to every documentation file regardless of type.

1. **Start with the reader's question.** Every section answers "why does this
   matter to me?" before "what is it?" and "how does it work?"

2. **One idea per paragraph.** A paragraph makes one point, supports it, and
   stops. If a paragraph covers two ideas, split it.

3. **Every paragraph has a topic sentence.** The first sentence states the
   paragraph's main point. Everything else supports or elaborates.

4. **Sections connect with transitions.** No section starts cold. A sentence
   at the end of one section sets up the next. A sentence at the start of
   the next section acknowledges what came before.

5. **Every document with 3+ sections must have a Table of Contents.** The ToC
   sits immediately after the document title and opening paragraph. It lists
   every top-level section (##) and every second-level subsection (###), linked
   by anchor. This lets readers jump directly to the section they need. ToCs
   are omitted only for documents with 2 or fewer sections.

5. **Active voice.** "The relay opens the PTY master" not "The PTY master is
   opened by the relay." Exceptions: when the actor is unknown or irrelevant.

7. **Second person for instructions; third person for describing tool behavior.**
   When telling the reader what to do, use second person: "To create a VM, run
   `mvm vm create my-vm`." When describing what the tool does, use third person:
   "The command starts a Firecracker microVM and attaches it to the default
   network." This matches the practice used by Google's and Microsoft's developer
   documentation: instructional content addresses the reader directly; reference
   content describes the machinery objectively.

8. **Technical precision without jargon explosion.** Define every domain term
   on first use. After that, use the term consistently. Never assume prior
   knowledge of mvmctl internals.

9. **Code examples illustrate, they do not substitute for explanation.**
   A code block follows a paragraph that explains what the code does and why.
   Never drop a code block without context.

10. **Lists enumerate; paragraphs explain.** Use lists only for items that are
    truly parallel and enumerable. Use paragraphs for concepts, relationships,
    and sequences. If you have a list of one-item paragraphs, you're avoiding
    actual writing.

---

## General documentation workflow

These sections describe the process of creating or updating documentation, from
prerequisites through submission.

### Before you start

Before writing or editing any documentation, confirm the following:

- You have read this document — it defines the tone and structure for each doc type.
- You have access to the codebase. Every claim you make must be traceable to Go
  source code at a specific file and line.
- You know which audience you are writing for: users (public website at
  `mvmctl.com`) or contributors (the repo's `docs/` directory).

### Link, do not duplicate

Do not copy information that already exists in another document. Duplication
creates stale information — updating one copy but forgetting the other is the
most common source of documentation rot.

Instead of copying content from another doc, link to it:
```markdown
See [DEPENDENCIES.md](../DEPENDENCIES.md) for the required build packages per distribution.
```

**Correct:** Reference existing docs by link. Keep package tables in
`DEPENDENCIES.md` only, kernel build commands in `KERNEL.md` only.

**Wrong:** Copying the `apt-get install` command from `DEPENDENCIES.md` into
`TROUBLESHOOTING.md` — now there are two places to update when the package name
changes.

**Exceptions:**
- Troubleshooting sections may inline commands that are workarounds for
  automated processes.
- The public website may reference docs in the repo but should not inline large
  sections from them.

### Verify every claim against source code

Before committing any documentation change, trace every factual claim to the
source:

1. **CLI flag names and behavior:** Run the command with `--help` or grep the
   Cobra command definition in `internal/cli/`. Confirm the flag exists and does
   what you describe.
2. **File paths:** Run `ls <path>` to confirm the file exists at the documented
   location.
3. **Function signatures:** Run `grep -rn 'func MyFunction' internal/` to
   confirm the function exists and its signature matches.
4. **Error messages:** Run the command with bad input and verify the exact error
   text.
5. **Example commands:** Copy-paste every code block into a terminal and verify
   it works.

If a claim cannot be traced, either remove it or mark it explicitly as a design
intent rather than current behavior.

### Pre-submit checklist

Before submitting any documentation change, run these checks:

1. **Copy-paste every code block** into a terminal and verify it works. This
   catches dead flags, renamed commands, and placeholder syntax.
2. **Search for old patterns** — if you changed `set-default` subcommand to
   `--default` flag, grep the entire project for `set-default`. If you changed
   the image pull syntax, grep for the old slug.
3. **Replace line number references** with function or type names. Line numbers
   drift as code changes.
4. **Verify links** — every internal section link and external URL must work.
5. **Check for dead code examples** — if an error type is documented, verify it
   still exists in Go source. If not, remove it.

### When a full documentation audit is needed

A full documentation audit is required when any of these happen:

- **CLI refactor:** Flags renamed, commands removed, positional args changed.
- **Model change:** Fields added or removed from model structs.
- **Architecture change:** New domain, backend swap, layer boundary change.
- **Pre-release:** Before any tagged release, all docs must be audited against
  HEAD.

During a full audit, divide the work by doc category:

| Scope | Files |
|-------|-------|
| Root docs | `CONTEXT.md`, `AGENTS.md`, `README.md`, `CHANGELOG.md` |
| `docs/` folder | Architecture, reference, troubleshooting, runtime, dependencies, kernel, release |
| ADR docs | All `docs/adr/*.md` |
| Implementation/development/optimization docs | All `docs/implementations/`, `docs/development/`, `docs/optimizations/*.md` |
| Agent instruction files | `.opencode/agent/*.md` |
| Public site | Files at `mvmctl.com/src/content/site/` |

For each file: read every claim, trace it to source code, fix inaccuracies, and
remove stale content.

---

## Doc type: README

**Audience:** New users evaluating the project. They have not installed mvmctl.

**Goal:** Convince and onboard. Answer "what is this, why should I care, and
how do I start?"

**Prose rules specific to README:**
- Opening paragraph must be inviting and state the value proposition in a
  single sentence: "mvmctl is a CLI for running Firecracker microVMs with
  container-like speed and VM-grade isolation."
- Every feature mention must connect to a user benefit. "Fast boot times"
  is not enough. "VMs boot in 2–4 seconds, so you can iterate as fast as
  containers" connects the feature to the user's workflow.
- Commands appear in context of a walkthrough, not a reference dump.
  The quick start section narrates a session — each command builds on the
  previous one.
- **Badge bar at the top:** CI status, Go version, license. These are the
  first thing many readers see. Keep them compact, one line, linked to
  the respective services.
- **Installation methods are prominent:** `go install`, prebuilt binary
  download, package manager (if available), build from source. Each method
  gets a one-paragraph explanation with the exact command.
- Remove "see also" chains. The README should be self-contained for the
  first 80% of readers. Deeper detail links to docs/ are acceptable but
  not required reading.
- Tone: confident, direct, minimal. No exclamation points except the
  tagline. No emoji except the project badge.

---

## Doc type: Tutorial

**Documents:** Quick start sections within `README.md`, onboarding guides

**Audience:** New users who want to learn the project by doing. They may not
understand the terminology yet.

**Goal:** Give the reader a successful first experience. By the end, they
should have achieved a concrete outcome (a running VM, a deployed environment)
without needing to understand the architecture.

**Relation to Diátaxis:** Tutorials are the first quadrant of the Diátaxis
framework. They are learning-oriented (not task-oriented), and their success
is measured by what the reader accomplished, not what they learned.

**Prose rules specific to Tutorials:**
- **One tutorial, one outcome.** Do not mix concepts. The quick start teaches
  one complete flow: init → kernel → image → key → create VM → SSH. Nothing
  else. Advanced topics (snapshots, volumes, env workflows) belong elsewhere.
- **Explain what to expect, not what is happening internally.** "The VM will
  be ready in 30-60 seconds while cloud-init runs" not "cloud-init's nocloud
  datasource fetches user-data and meta-data from the HTTP server bound to
  the bridge gateway IP."
- **Include the expected output.** After "Run `mvm vm ls`", show what the
  user will see. This lets them confirm they're on the right track.
- **Minimize choices.** Every decision point is a distraction. Either pick
  a reasonable default for the reader ("download the latest kernel") or
  defer the choice with a note ("see the reference for other options").
- **No "for more information" links mid-flow.** Links break the tutorial
  spell. If a concept needs explanation, either explain it inline or defer
  it to a reference section at the end.
- **End with a clear "what's next" section.** The tutorial should naturally
  lead to the next document the reader should consult.

---

## Doc type: Architecture / Domain Language

**Documents:** `CONTEXT.md`, `docs/RUNTIME.md`

**Audience:** Contributors and advanced users who need to understand the
system's structure and design decisions.

**Goal:** Build a shared mental model. The reader should finish knowing the
project's terminology, layer boundaries, and why things are structured the
way they are.

**Prose rules specific to Architecture docs:**
- **Define terms before using them.** Every domain-specific term gets a
  definition in its own paragraph. The term is bolded on first use.
- **Explain the rule AND the rationale.** "Core domains never import other
  core packages" is a rule. The next sentence explains why: "This prevents
  circular dependencies and keeps each domain independently testable and
  replaceable."
- **Use concrete examples.** Abstract rules are paired with real code
  snippets or real scenarios. Every pattern section has a "For example"
  paragraph that shows the rule in action.
- **Connect layers with flow.** Rather than listing layers, explain how
  a single request (e.g., `mvm vm create`) flows through CLI → API → Core,
  and what each layer contributes.
- **Tables for reference, prose for relationships.** A table listing all
  domain directories is fine. But the relationship between Controller,
  Service, and Repository is explained in prose with a paragraph per role
  and a paragraph showing how they collaborate.
- **No code block without a preceding paragraph.** Every code sample is
  introduced by "For example, this is how..." or "The following shows..."

**Structure:**
```
# Title

Opening paragraph: what this document covers and who should read it.

## Section

Topic paragraph: what this section explains and why it matters.

### Subsection

Definition paragraph: defines the concept.
Rationale paragraph: why it exists this way.
Example paragraph (optional): concrete illustration.
Boundary paragraph: what this concept is NOT or what it does NOT do.
```

---

## Doc type: Technical Reference

**Documents:** `docs/REFERENCES.md`

**Audience:** Users who know what they want to do and need to find the exact
command, flag, or option.

**Goal:** Be complete, scannable, and unambiguous. The reader should find
what they need in seconds.

**Prose rules specific to Reference docs:**
- **Commands are grouped by domain.** Each command gets a subsection with:
  - One or two sentences describing what the command does and when to use it
    (topic sentence). This is the only narrative prose allowed — compact,
    benefit-oriented, no fluff.
  - Syntax line in a code block
  - Flag table
  - One or two usage examples
- **The topic sentence earns its place.** It must answer "why would I use this
  command?" If the command's purpose is obvious from its name, skip the
  sentence. When present, keep it to two sentences max.
- **Flag tables are compact.** Three columns: Flag, Description, Default.
  Descriptions are sentence fragments. "Skip confirmation prompts" not
  "This flag, when provided, will skip confirmation prompts."
- **Usage examples show real output.** Not just the command — show what
  the user would see.

---

## Doc type: Implementation Deep Dive

**Documents:** `docs/implementations/*.md`

**Audience:** Contributors who need to understand, modify, or debug a specific
subsystem.

**Goal:** Give the reader enough context to make safe changes to the subsystem.
They should understand the data flow, the edge cases, and the failure modes.

**Prose rules specific to Implementation docs:**
- **Start with the problem statement.** What does this subsystem do? Why does
  it exist? What problem would occur if it were removed?
- **Narrate the happy path end-to-end.** Walk through one complete operation
  from entry point to result. Every step gets a paragraph: what happens,
  which code runs, what data moves.
- **Then cover the failure paths.** What happens when the vsock agent doesn't
  respond? When the loop device is busy? When the HTTP server port is taken?
  Each failure mode gets its own sub-section.
- **Architecture diagrams are ASCII or Mermaid, not images.** No embedded
  images that can't be diffed or updated.
- **File references include the one-line purpose.** "`internal/service/console/relay.go`
  contains the `relay()` function that multiplexes data between PTY, socket,
  and log." Not just "see `relay.go`."
- **Performance characteristics where relevant.** If the subsystem has latency
  or throughput trade-offs, document them with benchmark data. What is fast,
  what is slow, and why.

**Structure:**
```
# Title

## Problem

What this subsystem does and why it exists.

## Architecture

High-level flow. Diagram optional.

## Entry point

Where execution begins. What triggers this subsystem.

## Happy path

Step-by-step walkthrough.

## Failure modes

Each failure scenario as a subsection.

## Key files

Table: file path → one-line purpose.

## Design decisions

Brief notes on alternatives considered (or link to an ADR).
```

---

## Doc type: Decision Record (ADR)

**Documents:** `docs/adr/0001-*.md` through `docs/adr/0013-*.md`

**Audience:** Contributors evaluating whether a new decision is consistent
with past decisions, or trying to understand why something is the way it is.

**Goal:** Preserve the context, options considered, and rationale for a
decision. The reader should understand not just *what* was decided but *why*
the alternatives were rejected.

**Prose rules specific to ADRs:**
- **Follow the template:** Context → Decision → Consequences. Every ADR has
  these three sections in this order.
- **Context explains the tension, not just the facts.** What was the problem?
  What constraints made it hard? What approaches were considered?
- **Decision states clearly what was chosen.** One paragraph. "We will use
  nftables as the default firewall backend" not "After consideration, the
  team decided to go with nftables as the primary firewall backend option."
- **Consequences are honest about trade-offs.** What got better? What got
  worse? What was deferred?
- **No code snippets.** ADRs are about decisions, not implementations.
  If the decision is reflected in a specific file, reference it by path
  but do not paste code.

---

## Doc type: Contributor Guide

**Documents:** `docs/development/*.md`, `docs/STANDARDS.md`

**Audience:** Contributors setting up their environment or learning how to
write tests, docs, or code for this project.

**Goal:** Get the contributor from zero to productive in the shortest
possible time with the least confusion.

**Prose rules specific to Contributor Guides:**
- **Step-by-step instructional tone.** Imperative mood: "Clone the
  repository. Run `./scripts/build.sh`. Verify the binary works."
- **Every instruction includes a verification step.** After "run this
  command," tell the reader what successful output looks like and what
  to do if they see something different.
- **Prerequisites are explicit and checkable.** "Go 1.26.3+ (check with
  `go version`)" not "a recent Go installation."
- **Troubleshooting callouts for common failures.** "If you see error X,
  this means Y. Solution: Z."
- **No fluff.** The reader is here to get work done. Every paragraph should
  advance them toward a working setup or a completed task.

---

## Doc type: Troubleshooting Guide

**Documents:** `docs/TROUBLESHOOTING.md`

**Audience:** Users whose VM won't boot, SSH won't connect, or something
broke. They are already frustrated.

**Goal:** Get them unblocked as fast as possible with the least reading.

**Prose rules specific to Troubleshooting:**
- **Problem-first structure.** Each section is titled with the symptom:
  "VM won't boot" not "Boot issues overview."
- **Shortest path to resolution first.** Most common fix first. If there
  are three possible causes, order them by likelihood.
- **Diagnostic commands before remediation.** "Check if KVM is available:
  `ls /dev/kvm`" before "Reinstall KVM."
- **One fix per section.** If a symptom has multiple causes, each cause
  gets its own sub-section.
- **No "see also" without immediate value.** Cross-references must point
  to the exact fix, not to a document the user has to search.

---

## Doc type: Changelog / Release Notes

**Documents:** `CHANGELOG.md`

**Audience:** Users and contributors who already know the project and want to
know what changed between versions. They scan first, read selectively.

**Goal:** Give the reader an accurate, scannable record of what changed, why,
and how it affects them. Nothing more.

**Prose rules specific to Changelogs:**
- **Reverse chronological order.** Most recent version at the top. Each
  version is a heading two (`## v0.5.0`). Unreleased changes live under
  `## Unreleased` at the top.
- **One change, one line.** Each entry is a single bullet point starting
  with a verb in past tense: "Added", "Fixed", "Changed", "Removed",
  "Deprecated", "Security". No multi-paragraph entries.
- **Group by type.** Within a version, changes are grouped into sections:
  `### Added`, `### Fixed`, `### Changed`, `### Removed`. Sections appear
  in that order. If a section is empty, omit it.
- **Link to the relevant docs.** If a change affects usage (new flag,
  changed behavior, removed command), link to the relevant reference
  doc section so the reader can learn more without searching.
- **Include migration notes inline.** If a change breaks backward
  compatibility, the entry includes the migration command or config
  change in a code block within the entry.
- **No prose beyond the entries.** No introductions, no explanations of
  why the project exists, no roadmap. That belongs in the README and
  ADRs. The changelog is a ledger, not a newsletter.
- **Keep the "Unreleased" section.** Changes go there first during
  development. On release, `## Unreleased` becomes `## vX.Y.Z` and a
  new empty `## Unreleased` section appears at the top.
- **Tagged releases match git tags.** The version heading in the
  changelog must match the git tag exactly. If the tag is `v0.5.0`,
  the heading is `## v0.5.0`.

**Example entry:**
```
## Unreleased

### Added

- `mvm snapshot restore --resume`: restored VMs now auto-start without
  a separate `mvm vm start` call. See [snapshot restore reference].
```

---

## Doc type: Spec Reference

**Documents:** `docs/ENV_SPEC_REFERENCE.md`

**Audience:** Users writing YAML environment specs for `mvm env apply`.

**Goal:** Be the definitive reference for every field, type, default, and
constraint in the YAML spec. No ambiguity.

**Prose rules specific to Spec References:**
- **Field-first organization.** Each section covers one resource type
  (network, key, image, kernel, binary, vm, ssh, exec, copy). Within each
  section, fields are listed in the order they appear in the YAML.
- **Every field entry has:** name, type, required/optional, default value,
  description, example.
- **Types include valid values.** "`type: string` — one of `inject`, `net`,
  `iso`, `off`" not "`type: string`."
- **Constraints are exact.** "`name: string (1-64 chars, alphanumeric + hyphens)`"
  not "`name: string`."
- **Examples are complete YAML blocks,** not fragments. Each example should
  be valid input to `mvm env apply`.

---

## Doc type: Optimization Note

**Documents:** `docs/optimizations/*.md`

**Audience:** Contributors curious about why a particular optimization was
implemented and whether it's still relevant.

**Diátaxis mapping:** Optimization Notes are a specialized form of
Explanation (the fourth Diátaxis quadrant). They explain design decisions
and trade-offs with a specific focus on performance. Like all Explanation
content, their value is in the context they provide — not the instructions
they give.

**Goal:** Document the optimization's rationale, mechanism, and performance
impact so a future contributor knows whether to keep, modify, or remove it.

**Prose rules specific to Optimization Notes:**
- **Start with the problem.** What was slow? By how much? What was the
  threshold for "good enough"?
- **Describe the mechanism in prose.** How does the optimization work?
  What trade-off does it make (speed vs memory, speed vs correctness
  window, etc.)?
- **Include benchmark data.** Before and after numbers. Methodology of
  measurement. What changed between runs.
- **Reference code by function name, not line number.** Line numbers drift.
  Function names are stable. Every reference to code must be grep-able.

---

## Public website content

These sections describe how documentation is split between the public website
(`mvmctl.com`) and the repository's `docs/` directory.

### Audience classification

The project has two distinct documentation audiences:

| Audience | Where they read | What they need |
|----------|----------------|----------------|
| **Users** | `mvmctl.com` (public site) | Install, quick start, essential commands, full command reference |
| **Contributors** | `docs/` in the repo, `CONTEXT.md`, `AGENTS.md` | Architecture, build system, domain internals, test strategy, agent instructions |

Rules:
- The public site is for users only.
- Contributors must go into the repo's `docs/` directory to understand internals.
- Never inline contributor-level content on the public site. Link to it instead.

### What belongs on the site vs the repo

| Belongs on the site | Does NOT belong on the site |
|---------------------|----------------------------|
| Tagline, elevator pitch, feature list | Internal architecture (three-layer design, domain structure) |
| Install methods — one command each | Build system internals (Go flags, dist/ layout) |
| Prerequisites (KVM, Go version, packages) | Sudoers/sudo internals, privileged binaries |
| Quick start: init → running VM | Provisioner backends (LoopMount vs GuestFS) |
| Essential commands — one flag max per command | Manual sudoers configuration (`mvm init` handles it) |
| Full flag reference table | Kernel build dependencies |
| Configuration: file location, env vars list | DB schema, migrations, column layout |
| Cloud-init modes, how it works, security model | Cache directory structure |
| Troubleshooting: common errors with copy-paste fixes | Dependency tables per distro |

### Five rules for all public-facing content

**Rule 1: One command per action.** Never show multi-step setup when a single
flag exists. If the CLI has `--default`, use it — don't show `mvm key create
test` followed by `mvm key default test`. Two commands equal two chances for
the user to get it wrong.

Correct: `mvm key create my-key --default`
Wrong: `mvm key create my-key` then `mvm key default my-key`

This applies everywhere: `--default`, `--force`, `--json`.

**Rule 2: No internal architecture in user-facing docs.** The README and public
website cover what it is, install, quick start, essential commands, and
troubleshooting. Internal architecture — three-layer design, domain structure,
Controller/Service/Repository pattern, DB schema, cache layout, build system
internals — belongs in `docs/` or `CONTEXT.md`. A user should never need to
know how the code is organized to use the tool.

**Rule 3: Commands must be copy-paste ready.** Every code block should work if
the user copies it line by line. No placeholders, no shell variables, no
assumptions about prior state.

Correct: `mvm vm create myvm --image ubuntu:24.04`
Wrong: `mvm vm create <vm-name> --image ubuntu:24.04`

**Rule 4: Lazy linking.** The README is skimmable in 30 seconds. State the
essential and link to the reference. It is a front door, not an encyclopedia.

**Rule 5: Show the happy path first.** Every section leads with the simplest,
most common operation. Advanced flags and edge cases come later.

**Rule 6: If the CLI handles it, do not document the manual way.** If `mvm init`
handles sudoers setup automatically, do not show the manual equivalent. The
exception is troubleshooting sections, where the manual command is a workaround
for something that failed.

### Replacement pattern

When removing internal content from the public site, follow this pattern:

1. Remove the full section (code blocks, tables, descriptions).
2. Replace with a single sentence plus a link to the deeper doc.

**Before (internal):**
```markdown
### mvm sudoers configuration
The mvm binary needs passwordless sudo for provisioner operations...
%mvm ALL=(root) NOPASSWD: /home/*/.cache/mvmctl/bin/mvm
```

**After (user-facing):**
```markdown
> Internals: see the project documentation for provisioning backend details.
```

---

## Prohibited patterns

These patterns are forbidden in ALL document types. If you find them, rewrite.

| Pattern | Why it's banned | Replace with |
|---------|----------------|--------------|
| "This was deprecated" | Internal-memo language. User docs describe current state. | Remove the reference entirely or describe the current replacement. |
| "Previously known as" | Same as above. No one cares what it was called. | Just use the current name. |
| "This was validated" | Street language. Not a journal. | Delete. The doc is correct or it isn't. |
| "This was verified on YYYY-MM-DD" | Meta-commentary. Not user-facing. | Delete. |
| "See also: [link]" at the end of unrelated sections | Lazy writing. Every section should stand on its own. | Either integrate the reference into the prose or remove it. |
| "As mentioned above" / "As discussed earlier" | Assumes linear reading order. The reader may have jumped here. | Restate the context briefly or use a section cross-reference. |
| Bullet lists of one-sentence "paragraphs" | You're avoiding prose. If each item is a paragraph, write paragraphs. | Combine into a proper paragraph with transitions. |
| Code blocks without preceding explanation | The reader doesn't know what they're looking at or why it matters. | Add a sentence before the block explaining what it shows. |
| "/" for mutually exclusive options | Ambiguous. Does "enable/disable" mean two flags or one boolean? | Use explicit notation: "`--enable` or `--disable`" or "`--flag` (true\|false)." |
| Stub sections ("TODO", "Coming soon", "Future improvement") | Either it's documented or it isn't. Stubs erode trust. | Remove the section entirely. Come back when there's content. |

---

## Rewriting guide: bad → good

### Example 1: Architecture description (CONTEXT.md)

**Bad (current style):**
```
### Domain

A business capability with isolated logic. Each domain (vm, network, image,
kernel, binary, key, host, config, cache, volume, console, logs, cloudinit,
ssh, snapshot, vsock) lives in `internal/core/{domain}/`.
```

**Good (proper prose):**
```
### Domain

A domain is a self-contained business capability with its own logic, data
model, and test suite. Each domain lives in its own directory under
`internal/core/`, named after the capability — for example, `internal/core/vm/`
for VM lifecycle, `internal/core/network/` for networking, and
`internal/core/image/` for image management. The project currently has
sixteen domains, covering everything from SSH keys to snapshots.

Domains are strictly isolated from each other. A domain in `internal/core/vm/`
can never import from `internal/core/network/` or any other domain package.
The Go compiler enforces this rule through circular import detection. This
isolation means each domain can be tested, modified, and replaced independently
without affecting the rest of the system.

What unifies domains is the shared model layer at `internal/lib/model/`. Every
domain imports its types — structs with `db` and `json` tags for SQL and JSON
serialization — from this single package. No domain defines its own model types.
```

**What changed:**
- Added a topic sentence that defines "domain" in human terms
- Added a rationale paragraph explaining WHY the isolation exists
- Added the unifying mechanism (shared model layer)
- Used examples to illustrate, not just dump a comma-separated list
- Connected the rule to its enforcement (Go compiler)

### Example 2: Implementation deep dive (CONSOLE_RELAY.md)

**Bad (current style):**
```
- **Entry:** `mvm run console relay`
- **Purpose:** PTY-to-socket relay for interactive serial console
- **Files:**
  - `internal/service/console/entry.go`
  - `internal/service/console/spawn.go`
  - `internal/service/console/relay.go`
  - `internal/service/console/client.go`
```

**Good (proper prose):**
```
The console relay converts a Firecracker VM's serial console into an
interactive terminal session accessible through `mvm console`. It is
invoked as `mvm run console relay` and runs in the background as a
subprocess for the lifetime of the VM.

When a VM boots with `--console` enabled, the API layer calls
`console.Spawn()` in `internal/service/console/spawn.go`. This launches
the relay subprocess, which opens the PTY master file descriptor and
begins multiplexing data between three endpoints: the PTY device
(in `relay.go`), a Unix socket at
`~/.cache/mvmctl/vms/<vm-id>/console.sock` (for CLI attachment), and
a log file at `~/.cache/mvmctl/vms/<vm-id>/firecracker.console.log`.

The relay uses Go channels rather than `SetDeadline()` for multiplexing.
Data arriving from the PTY is forwarded to the socket; data arriving
from the socket is written to the PTY. A companion goroutine in
`client.go` handles the log file separately. This design avoids the
complexity of deadline-based polling while keeping latency under 1ms.
```

**What changed:**
- Narrates the flow from trigger to execution
- Explains HOW the multiplexing works (channels, not SetDeadline)
- Explains WHY the design was chosen (avoid deadline complexity)
- File references include context about what each file contributes
- The architecture diagram emerged naturally from the prose

### Example 3: Technical reference — command entry (REFERENCES.md)

**Bad (current style):**
```
| Command | Flags | Description |
|---------|-------|-------------|
| `mvm vm create` | --- | Create and start a new Firecracker VM |
```

**Good (proper prose):**
```
### mvm vm create

Creates and starts a new Firecracker microVM. This is the primary command
for running workloads. The VM boots immediately after creation and is
ready for SSH or vsock connections once cloud-init completes.

Syntax:
  mvm vm create NAME [flags]

Flags:
  --image IMAGE        Image name, type:version (e.g. ubuntu:24.04), short ID,
                       or path to .ext4 file. Default: auto-detected.
  --kernel KERNEL      Kernel short ID or path to vmlinux file. Default:
                       auto-detected.
  --vcpu N             vCPU count. Default: from config (typically 1).
  --mem N              Memory in MiB. Default: from config (typically 512).
  --disk-size SIZE     Disk size, e.g. 512M, 20G. Default: from config.
  ...
```

**What changed:**
- Each command gets its own subsection with a narrative description
- Syntax is explicit before the flag table
- Flag descriptions are topic-first: what it does, then the detail
- Default values are explained, not just referenced

### Example 4: Prohibited pattern — "deprecated" language

**Bad (internal memo style):**
```
## Cloud-Init Modes

| Mode | Flag | Description |
|------|------|-------------|
| **inject** | `--cloud-init-mode inject` | Direct injection into rootfs (was previously the fallback) |
```

**Good (proper prose):**
```
## Cloud-Init Modes

| Mode | Flag | Description |
|------|------|-------------|
| **inject** | `--cloud-init-mode inject` | Direct injection of cloud-init files into the root filesystem via loop-mount or guestfs |
```

**What changed:**
- No "previously" or "was" commentary. The doc describes current behavior.
- The description is complete on its own.

### Example 5: Prohibited pattern — "this was validated"

**Bad (internal memo style):**
```
### Provisioner Backend (LoopMount vs GuestFS -- mutual exclusion)

[documentation content]

(This was validated against the actual code in constants.go and provisioner/backend.go. The alias for binary commands was mvm bin not mvm binary.)
```

**Good (proper prose):**
```
### Provisioner Backend (LoopMount vs GuestFS -- mutual exclusion)

[documentation content — no trailing validation note]
```

**What changed:**
- Removed the street-language validation note entirely.
- The doc either is or isn't accurate. It doesn't need a note saying it was checked.
