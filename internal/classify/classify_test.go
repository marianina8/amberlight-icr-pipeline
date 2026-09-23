package classify_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/marianina8/amberlight-icr-pipeline/internal/classify"
	"github.com/marianina8/amberlight-icr-pipeline/internal/ingest"
	"github.com/marianina8/amberlight-icr-pipeline/internal/router"
	"github.com/marianina8/amberlight-icr-pipeline/internal/testutil"
)

func labels(t *testing.T) []string { return testutil.Config(t).ReadinessNames() }

func TestParseModelOutput(t *testing.T) {
	good := `{"readiness":"needs_revision","reasoning":"Arm intersects the table for 14 frames.","confidence":0.873}`
	cases := []struct {
		name, in string
		wantErr  bool
		wantConf float64
	}{
		{"plain json", good, false, 0.87},
		{"code fenced", "```json\n" + good + "\n```", false, 0.87},
		{"prose around json", "Here you go:\n" + good + "\nThanks", false, 0.87},
		{"uppercase label normalized", strings.Replace(good, `"needs_revision"`, `"NEEDS_REVISION"`, 1), false, 0.87},
		{"unknown readiness", strings.Replace(good, `"needs_revision"`, `"almost_ready"`, 1), true, 0},
		{"a stage name is not a readiness", strings.Replace(good, `"needs_revision"`, `"lighting"`, 1), true, 0},
		{"confidence > 1", strings.Replace(good, `0.873`, `1.4`, 1), true, 0},
		{"confidence negative", strings.Replace(good, `0.873`, `-0.1`, 1), true, 0},
		{"confidence missing", `{"readiness":"blocked","reasoning":"x"}`, true, 0},
		{"empty reasoning", strings.Replace(good, `Arm intersects the table for 14 frames.`, ``, 1), true, 0},
		{"no json", "I think this needs revision.", true, 0},
		{"malformed json", `{"readiness": "blocked",`, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := classify.ParseModelOutput(tc.in, labels(t))
			if tc.wantErr {
				if !errors.Is(err, classify.ErrBadOutput) {
					t.Fatalf("err = %v, want ErrBadOutput", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Readiness != classify.NeedsRevision || c.Confidence != tc.wantConf || c.Reasoning == "" {
				t.Errorf("got %+v", c)
			}
		})
	}
}

// Even if the model disobeys and names a stage, it has no effect: the
// classification cannot carry one, and routing uses the stage lookup.
func TestModelSuppliedTargetStageIsIgnored(t *testing.T) {
	cfg := testutil.Config(t)
	out := `{"readiness":"ready_for_next_stage","reasoning":"Signed off.","confidence":0.95,"target_stage":"compositing","next_stage":"delivered"}`
	c, err := classify.ParseModelOutput(out, cfg.ReadinessNames())
	if err != nil {
		t.Fatal(err)
	}
	d := router.New(cfg).Route(ingest.Handoff{CurrentStage: "layout", Artist: "A"}, c)
	if d.Queue != "animation" || d.NextStage != "animation" {
		t.Errorf("layout must advance to animation via lookup, got queue=%s next=%s", d.Queue, d.NextStage)
	}
}

func TestFallbackNeverAutoAdvances(t *testing.T) {
	cfg := testutil.Config(t)
	c := classify.Fallback(errors.New("timeout"), "x", time.Now())
	if c.Confidence != 0 || c.Readiness == classify.Ready {
		t.Errorf("fallback must be non-ready @ 0, got %s @ %v", c.Readiness, c.Confidence)
	}
	d := router.New(cfg).Route(ingest.Handoff{CurrentStage: "layout", Artist: "A"}, c)
	if !d.NeedsReview || d.Advanced || d.Queue != "coordinator-review" {
		t.Errorf("fallback must go to a coordinator: %+v", d)
	}
}

func TestKeywordRegexp(t *testing.T) {
	cases := []struct {
		kw, text string
		want     bool
	}{
		{"ready", "totally ready.", true},
		{"ready", "already", false}, // whole word
		{"fix*", "I fixed the edge", true},
		{"signed off", "Ines signed off", true},
		{"can't", "I can't render", true},
	}
	for _, tc := range cases {
		re, err := classify.KeywordRegexp(tc.kw)
		if err != nil {
			t.Fatal(err)
		}
		if got := re.MatchString(tc.text); got != tc.want {
			t.Errorf("%q in %q = %v, want %v", tc.kw, tc.text, got, tc.want)
		}
	}
}

// Golden results for the naive mock classifier on the demo fixtures. The mock
// is deliberately keyword-based; several of these are WRONG against the
// fixtures' intended readiness (see demo/expected.json and compare/), which
// is the point: the judgment needs more than keyword matching.
func TestMockClassifierOnFixtures(t *testing.T) {
	m, err := classify.NewMock(testutil.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		rd   string
		conf float64
	}{
		"01-storyboard-locked-direct.json":       {classify.Ready, 0.68},
		"02-layout-hedged-open-note.json":        {classify.NeedsRevision, 0.35},
		"03-animation-sarcastic-ready.json":      {classify.Ready, 0.65},
		"04-lighting-buried-missing-asset.json":  {classify.Ready, 0.68},
		"05-compositing-final-delivered.json":    {classify.Ready, 0.8},
		"06-animation-rig-crash-blocked.json":    {classify.Blocked, 0.68},
		"07-layout-vague.json":                   {classify.NeedsRevision, 0.35},
		"08-animation-approved-but-redo.json":    {classify.Ready, 0.65},
		"09-lighting-casual-ready.json":          {classify.NeedsRevision, 0.35},
		"10-storyboard-waiting-on-director.json": {classify.Ready, 0.65},
	}
	fixtures := testutil.Fixtures(t)
	if len(fixtures) != len(want) {
		t.Fatalf("%d fixtures, %d expectations", len(fixtures), len(want))
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			h, err := ingest.Normalize(f.Payload, "test", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			c, err := m.Classify(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			w, ok := want[f.Name]
			if !ok {
				t.Fatalf("no expectation for %s", f.Name)
			}
			if c.Readiness != w.rd || c.Confidence != w.conf {
				t.Errorf("got %s@%.2f, want %s@%.2f (%s)", c.Readiness, c.Confidence, w.rd, w.conf, c.Reasoning)
			}
		})
	}
}

func TestPromptRendersTaxonomyAndHandoff(t *testing.T) {
	p, err := classify.NewPrompt(testutil.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	sys, err := p.System()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Amberlight", "- ready_for_next_stage:", "- needs_revision:", "- blocked:", `"confidence"`, `"reasoning"`, "untrusted", "do NOT decide which\nstage comes next"} {
		if !strings.Contains(sys, s) {
			t.Errorf("system prompt missing %q", s)
		}
	}
	if strings.Contains(sys, "target_stage") || strings.Contains(sys, `"next_stage"`) {
		t.Error("the output contract must not ask for a stage")
	}
	usr, err := p.User(ingest.Handoff{ShotID: "PLC-1", Show: "Show", Episode: "104", CurrentStage: "layout", Artist: "Ada", Notes: "Camera is locked."})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"<handoff>", "shot: PLC-1", "Show / episode 104", "stage just finished: layout", "artist: Ada", "Camera is locked.", "</handoff>"} {
		if !strings.Contains(usr, s) {
			t.Errorf("user prompt missing %q", s)
		}
	}
}

// fakeConverse stands in for Bedrock Runtime. No AWS calls are made.
type fakeConverse struct {
	reply string
	err   error
	got   *bedrockruntime.ConverseInput
}

func (f *fakeConverse) Converse(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	f.got = in
	if f.err != nil {
		return nil, f.err
	}
	return &bedrockruntime.ConverseOutput{Output: &types.ConverseOutputMemberMessage{Value: types.Message{
		Role:    types.ConversationRoleAssistant,
		Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: f.reply}},
	}}}, nil
}

func TestBedrockClassifierWithFakeClient(t *testing.T) {
	cfg := testutil.Config(t)
	fake := &fakeConverse{reply: `{"readiness":"blocked","reasoning":"Set geometry was never published.","confidence":0.88}`}
	b, err := classify.NewBedrock(fake, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := ingest.Handoff{ShotID: "MRB-212-045", CurrentStage: "lighting", Artist: "Dev", Notes: "Looks lovely, except the far wall is a placeholder."}
	c, err := b.Classify(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if c.Readiness != classify.Blocked || c.Confidence != 0.88 || !strings.HasPrefix(c.Classifier, "bedrock:") {
		t.Errorf("got %+v", c)
	}
	if *fake.got.ModelId != cfg.Classifier.Bedrock.ModelID {
		t.Errorf("model id = %s", *fake.got.ModelId)
	}
	if *fake.got.InferenceConfig.Temperature != 0 {
		t.Error("classification should run at temperature 0")
	}
	userText := fake.got.Messages[0].Content[0].(*types.ContentBlockMemberText).Value
	if !strings.Contains(userText, "far wall is a placeholder") || !strings.Contains(userText, "stage just finished: lighting") {
		t.Errorf("handoff not in prompt: %s", userText)
	}

	fake.reply = "Sorry, I can't judge that."
	if _, err := b.Classify(context.Background(), h); !errors.Is(err, classify.ErrBadOutput) {
		t.Errorf("non-JSON reply: err = %v, want ErrBadOutput", err)
	}
	fake.err = errors.New("throttled")
	if _, err := b.Classify(context.Background(), h); err == nil {
		t.Error("client error should surface")
	}
}
