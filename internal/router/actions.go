package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marianina8/amberlight-icr-pipeline/internal/config"
)

// ActionRequest carries everything an action needs, including the reasoning
// that triggered it (so the downstream record is self-explanatory).
type ActionRequest struct {
	Action       config.Action `json:"action"`
	ItemID       string        `json:"item_id"`
	ShotID       string        `json:"shot_id"`
	Artist       string        `json:"artist"`
	CurrentStage string        `json:"current_stage"`
	NextStage    string        `json:"next_stage"`
	Readiness    string        `json:"readiness"`
	Confidence   float64       `json:"confidence"`
	Reasoning    string        `json:"reasoning"`
	Rule         string        `json:"rule"`
	Reason       string        `json:"reason"`
}

// ActionResult is what an action produced (a message ref, a tracker update).
type ActionResult struct {
	Target string `json:"target"`
	Ref    string `json:"ref"`
	Detail string `json:"detail"`
}

// ActionSink performs routing actions. The demo uses Outbox (JSONL files)
// locally and LogSink in AWS; a real deployment swaps in Slack and the
// studio's production tracker (ShotGrid, ftrack, …) behind this interface.
type ActionSink interface {
	Execute(ctx context.Context, req ActionRequest) (ActionResult, error)
}

// Describe renders an action deterministically (shared by every sink).
func Describe(req ActionRequest) (ActionResult, error) {
	a := req.Action
	switch a.Type {
	case config.ActionNotify:
		return ActionResult{Target: a.Target, Ref: "msg:" + a.Target + ":" + req.ItemID,
			Detail: fmt.Sprintf("%s (%s) %s @ %.2f — %s", req.ShotID, req.CurrentStage, strings.ToUpper(req.Readiness), req.Confidence, req.Reasoning)}, nil
	case config.ActionNotifyNextStage:
		ch := a.Target + req.NextStage
		return ActionResult{Target: ch, Ref: "msg:" + ch + ":" + req.ItemID,
			Detail: fmt.Sprintf("%s moves to %s (%s finished by %s)", req.ShotID, req.NextStage, req.CurrentStage, req.Artist)}, nil
	case config.ActionNotifyArtist:
		to := "@" + strings.TrimPrefix(ArtistQueue(req.Artist), "artist:")
		return ActionResult{Target: to, Ref: "dm:" + to + ":" + req.ItemID,
			Detail: fmt.Sprintf("%s needs another pass at %s: %s", req.ShotID, req.CurrentStage, req.Reasoning)}, nil
	case config.ActionUpdateTracker:
		return ActionResult{Target: "tracker", Ref: "tracker:" + req.ShotID + ":" + req.NextStage,
			Detail: fmt.Sprintf("%s: %s -> %s", req.ShotID, req.CurrentStage, req.NextStage)}, nil
	default:
		return ActionResult{}, fmt.Errorf("unknown action type %q", a.Type)
	}
}

// Kind groups actions for display: chat messages vs tracker updates.
func Kind(actionType string) string {
	if actionType == config.ActionUpdateTracker {
		return "tracker"
	}
	return "slack"
}

// Outbox is the local stub ActionSink: each action appends a JSON line to
// <dir>/<kind>.jsonl, which the dashboard and `icr outbox` display.
type Outbox struct {
	Dir string
	mu  sync.Mutex
	now func() time.Time
}

// NewOutbox creates the outbox directory.
func NewOutbox(dir string) (*Outbox, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Outbox{Dir: dir, now: time.Now}, nil
}

// OutboxEntry is one line in an outbox file.
type OutboxEntry struct {
	At      time.Time     `json:"at"`
	Kind    string        `json:"kind"`
	Ref     string        `json:"ref"`
	Target  string        `json:"target,omitempty"`
	Request ActionRequest `json:"request"`
	Detail  string        `json:"detail"`
}

// Execute performs a stubbed action.
func (o *Outbox) Execute(_ context.Context, req ActionRequest) (ActionResult, error) {
	res, err := Describe(req)
	if err != nil {
		return res, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	kind := Kind(req.Action.Type)
	e := OutboxEntry{At: o.now().UTC(), Kind: kind, Ref: res.Ref, Target: res.Target, Request: req, Detail: res.Detail}
	b, err := json.Marshal(e)
	if err != nil {
		return res, err
	}
	f, err := os.OpenFile(filepath.Join(o.Dir, kind+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return res, err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return res, err
}

// ReadOutbox returns all outbox entries across kinds, oldest first.
func ReadOutbox(dir string) ([]OutboxEntry, error) {
	var all []OutboxEntry
	for _, kind := range []string{"slack", "tracker"} {
		b, err := os.ReadFile(filepath.Join(dir, kind+".jsonl"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line == "" {
				continue
			}
			var e OutboxEntry
			if err := json.Unmarshal([]byte(line), &e); err == nil {
				all = append(all, e)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	return all, nil
}
