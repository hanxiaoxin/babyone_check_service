package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestNotifications(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "notify.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Seed()
	cfg := SMTPConfig{Host: "smtp.example.com", Port: 587, TLSMode: "starttls", From: "from@example.com", To: []string{"to@example.com"}}
	if e = s.ConfigureNotifications(cfg, true); e != nil {
		t.Fatal(e)
	}
	calls := 0
	shouldFail := false
	s.notify.send = func(context.Context, string, string) error {
		calls++
		if shouldFail {
			return fmt.Errorf("failed")
		}
		return nil
	}
	ts, _ := s.Targets(0)
	target := ts[0]
	r := Result{TargetID: target.ID, At: time.Now().Unix(), OK: true}
	if e = s.Notify(context.Background(), target, r); e != nil {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("initial healthy mail")
	}
	r.OK = false
	s.Notify(context.Background(), target, r)
	s.Notify(context.Background(), target, r)
	if calls != 1 {
		t.Fatal("duplicate failure notification")
	}
	r.OK = true
	s.Notify(context.Background(), target, r)
	if calls != 2 {
		t.Fatal("missing recovery")
	}
	shouldFail = true
	r.OK = false
	s.Notify(context.Background(), target, r)
	shouldFail = false
	s.Notify(context.Background(), target, r)
	if calls != 4 {
		t.Fatal("failed send not retried")
	}
	if e = s.ConfigureNotifications(cfg, false); e != nil {
		t.Fatal(e)
	}
	enabled, _ := s.autoNotify()
	if !enabled {
		t.Fatal("restart overwrote persisted setting")
	}
	s.notify.send = func(context.Context, string, string) error { calls++; return nil }
	s.Notify(context.Background(), target, r)
	if calls != 4 {
		t.Fatal("dedup lost on reconfiguration")
	}
	cert := ts[3]
	r = Result{TargetID: cert.ID, At: time.Now().Unix(), OK: true, Expires: time.Now().Add(5 * 24 * time.Hour).Unix()}
	s.Notify(context.Background(), cert, r)
	s.Notify(context.Background(), cert, r)
	if calls != 5 {
		t.Fatal("SSL warning dedup")
	}
	s.db.Exec(`UPDATE service_settings SET auto_notify=0`)
	r.OK = false
	s.Notify(context.Background(), cert, r)
	if calls != 5 {
		t.Fatal("disabled notifications sent")
	}
}
func TestSettingsAPI(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "settings.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.ConfigureNotifications(SMTPConfig{TLSMode: "starttls", Port: 587}, false); e != nil {
		t.Fatal(e)
	}
	router := s.Router("token", "")
	r := httptest.NewRequest("PATCH", "/api/v1/settings", strings.NewReader(`{"auto_notify":true}`))
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("missing SMTP enabled: %d", w.Code)
	}
	r = httptest.NewRequest("GET", "/api/v1/settings", nil)
	r.Header.Set("Authorization", "Bearer token")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "password") {
		t.Fatal(w.Body.String())
	}
}

func TestSMTPEnvironment(t *testing.T) {
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PORT", "465")
	t.Setenv("SMTP_TLS_MODE", "tls")
	t.Setenv("SMTP_FROM", "from@example.com")
	t.Setenv("SMTP_TO", "a@example.com, b@example.com")
	t.Setenv("SMTP_USERNAME", "user")
	t.Setenv("SMTP_PASSWORD", "secret")
	c, e := SMTPFromEnv()
	if e != nil || c.Port != 465 || len(c.To) != 2 || c.Validate() != nil {
		t.Fatalf("SMTP parse: %+v %v", c, e)
	}
	c.From = "from@example.com\r\nBcc: bad@example.com"
	if c.Validate() == nil {
		t.Fatal("header injection accepted")
	}
	t.Setenv("SMTP_PORT", "invalid")
	if _, e = SMTPFromEnv(); e == nil {
		t.Fatal("bad port accepted")
	}
}

func TestQueryToken(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "token.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	router := s.Router("secret-value", "*")
	cases := []struct {
		query, auth string
		want        int
	}{{"token=secret-value", "", 200}, {"token=wrong", "", 401}, {"", "", 401}, {"", "Bearer secret-value", 200}, {"token=secret-value", "Bearer wrong", 401}, {"token=wrong", "Bearer secret-value", 200}, {"token=secret-value&token=secret-value", "", 401}, {"token=secret-value", "Basic secret-value", 401}}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/api/v1/projects?"+tc.query, nil)
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s: %d", tc.query, tc.auth, w.Code)
		}
		if strings.Contains(r.URL.RawQuery, "token") {
			t.Fatal("token retained for logger")
		}
	}
}
