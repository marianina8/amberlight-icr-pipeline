package router

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// LogSink is the ActionSink used in AWS until real Slack / production-tracker
// integrations are wired in: every action is written as one structured JSON
// line (CloudWatch Logs in Lambda), and the item's audit trail records it
// too. References are derived from the item ID so they are stable across
// Lambda invocations without a counter.
type LogSink struct {
	W   io.Writer
	Now func() time.Time
	mu  sync.Mutex
}

// Execute logs the action and returns a deterministic reference.
func (l *LogSink) Execute(_ context.Context, req ActionRequest) (ActionResult, error) {
	res, err := Describe(req)
	if err != nil {
		return res, err
	}
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	line, err := json.Marshal(map[string]any{
		"msg": "icr_action", "at": now().UTC(), "action": req.Action.Type, "target": res.Target,
		"ref": res.Ref, "detail": res.Detail, "item_id": req.ItemID, "shot_id": req.ShotID, "rule": req.Rule, "reason": req.Reason,
		"readiness": req.Readiness, "confidence": req.Confidence, "current_stage": req.CurrentStage, "next_stage": req.NextStage,
	})
	if err != nil {
		return ActionResult{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.W.Write(append(line, '\n'))
	return res, err
}
