#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
# Fail closed on an unreachable controller. Recover/restart it and retry, or
# explicitly fence it through the supported VM lifecycle before manual cleanup.
for n in 1 2 3; do
  mvm exec "controller-$n" -- systemctl disable --now k8s-api-lease.service
  mvm exec "controller-$n" -- k8s-api-lease release
done
mvm env destroy k8s-policy.yaml
mvm env destroy k8s-worker.yaml
mvm env destroy k8s-controller.yaml
