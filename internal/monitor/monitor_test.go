package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestHTTPCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	target := Target{ID: 1, Kind: "http", Address: srv.URL, Timeout: 2, Expected: 200}
	r := Check(context.Background(), target)
	if r.OK || r.Status != 503 || r.Error == "" {
		t.Fatalf("bad failure: %+v", r)
	}
	target.Expected = 503
	if !Check(context.Background(), target).OK {
		t.Fatal("custom status failed")
	}
}
func TestStatsAndAuthentication(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Seed(); e != nil {
		t.Fatal(e)
	}
	if e = s.Seed(); e != nil {
		t.Fatal(e)
	}
	ts, e := s.Targets(0)
	if e != nil || len(ts) != 6 {
		t.Fatalf("seed: %v %d", e, len(ts))
	}
	now := time.Now().Unix()
	for _, ok := range []bool{true, true, false} {
		if e = s.Save(Result{TargetID: ts[0].ID, At: now, OK: ok, Latency: 100}); e != nil {
			t.Fatal(e)
		}
	}
	router := s.Router("test-token", "")
	request := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if request("/api/v1/projects", "").Code != 401 {
		t.Fatal("auth bypass")
	}
	w := request("/api/v1/targets/1/stats", "Bearer test-token")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body struct {
		Samples int     `json:"samples"`
		Percent float64 `json:"availability_percent"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if body.Samples != 3 || body.Percent < 66.66 || body.Percent > 66.67 {
		t.Fatalf("stats: %+v", body)
	}
	w = request("/api/v1/targets/2/stats", "Bearer test-token")
	var empty map[string]any
	json.Unmarshal(w.Body.Bytes(), &empty)
	if empty["availability_percent"] != nil {
		t.Fatal("no samples must be null")
	}
	if request("/api/v1/targets/1/stats?from=5&to=4", "Bearer test-token").Code != 400 {
		t.Fatal("bad time accepted")
	}
}
func TestValidationAndStale(t *testing.T) {
	if Validate(Target{Name: "x", Kind: "ssl", Address: "https://example.com", Interval: 60, Timeout: 10}) == nil {
		t.Fatal("bad host accepted")
	}
	if !stale(Target{Interval: 60, Timeout: 10}, &Result{At: time.Now().Unix() - 200}) {
		t.Fatal("old result considered current")
	}
}

func TestStatusPagination(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Seed(); e != nil {
		t.Fatal(e)
	}
	router := s.Router("token", "")
	get := func(path string) (int, map[string]any) {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var b map[string]any
		json.Unmarshal(w.Body.Bytes(), &b)
		return w.Code, b
	}
	code, b := get("/api/v1/status?page=2&page_size=2")
	if code != 200 || b["total"] != float64(6) || b["total_pages"] != float64(3) {
		t.Fatalf("metadata: %d %+v", code, b)
	}
	data := b["data"].([]any)
	if len(data) != 2 || data[0].(map[string]any)["target"].(map[string]any)["id"] != float64(3) {
		t.Fatalf("page: %+v", data)
	}
	code, b = get("/api/v1/projects/4/status?page_size=2")
	if code != 200 || b["total"] != float64(3) {
		t.Fatalf("project filter: %+v", b)
	}
	_, b = get("/api/v1/status?page=100&page_size=2")
	if len(b["data"].([]any)) != 0 {
		t.Fatal("out of range page")
	}
	for _, q := range []string{"page=0", "page=-1", "page=oops", "page_size=101", "page_size=0", "page=9223372036854775807"} {
		if c, _ := get("/api/v1/status?" + q); c != 400 {
			t.Fatalf("invalid query accepted: %s", q)
		}
	}
}
