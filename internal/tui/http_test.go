package tui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"handloom/internal/client"
)

// The hub tags only the outer fields of its metrics; this is the shape it sends.
const metricsJSON = `{"range":"7d","since":"2026-10-01T00:00:00Z","needs_you":{"count":2,"oldest_ns":300000000000},
"agents":{"Total":3,"Working":1,"Idle":1,"Blocked":1,"Offline":0,"Other":0},
"tasks":{"Claimed":2,"Submitted":1,"Open":4,"AwaitingCheck":0},
"checks":{"Pct":-1,"PriorPct":-1,"N":0,"PriorN":0},
"task_time":{"Value":720000000000,"Prior":0,"N":5,"PriorN":0},
"answer_time":{"Value":0,"Prior":0,"N":0,"PriorN":0},
"fleet":[{"Name":"lead-1","Kind":"claude","Role":"lead","Device":"box","State":"working","Job":1,"Task":"#4 Write docs","StateFor":1000000000}],
"buckets":[{"Label":"Mon","Accepted":2,"Rejected":1,"Passed":3,"Failed":0,"TimedOut":0},{"Label":"Tue","Accepted":0,"Rejected":0,"Passed":0,"Failed":1,"TimedOut":0}],
"utilization":[{"Agent":"lead-1","Working":60,"Idle":40,"Blocked":0,"Off":0}],
"profiles":[],
"insights":[{"Sev":"warn","Title":"A question is waiting","Detail":"d","Basis":"1 open question","Actions":[]}],
"usage":null}`

func TestMetricsDecodeTheHubsShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" || r.URL.Query().Get("range") != "30d" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"error":"bad request","code":"bad"}`, 400)
			return
		}
		w.Write([]byte(metricsJSON))
	}))
	defer srv.Close()
	m, err := NewHTTP(client.Direct(srv.URL, "tok")).Metrics("30d")
	if err != nil {
		t.Fatal(err)
	}
	if m.Needs.Count != 2 || m.Needs.Oldest != 5*time.Minute || m.Agents.Working != 1 || m.Agents.Blocked != 1 ||
		m.Tasks.Claimed != 2 || m.Checks.Pct != -1 || m.TaskTime.Value != 12*time.Minute || m.TaskTime.N != 5 ||
		len(m.Fleet) != 1 || m.Fleet[0].Task != "#4 Write docs" || len(m.Buckets) != 2 || m.Buckets[0].Accepted != 2 ||
		len(m.Util) != 1 || m.Util[0].Working != 60 || len(m.Insights) != 1 || m.Insights[0].Basis != "1 open question" || m.Spend != nil {
		t.Fatalf("decoded wrongly: %+v", m)
	}
}

func TestOverviewWithNothingToComputeFrom(t *testing.T) {
	f := seeded()
	f.metrics.Checks = Rate{Pct: -1}
	f.metrics.TaskTime = Dur{}
	f.metrics.Buckets = nil
	f.metrics.Insights = nil
	m := start(t, f, 120, 40)
	v := m.View()
	mustContain(t, v, "no checks in this range", "no finished tasks in this range", "Nothing to flag")
}

func TestHubErrorsKeepTheirStatusAndWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"a viewer may not accept work","code":"forbidden"}`))
	}))
	defer srv.Close()
	err := NewHTTP(client.Direct(srv.URL, "tok")).Accept(3)
	if got := plainErr(err); got != "a viewer may not accept work" {
		t.Fatalf("plainErr = %q", got)
	}
	if isOffline(err) {
		t.Fatal("an answer from the hub is not offline")
	}
	srv.Close()
	if err := NewHTTP(client.Direct(srv.URL, "tok")).Accept(3); !isOffline(err) {
		t.Fatalf("a refused connection is offline, got %v", err)
	}
}
