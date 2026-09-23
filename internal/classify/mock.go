package classify

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/marianina8/amberlight-icr-pipeline/internal/config"
	"github.com/marianina8/amberlight-icr-pipeline/internal/ingest"
)

// Mock is a deterministic keyword classifier. It exists so every phase of the
// pipeline can be built, tested and demoed with zero network and zero AWS.
// It is intentionally naive — it stands in for Bedrock; it is not the product.
// compare/ runs it against the fixtures to show where keyword matching fails.
type Mock struct {
	labels []scored // in taxonomy order, for stable tie-breaks
	now    func() time.Time
}

type scored struct {
	name     string
	patterns []*regexp.Regexp
	words    []string
}

// NewMock builds a mock classifier from the config keyword lists.
func NewMock(c *config.Config) (*Mock, error) {
	m := &Mock{now: time.Now}
	for _, label := range c.ReadinessNames() {
		words := c.Classifier.Mock.Readiness[label]
		ps, err := compile(words)
		if err != nil {
			return nil, fmt.Errorf("mock keywords for %s: %w", label, err)
		}
		m.labels = append(m.labels, scored{name: label, patterns: ps, words: words})
	}
	return m, nil
}

// KeywordRegexp compiles a config keyword into a case-insensitive whole-word
// pattern; a trailing "*" means prefix match (e.g. "fix*").
func KeywordRegexp(kw string) (*regexp.Regexp, error) {
	kw = strings.ToLower(strings.TrimSpace(kw))
	suffix := `\b`
	if strings.HasSuffix(kw, "*") {
		kw, suffix = strings.TrimSuffix(kw, "*"), ``
	}
	return regexp.Compile(`(?i)\b` + regexp.QuoteMeta(kw) + suffix)
}

func compile(words []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(words))
	for _, w := range words {
		re, err := KeywordRegexp(w)
		if err != nil {
			return nil, err
		}
		out = append(out, re)
	}
	return out, nil
}

// Name identifies this classifier in the audit trail.
func (m *Mock) Name() string { return "mock-keyword-v1" }

// Classify scores each readiness label by distinct keyword hits and derives
// a confidence from how many hits the winner has and how far ahead it is.
func (m *Mock) Classify(_ context.Context, h ingest.Handoff) (Classification, error) {
	text := normalizeText(h.Notes)

	type hit struct {
		name  string
		score int
		words []string
	}
	var hits []hit
	for _, l := range m.labels {
		x := hit{name: l.name}
		for i, re := range l.patterns {
			if re.MatchString(text) {
				x.score++
				x.words = append(x.words, l.words[i])
			}
		}
		hits = append(hits, x)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	top := hits[0]
	second := hit{}
	if len(hits) > 1 {
		second = hits[1]
	}

	c := Classification{Classifier: m.Name(), ClassifiedAt: m.now().UTC()}
	if top.score == 0 {
		c.Readiness, c.Confidence = NeedsRevision, 0.35
		c.Reasoning = "mock: no readiness keywords matched"
		return c, nil
	}
	c.Readiness = top.name
	strength := float64(min(top.score, 3))
	conf := 0.5 + 0.15*strength - 0.25*float64(second.score)/float64(top.score)
	c.Confidence = round2(clamp(conf, 0.05, 0.95))
	c.Reasoning = fmt.Sprintf("mock: %d keyword(s) for %s %q", top.score, top.name, top.words)
	if second.score > 0 {
		c.Reasoning += fmt.Sprintf("; runner-up %s (%d)", second.name, second.score)
	}
	return c, nil
}

func normalizeText(s string) string {
	return strings.NewReplacer("’", "'", "‘", "'", "“", `"`, "”", `"`).Replace(s)
}

func clamp(f, lo, hi float64) float64 {
	if f < lo {
		return lo
	}
	if f > hi {
		return hi
	}
	return f
}
