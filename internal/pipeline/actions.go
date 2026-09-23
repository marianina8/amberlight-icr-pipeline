package pipeline

import (
	"sort"

	"github.com/marianina8/amberlight-icr-pipeline/internal/router"
	"github.com/marianina8/amberlight-icr-pipeline/internal/store"
)

// ActionsFromItems rebuilds the action feed from the audit trails. This is
// how the CLI and dashboard show actions when running against DynamoDB,
// where there is no local outbox file.
func ActionsFromItems(items []store.Item) []router.OutboxEntry {
	var out []router.OutboxEntry
	str := func(m map[string]any, k string) string {
		s, _ := m[k].(string)
		return s
	}
	for _, it := range items {
		for _, ev := range it.Events {
			if ev.Type != "action" || ev.Data["action"] == nil {
				continue // skipped/duplicate actions carry no action data
			}
			kind := router.Kind(str(ev.Data, "action"))
			out = append(out, router.OutboxEntry{
				At: ev.At, Kind: kind, Ref: str(ev.Data, "ref"), Target: str(ev.Data, "target"), Detail: str(ev.Data, "detail"),
				Request: router.ActionRequest{ItemID: it.ID, ShotID: it.Handoff.ShotID, Artist: it.Handoff.Artist, Rule: str(ev.Data, "rule"), Reason: str(ev.Data, "reason"),
					Readiness: str(ev.Data, "readiness"), CurrentStage: str(ev.Data, "current_stage"), NextStage: str(ev.Data, "next_stage")},
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}
