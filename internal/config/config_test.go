package config_test

import (
	"strings"
	"testing"

	"github.com/marianina8/amberlight-icr-pipeline/internal/config"
	"github.com/marianina8/amberlight-icr-pipeline/internal/testutil"
)

func TestAmberlightConfigLoads(t *testing.T) {
	c := testutil.Config(t)
	if got := strings.Join(c.ReadinessNames(), ","); got != "ready_for_next_stage,needs_revision,blocked" {
		t.Errorf("readiness = %s", got)
	}
	var order []string
	for _, r := range c.Routing.Rules {
		order = append(order, r.Name)
	}
	// Precedence from the spec: blocked, low confidence, advance, revision.
	if got := strings.Join(order, ","); got != "blocked-escalate,low-confidence,auto-advance,return-to-artist" {
		t.Errorf("rule order = %s", got)
	}
	if c.Routing.Rules[0].MinConfidence != 0 || c.Routing.Rules[0].BelowConfidence != 0 {
		t.Error("blocked must escalate regardless of confidence")
	}
	if c.Routing.Rules[1].BelowConfidence != 0.6 || c.Routing.Rules[2].MinConfidence != 0.75 || c.Routing.Rules[3].MinConfidence != 0.6 {
		t.Error("thresholds drifted from the spec (0.6 / 0.75 / 0.6)")
	}
}

func mutate(t *testing.T, old, new string) error {
	t.Helper()
	b := testutil.ConfigBytes(t)
	s := string(b)
	if !strings.Contains(s, old) {
		t.Fatalf("config does not contain %q", old)
	}
	_, err := config.Parse([]byte(strings.Replace(s, old, new, 1)))
	return err
}

func TestValidateRejectsMistakes(t *testing.T) {
	cases := []struct{ name, old, new, want string }{
		{"duplicate stage", "stage_sequence: [storyboard, layout,", "stage_sequence: [storyboard, storyboard,", "twice"},
		{"final destination is a stage", "final_destination: delivered", "final_destination: lighting", "final_destination"},
		{"missing blocked label", "    - name: blocked\n", "    - name: stuck\n", "blocked"},
		{"unknown readiness in rule", "readiness: [blocked]", "readiness: [stuck]", "unknown readiness"},
		{"unknown destination", "destination: advance", "destination: teleport", "unknown destination"},
		{"advance with a fixed queue", "destination: advance", "destination: advance\n      queue: lighting", "computes its queue"},
		{"unknown action", "type: notify_artist", "type: email_everyone", "unknown action"},
		{"threshold out of range", "min_confidence: 0.75", "min_confidence: 7.5", "outside"},
		{"typo'd key is rejected", "below_confidence: 0.6", "below_confidance: 0.6", "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mutate(t, tc.old, tc.new)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
