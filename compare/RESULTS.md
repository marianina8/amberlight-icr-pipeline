# Naive keyword rules vs Bedrock (us.anthropic.claude-haiku-4-5-20251001-v1:0) on the Amberlight fixtures

Generated 2026-09-23 03:35 UTC. Both classifiers' verdicts go through the same router and stage lookup; ✗ marks a route that differs from the intended one.

| Fixture | Stage | Intended | Naive rules | Naive route | Bedrock | Bedrock route |
|---|---|---|---|---|---|---|
| 01-storyboard-locked-direct | storyboard → layout | ready → advance | ready @ 0.80 (matched "signed off") | advance → layout | ready @ 0.92 | advance → layout |
| 02-layout-hedged-open-note | layout → animation | needs_revision → return_to_artist | needs_revision @ 0.40 (no rule matched) | ✗ coordinator_review | needs_revision @ 0.92 | return_to_artist |
| 03-animation-sarcastic-ready | animation → lighting | needs_revision → return_to_artist | ready @ 0.80 (matched "ready") | ✗ advance → lighting | needs_revision @ 0.95 | return_to_artist |
| 04-lighting-buried-missing-asset | lighting → compositing | blocked → supervisor_review | blocked @ 0.80 (matched "can't") | supervisor_review | blocked @ 0.95 | supervisor_review |
| 05-compositing-final-delivered | compositing → delivered | ready → advance | ready @ 0.80 (matched "approved") | advance → delivered | ready @ 0.95 | advance → delivered |
| 06-animation-rig-crash-blocked | animation → lighting | blocked → supervisor_review | blocked @ 0.80 (matched "can't") | supervisor_review | blocked @ 0.95 | supervisor_review |
| 07-layout-vague | layout → animation | coordinator_review | needs_revision @ 0.40 (no rule matched) | coordinator_review | needs_revision @ 0.75 | ✗ return_to_artist |
| 08-animation-approved-but-redo | animation → lighting | needs_revision → return_to_artist | ready @ 0.80 (matched "approved") | ✗ advance → lighting | needs_revision @ 0.92 | return_to_artist |
| 09-lighting-casual-ready | lighting → compositing | ready → advance | needs_revision @ 0.40 (no rule matched) | ✗ coordinator_review | ready @ 0.95 | advance → compositing |
| 10-storyboard-waiting-on-director | storyboard → layout | blocked → supervisor_review | ready @ 0.80 (matched "done") | ✗ advance → layout | blocked @ 0.95 | supervisor_review |

**Naive rules:** 5 of 10 routed differently than intended; 3 of those auto-advanced a shot that should not have moved.

**Bedrock:** 1 of 10 routed differently than intended; 0 auto-advanced a shot that should not have moved.

## Bedrock's reasoning per fixture

- **01-storyboard-locked-direct** — Bedrock: The artist reports boards are locked, supervisor (Ines) has signed off, timing sheet is attached, and no open notes remain. All standard completion criteria for storyboard stage are met.
- **02-layout-hedged-open-note** — Bedrock: The artist acknowledges an unfixed issue flagged in dailies (dolly-in timing) that they have not yet addressed. Despite the casual hedge ('mostly there'), this is an open problem in the current stage's work that should be resolved before handoff.
- **03-animation-sarcastic-ready** — Bedrock: The artist reports a specific, unresolved collision issue (arm clipping through table for 14 frames at timecode 1040) and uses sarcasm to indicate this is not actually acceptable work. The schedule pressure does not override the technical problem in the animation itself.
- **04-lighting-buried-missing-asset** — Bedrock: The artist cannot render finals because updated set geometry from the modeling department has not been published; the far wall remains a placeholder. This is an upstream asset dependency outside the lighting artist's control.
- **05-compositing-final-delivered** — Bedrock: The artist reports final review approval from Ines, completion of all technical requirements (grain matching, clean holdouts, published EXRs), and explicitly states no remaining work on their side. The note is straightforward and contains no hedging, caveats, or buried problems.
- **06-animation-rig-crash-blocked** — Bedrock: The artist cannot complete their animation work because the face rig for the character crashes on load, which is a broken upstream asset outside their control. Body blocking is finished, but lip sync—a core part of animation—is blocked by the rig failure.
- **07-layout-vague** — Bedrock: The handoff note provides no substantive information about what was completed, what was discussed, or the actual state of the shot. 'Did what we talked about' is unverifiable and suggests either incomplete documentation or that the artist assumes context that may not be recorded. A production coordinator cannot assess readiness without concrete details about layout completion.
- **08-animation-approved-but-redo** — Bedrock: The artist reports that a creative stakeholder (Ines) has requested a specific revision to the eyeline in the final beat to improve the lantern handoff clarity. This is an open note that requires the animator to address before the shot is truly complete.
- **09-lighting-casual-ready** — Bedrock: Renders are complete and clean, supervisor (Ines) has approved in dailies and explicitly instructed to leave the rim as-is. No open issues remain in the lighting stage.
- **10-storyboard-waiting-on-director** — Bedrock: The storyboard work is complete, but the shot cannot progress to layout because a creative decision (selection between two versions) is pending and awaits the decision-maker's return. This is a blocker outside the artist's control.

## Why each fixture is there

- **01-storyboard-locked-direct** — Direct and unambiguous: signed off, nothing open. Should auto-advance to layout.
- **02-layout-hedged-open-note** — Hedged, and the director's dailies note hasn't been addressed. Back to the artist, not onward.
- **03-animation-sarcastic-ready** — Sarcastic 'ready' over a real intersection problem. Keyword matching reads 'ready'.
- **04-lighting-buried-missing-asset** — The real issue (unpublished upstream asset) is buried mid-sentence between positive phrases.
- **05-compositing-final-delivered** — Last stage in the sequence: the stage lookup must return 'delivered', not wrap or error.
- **06-animation-rig-crash-blocked** — A plainly stated block outside the artist's control.
- **07-layout-vague** — Too vague to judge; any verdict should come with low confidence and go to a coordinator.
- **08-animation-approved-but-redo** — Opens with 'Approved!', but a director change is still outstanding.
- **09-lighting-casual-ready** — Casual and hedged in tone, but approved with no open work. None of the naive 'ready' keywords appear.
- **10-storyboard-waiting-on-director** — Says 'done', but a pending creative decision blocks the next department.
