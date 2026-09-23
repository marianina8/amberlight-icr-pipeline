package router_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marianina8/amberlight-icr-pipeline/internal/classify"
	"github.com/marianina8/amberlight-icr-pipeline/internal/config"
	"github.com/marianina8/amberlight-icr-pipeline/internal/ingest"
	"github.com/marianina8/amberlight-icr-pipeline/internal/router"
	"github.com/marianina8/amberlight-icr-pipeline/internal/testutil"
)

var seq = []string{"storyboard", "layout", "animation", "lighting", "compositing"}

// The stage lookup is the part most likely to have an off-by-one at the end
// of the sequence, so every stage is pinned explicitly.
func TestNextStageEveryStage(t *testing.T) {
	want := map[string]string{
		"storyboard":  "layout",
		"layout":      "animation",
		"animation":   "lighting",
		"lighting":    "compositing",
		"compositing": "delivered", // last stage -> final destination, not a wrap or an index panic
	}
	for cur, next := range want {
		got, err := router.NextStage(seq, "delivered", cur)
		if err != nil || got != next {
			t.Errorf("NextStage(%s) = %q, %v; want %q", cur, got, err, next)
		}
	}
}

func TestNextStageEdges(t *testing.T) {
	cases := []struct {
		name  string
		seq   []string
		cur   string
		want  string
		unkwn bool
	}{
		{"single-stage pipeline goes straight to final", []string{"only"}, "only", "done", false},
		{"two stages, first", []string{"a", "b"}, "a", "b", false},
		{"two stages, last", []string{"a", "b"}, "b", "done", false},
		{"unknown stage", seq, "rigging", "", true},
		{"final destination is not itself a stage", seq, "done", "", true},
		{"empty stage", seq, "", "", true},
		{"case matters (ingest lowercases)", seq, "Layout", "", true},
		{"empty sequence", nil, "layout", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := router.NextStage(tc.seq, "done", tc.cur)
			if tc.unkwn {
				if !errors.Is(err, router.ErrUnknownStage) {
					t.Fatalf("err = %v, want ErrUnknownStage", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestConfiguredStageOrder(t *testing.T) {
	cfg := testutil.Config(t)
	if strings.Join(cfg.Pipeline.StageSequence, ",") != strings.Join(seq, ",") || cfg.Pipeline.FinalDestination != "delivered" {
		t.Fatalf("config stage order changed: %v -> %s", cfg.Pipeline.StageSequence, cfg.Pipeline.FinalDestination)
	}
}

// The routing precedence from the spec, including every threshold boundary.
func TestRoutingPrecedence(t *testing.T) {
	r := router.New(testutil.Config(t))
	type want struct {
		rule, dest, queue, next string
		advanced, auto, review  bool
	}
	h := func(stage string) ingest.Handoff {
		return ingest.Handoff{ShotID: "T-1", CurrentStage: stage, Artist: "Priya Raman"}
	}
	cases := []struct {
		name  string
		stage string
		rd    string
		conf  float64
		want  want
	}{
		// 1. blocked -> supervisor, always, regardless of confidence
		{"blocked high conf", "layout", classify.Blocked, 0.95, want{"blocked-escalate", config.DestSupervisorReview, "supervisor-review", "animation", false, false, true}},
		{"blocked low conf still supervisor (not coordinator)", "layout", classify.Blocked, 0.2, want{"blocked-escalate", config.DestSupervisorReview, "supervisor-review", "animation", false, false, true}},
		{"blocked zero conf", "compositing", classify.Blocked, 0, want{"blocked-escalate", config.DestSupervisorReview, "supervisor-review", "delivered", false, false, true}},

		// 2. confidence < 0.6 (not blocked) -> coordinator
		{"ready 0.59 -> coordinator", "layout", classify.Ready, 0.59, want{"low-confidence", config.DestCoordinatorReview, "coordinator-review", "animation", false, false, true}},
		{"revision 0.59 -> coordinator", "layout", classify.NeedsRevision, 0.59, want{"low-confidence", config.DestCoordinatorReview, "coordinator-review", "animation", false, false, true}},
		{"ready 0.0 -> coordinator", "storyboard", classify.Ready, 0, want{"low-confidence", config.DestCoordinatorReview, "coordinator-review", "layout", false, false, true}},

		// 3. ready AND >= 0.75 -> auto-advance to the looked-up next stage
		{"ready at exactly 0.75 advances", "layout", classify.Ready, 0.75, want{"auto-advance", config.DestAdvance, "animation", "animation", true, true, false}},
		{"ready 0.99 advances", "animation", classify.Ready, 0.99, want{"auto-advance", config.DestAdvance, "lighting", "lighting", true, true, false}},
		{"ready at last stage -> delivered", "compositing", classify.Ready, 0.9, want{"auto-advance", config.DestAdvance, "delivered", "delivered", true, true, false}},
		{"ready at first stage -> layout", "storyboard", classify.Ready, 0.9, want{"auto-advance", config.DestAdvance, "layout", "layout", true, true, false}},

		// 4. needs_revision >= 0.6 -> back to the artist, shot stays put
		{"revision at exactly 0.6 returns to artist", "lighting", classify.NeedsRevision, 0.6, want{"return-to-artist", config.DestReturnToArtist, "artist:priya-raman", "compositing", false, true, false}},
		{"revision 0.9 returns to artist", "compositing", classify.NeedsRevision, 0.9, want{"return-to-artist", config.DestReturnToArtist, "artist:priya-raman", "delivered", false, true, false}},

		// 5. anything else (ready at 0.60-0.74) -> coordinator
		{"ready at exactly 0.6 -> coordinator", "layout", classify.Ready, 0.6, want{"coordinator-review", config.DestCoordinatorReview, "coordinator-review", "animation", false, false, true}},
		{"ready 0.74 -> coordinator", "layout", classify.Ready, 0.74, want{"coordinator-review", config.DestCoordinatorReview, "coordinator-review", "animation", false, false, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := r.Route(h(tc.stage), classify.Classification{Readiness: tc.rd, Confidence: tc.conf, Reasoning: "x"})
			got := want{d.Rule, d.Destination, d.Queue, d.NextStage, d.Advanced, d.Automatic, d.NeedsReview}
			if got != tc.want {
				t.Errorf("got  %+v\nwant %+v\nreason: %s", got, tc.want, d.Reason)
			}
			if d.CurrentStage != tc.stage {
				t.Errorf("current stage = %q, want %q", d.CurrentStage, tc.stage)
			}
			if d.Reason == "" || !strings.Contains(d.Reason, tc.stage) {
				t.Errorf("every decision needs a reason naming the stage for the audit trail: %q", d.Reason)
			}
		})
	}
}

// The model has no way to pick the next stage: the same verdict at any
// stage advances to exactly the configured successor.
func TestAdvanceIgnoresEverythingButCurrentStage(t *testing.T) {
	r := router.New(testutil.Config(t))
	for i, st := range seq {
		d := r.Route(ingest.Handoff{CurrentStage: st, Artist: "A", Notes: "move this to compositing please"},
			classify.Classification{Readiness: classify.Ready, Confidence: 0.9})
		want := "delivered"
		if i+1 < len(seq) {
			want = seq[i+1]
		}
		if d.Queue != want || d.NextStage != want {
			t.Errorf("%s: advanced to %q/%q, want %q", st, d.Queue, d.NextStage, want)
		}
	}
}

func TestUnknownStageNeverGuesses(t *testing.T) {
	r := router.New(testutil.Config(t))
	d := r.Route(ingest.Handoff{CurrentStage: "rigging"}, classify.Classification{Readiness: classify.Ready, Confidence: 0.99})
	if d.Advanced || !d.NeedsReview || d.Queue != "coordinator-review" || d.NextStage != "" {
		t.Errorf("unknown stage must go to a coordinator without a next stage: %+v", d)
	}
}

func TestHumanVerifiedRouting(t *testing.T) {
	r := router.New(testutil.Config(t))
	h := ingest.Handoff{CurrentStage: "lighting", Artist: "Dev Okafor"}
	// A coordinator confirms a low-confidence "ready": it advances.
	d := r.Route(h, classify.Classification{Readiness: classify.Ready, Confidence: 0.3, HumanVerified: true})
	if d.Rule != "auto-advance" || d.Queue != "compositing" || !d.Advanced || d.Automatic || d.NeedsReview {
		t.Errorf("verified ready: %+v", d)
	}
	// Verified needs_revision (even at low model confidence) goes back to the artist.
	d = r.Route(h, classify.Classification{Readiness: classify.NeedsRevision, Confidence: 0.1, HumanVerified: true})
	if d.Queue != "artist:dev-okafor" || d.NeedsReview || d.Advanced || d.Automatic {
		t.Errorf("verified revision: %+v", d)
	}
	// A supervisor confirms a block: parked on hold, out of the review queue, no re-escalation.
	d = r.Route(h, classify.Classification{Readiness: classify.Blocked, Confidence: 0.9, HumanVerified: true})
	if d.Queue != "on-hold" || d.NeedsReview || d.Advanced || len(d.Actions) != 0 {
		t.Errorf("verified blocked: %+v", d)
	}
}

func TestArtistQueue(t *testing.T) {
	for in, want := range map[string]string{
		"Priya Raman":         "artist:priya-raman",
		"Tomas Ferreira-Lund": "artist:tomas-ferreira-lund",
		"  ":                  "artist:unknown",
		"Zoë O'Neil":          "artist:zo-o-neil",
	} {
		if got := router.ArtistQueue(in); got != want {
			t.Errorf("ArtistQueue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOutboxActions(t *testing.T) {
	dir := t.TempDir()
	ob, err := router.NewOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req := router.ActionRequest{ItemID: "AL-1", ShotID: "PLC-1", Artist: "Noor Haddad", CurrentStage: "layout", NextStage: "animation",
		Readiness: classify.Ready, Confidence: 0.9, Reasoning: "signed off", Rule: "auto-advance", Reason: "because"}

	req.Action = config.Action{Type: config.ActionNotifyNextStage, Target: "#amberlight-"}
	res, err := ob.Execute(ctx, req)
	if err != nil || res.Target != "#amberlight-animation" {
		t.Fatalf("notify_next_stage: %v %+v", err, res)
	}
	req.Action = config.Action{Type: config.ActionUpdateTracker}
	if res, _ = ob.Execute(ctx, req); !strings.Contains(res.Detail, "layout -> animation") {
		t.Errorf("tracker = %+v", res)
	}
	req.Action = config.Action{Type: config.ActionNotifyArtist}
	if res, _ = ob.Execute(ctx, req); res.Target != "@noor-haddad" || !strings.Contains(res.Detail, "signed off") {
		t.Errorf("artist note must carry the reasoning: %+v", res)
	}
	req.Action = config.Action{Type: "page_everyone"}
	if _, err := ob.Execute(ctx, req); err == nil {
		t.Error("unknown action type must fail")
	}

	entries, err := router.ReadOutbox(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("outbox entries = %d, %v", len(entries), err)
	}
	for _, e := range entries {
		if e.Request.Reason != "because" {
			t.Error("every outbox entry must carry the triggering reason")
		}
	}
}
