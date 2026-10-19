# Environment Workflow Engine — `mvm env`

An environment spec provisions resources in dependency order and records their identities for reapply and teardown.
Use the [environment reference](../ENV_SPEC_REFERENCE.md) for all version-1 fields and examples.

## Operator workflow

1. Complete host installation and operator setup, then write a version-`"1"` spec.
2. Declare each VM as a separate named step. Add volumes and policies explicitly.
3. Use `@type:name` references and `depends_on` paths for every referenced prerequisite.
4. Add a `ready` step after guest provisioning and make consumers depend on it.
5. Run `mvm env apply spec.yaml`; inspect saved environments with `mvm env ls`.
6. Use `mvm env diff spec.yaml` to compare declarations with saved state.
7. Run `mvm env destroy <workflow-id>` when finished. Retained data remains.

Apply accepts local paths and HTTP(S) URLs. Apply and destroy discover `mvmctl.yaml`, then `mvmctl.yml`, when no
argument is given. `up` and `down` alias apply and destroy. `--env KEY=VALUE` overrides exec-step environment
variables; it does not interpolate YAML or change SSH steps.

## Dependencies and references

Sections are maps keyed by step name. A `name` override changes the resource name, not its reference.
For example, `@vm:database` resolves the recorded VM identity even when that step creates a VM named `db-prod`.

A reference does not add a dependency. The referenced step must be reachable through `depends_on`, directly or
transitively. Independent steps may execute concurrently. Consumers of a readiness check must depend on
`@ready:<name>`, not merely the VM.

Validation rejects unknown sections/fields, unsupported versions, malformed or missing references, missing
dependency paths, self-dependencies, cycles, and VM counts other than one before effects. Nested environment and CPU
configuration maps retain arbitrary keys. Only resource-reference fields are interpreted; commands, local paths,
and literal values retain their `@` characters.

## Resource and command behavior

| Steps | Apply | Destroy |
|-------|-------|---------|
| `network`, `key`, `vm` | Acquire and record identity; ensure that identity on reapply | Remove only recorded workflow-created resources |
| `volume` | Create raw data storage or explicitly select existing storage | Preserve by default; remove owned disposable volumes after dependent VMs |
| `traffic_policy`, `exec_policy` | Create a typed grant or select an existing policy by ID | Revoke only the exact owned grant, before endpoints |
| `image`, `image_import`, `kernel`, `binary` | Acquire shared assets and record identity | Preserve shared assets |
| `exec`, `ssh`, `copy`, `ready` | Rerun on every apply | No undo of guest commands or copied files |

Reapply does not silently update an owned resource declaration or replace its recorded identity. Changed owned
declarations fail with recovery guidance. Resolve the conflict or deliberately tear down and recreate the affected
environment after preserving needed data. A new object with the old name never inherits ownership.

`volume.retain` defaults to `true`. Declare `retain: false` when creating disposable data. An `existing` selector
does not adopt a resource, even if deletion is requested. Raw disks still need guest formatting/mounting.
To retire retained data after teardown, preserve anything needed, run `mvm volume rm <volume-id>` with the exact
saved volume ID, then rerun `mvm env destroy <workflow-id>` to clear its absent state entry.

Traffic grants use either a source network or a source VM, a destination VM, TCP/UDP, and a port or inclusive port range. Same-network flows
use the same policy boundary. Exec grants are directional and require an explicit non-root target user; they neither
create that guest user nor turn on `exec.allow_vm_exec`. No step grants host execution. Selecting an existing policy
also requires its matching declaration: source, destination, protocol and port/range for traffic, or source,
destination and target user for exec. The selected policy remains unowned and is preserved on destroy.

`ready` runs a guest command through the vsock agent until it succeeds or its total deadline expires. `timeout`
defaults to 60 seconds and `interval` to one second; both must be positive. The deadline covers probes, commands,
and waits. Cancellation stops the check. Ordinary exec and SSH timeout semantics are unchanged.

## Cleanup and recovery

Destroy uses saved state and reverse dependency order. Policies precede endpoints; VMs precede their disposable
volumes. Cached assets, retained volumes, and pre-existing resources survive. State binds creation ownership to
the exact resource identity, including policy incarnation. Policy removal checks a durable workflow claim and
incarnation atomically; the claim narrows cleanup eligibility and does not create new authority.

Kernels remain retained. A kernel reference in `removes` is rejected before effects; kernel retirement is a separate
kernel CLI operation.

`removes` runs only after the entire pipeline succeeds. It follows the same ownership and retention rules and
cannot free resources for another step during that pipeline. `ephemeral: true` attempts cleanup after success
or failure. Cleanup errors are returned alongside an original apply error; failed cleanup retains retry state.

Resource identities are persisted after acquisition. Persistence failure is an error, not successful apply.
Saved state supports retry but cannot make external API effects and local state writes one transaction. Keep
state when investigating failures and retry cleanup after resolving the reported cause. Do not delete state to
silence an ownership or identity conflict.

`diff` compares declarations with saved state. It is not a live configuration drift check. Imperative steps and
readiness rerun after partial apply as well as ordinary reapply, so choose repeatable commands.

## Architecture and state

The CLI calls `Apply`, `Destroy`, `List`, and `Diff` in `internal/workflow/env/`. This package consumes typed
`pkg/api` interfaces; it does not import core domains. `internal/lib/workflow/` owns the generic DAG,
shared state, and persistence. Resource factories register spec and saved-state reconstruction in `factory.go`.

State lives beneath the active cache at `workflows/<workflow-id>/state.yaml`, normally
`~/.cache/mvmctl/workflows/`. Each entry records its step name/type, dependencies, declaration, output identity,
and metadata including `was_created` and `spec_hash`. Saved-state reconstruction permits destroy without the
original spec; the corresponding host resources and authority must still be available.

Workflow IDs derive from the absolute spec path. Moving a spec changes its identity. URL strings use the same
path-based handling, so retain the workflow ID from `env ls` for unambiguous subsequent selection.
