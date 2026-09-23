package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/marianina8/amberlight-icr-pipeline/internal/testutil"
)

type cli struct {
	t    *testing.T
	base []string
}

func newCLI(t *testing.T) cli {
	return cli{t: t, base: []string{"-config", testutil.ConfigPath(t), "-data", t.TempDir()}}
}

func (c cli) run(stdin string, args ...string) (string, string, int) {
	c.t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), append(append([]string{}, c.base...), args...), strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func handoffs(t *testing.T) string { return filepath.Join(testutil.RepoRoot(t), "demo", "handoffs") }

func TestIngestProcessStatus(t *testing.T) {
	c := newCLI(t)
	out, errOut, code := c.run("", "ingest", "-process", handoffs(t))
	if code != 0 {
		t.Fatalf("ingest exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "processed 10 item(s)") {
		t.Errorf("ingest output: %s", out)
	}
	// re-ingest is idempotent
	out, _, _ = c.run("", "ingest", handoffs(t))
	if strings.Count(out, "duplicate") != 10 {
		t.Errorf("second ingest should report 10 duplicates: %s", out)
	}

	out, _, code = c.run("", "-json", "status")
	if code != 0 {
		t.Fatal(code)
	}
	var st struct {
		Stats struct {
			Total       int `json:"total"`
			Automatic   int `json:"automatic"`
			NeedsReview int `json:"needs_review"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if st.Stats.Total != 10 || st.Stats.NeedsReview != 9 {
		t.Errorf("stats: %+v", st.Stats)
	}

	out, _, _ = c.run("", "status", "-review")
	if strings.Count(out, "YES") != 9 {
		t.Errorf("review filter:\n%s", out)
	}

	out, _, _ = c.run("", "outbox")
	for _, want := range []string{"#amberlight-supervisors", "#amberlight-delivered", "tracker", "PLC-103-110: compositing -> delivered", "why:"} {
		if !strings.Contains(out, want) {
			t.Errorf("outbox missing %q:\n%s", want, out)
		}
	}
}

func TestClassifyPreviewIsDryRun(t *testing.T) {
	c := newCLI(t)
	out, errOut, code := c.run("", "classify", "-file", filepath.Join(handoffs(t), "04-lighting-buried-missing-asset.json"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"readiness   ready_for_next_stage", "stage       lighting -> compositing", `queue "coordinator-review"`, "dry run"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	out, _, _ = c.run("", "status")
	if !strings.HasPrefix(out, "0 item(s)") {
		t.Errorf("preview must not store anything:\n%s", out)
	}
}

func TestReviewRouteAndItemDetail(t *testing.T) {
	c := newCLI(t)
	out, _, _ := c.run("", "ingest", "-process", filepath.Join(handoffs(t), "08-animation-approved-but-redo.json"))
	id := regexp.MustCompile(`AL-[0-9A-F]{8}`).FindString(out)
	if id == "" {
		t.Fatalf("no id in: %s", out)
	}
	out, _, _ = c.run("", "route", "-dry-run", id)
	if !strings.Contains(out, `queue "coordinator-review"`) || !strings.Contains(out, "animation -> lighting") {
		t.Errorf("dry-run route: %s", out)
	}
	out, errOut, code := c.run("", "review", "override", id, "-reviewer", "sam", "-readiness", "needs_revision", "-note", "eyeline redo is still open")
	if code != 0 {
		t.Fatalf("override exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "queue=artist:keiko-ambrose") || !strings.Contains(out, "human-verified") {
		t.Errorf("override output:\n%s", out)
	}
	out, _, _ = c.run("", "status", id)
	for _, want := range []string{"audit trail:", "received", "classified", "routed", "review", "cli:sam", "notify_artist", "@keiko-ambrose"} {
		if !strings.Contains(out, want) {
			t.Errorf("item detail missing %q:\n%s", want, out)
		}
	}
}

func TestMCPOverCLI(t *testing.T) {
	c := newCLI(t)
	if _, errOut, code := c.run("", "ingest", "-process", handoffs(t)); code != 0 {
		t.Fatal(errOut)
	}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_queue","arguments":{}}}`,
	}, "\n")
	out, errOut, code := c.run(in, "mcp")
	if code != 0 {
		t.Fatalf("mcp exit %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "none — read-only") {
		t.Errorf("stderr should announce read-only mode: %s", errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || strings.Contains(lines[1], "route_item") || !strings.Contains(lines[2], `\"count\": 9`) {
		t.Errorf("mcp output:\n%s", out)
	}
	if _, errOut, code := c.run("", "mcp", "-allow-write", "all"); code != 2 || !strings.Contains(errOut, "unknown write tool") {
		t.Errorf("-allow-write all should be rejected: %d %s", code, errOut)
	}
}

func TestUsageErrors(t *testing.T) {
	c := newCLI(t)
	for _, args := range [][]string{{}, {"bogus"}, {"route"}, {"ingest"}, {"reset"}} {
		if _, _, code := c.run("", args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestStagesCommand(t *testing.T) {
	c := newCLI(t)
	out, errOut, code := c.run("", "stages")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"storyboard  layout", "lighting    compositing", "compositing  delivered"} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("stages missing %q:\n%s", want, out)
		}
	}
}

func TestIngestRejectsUnknownStage(t *testing.T) {
	c := newCLI(t)
	out, _, code := c.run(`{"shot_id":"X-1","current_stage":"rigging","artist":"A","notes":"done"}`, "ingest", "-")
	if code != 1 || !strings.Contains(out, "REJECTED") || !strings.Contains(out, "unknown pipeline stage") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}
