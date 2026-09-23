#!/usr/bin/env python3
"""Add the Amberlight demo to marian.online (edits files in place, idempotent).

Usage (from anywhere):
  python3 add-amberlight-to-marian-online.py <path-to-marian.online> <DashboardOrigin>

<DashboardOrigin> is the `DashboardOrigin` output of the amberlight-icr-pipeline
stack, e.g. https://abc123.execute-api.us-west-2.amazonaws.com

What it changes:
  - vercel.json: three /demos/amberlight rewrites (same shape as Rivergate's),
    placed before the SPA fallback.
  - src/pages/DemosPage.tsx: an optional `repoHref` on each demo, a GitHub link
    on every live demo card, Rivergate's repo link, and the Amberlight entry
    directly below Rivergate (same layout, badge, sections and stack chips).
"""
import json
import re
import sys
from pathlib import Path

if len(sys.argv) != 3 or not sys.argv[2].startswith("https://"):
    sys.exit(__doc__)
site, origin = Path(sys.argv[1]), sys.argv[2].rstrip("/")

# ---- vercel.json -------------------------------------------------------------
vp = site / "vercel.json"
v = json.loads(vp.read_text())
rewrites = [r for r in v["rewrites"] if not r["source"].startswith("/demos/amberlight")]
fallback = next(i for i, r in enumerate(rewrites) if r["source"] == "/(.*)")
rewrites[fallback:fallback] = [
    {"source": "/demos/amberlight", "destination": f"{origin}/demos/amberlight/"},
    {"source": "/demos/amberlight/", "destination": f"{origin}/demos/amberlight/"},
    {"source": "/demos/amberlight/:path+", "destination": f"{origin}/demos/amberlight/:path+"},
]
v["rewrites"] = rewrites
vp.write_text(json.dumps(v, indent=2) + "\n")
print("vercel.json: amberlight rewrites ->", origin)

# ---- DemosPage.tsx -----------------------------------------------------------
dp = site / "src" / "pages" / "DemosPage.tsx"
s = dp.read_text()

def sub(old, new, count=1):
    global s
    if new in s:
        return  # already applied
    if old not in s:
        sys.exit(f"DemosPage.tsx: expected text not found:\n{old}")
    s = s.replace(old, new, count)

# 1. optional repo link on the Demo type
sub("  launchHref?: string\n}", "  launchHref?: string\n  repoHref?: string\n}")

# 2. Rivergate's repo link
sub("    launchHref: '/demos/rivergate/',\n  },",
    "    launchHref: '/demos/rivergate/',\n    repoHref: 'https://github.com/marianina8/rivergate-icr-pipeline',\n  },")

# 3. Amberlight entry, directly below Rivergate
AMBERLIGHT = """  {
    id: 'amberlight',
    status: 'Live',
    title: 'Production handoff triage: Ingest → Classify → Route',
    forWho:
      'Animation and production studios where a coordinator manually checks every shot handoff by hand — the step that breaks first as a show scales up.',
    scenario:
      'Amberlight Animation is a fictional 280-person episodic animation studio. Shots move through storyboard, layout, animation, lighting, and compositing. Every shot, artist, and show in the demo is synthetic.',
    steps: [
      { label: 'Ingest', detail: 'A shot handoff — its ID, current stage, artist, and notes — is normalized and queued.' },
      {
        label: 'Classify',
        detail:
          "One bounded model call (Claude on Amazon Bedrock) reads the artist's notes and returns a readiness verdict — ready, needs revision, or blocked — with reasoning and a confidence score. It does not decide what the next stage is; that's fixed, known sequencing, not a judgment call.",
      },
      {
        label: 'Route',
        detail:
          "The next stage is looked up from a configured pipeline order, not guessed by the model. Blocked shots always escalate to a supervisor; anything the model isn't confident about goes to a coordinator; confident, ready shots auto-advance to the next stage's queue; shots needing revision go back to the artist with the reasoning attached.",
      },
      {
        label: 'Review',
        detail:
          'Anything the model is unsure about goes to a human review queue with approve / override — low confidence never auto-advances.',
      },
    ],
    capabilities: [
      'Full audit trail for every handoff: what the model said, which rule fired, current stage and next stage',
      "Same pipeline, second industry: this is the identical underlying system that runs the support-ticket demo, re-pointed at animation production by changing only the prompt, the routing rules, and the pipeline's stage order",
      'MCP tools let an AI agent operate the pipeline — read access by default, write access opt-in per tool',
    ],
    tryIt: [
      'Pick an example shot handoff — or write your own coordinator note',
      'Watch it get classified and routed in a few seconds',
      'Open the review queue and approve or override the model',
    ],
    stack: ['Go', 'AWS Lambda', 'SQS', 'S3', 'DynamoDB', 'API Gateway', 'Amazon Bedrock', 'AWS SAM', 'MCP'],
    launchHref: '/demos/amberlight/',
    repoHref: 'https://github.com/marianina8/amberlight-icr-pipeline',
  },
"""
if "id: 'amberlight'" not in s:
    anchor = "    repoHref: 'https://github.com/marianina8/rivergate-icr-pipeline',\n  },\n"
    if anchor not in s:
        sys.exit("DemosPage.tsx: Rivergate entry not found")
    s = s.replace(anchor, anchor + AMBERLIGHT, 1)

# 4. GitHub link on every live card, beside the two existing buttons
sub("""              Request the access password
            </Link>
""", """              Request the access password
            </Link>
            {demo.repoHref ? (
              <a
                href={demo.repoHref}
                target="_blank"
                rel="noopener noreferrer"
                style={{
                  color: 'var(--secondary)',
                  fontFamily: 'var(--font-body)',
                  fontSize: '0.85rem',
                  fontWeight: 600,
                  letterSpacing: '0.03em',
                  textDecoration: 'none',
                  padding: '0.85rem 0.25rem',
                }}
              >
                View the code on GitHub ↗
              </a>
            ) : null}
""")

dp.write_text(s)
print("DemosPage.tsx: Amberlight entry + GitHub links")
