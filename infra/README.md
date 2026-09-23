# infra/ — deploying to AWS with SAM (phase 5)

`template.yaml` is the AWS version of the pipeline. Each local stand-in from
phases 1–4 maps to a real service:

| Local (phases 1–4)                      | AWS (phase 5)                                            | Code |
|-----------------------------------------|----------------------------------------------------------|------|
| `.icr/dropzone/incoming/*.json`         | `DropZoneBucket` (S3, `incoming/*.json`) → event → SQS   | `awsapp.Worker` reads the object |
| `POST 127.0.0.1:8081/handoffs`           | `WebhookApi` (HTTP API) + `WebhookFunction`              | `cmd/lambda/webhook`, `awsapp.Webhook` |
| `.icr/queue/` (`ingest.DirQueue`)       | `HandoffQueue` (SQS) + `HandoffDLQ` (after 3 receives)     | `ingest.SQSQueue` |
| `cmd/worker` loop                       | `WorkerFunction` (SQS-triggered, partial batch failures) | `cmd/lambda/worker`, `awsapp.Worker` |
| `.icr/items/*.json` (`store.File`)      | `ItemsTable` (DynamoDB, on-demand, PITR)                 | `store.Dynamo` |
| `classify.Mock`                         | Bedrock Converse, IAM scoped to one model                | `classify.Bedrock` |
| `.icr/outbox/*.jsonl`                   | structured `icr_action` log lines in CloudWatch + audit trail | `router.LogSink` |
| `cmd/dashboard` on 127.0.0.1            | `DashboardApi` (HTTP API) + `DashboardFunction`, password-protected, served at `/demos/amberlight/` | `cmd/lambda/dashboard`, `internal/dashboard`, `internal/httplambda` |

The CLI and the local dashboard also work against the deployed table with
`-store dynamo`.

## Before the first deploy (one time)

- AWS CLI v2, SAM CLI and **Go 1.24+** installed on your Mac (`sam build`
  compiles the Lambdas with your local Go via the Makefile). Docker is only
  needed for `sam local`.
- Logged in: `aws sso login --profile demos-admin` (samconfig.toml uses that
  profile and `us-west-2`).
- Bedrock: the Claude Haiku 4.5 inference profile answers a test `converse`
  call in us-west-2.
- The webhook secret exists:
  `aws ssm put-parameter --name /amberlight-icr/webhook-shared-secret --type SecureString --value "$(openssl rand -hex 32)" --profile demos-admin --region us-west-2`
- The demo UI password exists (this is what you share with prospects):
  `aws ssm put-parameter --name /amberlight-icr/dashboard-password --type SecureString --value 'choose-a-demo-password' --profile demos-admin --region us-west-2`
  To change it later, re-run with `--overwrite`; the UI picks it up on its next
  cold start (or redeploy), and everyone is signed out.

## Deploy

From the repo root:

```sh
make test          # everything passes locally first
make sam-validate  # sam validate --lint
make sam-build     # cross-compiles both Lambdas for linux/arm64
cd infra && sam deploy --guided   # first time only: accept the defaults, say Y to save
make sam-deploy    # every deploy after that
make sam-outputs   # WebhookUrl, DropZoneBucketName, HandoffQueueUrl, ItemsTableName
```

`sam deploy --guided` shows a changeset of what it will create and asks before
applying it (`confirm_changeset = true`).

## Hosted demo UI

`make sam-outputs` prints `DashboardUrl` (the direct API Gateway URL) and
`DashboardOrigin`. marian.online proxies `https://marian.online/demos/amberlight/*`
to it with a rewrite in its `vercel.json`, so prospects use your domain:

```json
{ "source": "/demos/amberlight/:path*", "destination": "<DashboardOrigin>/demos/amberlight/:path*" }
```

Every page requires the shared password (SSM `/amberlight-icr/dashboard-password`).
**Each sign-in gets a private sandbox**: a fresh, empty workspace (the 10
example shot handoffs are one click away on the submit page). Visitors only ever see
their own sandbox; handoffs are tagged with the workspace, the session cookie
carries it (signed, so it can't be swapped), and every page/action checks it.
Sandbox items get an `expires_at` 24h out (`ICR_SANDBOX_TTL`): the app hides
them immediately after that and DynamoDB TTL deletes them. Handoffs sent through
the webhook, S3 or CLI have no workspace; they don't expire and don't appear in
any sandbox (see them with `icr status -store dynamo`).
Sessions are signed cookies valid for 12 hours. Submitted handoffs go through the
real pipeline (SQS -> worker Lambda -> Bedrock); the result page refreshes itself
until the readiness verdict lands, usually within a few seconds. Form posts are only
accepted from the dashboard's own origin or `DashboardAllowedOrigins`
(marian.online by default).

## Try it against the deployed stack

```sh
export AWS_PROFILE=demos-admin
OUT=$(aws cloudformation describe-stacks --stack-name amberlight-icr-pipeline --region us-west-2 --query "Stacks[0].Outputs" --output json)
get() { echo "$OUT" | python3 -c "import sys,json;print({o['OutputKey']:o['OutputValue'] for o in json.load(sys.stdin)}['$1'])"; }
export ICR_STORE=dynamo ICR_ITEMS_TABLE=$(get ItemsTableName) ICR_QUEUE_URL=$(get HandoffQueueUrl)
WEBHOOK=$(get WebhookUrl); BUCKET=$(get DropZoneBucketName)
TOKEN=$(aws ssm get-parameter --name /amberlight-icr/webhook-shared-secret --with-decryption --region us-west-2 --query Parameter.Value --output text)

# 1. webhook (API Gateway -> Lambda -> SQS)
curl -sS -X POST "$WEBHOOK" -H "X-Amberlight-Token: $TOKEN" --data-binary @demo/handoffs/04-lighting-buried-missing-asset.json

# 2. drop zone (S3 -> SQS)
aws s3 cp demo/handoffs/05-compositing-final-delivered.json s3://$BUCKET/incoming/ --region us-west-2

# 3. CLI (straight onto SQS)
./bin/icr ingest demo/handoffs

# a few seconds later: real Bedrock readiness verdicts, in DynamoDB
./bin/icr status
./bin/icr outbox
./bin/dashboard -store dynamo      # review queue UI on http://127.0.0.1:8080
```

Worker logs, including the `icr_action` lines, are in CloudWatch:
`sam logs -n WorkerFunction --stack-name amberlight-icr-pipeline --tail`
(run from `infra/`).

## Local Lambda testing (optional, needs Docker)

```sh
cp infra/local-env.example.json infra/local-env.json   # fill in the stack outputs
cd infra
sam local invoke WorkerFunction -e events/sqs-handoff.json
sam local invoke WebhookFunction -e events/webhook-post.json
```

These run the real handlers in a Lambda container but against your deployed
DynamoDB/SQS/Bedrock. The same sample events also run in the unit tests with
fake AWS clients (`internal/awsapp`).

## Cost and cleanup

At demo volume this costs cents: on-demand DynamoDB, SQS, Lambda and HTTP API
are pay-per-request, and each readiness check is one short Haiku call. The
worker's `MaximumConcurrency: 2` caps parallel Bedrock calls. Remove it all
with:

```sh
aws s3 rm s3://$BUCKET --recursive --region us-west-2   # the bucket must be empty first
cd infra && sam delete
```

## Design notes / things to know

- **Webhook auth**: callers send the shared secret in `X-Amberlight-Token`.
  The Lambda reads it from SSM once per cold start and rejects every request
  if it can't (fails closed). Swap for an IAM or JWT authorizer if callers
  support it.
- **Drop zone vs. queue messages**: an S3 notification *is* the SQS message,
  so the worker reads the object and processes it in place. Webhook/CLI
  handoffs arrive already normalized.
- **Retries**: failed records are returned as partial batch failures; after 3
  receives SQS moves them to the DLQ. Invalid payloads are logged and dropped
  (retrying can't fix them). Re-deliveries never repeat actions.
- **Model**: `BedrockInferenceProfileId` / `BedrockFoundationModelId` default
  to Claude Haiku 4.5; the IAM statement is scoped to exactly those ARNs.
- **Bucket name** is fixed (`<stack>-dropzone-<account>`) to avoid a circular
  dependency between the bucket notification and the queue policy.
- **Concurrency**: `ScalingConfig.MaximumConcurrency: 2` caps worker
  concurrency without reserving account concurrency, which fails on accounts
  with a low Lambda concurrency quota.
- **Stage lookup** happens in the worker (router), from
  `pipeline.stage_sequence` in `config/amberlight.yaml`. Changing the studio's
  pipeline order is a config change + redeploy, not a prompt change.
- **Actions** are still stubs in AWS: they are logged as structured
  `icr_action` lines and recorded in each item's audit trail. Real Slack /
  production-tracker clients plug in behind `router.ActionSink`.
