// Package ingest normalizes raw shot-handoff events into one Handoff shape,
// and provides the local stand-ins for the AWS ingestion path: a
// directory-backed queue (SQS), a drop-zone folder (S3 bucket + event
// notification) and an HTTP webhook receiver (API Gateway + Lambda).
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Handoff is the normalized message every entry point produces and the
// worker consumes. In AWS this is the SQS message body.
//
// CurrentStage is plain metadata supplied by whoever triggers the handoff
// (the production tracker, a submit form, a person) — it is never inferred.
type Handoff struct {
	ID           string    `json:"id"`
	Source       string    `json:"source"` // cli | dropzone | webhook | dashboard
	ReceivedAt   time.Time `json:"received_at"`
	ShotID       string    `json:"shot_id"`
	Show         string    `json:"show,omitempty"`
	Episode      string    `json:"episode,omitempty"`
	CurrentStage string    `json:"current_stage"`
	Artist       string    `json:"artist"`
	Notes        string    `json:"notes"`
	// Workspace scopes a handoff to one private demo sandbox ("" = shared).
	Workspace string `json:"workspace,omitempty"`
}

// Title is a one-line label for lists: "AMB-104 · layout".
func (h Handoff) Title() string {
	return h.ShotID + " · " + h.CurrentStage
}

// raw is the handoff payload shape (demo/handoffs/*.json, webhook body).
type raw struct {
	ShotID       string `json:"shot_id"`
	Show         string `json:"show"`
	Episode      string `json:"episode"`
	CurrentStage string `json:"current_stage"`
	Artist       string `json:"artist"`
	Notes        string `json:"notes"`
	SubmittedAt  string `json:"submitted_at"`
}

// ErrInvalid marks payloads that can never be ingested (bad JSON, missing
// fields, unknown stage). Entry points map it to a 4xx / rejected file.
var ErrInvalid = errors.New("invalid handoff payload")

// MaxNotes caps the notes text.
const MaxNotes = 8000

// Normalize converts a raw payload (JSON) into a Handoff. source records
// which entry point received it. Stage validity against the configured
// sequence is checked by the pipeline, which has the config.
func Normalize(payload []byte, source string, now time.Time) (Handoff, error) {
	var r raw
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	if err := dec.Decode(&r); err != nil {
		return Handoff{}, fmt.Errorf("%w: decode: %v", ErrInvalid, err)
	}
	h := Handoff{
		Source:       source,
		ShotID:       strings.ToUpper(strings.TrimSpace(r.ShotID)),
		Show:         strings.TrimSpace(r.Show),
		Episode:      strings.TrimSpace(r.Episode),
		CurrentStage: strings.ToLower(strings.TrimSpace(r.CurrentStage)),
		Artist:       strings.TrimSpace(r.Artist),
		Notes:        strings.TrimSpace(r.Notes),
	}
	var missing []string
	for _, f := range []struct{ name, v string }{{"shot_id", h.ShotID}, {"current_stage", h.CurrentStage}, {"artist", h.Artist}, {"notes", h.Notes}} {
		if f.v == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return Handoff{}, fmt.Errorf("%w: missing %s", ErrInvalid, strings.Join(missing, ", "))
	}
	if len(h.Notes) > MaxNotes {
		return Handoff{}, fmt.Errorf("%w: notes longer than %d characters", ErrInvalid, MaxNotes)
	}
	if len(h.ShotID) > 64 || len(h.Artist) > 120 || len(h.Show) > 120 || len(h.Episode) > 64 {
		return Handoff{}, fmt.Errorf("%w: a field is too long", ErrInvalid)
	}
	h.ReceivedAt = now.UTC()
	if r.SubmittedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, r.SubmittedAt); err == nil {
			h.ReceivedAt = parsed.UTC()
		}
	}
	h.ID = HandoffID(h)
	return h, nil
}

// HandoffID is a stable content hash, so re-ingesting the same handoff (a
// retry, a duplicate webhook delivery) is idempotent.
func HandoffID(h Handoff) string {
	parts := []string{h.ShotID, h.CurrentStage, strings.ToLower(h.Artist), h.Notes}
	if h.Workspace != "" {
		// The same handoff submitted in two sandboxes is two different items.
		parts = append(parts, "ws:"+h.Workspace)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return "AL-" + strings.ToUpper(hex.EncodeToString(sum[:4]))
}
