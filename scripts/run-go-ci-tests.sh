#!/usr/bin/env bash
set -euo pipefail

# The full Jailer suite exercises fixed root-owned authority and runs locally
# inside a disposable test VM. CI still runs its unprivileged contracts.
module_path="$(go list -m)"
all_packages="$(go list ./...)"
mapfile -t packages < <(printf '%s\n' "$all_packages" | grep -Fvx "$module_path/internal/service/jailer")
if ((${#packages[@]} == 0)); then
  printf 'no unprivileged Go packages found\n' >&2
  exit 1
fi

go test "${packages[@]}" -count=1 -coverprofile=coverage.out -covermode=atomic

# Route only the named root-owned lifecycle families to the local full-package run.
# New Jailer tests run in CI by default, so an unclassified root effect fails visibly.
local_jailer_tests='^Test('
local_jailer_tests+='FreshRecovery(Active|Launching|Missing|Membership|Final|Same|Admission|Cleaning|'
local_jailer_tests+='Cleanup(Reopens|Absent|Rejects))|'
local_jailer_tests+='Launch(Abort|Managed|Release|Relay|Final|Lease)|AbortCleanup|'
local_jailer_tests+='InstanceAuthority(ReleaseSlotRegisters|CleanupAndSameOwner|RejectsForeign|CrossUID|'
local_jailer_tests+='ReleaseSlotReferenceCheckTracks|RegisterLockOrder|UsesOnlyFixedRootPathOpen|IndexReleaseAfter|'
local_jailer_tests+='PostRename)|'
local_jailer_tests+='Cleanup(LeaseCopies|WaitFailure|CompleteNilContext|LeaseDoesNotCommit)|'
local_jailer_tests+='PreparedRelease(TransfersToSuccessfulLaunch|SerializesReleaseAgainstLaunchingTransfer|'
local_jailer_tests+='RegistrationFailuresBeforeRenameRetainAuthority)|'
local_jailer_tests+='PersistentOutput|BeginFreshRecoveryCleanup(Verifies|BootMismatch)|'
local_jailer_tests+='ActivateVerifiedProcess|ActiveCleanup)'
go test ./internal/service/jailer \
  -skip "$local_jailer_tests" \
  -count=1 -coverprofile=coverage-jailer.out -covermode=atomic
