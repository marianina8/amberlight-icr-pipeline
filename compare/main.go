// Command compare runs the demo shot handoffs through a naive keyword-rules
// classifier and (optionally) the real Bedrock classifier, routes both
// through the same router, and prints a side-by-side table against each
// fixture's intended outcome (demo/expected.json).
//
// It is a small add-on that shows why readiness needs judgment rather than
// keyword matching, not a second pipeline.
//
//	go run ./compare                                  # naive rules only (offline)
//	go run ./compare -bedrock -profile demos-admin    # + one live Bedrock call per fixture
//	go run ./compare -bedrock -out compare/RESULTS.md # save the table
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/marianina8/amberlight-icr-pipeline/internal/classify"
	"github.com/marianina8/amberlight-icr-pipeline/internal/config"
	"github.com/marianina8/amberlight-icr-pipeline/internal/ingest"
	"github.com/marianina8/amberlight-icr-pipeline/internal/router"
)

type expectation struct {
	Readiness string `json:"readiness"` // "" = any verdict, as long as a human reviews it
	Route     string `json:"route"`     // advance | return_to_artist | supervisor_review | coordinator_review
	Why       string `json:"why"`
}

type result struct {
	c classify.Classification
	d router.Decision
}

func main() {
	var (
		cfgPath = flag.String("config", "config/amberlight.yaml", "instance config")
		dir     = flag.String("fixtures", "demo/handoffs", "fixture directory")
		expPath = flag.String("expected", "demo/expected.json", "intended outcomes")
		bedrock = flag.Bool("bedrock", false, "also run the real Bedrock classifier (one model call per fixture)")
		profile = flag.String("profile", os.Getenv("AWS_PROFILE"), "AWS profile for -bedrock")
		modelID = flag.String("model", "", "override the Bedrock model / inference profile ID")
		outPath = flag.String("out", "", "also write the table to this file")
	)
	flag.Parse()
	if err := run(*cfgPath, *dir, *expPath, *bedrock, *profile, *modelID, *outPath); err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		os.Exit(1)
	}
}

func run(cfgPath, dir, expPath string, useBedrock bool, profile, modelID, outPath string) error {
	ctx := context.Background()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	var exp struct {
		Fixtures map[string]expectation `json:"fixtures"`
	}
	b, err := os.ReadFile(expPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		return fmt.Errorf("%s: %w", expPath, err)
	}
	rt := router.New(cfg)

	var model classify.Classifier
	if useBedrock {
		if modelID != "" {
			cfg.Classifier.Bedrock.ModelID = modelID
		}
		opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Classifier.Bedrock.Region)}
		if profile != "" {
			opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return err
		}
		if model, err = classify.NewBedrock(bedrockruntime.NewFromConfig(awsCfg), cfg); err != nil {
			return err
		}
	}

	names := make([]string, 0, len(exp.Fixtures))
	for n := range exp.Fixtures {
		names = append(names, n)
	}
	sort.Strings(names)

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Naive keyword rules vs %s on the Amberlight fixtures\n\n", map[bool]string{true: "Bedrock (" + cfg.Classifier.Bedrock.ModelID + ")", false: "the intended outcome"}[useBedrock])
	fmt.Fprintf(&sb, "Generated %s. Both classifiers' verdicts go through the same router and stage lookup; ✗ marks a route that differs from the intended one.\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	header := "| Fixture | Stage | Intended | Naive rules | Naive route |"
	sep := "|---|---|---|---|---|"
	if useBedrock {
		header += " Bedrock | Bedrock route |"
		sep += "---|---|"
	}
	fmt.Fprintln(&sb, header)
	fmt.Fprintln(&sb, sep)

	naiveWrong, modelWrong, naiveUnsafe, modelUnsafe := 0, 0, 0, 0
	var notes []string
	for _, n := range names {
		e := exp.Fixtures[n]
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return err
		}
		h, err := ingest.Normalize(raw, "compare", time.Now())
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		intended := e.Route
		if e.Readiness != "" {
			intended = short(e.Readiness) + " → " + e.Route
		}
		nc := naiveClassify(h)
		nr := result{nc, rt.Route(h, nc)}
		ok := routeOK(e, nr.d)
		if !ok {
			naiveWrong++
			if nr.d.Advanced {
				naiveUnsafe++
			}
		}
		row := fmt.Sprintf("| %s | %s → %s | %s | %s | %s |", strings.TrimSuffix(n, ".json"), h.CurrentStage, nr.d.NextStage, intended,
			fmt.Sprintf("%s @ %.2f (%s)", short(nc.Readiness), nc.Confidence, nc.Reasoning), mark(ok, nr.d))
		if model != nil {
			mc, err := model.Classify(ctx, h)
			if err != nil {
				mc = classify.Fallback(err, model.Name(), time.Now())
			}
			md := rt.Route(h, mc)
			mok := routeOK(e, md)
			if !mok {
				modelWrong++
				if md.Advanced {
					modelUnsafe++
				}
			}
			row += fmt.Sprintf(" %s @ %.2f | %s |", short(mc.Readiness), mc.Confidence, mark(mok, md))
			notes = append(notes, fmt.Sprintf("- **%s** — Bedrock: %s", strings.TrimSuffix(n, ".json"), mc.Reasoning))
		}
		fmt.Fprintln(&sb, row)
	}
	fmt.Fprintf(&sb, "\n**Naive rules:** %d of %d routed differently than intended; %d of those auto-advanced a shot that should not have moved.\n", naiveWrong, len(names), naiveUnsafe)
	if model != nil {
		fmt.Fprintf(&sb, "\n**Bedrock:** %d of %d routed differently than intended; %d auto-advanced a shot that should not have moved.\n", modelWrong, len(names), modelUnsafe)
		fmt.Fprintf(&sb, "\n## Bedrock's reasoning per fixture\n\n%s\n", strings.Join(notes, "\n"))
	}
	fmt.Fprintf(&sb, "\n## Why each fixture is there\n\n")
	for _, n := range names {
		fmt.Fprintf(&sb, "- **%s** — %s\n", strings.TrimSuffix(n, ".json"), exp.Fixtures[n].Why)
	}

	var w io.Writer = os.Stdout
	fmt.Fprint(w, sb.String())
	if outPath != "" {
		return os.WriteFile(outPath, []byte(sb.String()), 0o644)
	}
	return nil
}

// routeOK compares where a decision sent the shot with where it should go.
func routeOK(e expectation, d router.Decision) bool {
	return d.Destination == e.Route
}

func mark(ok bool, d router.Decision) string {
	s := d.Destination
	switch d.Destination {
	case config.DestAdvance:
		s = "advance → " + d.Queue
	case config.DestReturnToArtist:
		s = "return_to_artist"
	}
	if !ok {
		s = "✗ " + s
	}
	return s
}

func short(readiness string) string {
	switch readiness {
	case classify.Ready:
		return "ready"
	case classify.NeedsRevision:
		return "needs_revision"
	}
	return readiness
}
