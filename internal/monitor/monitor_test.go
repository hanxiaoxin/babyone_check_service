package monitor

import (
	"context"
	"database/sql"
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
	if w.Code != 200 || strings.Contains(w.Body.String(), `"password":`) {
		t.Fatal(w.Body.String())
	}
}

func TestSMTPValidation(t *testing.T) {
	c := SMTPConfig{Host: "smtp.example.com", Port: 465, TLSMode: "tls", From: "from@example.com", To: []string{"to@example.com"}}
	if c.Validate() != nil {
		t.Fatal("valid SMTP rejected")
	}
	c.From = "from@example.com\r\nBcc: bad@example.com"
	if c.Validate() == nil {
		t.Fatal("header injection accepted")
	}
}

func TestSMTPSettingsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smtp.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if err = s.ConfigureNotifications(SMTPConfig{Port: 587, TLSMode: "starttls"}, false); err != nil {
		t.Fatal(err)
	}
	router := s.Router("token", "")
	patch := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PATCH", "/api/v1/settings", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer token")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	body := `{"auto_notify":true,"smtp":{"host":"smtp.example.com","port":465,"tls_mode":"tls","username":"user","password":"secret-value","from":"from@example.com","to":["a@example.com","b@example.com"]}}`
	w := patch(body)
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-value") || strings.Contains(w.Body.String(), `"password":`) {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if s.notify.config.Host != "smtp.example.com" || s.notify.config.Password != "secret-value" {
		t.Fatal("live config not updated")
	}
	body = strings.Replace(body, `"password":"secret-value"`, `"password":""`, 1)
	if w = patch(body); w.Code != 200 || s.notify.config.Password != "secret-value" {
		t.Fatal("blank password did not preserve secret")
	}
	if w = patch(strings.Replace(body, `"port":465`, `"port":0`, 1)); w.Code != 400 {
		t.Fatal("invalid SMTP accepted")
	}
	if w = patch(`{"auto_notify":false}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureNotifications(SMTPConfig{}, false); err != nil {
		t.Fatal(err)
	}
	if s.notify.config.Password != "secret-value" || s.notify.config.Port != 465 {
		t.Fatal("SMTP config lost on restart")
	}
	on, err := s.autoNotify()
	if err != nil || on {
		t.Fatal("notification toggle lost")
	}
	router = s.Router("token", "")
	if w = patch(`{"smtp":{"host":"smtp.example.com","port":587,"tls_mode":"starttls","from":"from@example.com","to":["to@example.com"]},"clear_smtp_password":true}`); w.Code != 200 || s.notify.config.Password != "" {
		t.Fatalf("clear password: %d %s", w.Code, w.Body.String())
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

func TestKindFiltersAndProjectDescription(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "filters.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Seed(); e != nil {
		t.Fatal(e)
	}
	router := s.Router("token", "")
	request := func(method, path, body string) (int, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer token")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var b map[string]any
		json.Unmarshal(w.Body.Bytes(), &b)
		return w.Code, b
	}
	code, b := request("GET", "/api/v1/status?kind=ssl&page_size=2", "")
	if code != 200 || b["total"] != float64(3) || b["total_pages"] != float64(2) || len(b["data"].([]any)) != 2 {
		t.Fatalf("filtered pages: %d %+v", code, b)
	}
	for _, v := range b["data"].([]any) {
		if v.(map[string]any)["target"].(map[string]any)["kind"] != "ssl" {
			t.Fatal("wrong kind")
		}
	}
	code, b = request("GET", "/api/v1/projects/1/status?kind=ssl", "")
	if code != 200 || b["total"] != float64(0) {
		t.Fatal("project and kind not combined")
	}
	code, b = request("GET", "/api/v1/projects/4/targets?kind=ssl", "")
	if code != 200 || len(b["data"].([]any)) != 3 {
		t.Fatal("targets filter")
	}
	for _, path := range []string{"/api/v1/status?kind=nope", "/api/v1/status?kind=", "/api/v1/projects/1/targets?kind=http&kind=ssl"} {
		if code, _ = request("GET", path, ""); code != 400 {
			t.Fatalf("invalid kind: %s", path)
		}
	}
	code, b = request("POST", "/api/v1/projects", `{"name":"new","description":"项目描述"}`)
	if code != 201 || b["description"] != "项目描述" {
		t.Fatalf("create: %+v", b)
	}
	pid := int(b["id"].(float64))
	code, b = request("PATCH", fmt.Sprintf("/api/v1/projects/%d", pid), `{"description":"updated"}`)
	if code != 200 || b["description"] != "updated" {
		t.Fatal("description patch")
	}
	code, b = request("GET", "/api/v1/projects", "")
	found := false
	for _, v := range b["data"].([]any) {
		p := v.(map[string]any)
		if p["name"] == "new" && p["description"] == "updated" {
			found = true
		}
	}
	if code != 200 || !found {
		t.Fatal("description not returned")
	}
	if code, _ = request("PATCH", "/api/v1/projects/99999", `{"description":"x"}`); code != 404 {
		t.Fatal("missing project")
	}
	if code, _ = request("PATCH", fmt.Sprintf("/api/v1/projects/%d", pid), `{"description":""}`); code != 200 {
		t.Fatal("cannot clear description")
	}
}
func TestDescriptionMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE projects(id INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE);INSERT INTO projects(name)VALUES('existing');`)
	db.Close()
	if e != nil {
		t.Fatal(e)
	}
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	var desc string
	if e = s.db.QueryRow(`SELECT description FROM projects WHERE name='existing'`).Scan(&desc); e != nil || desc != "" {
		t.Fatal("migration lost old project")
	}
	s.db.Exec(`UPDATE projects SET description='keep'`)
	s.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.db.QueryRow(`SELECT description FROM projects WHERE name='existing'`).Scan(&desc); e != nil || desc != "keep" {
		t.Fatal("repeated migration overwrote description")
	}
}

func TestEmbeddedUI(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ui.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	router := s.Router("test-token", "")
	for path, content := range map[string]string{
		"/ui/": "type=\"module\"", "/ui/app.js": "async function api", "/ui/style.css": "@media",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), content) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/?token=a%2Bb", "/ui?token=a%2Bb"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "/ui/?token=a%2Bb" {
			t.Fatalf("entry redirect: %d %s", w.Code, w.Header().Get("Location"))
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/ui/embed.go", nil))
	if w.Code != 404 {
		t.Fatalf("source exposed: %d", w.Code)
	}
}
