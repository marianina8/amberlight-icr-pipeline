// Package config loads the per-instance configuration: the classification
// prompt and taxonomy, the pipeline stage sequence, and the routing rules.
// These are the parts of the ICR pipeline that change per prospect; code
// does not.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/goccy/go-yaml"
)

// Config is the full instance configuration (see config/amberlight.yaml).
type Config struct {
	Instance   Instance   `yaml:"instance"`
	Pipeline   Pipeline   `yaml:"pipeline"`
	Classifier Classifier `yaml:"classifier"`
	Taxonomy   Taxonomy   `yaml:"taxonomy"`
	Prompt     Prompt     `yaml:"prompt"`
	Routing    Routing    `yaml:"routing"`
}

type Instance struct {
	Company string `yaml:"company"`
	UseCase string `yaml:"use_case"`
}

// Pipeline is the studio's fixed stage order. The router looks up the next
// stage here; the model never decides it.
type Pipeline struct {
	StageSequence    []string `yaml:"stage_sequence"`
	FinalDestination string   `yaml:"final_destination"`
}

type Classifier struct {
	Provider string  `yaml:"provider"`
	Bedrock  Bedrock `yaml:"bedrock"`
	Mock     Mock    `yaml:"mock"`
}

type Bedrock struct {
	Region      string  `yaml:"region"`
	ModelID     string  `yaml:"model_id"`
	MaxTokens   int32   `yaml:"max_tokens"`
	Temperature float32 `yaml:"temperature"`
}

// Mock holds keyword lists for the offline keyword classifier.
type Mock struct {
	Readiness map[string][]string `yaml:"readiness"`
}

type Taxonomy struct {
	Readiness []Term `yaml:"readiness"`
}

type Term struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type Prompt struct {
	System string `yaml:"system"`
	User   string `yaml:"user"`
}

type Routing struct {
	Rules   []Rule  `yaml:"rules"`
	Default Default `yaml:"default"`
}

// Destinations a rule can send a shot to.
const (
	DestSupervisorReview  = "supervisor_review"
	DestCoordinatorReview = "coordinator_review"
	DestAdvance           = "advance"
	DestReturnToArtist    = "return_to_artist"
)

// Rule is one routing rule. It matches when the readiness is listed (or the
// list is empty) and the confidence falls in [MinConfidence, BelowConfidence).
type Rule struct {
	Name            string   `yaml:"name"`
	Description     string   `yaml:"description"`
	Readiness       []string `yaml:"readiness"`
	MinConfidence   float64  `yaml:"min_confidence"`
	BelowConfidence float64  `yaml:"below_confidence"`
	Destination     string   `yaml:"destination"`
	Queue           string   `yaml:"queue"`
	// VerifiedQueue is where a human-verified item goes for review
	// destinations (e.g. a supervisor-confirmed block is parked "on-hold").
	VerifiedQueue string   `yaml:"verified_queue"`
	Actions       []Action `yaml:"actions"`
}

// IsReview reports whether the rule sends items to a human.
func (r Rule) IsReview() bool {
	return r.Destination == DestSupervisorReview || r.Destination == DestCoordinatorReview
}

type Action struct {
	Type   string `yaml:"type" json:"type"`
	Target string `yaml:"target" json:"target,omitempty"`
}

// Action types (all stubbed in the demo: logged + recorded in the audit trail).
const (
	ActionNotify          = "notify"            // post to a fixed channel
	ActionNotifyNextStage = "notify_next_stage" // post to <target><next_stage>
	ActionNotifyArtist    = "notify_artist"     // message the originating artist
	ActionUpdateTracker   = "update_tracker"    // move the shot in the production tracker
)

type Default struct {
	Name  string `yaml:"name"`
	Queue string `yaml:"queue"`
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(b)
}

// Parse decodes and validates config YAML.
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalWithOptions(b, &c, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// ReadinessNames returns the allowed readiness labels in config order.
func (c *Config) ReadinessNames() []string {
	out := make([]string, len(c.Taxonomy.Readiness))
	for i, t := range c.Taxonomy.Readiness {
		out[i] = t.Name
	}
	return out
}

// Contains reports whether xs holds s.
func Contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Validate checks internal consistency so a typo in a rule fails loudly at
// startup instead of silently never matching.
func (c *Config) Validate() error {
	seq := c.Pipeline.StageSequence
	if len(seq) == 0 {
		return fmt.Errorf("config: pipeline.stage_sequence needs at least one stage")
	}
	seenStage := map[string]bool{}
	for _, s := range seq {
		if s == "" || s != strings.ToLower(strings.TrimSpace(s)) {
			return fmt.Errorf("config: stage %q must be a non-empty lowercase name", s)
		}
		if seenStage[s] {
			return fmt.Errorf("config: stage %q appears twice in pipeline.stage_sequence", s)
		}
		seenStage[s] = true
	}
	if c.Pipeline.FinalDestination == "" || seenStage[c.Pipeline.FinalDestination] {
		return fmt.Errorf("config: pipeline.final_destination must be set and must not be one of the stages")
	}
	labels := c.ReadinessNames()
	for _, want := range []string{"ready_for_next_stage", "needs_revision", "blocked"} {
		if !Contains(labels, want) {
			return fmt.Errorf("config: taxonomy.readiness must include %q", want)
		}
	}
	if c.Prompt.System == "" || c.Prompt.User == "" {
		return fmt.Errorf("config: prompt.system and prompt.user are required")
	}
	if c.Routing.Default.Queue == "" {
		return fmt.Errorf("config: routing.default.queue is required")
	}
	seen := map[string]bool{}
	for _, r := range c.Routing.Rules {
		if r.Name == "" {
			return fmt.Errorf("config: every routing rule needs a name")
		}
		if seen[r.Name] {
			return fmt.Errorf("config: duplicate routing rule %q", r.Name)
		}
		seen[r.Name] = true
		for _, x := range r.Readiness {
			if !Contains(labels, x) {
				return fmt.Errorf("config: rule %q references unknown readiness %q", r.Name, x)
			}
		}
		if r.MinConfidence < 0 || r.MinConfidence > 1 || r.BelowConfidence < 0 || r.BelowConfidence > 1 {
			return fmt.Errorf("config: rule %q has a confidence threshold outside [0,1]", r.Name)
		}
		if r.BelowConfidence > 0 && r.BelowConfidence <= r.MinConfidence {
			return fmt.Errorf("config: rule %q can never match (below_confidence <= min_confidence)", r.Name)
		}
		switch r.Destination {
		case DestSupervisorReview, DestCoordinatorReview:
			if r.Queue == "" {
				return fmt.Errorf("config: review rule %q needs a queue", r.Name)
			}
		case DestAdvance, DestReturnToArtist:
			if r.Queue != "" {
				return fmt.Errorf("config: rule %q: %s computes its queue; remove queue", r.Name, r.Destination)
			}
		default:
			return fmt.Errorf("config: rule %q has unknown destination %q", r.Name, r.Destination)
		}
		for _, a := range r.Actions {
			switch a.Type {
			case ActionNotify, ActionNotifyNextStage:
				if a.Target == "" {
					return fmt.Errorf("config: rule %q action %s needs a target", r.Name, a.Type)
				}
			case ActionNotifyArtist, ActionUpdateTracker:
			default:
				return fmt.Errorf("config: rule %q has unknown action type %q", r.Name, a.Type)
			}
		}
	}
	for label := range c.Classifier.Mock.Readiness {
		if !Contains(labels, label) {
			return fmt.Errorf("config: classifier.mock.readiness has unknown label %q", label)
		}
	}
	return nil
}
