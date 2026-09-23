// Package router decides what happens to a classified shot handoff.
//
// Two separate things happen here, on purpose:
//
//  1. Stage lookup (NextStage) is a deterministic table lookup in the
//     configured pipeline.stage_sequence: whatever immediately follows the
//     handoff's current_stage, or the final destination after the last stage.
//     The model is never consulted.
//  2. Routing rules (from config, evaluated top to bottom, first match wins)
//     use the model's readiness + confidence to pick a destination: advance
//     to the looked-up next stage, return to the artist, or a human review
//     queue. Anything no rule confidently claims goes to a coordinator.
package router

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/marianina8/amberlight-icr-pipeline/internal/classify"
	"github.com/marianina8/amberlight-icr-pipeline/internal/config"
	"github.com/marianina8/amberlight-icr-pipeline/internal/ingest"
)

// ErrUnknownStage is returned for a current_stage not in the sequence.
var ErrUnknownStage = errors.New("unknown pipeline stage")

// NextStage returns the stage that immediately follows current in seq, or
// final when current is the last stage. It is the only place the pipeline
// order is interpreted.
func NextStage(seq []string, final, current string) (string, error) {
	for i, s := range seq {
		if s != current {
			continue
		}
		if i == len(seq)-1 {
			return final, nil
		}
		return seq[i+1], nil
	}
	return "", fmt.Errorf("%w %q (want one of: %s)", ErrUnknownStage, current, strings.Join(seq, ", "))
}

// Decision is the router's verdict for one item.
type Decision struct {
	Rule        string `json:"rule"`
	Destination string `json:"destination"`
	Queue       string `json:"queue"`
	// CurrentStage is the stage the artist just finished (from the handoff).
	CurrentStage string `json:"current_stage"`
	// NextStage is the looked-up next stage (always recorded for the audit
	// trail, even when the shot is not advancing).
	NextStage string `json:"next_stage"`
	// Advanced is true when this decision moves the shot to NextStage.
	Advanced bool            `json:"advanced"`
	Actions  []config.Action `json:"actions,omitempty"`
	// Automatic is true when the item was routed without a human decision.
	Automatic bool `json:"automatic"`
	// NeedsReview puts the item in a human review queue.
	NeedsReview bool `json:"needs_review"`
	// Reason is the human-readable explanation logged with every decision.
	Reason string `json:"reason"`
}

// Router applies the stage sequence and routing rules from config.
type Router struct {
	pipe config.Pipeline
	cfg  config.Routing
}

// New builds a router from config.
func New(c *config.Config) *Router { return &Router{pipe: c.Pipeline, cfg: c.Routing} }

// Rules exposes the configured rules (read-only use: status, dashboard).
func (r *Router) Rules() []config.Rule { return r.cfg.Rules }

// NextStage looks up the stage after current in the configured sequence.
func (r *Router) NextStage(current string) (string, error) {
	return NextStage(r.pipe.StageSequence, r.pipe.FinalDestination, current)
}

// Route decides where a classified handoff goes. Pure function of its
// inputs: no side effects, so it is trivially testable and safe to dry-run.
func (r *Router) Route(h ingest.Handoff, c classify.Classification) Decision {
	next, err := r.NextStage(h.CurrentStage)
	if err != nil {
		// Should be impossible (ingest validates the stage), but never guess.
		return Decision{Rule: r.cfg.Default.Name, Destination: config.DestCoordinatorReview, Queue: r.cfg.Default.Queue,
			CurrentStage: h.CurrentStage, NeedsReview: true, Reason: err.Error() + "; sent to coordinator review."}
	}
	verified := c.HumanVerified
	label := fmt.Sprintf("readiness=%s confidence=%.2f", c.Readiness, c.Confidence)
	if verified {
		label = fmt.Sprintf("readiness=%s (human-verified)", c.Readiness)
	}
	stages := fmt.Sprintf("stage %s -> next %s", h.CurrentStage, next)
	for _, rule := range r.cfg.Rules {
		if !matches(rule, c) {
			continue
		}
		d := Decision{Rule: rule.Name, Destination: rule.Destination, CurrentStage: h.CurrentStage, NextStage: next,
			Automatic: !verified, Reason: fmt.Sprintf("%s, %s: matched rule %q: %s", label, stages, rule.Name, rule.Description)}
		switch rule.Destination {
		case config.DestSupervisorReview, config.DestCoordinatorReview:
			d.Queue, d.NeedsReview, d.Actions = rule.Queue, true, rule.Actions
			d.Automatic = false
			if verified {
				// A reviewer has already made the call; don't loop it back.
				d.Queue, d.NeedsReview = rule.VerifiedQueue, false
				if d.Queue == "" {
					d.Queue = rule.Queue
				}
				d.Actions = nil // the escalation already happened
				d.Reason += " Confirmed by a reviewer; shot held at " + h.CurrentStage + "."
			}
		case config.DestAdvance:
			d.Queue, d.Advanced, d.Actions = next, true, rule.Actions
		case config.DestReturnToArtist:
			d.Queue, d.Actions = ArtistQueue(h.Artist), rule.Actions
			d.Reason += " Shot stays at " + h.CurrentStage + "."
		}
		return d
	}
	return Decision{Rule: r.cfg.Default.Name, Destination: config.DestCoordinatorReview, Queue: r.cfg.Default.Queue,
		CurrentStage: h.CurrentStage, NextStage: next, NeedsReview: true,
		Reason: fmt.Sprintf("%s, %s: matched no automatic rule; sent to coordinator review.", label, stages)}
}

// matches applies a rule's readiness list and confidence window. Human-
// verified items skip confidence checks: rules that only exist because the
// model was unsure (below_confidence) never match them.
func matches(rule config.Rule, c classify.Classification) bool {
	if len(rule.Readiness) > 0 && !config.Contains(rule.Readiness, c.Readiness) {
		return false
	}
	if c.HumanVerified {
		return rule.BelowConfidence == 0
	}
	if c.Confidence < rule.MinConfidence {
		return false
	}
	if rule.BelowConfidence > 0 && c.Confidence >= rule.BelowConfidence {
		return false
	}
	return true
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ArtistQueue is the per-artist rework queue name: "artist:priya-raman".
func ArtistQueue(artist string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(artist), "-"), "-")
	if s == "" {
		s = "unknown"
	}
	return "artist:" + s
}
