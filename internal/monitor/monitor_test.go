package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testService(t *testing.T) *Service {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.ConfigureNotifications(SMTPConfig{Port: 587, TLSMode: "starttls"}, false); e != nil {
		t.Fatal(e)
	}
	return s
}
func request(s *Service, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Router("token", "*").ServeHTTP(w, r)
	return w
}
func createTarget(t *testing.T, s *Service, name, address string) Target {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"kind":"http","address":%q,"description":"notes","interval_seconds":60}`, name, address)
	w := request(s, "POST", "/api/v1/monitors", body)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var target Target
	if json.Unmarshal(w.Body.Bytes(), &target) != nil {
		t.Fatal("invalid response")
	}
	return target
}
func TestMonitorLifecycle(t *testing.T) {
	s := testService(t)
	target := createTarget(t, s, "service", "https://example.com/health")
	if w := request(s, "PATCH", fmt.Sprintf("/api/v1/monitors/%d", target.ID), `{"name":"edited","description":"new","interval_seconds":120,"timeout_seconds":5,"enabled":false}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	updated, e := s.monitor(target.ID)
	if e != nil || updated.Interval != 120 || updated.Enabled || updated.Description != "new" || updated.Name != "edited" {
		t.Fatalf("edit: %+v %v", updated, e)
	}
	if w := request(s, "PATCH", fmt.Sprintf("/api/v1/monitors/%d", target.ID), `{"timeout_seconds":200}`); w.Code != 400 {
		t.Fatal("invalid timeout accepted")
	}
	if e = s.Save(Result{TargetID: target.ID, At: time.Now().Unix(), OK: true, Latency: 20}); e != nil {
		t.Fatal(e)
	}
	if w := request(s, "DELETE", fmt.Sprintf("/api/v1/monitors/%d", target.ID), ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	targets, e := s.Targets()
	if e != nil || len(targets) != 0 {
		t.Fatal("deleted monitor still visible")
	}
	if r, e := s.Latest(target.ID); e != nil || r == nil {
		t.Fatal("history lost")
	}
	if w := request(s, "POST", "/api/v1/monitors", `{"name":"edited","kind":"ssl","address":"example.com","interval_seconds":86400}`); w.Code != 201 {
		t.Fatal("deleted name cannot be reused")
	}
}
func TestStatusTimelineAndAuth(t *testing.T) {
	s := testService(t)
	a := createTarget(t, s, "a", "https://example.com")
	b := createTarget(t, s, "b", "https://example.com")
	now := time.Now().Unix()
	s.Save(Result{TargetID: a.ID, At: now, OK: true, Latency: 10})
	s.Save(Result{TargetID: a.ID, At: now - 1, OK: false, Latency: 30})
	s.Save(Result{TargetID: b.ID, At: now, OK: false})
	s.scheduleMu.Lock()
	s.schedule[a.ID] = scheduleEntry{Next: time.Now().Add(60 * time.Second)}
	s.scheduleMu.Unlock()
	w := request(s, "GET", "/api/v1/status?page_size=1", "")
	var response struct {
		Total   int            `json:"total"`
		Summary map[string]int `json:"summary"`
		Data    []struct {
			Next int64 `json:"next_check_at"`
		} `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Total != 2 || response.Summary["up"] != 1 || response.Summary["down"] != 1 || response.Data[0].Next <= now {
		t.Fatal(w.Body.String())
	}
	w = request(s, "GET", fmt.Sprintf("/api/v1/monitors/timeline?ids=%d,%d", a.ID, b.ID), "")
	var timeline struct {
		Data []struct {
			Samples, Successful int
			Target              int64 `json:"target_id"`
		}
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &timeline) != nil {
		t.Fatal(w.Body.String())
	}
	n := 0
	for _, bucket := range timeline.Data {
		n += bucket.Samples
	}
	if n != 3 {
		t.Fatal("timeline sample count incorrect")
	}
	w = httptest.NewRecorder()
	s.Router("token", "").ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/monitors", nil))
	if w.Code != 401 {
		t.Fatal("auth bypass")
	}
	w = httptest.NewRecorder()
	s.Router("", "", false).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/monitors", nil))
	if w.Code != 200 {
		t.Fatal("auth toggle ignored")
	}
	for _, path := range []string{"/ui/", "/ui/app.js", "/ui/expiry.js", "/ui/style.css"} {
		w = request(s, "GET", path, "")
		if w.Code != 200 {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
	}
}
func TestSettingsTestMailFailureReturnsDetailAndCanRetry(t *testing.T) {
	s := testService(t)
	body := `{"ssl_warning_days":7,"auto_notify":false,"smtp":{"host":"smtp.example.com","port":587,"tls_mode":"starttls","username":"user","password":"secret-value","from":"from@example.com","to":["to@example.com"]}}`
	if w := request(s, "PATCH", "/api/v1/settings", body); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.notify.send = func(context.Context, string, string) error {
		return fmt.Errorf("SMTP authentication: 535 authorization failed\nsecret omitted")
	}
	w := request(s, "POST", "/api/v1/settings/test-mail", "")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "535 authorization failed") || strings.Contains(w.Body.String(), "\n") || !strings.Contains(w.Body.String(), `"password_length":12`) || !strings.Contains(w.Body.String(), `"username":"user"`) || strings.Contains(w.Body.String(), "secret-value") {
		t.Fatal(w.Body.String())
	}
	s.notify.send = func(context.Context, string, string) error { return nil }
	if w = request(s, "POST", "/api/v1/settings/test-mail", ""); w.Code != 200 {
		t.Fatalf("failed test should not start rate limit: %s", w.Body.String())
	}
}

func TestSettingsTestMailAndTemplate(t *testing.T) {
	s := testService(t)
	body := `{"ssl_warning_days":7,"auto_notify":false,"smtp":{"host":"smtp.example.com","port":587,"tls_mode":"starttls","username":"user","password":"secret-value","from":"from@example.com","to":["to@example.com"]}}`
	w := request(s, "PATCH", "/api/v1/settings", body)
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-value") {
		t.Fatal(w.Body.String())
	}
	s.notify.send = func(ctx context.Context, subject, body string) error {
		if !strings.Contains(subject, "测试") {
			t.Fatal("test subject missing")
		}
		return nil
	}
	if w = request(s, "POST", "/api/v1/settings/test-mail", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = request(s, "POST", "/api/v1/settings/test-mail", ""); w.Code != 429 {
		t.Fatal("test mail not rate limited")
	}
	w = request(s, "PATCH", "/api/v1/settings", strings.Replace(body, `"password":"secret-value"`, `"password":""`, 1))
	if w.Code != 200 || s.notify.config.Password != "secret-value" {
		t.Fatal("blank password not retained")
	}
	if e := s.ConfigureNotifications(SMTPConfig{}, false); e != nil || s.notify.warningDays != 7 || s.notify.config.Password != "secret-value" {
		t.Fatal("settings not persisted")
	}
	raw, e := buildEmail(s.notify.config, "[Babyone Check] down · 中文名称", "Target: <script>alert(1)</script>\nError: connection failed")
	if e != nil {
		t.Fatal(e)
	}
	msg, e := mail.ReadMessage(strings.NewReader(string(raw)))
	if e != nil {
		t.Fatal(e)
	}
	kind, params, e := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if e != nil || kind != "multipart/alternative" {
		t.Fatal("missing MIME alternatives")
	}
	parts := multipart.NewReader(msg.Body, params["boundary"])
	count := 0
	for {
		part, e := parts.NextRawPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(quotedprintable.NewReader(part))
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(part.Header.Get("Content-Type"), "text/html") && (strings.Contains(string(data), "<script>") || !strings.Contains(string(data), "&lt;script&gt;")) {
			t.Fatal("unescaped HTML template")
		}
		count++
	}
	if count != 2 {
		t.Fatal("plain and HTML alternatives missing")
	}
}
func TestSchedulerAndNotificationDedup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	s := testService(t)
	target := createTarget(t, s, "local", server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(4 * time.Second)
	for {
		result, e := s.Latest(target.ID)
		if e != nil {
			t.Fatal(e)
		}
		if result != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not check target")
		}
		time.Sleep(25 * time.Millisecond)
	}
	next, _ := s.scheduleStatus(target.ID)
	if next <= time.Now().Unix() {
		t.Fatal("next check not published")
	}
	cfg := SMTPConfig{Host: "smtp.example.com", Port: 587, TLSMode: "starttls", From: "a@example.com", To: []string{"b@example.com"}}
	s.notify.mu.Lock()
	s.notify.config = cfg
	calls := 0
	s.notify.send = func(context.Context, string, string) error { calls++; return nil }
	s.db.Exec(`UPDATE service_settings SET auto_notify=1 WHERE id=1`)
	s.notify.mu.Unlock()
	result := Result{TargetID: target.ID, At: time.Now().Unix(), OK: false}
	if e := s.Notify(context.Background(), target, result); e != nil {
		t.Fatal(e)
	}
	s.Notify(context.Background(), target, result)
	if calls != 1 {
		t.Fatal("duplicate failure notification")
	}
	result.OK = true
	s.Notify(context.Background(), target, result)
	if calls != 2 {
		t.Fatal("recovery notification missing")
	}
}
func TestSeedOnceAndCertificateThresholds(t *testing.T) {
	s := testService(t)
	if e := s.Seed(); e != nil {
		t.Fatal(e)
	}
	targets, _ := s.Targets()
	if len(targets) != 6 {
		t.Fatal("seed count")
	}
	request(s, "DELETE", fmt.Sprintf("/api/v1/monitors/%d", targets[0].ID), "")
	if e := s.Seed(); e != nil {
		t.Fatal(e)
	}
	targets, _ = s.Targets()
	if len(targets) != 5 {
		t.Fatal("deleted seed recreated")
	}
	target := Target{Kind: "ssl"}
	r := Result{OK: true, Expires: time.Now().Add(2 * 24 * time.Hour).Unix()}
	if notificationKey(target, r, 1) != "healthy" || !strings.HasPrefix(notificationKey(target, r, 7), "expiry:") {
		t.Fatal("certificate notification threshold incorrect")
	}
}
