#!/usr/bin/env bash
# Post one synthetic shot handoff to the local webhook receiver (the stand-in
# for API Gateway + the webhook Lambda). Start the worker first: make worker
set -euo pipefail
cd "$(dirname "$0")/.."
URL="${ICR_WEBHOOK_URL:-http://127.0.0.1:8081/handoffs}"
FILE="${1:-demo/handoffs/04-lighting-buried-missing-asset.json}"
curl -sS -X POST -H 'Content-Type: application/json' --data-binary @"$FILE" "$URL"
echo
