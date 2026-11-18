#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
command -v jq >/dev/null
mvm env apply k8s-controller.yaml
mvm env apply k8s-worker.yaml
mvm env apply k8s-policy.yaml
# Root-backed inspection resolves the reservation's immutable identity. Exec
# strings in env YAML do not interpolate resource outputs.
id=$(mvm floating-ip --network k8s inspect k8s-api --json | jq -er .id)
[[ $id =~ ^[0-9a-f]{32}$ ]] || { echo 'Invalid floating IP identity' >&2; exit 1; }
for n in 1 2 3; do
  mvm exec "controller-$n" -- hostnamectl set-hostname "controller-$n"
  mvm exec "controller-$n" -- k8s-api-lease-setup "$id" "10.10.0.$((9+n))"
done
# All lease loops must be running before init: kubeadm uses the VIP immediately.
mvm exec controller-1 -- timeout 300 k8s-controller-primary
for n in 2 3; do
  mvm exec controller-1 -- k8s-seed-controller-token
  mvm exec --allow-vm-exec "controller-$n" -- timeout 180 k8s-join controller-1 join-controller
done
mvm exec controller-1 -- k8s-cilium
mvm exec controller-1 -- k8s-seed-token
for n in 1 2 3; do
  mvm exec "worker-$n" -- hostnamectl set-hostname "worker-$n"
  mvm exec --allow-vm-exec "worker-$n" -- timeout 180 k8s-join controller-1 join-worker
done
