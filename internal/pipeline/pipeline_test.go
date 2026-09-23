package pipeline_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marianina8/amberlight-icr-pipeline/internal/classify"
	"github.com/marianina8/amberlight-icr-pipeline/internal/ingest"
	"github.com/marianina8/amberlight-icr-pipeline/internal/pipeline"
	"github.com/marianina8/amberlight-icr-pipeline/internal/router"
	"github.com/marianina8/amberlight-icr-pipeline/internal/store"
	"github.com/marianina8/amberlight-icr-pipeline/internal/testutil"
)

type harness struct {
	svc    *pipeline.Service
	outbox string
}

func newHarness(t *testing.T, cl classify.Classifier) harness {
	t.Helper()
	cfg := testutil.Config(t)
	if cl == nil {
		cl = scripted{}
	}
	q, err := ingest.NewDirQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	ob, err := router.NewOutbox(outDir)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	svc := &pipeline.Service{
		Cfg: cfg, Classifier: cl, Store: store.NewMemory(), Router: router.New(cfg), Actions: ob, Queue: q,
		Now: func() time.Time { clock = clock.Add(time.Second); return clock },
	}
	return harness{svc: svc, outbox: outDir}
}

func (h harness) ingestAll(t *testing.T) map[string]string {
	t.Helper()
	ctx := context.Background()
	ids := map[string]string{}
	for _, f := range testutil.Fixtures(t) {
		tk, dup, err := h.svc.Ingest(ctx, f.Payload, "test")
		if err != nil || dup {
			t.Fatalf("%s: dup=%v err=%v", f.Name, dup, err)
		}
		ids[f.Name] = tk.ID
	}
	for {
		n, err := h.svc.RunOnce(ctx, 4)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	return ids
}

// scripted returns what a good model should say for each demo fixture (the
// intended verdicts in demo/expected.json), so the end-to-end test exercises
// every routing destination deterministically. The live Bedrock run is
// compared against the same intent in compare/.
type scripted struct{}

var intended = map[string]classify.Classification{
	"PLC-104-020": {Readiness: classify.Ready, Confidence: 0.94},
	"PLC-104-035": {Readiness: classify.NeedsRevision, Confidence: 0.8},
	"MRB-212-018": {Readiness: classify.NeedsRevision, Confidence: 0.9},
	"MRB-212-045": {Readiness: classify.Blocked, Confidence: 0.86},
	"PLC-103-110": {Readiness: classify.Ready, Confidence: 0.96},
	"MRB-212-022": {Readiness: classify.Blocked, Confidence: 0.93},
	"PLC-104-050": {Readiness: classify.NeedsRevision, Confidence: 0.3},
	"PLC-104-062": {Readiness: classify.NeedsRevision, Confidence: 0.82},
	"MRB-211-030": {Readiness: classify.Ready, Confidence: 0.8},
	"MRB-213-005": {Readiness: classify.Blocked, Confidence: 0.84},
}

func (scripted) Classify(_ context.Context, h ingest.Handoff) (classify.Classification, error) {
	c, ok := intended[h.ShotID]
	if !ok {
		c = classify.Classification{Readiness: classify.Ready, Confidence: 0.7}
	}
	c.Reasoning, c.Classifier = "scripted verdict for "+h.ShotID, "scripted"
	return c, nil
}
func (scripted) Name() string { return "scripted" }

// The demo storyline, end to end: every fixture lands where the Amberlight
// routing rules and the stage lookup say it should.
func TestFixturesEndToEnd(t *testing.T) {
	h := newHarness(t, nil)
	ids := h.ingestAll(t)
	want := map[string]struct {
		rule, queue, next string
		advanced          bool
		status            store.Status
		review            bool
		actions           int
	}{
		"01-storyboard-locked-direct.json":       {"auto-advance", "layout", "layout", true, store.StatusRouted, false, 2},
		"02-layout-hedged-open-note.json":        {"return-to-artist", "artist:tomas-ferreira-lund", "animation", false, store.StatusRouted, false, 1},
		"03-animation-sarcastic-ready.json":      {"return-to-artist", "artist:keiko-ambrose", "lighting", false, store.StatusRouted, false, 1},
		"04-lighting-buried-missing-asset.json":  {"blocked-escalate", "supervisor-review", "compositing", false, store.StatusPendingReview, true, 1},
		"05-compositing-final-delivered.json":    {"auto-advance", "delivered", "delivered", true, store.StatusRouted, false, 2},
		"06-animation-rig-crash-blocked.json":    {"blocked-escalate", "supervisor-review", "lighting", false, store.StatusPendingReview, true, 1},
		"07-layout-vague.json":                   {"low-confidence", "coordinator-review", "animation", false, store.StatusPendingReview, true, 0},
		"08-animation-approved-but-redo.json":    {"return-to-artist", "artist:keiko-ambrose", "lighting", false, store.StatusRouted, false, 1},
		"09-lighting-casual-ready.json":          {"auto-advance", "compositing", "compositing", true, store.StatusRouted, false, 2},
		"10-storyboard-waiting-on-director.json": {"blocked-escalate", "supervisor-review", "layout", false, store.StatusPendingReview, true, 1},
	}
	ctx := context.Background()
	for name, id := range ids {
		it, err := h.svc.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		w, ok := want[name]
		if !ok {
			t.Fatalf("no expectation for %s", name)
		}
		d := it.Decision
		if d == nil || d.Rule != w.rule || it.Queue != w.queue || d.NextStage != w.next || d.Advanced != w.advanced ||
			it.Status != w.status || it.NeedsReview != w.review || len(it.ExecutedActions) != w.actions {
			t.Errorf("%s: decision=%+v queue=%s status=%s review=%v actions=%d; want %+v", name, d, it.Queue, it.Status, it.NeedsReview, len(it.ExecutedActions), w)
		}
		if d != nil && d.CurrentStage != it.Handoff.CurrentStage {
			t.Errorf("%s: decision stage %s != handoff stage %s", name, d.CurrentStage, it.Handoff.CurrentStage)
		}
		// Lifecycle is fully recorded.
		types := eventTypes(it)
		if !strings.HasPrefix(types, "received,classified,routed") {
			t.Errorf("%s: events = %s", name, types)
		}
		// The audit trail records what the model said, which rule fired, and
		// the current and next stage.
		for _, e := range it.Events {
			switch e.Type {
			case "routed":
				for _, k := range []string{"readiness", "confidence", "reasoning", "rule", "current_stage", "next_stage", "queue"} {
					if e.Data[k] == nil {
						t.Errorf("%s: routed event missing %s", name, k)
					}
				}
			case "action":
				if e.Data["reason"] == "" || e.Data["confidence"] == nil || e.Data["reasoning"] == "" || e.Data["next_stage"] == nil {
					t.Errorf("%s: action event missing reasoning: %+v", name, e.Data)
				}
			}
		}
	}
	entries, _ := router.ReadOutbox(h.outbox)
	// 3 advances x (tracker + next-stage msg) + 3 artist notes + 3 supervisor notes
	if len(entries) != 12 {
		t.Errorf("outbox has %d entries, want 12", len(entries))
	}
	st, _ := h.svc.Stats(ctx)
	if st.Total != 10 || st.Automatic != 6 || st.NeedsReview != 4 {
		t.Errorf("stats = %+v", st)
	}
}

// The naive keyword mock also runs end to end, and never auto-advances a
// shot it is unsure about.
func TestMockClassifierEndToEnd(t *testing.T) {
	m, err := classify.NewMock(testutil.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, m)
	h.ingestAll(t)
	items, _ := h.svc.List(context.Background(), store.Filter{})
	for _, it := range items {
		if it.Decision.Advanced && it.Classification.Confidence < 0.75 {
			t.Errorf("%s advanced at %.2f", it.ID, it.Classification.Confidence)
		}
	}
}

func TestIngestRejectsUnknownStage(t *testing.T) {
	h := newHarness(t, nil)
	for _, stage := range []string{"rigging", "delivered", ""} {
		_, _, err := h.svc.Ingest(context.Background(), []byte(`{"shot_id":"X-1","current_stage":"`+stage+`","artist":"A","notes":"done"}`), "test")
		if !errors.Is(err, ingest.ErrInvalid) {
			t.Errorf("stage %q: err = %v, want ErrInvalid", stage, err)
		}
	}
}

func eventTypes(it store.Item) string {
	var xs []string
	for _, e := range it.Events {
		xs = append(xs, e.Type)
	}
	return strings.Join(xs, ",")
}

func TestIngestIsIdempotent(t *testing.T) {
	h := newHarness(t, nil)
	f := testutil.Fixtures(t)[0]
	ctx := context.Background()
	a, dup, err := h.svc.Ingest(ctx, f.Payload, "cli")
	if err != nil || dup {
		t.Fatal(dup, err)
	}
	b, dup, err := h.svc.Ingest(ctx, f.Payload, "webhook")
	if err != nil || !dup || a.ID != b.ID {
		t.Fatalf("second ingest should be a duplicate: %v %v", dup, err)
	}
	if n, _ := h.svc.RunOnce(ctx, 10); n != 1 {
		t.Errorf("processed %d, want 1 (duplicate must not be re-queued)", n)
	}
	// Redelivery of an already-processed message is a no-op (no double page).
	it, _ := h.svc.Get(ctx, a.ID)
	if _, err := h.svc.Process(ctx, it.Handoff, "worker"); err != nil {
		t.Fatal(err)
	}
	again, _ := h.svc.Get(ctx, a.ID)
	if len(again.Events) != len(it.Events) {
		t.Error("redelivered message changed the item")
	}
}

type failingClassifier struct{}

func (failingClassifier) Classify(context.Context, ingest.Handoff) (classify.Classification, error) {
	return classify.Classification{}, errors.New("model timeout")
}
func (failingClassifier) Name() string { return "failing" }

type liarClassifier struct{}

func (liarClassifier) Classify(context.Context, ingest.Handoff) (classify.Classification, error) {
	return classify.Classification{Readiness: "ship_it", Confidence: 0.99, Reasoning: "x"}, nil
}
func (liarClassifier) Name() string { return "liar" }

func TestClassifierFailureDefersToHuman(t *testing.T) {
	for _, cl := range []classify.Classifier{failingClassifier{}, liarClassifier{}} {
		t.Run(cl.Name(), func(t *testing.T) {
			h := newHarness(t, cl)
			ctx := context.Background()
			f := testutil.Fixtures(t)[0] // a clearly-ready storyboard handoff
			tk, _, err := h.svc.Ingest(ctx, f.Payload, "test")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.svc.RunOnce(ctx, 1); err != nil {
				t.Fatal(err)
			}
			it, _ := h.svc.Get(ctx, tk.ID)
			if !it.NeedsReview || it.Queue != "coordinator-review" || it.Decision.Advanced || it.Classification.Confidence != 0 {
				t.Errorf("failure must route to a human: %+v", it.Decision)
			}
			if !strings.Contains(eventTypes(it), "error") {
				t.Error("failure should be recorded in the audit trail")
			}
		})
	}
}

func TestApproveAndOverride(t *testing.T) {
	h := newHarness(t, nil)
	ids := h.ingestAll(t)
	ctx := context.Background()

	// A coordinator approves the vague layout note's verdict (needs_revision):
	// it now goes back to the artist, human-verified.
	id := ids["07-layout-vague.json"]
	it, err := h.svc.Approve(ctx, id, "dashboard:sam", "asked Mina, she's still on it")
	if err != nil {
		t.Fatal(err)
	}
	if it.Status != store.StatusReviewed || it.Queue != "artist:mina-castellanos" || it.NeedsReview || it.Review.Outcome != "approved" {
		t.Errorf("approve: status=%s queue=%s review=%v", it.Status, it.Queue, it.NeedsReview)
	}
	if len(it.ExecutedActions) != 1 {
		t.Errorf("approved revision should notify the artist: %v", it.ExecutedActions)
	}

	// A supervisor overrides a blocked verdict to ready: the shot advances to
	// the looked-up next stage (lighting -> compositing), not anywhere else.
	id = ids["04-lighting-buried-missing-asset.json"]
	it, err = h.svc.Override(ctx, id, "cli:sam", "ready_for_next_stage", "geometry published this morning")
	if err != nil {
		t.Fatal(err)
	}
	if it.Queue != "compositing" || !it.Decision.Advanced || it.Classification.Readiness != "ready_for_next_stage" || it.Review.Original.Readiness != "blocked" {
		t.Errorf("override: queue=%s decision=%+v class=%+v", it.Queue, it.Decision, it.Classification)
	}
	if !strings.Contains(it.Classification.Reasoning, "geometry published") || !strings.Contains(it.Classification.Reasoning, "model said blocked") {
		t.Errorf("override reasoning should keep both the note and the model's view: %s", it.Classification.Reasoning)
	}

	// A supervisor confirms a block: parked on hold, not re-escalated.
	id = ids["06-animation-rig-crash-blocked.json"]
	before, _ := router.ReadOutbox(h.outbox)
	it, err = h.svc.Approve(ctx, id, "dashboard:lee", "rigging fix ETA Thursday")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := router.ReadOutbox(h.outbox)
	if it.Queue != "on-hold" || it.NeedsReview || len(after) != len(before) {
		t.Errorf("confirmed block: queue=%s review=%v outbox %d -> %d", it.Queue, it.NeedsReview, len(before), len(after))
	}
	if !strings.Contains(eventTypes(it), "review") {
		t.Error("review not in audit trail")
	}

	// Invalid override label is rejected, nothing changes.
	if _, err := h.svc.Override(ctx, id, "cli:sam", "lighting", ""); !errors.Is(err, pipeline.ErrInvalidLabel) {
		t.Errorf("err = %v, want ErrInvalidLabel", err)
	}
	if _, err := h.svc.Override(ctx, id, "cli:sam", "", ""); !errors.Is(err, pipeline.ErrInvalidLabel) {
		t.Errorf("empty override: err = %v, want ErrInvalidLabel", err)
	}
	if _, err := h.svc.Approve(ctx, id, " ", ""); err == nil {
		t.Error("reviewer is required")
	}
}

func TestPreviewHasNoSideEffects(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	tk, err := ingest.Normalize(testutil.Fixtures(t)[0].Payload, "preview", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c, d, err := h.svc.Preview(ctx, tk)
	if err != nil || c.Readiness != classify.Ready || d.Rule != "auto-advance" || d.Queue != "layout" {
		t.Fatalf("preview: %+v %+v %v", c, d, err)
	}
	if items, _ := h.svc.List(ctx, store.Filter{}); len(items) != 0 {
		t.Error("preview stored an item")
	}
	if entries, _ := router.ReadOutbox(h.outbox); len(entries) != 0 {
		t.Error("preview ran actions")
	}
}

func TestRouteRequiresClassification(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	tk, _, _ := h.svc.Ingest(ctx, testutil.Fixtures(t)[0].Payload, "test")
	if _, err := h.svc.Route(ctx, tk.ID, "cli"); !errors.Is(err, pipeline.ErrNotClassified) {
		t.Errorf("err = %v", err)
	}
}

func TestActionsFromItemsMatchesOutbox(t *testing.T) {
	h := newHarness(t, nil)
	h.ingestAll(t)
	items, _ := h.svc.List(context.Background(), store.Filter{})
	derived := pipeline.ActionsFromItems(items)
	files, _ := router.ReadOutbox(h.outbox)
	if len(derived) != len(files) || len(derived) != 12 {
		t.Fatalf("derived %d actions, outbox has %d", len(derived), len(files))
	}
	refs := map[string]bool{}
	for _, e := range files {
		refs[e.Kind+"|"+e.Ref] = true
	}
	for _, e := range derived {
		if !refs[e.Kind+"|"+e.Ref] || e.Request.Reason == "" || e.Detail == "" {
			t.Errorf("derived entry does not match outbox: %+v", e)
		}
	}
}
