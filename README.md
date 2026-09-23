# amberlight-icr-pipeline

An **Ingest → Classify → Route** pipeline for shot handoff triage in an
animation studio, built in Go with an AWS SAM deployment target. It's an
instance of the ICR reference architecture ([ARCHITECTURE.md](ARCHITECTURE.md)),
and the same underlying system as the support-ticket demo
([rivergate-icr-pipeline](https://github.com/marianina8/rivergate-icr-pipeline)),
re-pointed at animation production by changing only the prompt, the routing
rules and the pipeline's stage order.

> **Amberlight Animation is fictional.** It's a made-up mid-size episodic
> animation studio (~280 employees). Every shot, artist, show and episode in
> this repo is synthetic. None of it refers to a real studio, production or
> person.

## The problem

Shots move through a fixed pipeline: **storyboard → layout → animation →
lighting → compositing**. When an artist finishes their stage they leave a
handoff note, and a coordinator reads every one by hand to decide whether the
shot moves on, goes back for revision, or is blocked. That's the step that
breaks first as a show scales up. This pipeline:

1. normalizes each handoff (`shot_id`, `current_stage`, `artist`, `episode`,
   `notes`) and queues it,
2. asks the model **one** question about the notes: is the shot
   `ready_for_next_stage`, `needs_revision` or `blocked`? (with one to two
   sentences of reasoning and a confidence score),
3. **looks up** the next stage from the configured stage order. The model never
   picks the stage ([why](ARCHITECTURE.md#design-point-stage-sequencing-is-configuration-not-a-model-decision)),
4. routes by configurable rules: blocked → supervisor, unsure → coordinator,
   confidently ready → the next stage's queue, needs revision → back to the
   artist with the reasoning attached,
5. records every step in an audit trail (what the model said, which rule
   fired, current and next stage), and exposes all of it to AI agents through
   **MCP**, with write access that is opt-in per tool.

## Status

| Phase | What | State |
|---|---|---|
| 1 | Local-first MVP: CLI + readiness classifier (mock + Bedrock behind an interface), local store, tests | ✅ done |
| 2 | Worker + queue against local stand-ins for S3 (drop-zone folder), API Gateway (webhook on :8081) and SQS (directory queue) | ✅ done |
| 3 | Stage lookup + router rules engine + human-review dashboard (approve/override) | ✅ done |
| 4 | MCP server: read tools always on, write tools opt-in and scoped | ✅ done |
| 5 | Real AWS via SAM: Lambda handlers, DynamoDB store, SQS queue, Bedrock classifier | see [infra/README.md](infra/README.md) |
| + | Hosted, password-protected demo UI: submit a handoff, watch it get checked and routed, review the queue. Each visitor gets a private sandbox that expires after 24h | served at `marian.online/demos/amberlight/` |
| + | `compare/`: naive keyword rules vs Bedrock on the same fixtures | ✅ done |

Locally, everything runs with **zero AWS calls**: the default classifier is a
deterministic keyword mock that stands in for Bedrock. It is deliberately
naive, so several demo verdicts are wrong locally (see `compare/`). Against
AWS, the same pipeline uses Bedrock (Claude Haiku 4.5), DynamoDB and SQS.

## Quick start

Requires Go 1.24+.

```sh
make test            # all unit tests; no network, no AWS
make demo            # reset .icr/, ingest demo/handoffs, show stages + routing + outbox + review queue
make dashboard       # http://127.0.0.1:8080 — submit handoffs, see results, review queue
make compare         # naive keyword rules vs intended outcomes (BEDROCK=1 adds the live model)
```

Run the pipeline with its local entry points:

```sh
make worker                                                            # drop zone + webhook + queue consumer
cp demo/handoffs/05-compositing-final-delivered.json .icr/dropzone/incoming/   # "S3" drop
./demo/webhook-example.sh demo/handoffs/04-lighting-buried-missing-asset.json  # "API Gateway" POST
./bin/icr status
```

## Deploy to AWS

```sh
aws sso login --profile demos-admin
make sam-validate && make sam-build
cd infra && sam deploy --guided     # first time; afterwards: make sam-deploy
```

Full runbook, including how to send handoffs to the deployed stack and point
the CLI and dashboard at it: [infra/README.md](infra/README.md).

## CLI

```
icr ingest [-process] <file|dir|->...   normalize handoffs and enqueue them
icr classify -file <handoff.json>       preview readiness + stage lookup + routing (dry run, no writes)
icr classify <id>                       re-run the readiness check on a stored item and re-route it
icr route [-dry-run] <id>               (re-)apply routing rules to a classified item
icr status [-queue q] [-review] [id]    summary, filtered list, or one item's audit trail
icr review approve <id> -reviewer NAME [-note TEXT]
icr review override <id> -reviewer NAME -readiness R [-note TEXT]
icr stages                              the configured stage order and each stage's next stage
icr outbox                              stubbed Slack posts / production-tracker updates, with reasons
icr mcp [-allow-write tools]            serve the pipeline as MCP tools on stdio
```

Global flags: `-config` (default `config/amberlight.yaml`), `-data` (default
`.icr`), `-classifier mock|bedrock`, `-json`. Each also has an environment
variable: `ICR_CONFIG`, `ICR_DATA_DIR`, `ICR_CLASSIFIER`.

Against the deployed stack, add `-store dynamo -table <ItemsTableName>
-queue-url <HandoffQueueUrl> -profile demos-admin` (or set `ICR_STORE`,
`ICR_ITEMS_TABLE`, `ICR_QUEUE_URL`, `AWS_PROFILE`). The dashboard takes the same
`-store dynamo -table … -profile …` flags.

## MCP (agent layer)

`icr mcp` speaks MCP over stdio (JSON-RPC 2.0, protocol revisions 2024-11-05
through 2025-06-18).

| Tool | Kind | Available |
|---|---|---|
| `status`, `get_item`, `list_queue` | read | always |
| `route_item`, `reclassify`, `approve` | write | only when named in `-allow-write` (no `all` shortcut) |

- Write tools that aren't enabled are hidden from `tools/list`. Calling one
  anyway returns an error.
- Write calls are recorded in the audit trail as `mcp:<actor>`.
- `route_item` runs the same rules and stage lookup as everything else. An
  agent can't push a low-confidence shot past the coordinator, a blocked shot
  past the supervisor, or a shot to any stage other than the next one.
- `approve` (approve or override readiness) requires a note, and should only
  be enabled for an agent acting on a human reviewer's instructions.

See `demo/mcp-client-config.example.json` for a desktop-client config.

## What was customized for Amberlight

Only the per-prospect inputs changed. Everything else is the generic ICR
pipeline.

1. **Classification prompt**: `config/amberlight.yaml` → `taxonomy` + `prompt`.
   The output is `{readiness, reasoning, confidence}`. Readiness is one of
   `ready_for_next_stage`, `needs_revision`, `blocked`. There is no
   `target_stage` field. The prompt tells the model to read for what's
   actually true (sarcasm, hedging, a problem buried mid-sentence), not for
   keywords.
2. **Routing rules + stage order**: `config/amberlight.yaml` → `pipeline` and
   `routing`. Rules are evaluated top to bottom, first match wins:

   | Rule | When | Then |
   |---|---|---|
   | blocked-escalate | blocked, *any* confidence | notify `#amberlight-supervisors` → `supervisor-review` |
   | low-confidence | confidence < 0.6 (not blocked) | → `coordinator-review` |
   | auto-advance | ready ≥ 0.75 | tracker update + notify `#amberlight-<next>` → the looked-up next stage's queue |
   | return-to-artist | needs_revision ≥ 0.6 | notify the artist with the reasoning → `artist:<name>`; the shot stays at its stage |
   | *(default)* | anything else, e.g. ready at 0.60–0.74 | → `coordinator-review` |

3. **Demo fixtures**: `demo/handoffs/`, 10 synthetic handoffs across every
   stage and every outcome, with deliberately varied phrasing. Intended
   outcomes are in `demo/expected.json`:

   | Fixture | Stage | Note style | Intended |
   |---|---|---|---|
   | 01 boards locked | storyboard | direct | ready → advance to layout |
   | 02 dolly-in still fast | layout | hedged | needs revision → artist |
   | 03 "totally ready" | animation | sarcastic | needs revision → artist |
   | 04 lovely, except the far wall… | lighting | issue buried mid-sentence | blocked → supervisor |
   | 05 comp v12 approved | compositing | direct, **last stage** | ready → **delivered** |
   | 06 face rig crashes | animation | direct | blocked → supervisor |
   | 07 "did what we talked about. lmk" | layout | vague | → coordinator (low confidence) |
   | 08 "Approved!" but redo the eyeline | animation | cheerful, buried change | needs revision → artist |
   | 09 "in the bag" | lighting | casual, no ready keywords | ready → advance to compositing |
   | 10 boards done, director must pick | storyboard | says "done" | blocked → supervisor |

   `internal/pipeline` tests pin where each fixture lands given the intended
   verdict; `internal/router` tests pin every threshold and the stage lookup
   for every stage, including the end of the sequence.

## Naive rules vs the model (`compare/`)

`go run ./compare` runs a short keyword-rules classifier (the kind of rules a
studio might script first) over the fixtures and routes its verdicts through
the same router. Offline, it already shows the problem: it routes 5 of 10
fixtures differently than intended, and **auto-advances 3 shots that should not
move** (the sarcastic "ready", the "Approved!" that still needs a redo, and the
"done" storyboard waiting on a director). `go run ./compare -bedrock -profile
demos-admin` adds a live Bedrock column; the latest run is saved in
[compare/RESULTS.md](compare/RESULTS.md).

## Safety rules (enforced in code and tests)

- Synthetic data only. Fixtures use invented shows, shots and names.
- Low confidence always defers to a human. A classifier error, an invalid
  label or malformed model output becomes `needs_revision @ 0.0` and goes to
  `coordinator-review`. It is never a guess, and never an auto-advance.
- Blocked shots always escalate to a supervisor, whatever the confidence.
- The model cannot choose a stage. Stage order is config; the next stage is a
  lookup; unknown stages are rejected at ingest.
- Every automatic action is logged with the readiness, reasoning, rule and
  current/next stage that triggered it, in the item's audit trail and the
  outbox record.
- Actions are idempotent per item. Re-routing, reclassifying or approving
  never notifies twice.
- MCP write tools are opt-in, scoped per tool, and attributed.
- The model prompt treats handoff notes as untrusted data. The dashboard binds
  to localhost and rejects cross-origin form posts.

## Layout

```
cmd/cli          icr binary: ingest, classify, route, status, review, stages, outbox, mcp
cmd/worker       queue consumer + local drop zone + local webhook receiver
cmd/dashboard    local web UI (submit, results, review queue); UI code in internal/dashboard
cmd/lambda/dashboard  the same UI hosted on Lambda, behind a shared password
cmd/lambda/worker   SQS-triggered Lambda
cmd/lambda/webhook  API Gateway webhook Lambda
internal/classify  Classifier interface, prompt templates, Mock, Bedrock (Converse API)
internal/store     item state + audit trail: Memory, File, DynamoDB
internal/router    stage lookup + rules engine + action sinks (stub outbox, CloudWatch log sink)
internal/mcp       MCP stdio server over the pipeline
internal/ingest    handoff normalization + local SQS/S3/API Gateway stand-ins
internal/pipeline  the shared lifecycle: normalize → classify → store → route → act
internal/config    loads + validates config/amberlight.yaml
internal/awsapp    AWS wiring + Lambda handlers (DynamoDB, SQS, S3, SSM, Bedrock)
internal/dashboard web UI: submit page, results, review queue, password login
internal/httplambda  runs a net/http handler behind API Gateway HTTP APIs
config/          the per-prospect inputs (prompt, taxonomy, stage order, rules)
compare/         naive keyword rules vs Bedrock on the fixtures
infra/           SAM template, samconfig, sample Lambda events, deploy runbook
demo/            synthetic handoffs + intended outcomes, demo scripts, MCP client config example
```
