#!/bin/bash

# Start an AgentEmulator experiment: compile the agent trace into a
# transaction plan and automatically run it on a freshly launched
# BlockEmulator-X cluster. Results land in ./exp/agentemu-results/.
#
# Usage:
#   bash run_agentemu.sh                    # use agentEmuConfig.yaml
#   bash run_agentemu.sh my-config.yaml     # use another config

set -euo pipefail

cd "$(dirname "$0")"

CONFIG="${1:-agentEmuConfig.yaml}"

if [ ! -f "${CONFIG}" ]; then
  echo "config file not found: ${CONFIG}" >&2
  exit 1
fi

# Compile first so a broken build does not wipe the previous results.
go build ./...

# Clean previous outputs: measurement files are created exclusively, and a
# stale agent_registry.json would suppress re-registration of active agents.
rm -rf ./exp

# Run the whole pipeline: trace -> plan -> auto-launched cluster -> results.
# Cluster logs are mirrored to this console and kept under
# exp/agentemu-results/chain/round_001/.
go run cmd/agentemu/main.go -config "${CONFIG}"

echo
echo "agentemu finished; results:"
echo "  plan & action map : exp/agentemu-results/round_001/"
echo "  agent registry    : exp/agentemu-results/agent_registry.json"
echo "  chain measurements: exp/agentemu-results/chain/round_001/results/"
echo "  round summary     : exp/agentemu-results/rounds_summary.json"
