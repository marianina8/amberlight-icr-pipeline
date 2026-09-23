package main

import (
	"strings"

	"github.com/marianina8/amberlight-icr-pipeline/internal/classify"
	"github.com/marianina8/amberlight-icr-pipeline/internal/ingest"
)

// naiveRules is what a studio might script before reaching for a model: an
// ordered list of "if the note mentions X, it's Y" rules, first match wins.
// It is deliberately simple and reasonable-looking. It has no notion of
// sarcasm, hedging, or which clause of a sentence actually matters.
var naiveRules = []struct {
	readiness string
	words     []string
}{
	{classify.Blocked, []string{"blocked", "waiting on", "missing", "can't", "cannot", "crash", "no access"}},
	{classify.Ready, []string{"ready", "approved", "signed off", "final", "done", "good to go", "locked", "no notes"}},
	{classify.NeedsRevision, []string{"fix", "redo", "revis", "notes", "wrong", "retake"}},
}

// naiveClassify returns the first matching rule's readiness. A rule match is
// treated as confident (0.8); no match defaults to needs_revision at 0.4, so
// the router still sends unknowns to a coordinator.
func naiveClassify(h ingest.Handoff) classify.Classification {
	text := strings.ToLower(strings.NewReplacer("’", "'", "‘", "'").Replace(h.Notes))
	for _, r := range naiveRules {
		for _, w := range r.words {
			if strings.Contains(text, w) {
				return classify.Classification{Readiness: r.readiness, Confidence: 0.8, Reasoning: "matched " + `"` + w + `"`, Classifier: "naive-rules"}
			}
		}
	}
	return classify.Classification{Readiness: classify.NeedsRevision, Confidence: 0.4, Reasoning: "no rule matched", Classifier: "naive-rules"}
}
