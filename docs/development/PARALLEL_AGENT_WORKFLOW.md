# Parallel Agent Workflow

Use this protocol only when the user authorizes parallel agents or the active task is explicitly divided across
agents. It governs delegation and integration; `AGENTS.md`, `CONTEXT.md`, accepted ADRs, `tasks/plan.md`,
`tasks/todo.md`, commits, and live repository state remain authoritative.

## Head-agent loop

1. Inspect the main worktree, branch, recent commits, active plan, dirty files, worktrees, and live agent handles.
   Completion criterion: every proposed assignment has a current dependency, an exclusive owner, and a concrete
   output that advances the active goal.
2. Assign independent work only. Use one writer in a shared worktree or one persistent isolated worktree per concurrent
   writer. Read-only reviewers may inspect a stable commit while a writer owns later uncommitted changes. Completion
   criterion: no two writers own overlapping files or the same effect boundary.
3. Give each agent a bounded work packet containing the worktree, fixed commit or scope, required project references,
   exact deliverable, prohibited side effects, verification gate, and stop condition. Completion criterion: the packet
   can be accepted or rejected from repository evidence without relying on the agent's narrative.
4. Do useful local integration work while agents run. Check live state after each bounded local step, on every agent
   message, and before opening another assignment. During a long command, retain and poll its real process handle;
   do not create replacement work from a timeout alone.
5. Review each ready implementation commit once, tests first. Inspect its fixed implementation diff, interfaces,
   imports, effect boundaries, failure paths, ownership transfer, and cleanup. Run one focused evidence pass.
   Completion criterion: the commit is approved or has one severity-ranked correction request with exact evidence.
   After that correction, inspect only the correction delta, the previously failed invariants, and their regression
   tests. Repeat the full implementation review only when the correction materially changes architecture, a public
   interface, or a security boundary; record that reason before expanding the review.
6. Integrate one approved slice at a time. Re-run focused checks on the integration branch, then record user-visible
   behavior and remaining limitations in a separate documentation commit. Completion criterion: the main branch is a
   working save point and unrelated user changes are unchanged.

## Usefulness monitor

An agent is useful only when its live handle and repository evidence agree. Acceptable evidence is a new scoped diff,
commit, test result, reproduced blocker, or decision-changing review finding.

- Inspect the live agent tree and its assigned worktree together. Agent listings can omit a still-running worker.
  Before reassigning an existing worktree, explicitly stop or interrupt every previously named owner and confirm its
  prior status; when that cannot be confirmed, give the replacement a fresh worktree.
- A writer with no new evidence and no confirmed live command receives one concise status request. If the next check
  is still unchanged, stop it and reuse the slot for a non-overlapping dependency.
- Stop an agent immediately when it duplicates another owner, edits outside its assignment, repeats an unchanged
  command, broadens scope, or continues reviewing after a release blocker is established.
- Do not spawn reviewers for a moving uncommitted target. Review a fixed commit; send one correction round to its
  owner; perform final sign-off after the correction.
- Parallelism has no fixed agent-count target or cap. Open every independent stream whose output can integrate without
  waiting on another active writer; close a stream when that independence or value disappears. Agent count is not a
  token-control mechanism.

## Context and token control

Control context and repeated work rather than useful parallelism:

- Give each agent the smallest self-contained packet: exact worktree, fixed base or commit, owned files/effect
  boundary, relevant references, deliverable, verification gate, and stop condition. Use minimal-history forks; a full
  conversation fork requires a concrete dependency on that history.
- Require evidence-dense reports: commit hash, changed-file scope, test commands/results, actionable findings, and an
  exact blocker when present. Do not request narrative progress histories or repeated repository summaries.
- Monitor live handle and worktree evidence at bounded checkpoints. One unchanged checkpoint receives one concise
  status request; a second unchanged checkpoint without a confirmed live command ends the assignment.
- Assign distinct review axes only when each can change the integration decision. Consolidate overlapping findings
  into one correction packet, then perform one correction-delta verification instead of another full audit.
- Spend broad test and review work once per coherent integration milestone. Focused package evidence is sufficient for
  an isolated commit; unchanged broad checks are not repeated.

## Circuit breaker

Each implementation gets one fixed-diff review pass. Stop at the first Critical blocker and consolidate the completed
review axes into one bounded correction packet. The correction receives delta-only verification of the changed lines,
affected invariants, and targeted regression tests. A repeated blocker returns the slice to dependency planning rather
than another review loop. Broad CI waits for a coherent integration checkpoint.

Never rerun an unchanged failing command. Change the hypothesis, command, code, or environment first. A quota,
missing generated asset, sandbox denial, or expired observation is evidence to classify, not a reason to spin.

## Worktree durability

Create agent worktrees under a persistent project-owned location such as ignored `dist/worktrees/`. Do not use `/tmp`
for project worktrees, Go build/test caches, Python test roots, or other project-generated bulk data. The host may apply
a per-user quota to `/tmp` independently of filesystem free space, and exhausting it also prevents the agent sandbox
from creating its small internal mount markers.

Before a writer runs any command, create a task-specific ignored directory beneath that writer's worktree and set
`TMPDIR`, `GOTMPDIR`, and `GOCACHE` to descendants of it for every Go command. Give Python/pytest an equivalent
worktree-local base temporary directory. Do not share writable cache directories between concurrent workers. `/tmp`
is reserved for small platform-owned disposable files that the workflow cannot redirect. A worktree commit is durable
in the main repository object database; unstaged, staged, and untracked files are not.

When inheriting a live `/tmp` worktree, preserve its unique state under ignored `dist/agent-snapshots/` before a long
command or ownership handoff. Capture tracked and staged binary patches separately, archive every untracked file, and
verify that the patches parse and the archives list the expected paths. Commit a coherent verified chunk promptly;
snapshots are crash recovery, not integration evidence.

## Integration sign-off

The head agent signs off; agent self-approval is insufficient. Before cherry-picking or committing, prove:

- the diff matches the assigned requirement and accepted ADRs;
- tests exercise the claimed behavior and important failure paths;
- focused format, vet, and test gates pass for Go changes;
- privileged effects consume typed, validated authority and clean up partial progress;
- the commit remains a working save point rather than activating a half-wired runtime path;
- no user-owned staged, unstaged, or untracked file is included;
- documentation states what is active, what is only a foundation, and what remains blocked.

Full CI and system qualification remain release gates; a focused sign-off never substitutes for them.

## Compaction recovery

After context compaction or leadership handoff:

1. Re-read `AGENTS.md` and this file.
2. Read the active entries in `tasks/plan.md` and `tasks/todo.md`, then inspect the current branch, log, status, and
   worktree list.
3. Query live agent handles and reconcile them with every named writer in the compacted summary. An absent handle is
   inconclusive: explicitly stop or interrupt the previous owner before reusing its worktree.
4. For each remaining live writer, inspect its current diff or commit and confirm exclusive ownership before sending
   follow-up work. For each running command, resume its existing handle.
5. State the next integration checkpoint and continue from repository evidence. Do not recreate completed work or
   start a broad audit merely because conversational detail was compacted.

Keep prompts and reports evidence-dense: paths, commit hashes, commands, findings, and remaining dependency. Avoid
duplicated context, speculative fan-out, repeated summaries, and review narration that does not change the next action.
