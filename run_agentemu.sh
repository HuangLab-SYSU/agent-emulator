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
# exp/agentemu-results/round_001/chain/logs/.
go run cmd/agentemu/main.go -config "${CONFIG}"

echo
echo "agentemu finished; results:"
echo "  plan & action map : exp/agentemu-results/round_001/"
echo "  agent csvs        : exp/agentemu-results/round_001/agents/"
echo "  agent registry    : exp/agentemu-results/agent_registry.json"
echo "  chain measurements: exp/agentemu-results/round_001/chain/results/"
echo "  round summary     : exp/agentemu-results/rounds_summary.json"

# Post-experiment figures: plot agent balances from the latest round, build an
# HTML gallery and open it in the default browser. A failed experiment aborts
# earlier via set -e, so this only runs on success.
FIGS_CODE="./figs/python_code"
FIGS_OUT="./figs/figs_results"

latest_round=""
for d in exp/agentemu-results/round_*/; do
  [ -d "$d" ] && latest_round="$d"
done
agents_dir="${latest_round}agents"

if [ -n "${latest_round}" ] && ls "${agents_dir}"/agent-*.csv >/dev/null 2>&1; then
  mkdir -p "${FIGS_OUT}"
  # Drop figures from the previous run so the gallery never mixes runs.
  rm -f "${FIGS_OUT}"/*.png "${FIGS_OUT}"/index.html
  python3 "${FIGS_CODE}/plot_agent_balance.py" --data-dir "${agents_dir}" --fig-dir "${FIGS_OUT}"
  python3 "${FIGS_CODE}/build_fig_html.py" --fig-dir "${FIGS_OUT}" --data-dir "${agents_dir}"
  echo "  figures & gallery : ${FIGS_OUT}/index.html"
  if command -v open >/dev/null 2>&1; then
    open "${FIGS_OUT}/index.html"
  elif command -v xdg-open >/dev/null 2>&1; then
    xdg-open "${FIGS_OUT}/index.html"
  fi
else
  echo "warn: no agent CSVs under ${agents_dir:-exp/agentemu-results}; skip plotting" >&2
fi
