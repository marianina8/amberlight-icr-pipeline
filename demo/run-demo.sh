#!/usr/bin/env bash
# One-shot local demo: reset the local data dir, ingest every synthetic shot
# handoff, process them, and show where each one went. No network, no AWS.
#
# This runs the naive KEYWORD mock classifier, so several verdicts are wrong
# on purpose (see compare/ for the side-by-side with Bedrock). Pass
# CLASSIFIER=bedrock to use the real model with your AWS credentials.
set -euo pipefail
cd "$(dirname "$0")/.."

DATA="${ICR_DATA_DIR:-.icr}"
CLASSIFIER="${CLASSIFIER:-mock}"
ICR="./bin/icr"
[ -x "$ICR" ] || go build -o bin/icr ./cmd/cli

echo "== Amberlight ICR demo ($CLASSIFIER classifier, synthetic shot handoffs) =="
"$ICR" -data "$DATA" reset -yes >/dev/null 2>&1 || true

echo
echo "-- 1. The stage order (the router looks the next stage up here; the model never picks it)"
"$ICR" -data "$DATA" stages

echo
echo "-- 2. Ingest 10 shot handoffs and process them"
"$ICR" -data "$DATA" -classifier "$CLASSIFIER" ingest -process demo/handoffs

echo
echo "-- 3. Where did everything go?"
"$ICR" -data "$DATA" status

echo
echo "-- 4. Automatic actions (stubbed Slack / production tracker), each with the reason it fired"
"$ICR" -data "$DATA" outbox

echo
echo "-- 5. Everything waiting on a supervisor or coordinator"
"$ICR" -data "$DATA" status -review

cat <<'EOF2'

Next:
  make dashboard                      # review queue UI at http://127.0.0.1:8080
  ./bin/icr status <ID>               # one handoff's full audit trail
  ./bin/icr review approve <ID> -reviewer you -note "looks right"
  ./bin/icr review override <ID> -reviewer you -readiness blocked -note "rig still broken"
  ./bin/icr mcp                       # the same pipeline as MCP tools (read-only)
  go run ./compare                    # naive keywords vs intended outcome (add -bedrock for the model)
EOF2
