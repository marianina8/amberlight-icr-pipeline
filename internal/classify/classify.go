// Package classify judges a shot handoff's readiness from its notes:
// {readiness, reasoning, confidence}. It is one bounded model call, not a
// chatbot, and it deliberately does NOT decide which stage comes next — stage
// sequencing is fixed configuration applied by the router.
//
// The model sits behind the Classifier interface so the pipeline runs fully
// offline with the Mock classifier and uses Bedrock when deployed.
package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"text/template"
	"time"

	"github.com/marianina8/amberlight-icr-pipeline/internal/config"
	"github.com/marianina8/amberlight-icr-pipeline/internal/ingest"
)

// Readiness verdicts.
const (
	Ready         = "ready_for_next_stage"
	NeedsRevision = "needs_revision"
	Blocked       = "blocked"
)

// Classification is the structured model output, plus provenance. There is
// intentionally no target/next-stage field: the model cannot express one.
type Classification struct {
	Readiness string `json:"readiness"`
	// Reasoning is the one-to-two sentence "why", kept for the audit trail
	// and sent to the artist when a shot is returned for revision.
	Reasoning    string    `json:"reasoning"`
	Confidence   float64   `json:"confidence"`
	Classifier   string    `json:"classifier"`
	ClassifiedAt time.Time `json:"classified_at"`
	// HumanVerified is set when a reviewer approved or overrode the result.
	HumanVerified bool `json:"human_verified,omitempty"`
}

// Classifier is the seam between the pipeline and whatever model is used.
type Classifier interface {
	Classify(ctx context.Context, h ingest.Handoff) (Classification, error)
	Name() string
}

// ErrBadOutput means the model's response could not be trusted. The pipeline
// treats this as confidence 0 -> human review, never as a guess.
var ErrBadOutput = errors.New("classifier output invalid")

// ParseModelOutput extracts and strictly validates the JSON object the model
// returned. Unknown readiness labels are rejected rather than coerced. Extra
// fields (for example a "target_stage" the model was told not to produce)
// are dropped: the struct has nowhere to put them, so they can never reach
// the router.
func ParseModelOutput(text string, labels []string) (Classification, error) {
	s := strings.TrimSpace(text)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return Classification{}, fmt.Errorf("%w: no JSON object in response", ErrBadOutput)
	}
	var out struct {
		Readiness  string   `json:"readiness"`
		Reasoning  string   `json:"reasoning"`
		Confidence *float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &out); err != nil {
		return Classification{}, fmt.Errorf("%w: %v", ErrBadOutput, err)
	}
	c := Classification{
		Readiness: strings.ToLower(strings.TrimSpace(out.Readiness)),
		Reasoning: strings.TrimSpace(out.Reasoning),
	}
	if !config.Contains(labels, c.Readiness) {
		return Classification{}, fmt.Errorf("%w: unknown readiness %q", ErrBadOutput, out.Readiness)
	}
	if out.Confidence == nil || math.IsNaN(*out.Confidence) || *out.Confidence < 0 || *out.Confidence > 1 {
		return Classification{}, fmt.Errorf("%w: confidence missing or outside [0,1]", ErrBadOutput)
	}
	if c.Reasoning == "" {
		return Classification{}, fmt.Errorf("%w: empty reasoning", ErrBadOutput)
	}
	c.Confidence = round2(*out.Confidence)
	return c, nil
}

// Fallback is the classification recorded when the classifier fails:
// needs_revision at confidence 0, which the low-confidence rule sends to a
// coordinator. It is never auto-advanced.
func Fallback(err error, classifier string, now time.Time) Classification {
	return Classification{
		Readiness:    NeedsRevision,
		Reasoning:    "Automatic readiness check failed (" + err.Error() + "); a coordinator needs to read the note.",
		Confidence:   0,
		Classifier:   classifier,
		ClassifiedAt: now.UTC(),
	}
}

// Prompt renders the system and user prompts from config templates.
type Prompt struct {
	system *template.Template
	user   *template.Template
	cfg    *config.Config
}

// NewPrompt parses the prompt templates in config.
func NewPrompt(c *config.Config) (*Prompt, error) {
	sys, err := template.New("system").Option("missingkey=error").Parse(c.Prompt.System)
	if err != nil {
		return nil, fmt.Errorf("prompt.system: %w", err)
	}
	usr, err := template.New("user").Option("missingkey=error").Parse(c.Prompt.User)
	if err != nil {
		return nil, fmt.Errorf("prompt.user: %w", err)
	}
	return &Prompt{system: sys, user: usr, cfg: c}, nil
}

// System renders the system prompt (taxonomy + output contract).
func (p *Prompt) System() (string, error) {
	var b bytes.Buffer
	err := p.system.Execute(&b, p.cfg.Taxonomy)
	return b.String(), err
}

// User renders the per-handoff user message.
func (p *Prompt) User(h ingest.Handoff) (string, error) {
	var b bytes.Buffer
	err := p.user.Execute(&b, h)
	return b.String(), err
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
