#!/bin/bash
set -euo pipefail
DIR=$(cd "$(dirname "$0")" && pwd)
"$DIR/kubeconfig.sh"
exec kubectl --kubeconfig="$DIR/.kubeconfig" "$@"
