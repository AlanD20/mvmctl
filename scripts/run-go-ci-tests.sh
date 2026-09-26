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
go test ./internal/service/jailer \
  -skip '^Test(FreshRecovery|Launch|AbortCleanup|InstanceAuthority|Cleanup|PreparedRelease|PersistentOutput|BeginFreshRecovery|ActivateVerifiedProcess|ActiveCleanup)' \
  -count=1 -coverprofile=coverage-jailer.out -covermode=atomic
