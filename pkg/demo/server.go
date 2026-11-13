package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
	"text/template"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
	"github.com/Git-Hospital/meshmedic/pkg/detect"
)

// Request is one query the detector sent to the replayed Prometheus.
type Request struct {
	Step   int
	Role   string // "coverage probe", "signal <id>", "evidence <name>", or "unexpected"
	PromQL string
	Body   string // the response body that was served
}

// replay is a Prometheus HTTP API (instant queries only) that answers from a
// fixture. It decides which answer a query gets by rendering the catalog's own
// query templates with the fixture's target, so a changed catalog query is
// still recognised, and a query nothing in the fixture explains is refused
// rather than answered with a guess.
type replay struct {
	fx    Fixture
	roles map[string]role // rendered PromQL -> what it asks for

	mu       sync.Mutex
	step     int
	requests []Request
}

type role struct {
	label    string
	evidence string // evidence name, for evidence queries
	signal   bool
	coverage bool
}

func newReplay(fx Fixture, scenarios []catalog.Scenario) (*replay, error) {
	r := &replay{fx: fx, roles: map[string]role{}}
	add := func(name, promql string, ro role) error {
		q, err := render(name, promql, fx.Target)
		if err != nil {
			return err
		}
		if _, dup := r.roles[q]; !dup {
			r.roles[q] = ro
		}
		return nil
	}
	if err := add("coverage-probe", detect.DefaultCoverageProbe, role{label: "coverage probe", coverage: true}); err != nil {
		return nil, err
	}
	for _, s := range scenarios {
		if err := add(s.ID, s.Signal.PromQL, role{label: "signal " + s.ID, signal: true}); err != nil {
			return nil, err
		}
		for _, e := range s.Evidence {
			if err := add(s.ID+"/"+e.Name, e.PromQL, role{label: "evidence " + e.Name, evidence: e.Name}); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func render(name, promql string, params map[string]string) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(promql)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, params); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (r *replay) setStep(i int) {
	r.mu.Lock()
	r.step = i
	r.mu.Unlock()
}

func (r *replay) log() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Request(nil), r.requests...)
}

func (r *replay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/api/v1/query" {
		http.NotFound(w, req)
		return
	}
	q := req.URL.Query().Get("query")
	r.mu.Lock()
	defer r.mu.Unlock()
	step := r.fx.Steps[r.step]
	ro, known := r.roles[q]

	var series []Series
	switch {
	case !known:
	case !step.Telemetry:
	case ro.coverage:
		series = []Series{{Labels: map[string]string{}, Value: r.fx.CoverageSeries}}
	case ro.signal:
		if step.Signal != nil {
			series = []Series{{Labels: map[string]string{}, Value: *step.Signal}}
		}
	default:
		series = r.fx.Evidence[ro.evidence]
	}

	if !known {
		r.requests = append(r.requests, Request{Step: r.step, Role: "unexpected", PromQL: q, Body: "400 Bad Request"})
		http.Error(w, "the replay holds no answer for this query", http.StatusBadRequest)
		return
	}
	body := vector(series, step.At)
	r.requests = append(r.requests, Request{Step: r.step, Role: ro.label, PromQL: q, Body: body})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// vector renders a Prometheus instant-query response.
func vector(series []Series, at time.Time) string {
	type result struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`
	}
	out := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string   `json:"resultType"`
			Result     []result `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	out.Data.ResultType = "vector"
	out.Data.Result = []result{}
	for _, s := range series {
		m := s.Labels
		if m == nil {
			m = map[string]string{}
		}
		out.Data.Result = append(out.Data.Result, result{
			Metric: m,
			Value:  [2]any{at.Unix(), strconv.FormatFloat(s.Value, 'g', -1, 64)},
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// serve starts the replay on an ephemeral loopback port. The returned URL is
// the Prometheus base URL; stop shuts the server down.
func (r *replay) serve() (url string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}, nil
}
