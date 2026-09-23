package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marianina8/amberlight-icr-pipeline/internal/ingest"
)

// MaxSubmitBytes caps a submission (form, pasted JSON or uploaded file).
const MaxSubmitBytes = 64 << 10

// Example is a ready-made handoff offered on the submit page.
type Example struct {
	Name    string // file name, used as the form value
	Label   string // "03 · AMB-212 · animation"
	Payload []byte
}

// LoadExamples reads *.json handoff fixtures from dir (e.g. demo/handoffs).
// A missing dir yields no examples rather than an error.
func LoadExamples(dir string) ([]Example, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var out []Example
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(m)
		label := strings.TrimSuffix(name, ".json")
		if h, err := ingest.Normalize(b, "example", zeroTime); err == nil {
			label = fmt.Sprintf("%s · %s · %s finished by %s", strings.SplitN(name, "-", 2)[0], h.ShotID, h.CurrentStage, h.Artist)
		}
		out = append(out, Example{Name: name, Label: label, Payload: b})
	}
	return out, nil
}

type submitData struct {
	Page     page
	Examples []Example
	Stages   []string
	Selected string
	JSON     string
	Error    string
}

func (s *Server) submitForm(w http.ResponseWriter, r *http.Request) {
	d := submitData{Page: s.page("Submit a handoff"), Examples: s.opt.Examples, Stages: s.opt.Svc.Cfg.Pipeline.StageSequence, Selected: r.URL.Query().Get("example")}
	for _, ex := range s.opt.Examples {
		if ex.Name == d.Selected {
			d.JSON = string(ex.Payload)
		}
	}
	if d.JSON == "" {
		d.JSON = exampleSkeleton
	}
	s.render(w, "submit.html", d)
}

const exampleSkeleton = `{
  "shot_id": "EX-101-010",
  "show": "Fictional Show",
  "episode": "101",
  "current_stage": "layout",
  "artist": "Alex Example",
  "notes": "Describe the handoff the way an artist would."
}`

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	if s.opt.Svc.Queue == nil {
		s.submitError(w, r, errors.New("this dashboard has no queue configured (start it with -queue-url)"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxSubmitBytes+4096)
	payload, err := submissionPayload(r)
	if err != nil {
		s.submitError(w, r, err)
		return
	}
	ctx := r.Context()
	h, dup, err := s.opt.Svc.IngestIn(ctx, workspace(r), payload, "dashboard")
	if err != nil {
		if errors.Is(err, ingest.ErrInvalid) {
			s.submitError(w, r, err)
			return
		}
		s.fail(w, err)
		return
	}
	msg := "Submitted " + h.ID
	if dup {
		msg = "This exact handoff was already submitted; showing the existing result."
	} else if s.opt.ProcessInline != nil {
		if err := s.opt.ProcessInline(ctx); err != nil {
			s.fail(w, err)
			return
		}
	}
	http.Redirect(w, r, s.url("/items/"+url.PathEscape(h.ID)+"?msg="+url.QueryEscape(msg)), http.StatusSeeOther)
}

func (s *Server) submitError(w http.ResponseWriter, r *http.Request, err error) {
	d := submitData{Page: s.page("Submit a handoff"), Examples: s.opt.Examples, Stages: s.opt.Svc.Cfg.Pipeline.StageSequence,
		JSON: r.FormValue("json"), Error: "Couldn't submit that handoff: " + err.Error()}
	if d.JSON == "" {
		d.JSON = exampleSkeleton
	}
	s.renderStatus(w, http.StatusBadRequest, "submit.html", d)
}

// submissionPayload turns the form into a raw handoff payload: an uploaded
// file, pasted JSON, or the structured form fields.
func submissionPayload(r *http.Request) ([]byte, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(MaxSubmitBytes); err != nil {
			return nil, fmt.Errorf("%w: form too large or malformed", ingest.ErrInvalid)
		}
	} else if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("%w: form too large or malformed", ingest.ErrInvalid)
	}
	if r.FormValue("mode") == "json" {
		if f, _, err := r.FormFile("file"); err == nil {
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, MaxSubmitBytes+1))
			if err != nil {
				return nil, err
			}
			if len(b) > MaxSubmitBytes {
				return nil, fmt.Errorf("%w: file larger than %d KB", ingest.ErrInvalid, MaxSubmitBytes>>10)
			}
			if len(strings.TrimSpace(string(b))) > 0 {
				return b, nil
			}
		}
		j := strings.TrimSpace(r.FormValue("json"))
		if j == "" {
			return nil, fmt.Errorf("%w: paste handoff JSON or choose a file", ingest.ErrInvalid)
		}
		return []byte(j), nil
	}
	f := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	if f("notes") == "" {
		return nil, fmt.Errorf("%w: the notes are empty", ingest.ErrInvalid)
	}
	return json.Marshal(map[string]string{
		"shot_id": f("shot_id"), "show": f("show"), "episode": f("episode"),
		"current_stage": f("current_stage"), "artist": f("artist"), "notes": f("notes"),
	})
}

var zeroTime = time.Time{}
