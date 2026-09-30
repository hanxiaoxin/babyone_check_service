package monitor

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SMTPConfig struct {
	Host                     string
	Port                     int
	Username, Password, From string
	To                       []string
	TLSMode                  string
}
type notifier struct {
	config SMTPConfig
	mu     sync.Mutex
	send   func(context.Context, string, string) error
}

func SMTPFromEnv() (SMTPConfig, error) {
	c := SMTPConfig{Host: os.Getenv("SMTP_HOST"), Port: 587, Username: os.Getenv("SMTP_USERNAME"), Password: os.Getenv("SMTP_PASSWORD"), From: os.Getenv("SMTP_FROM"), TLSMode: os.Getenv("SMTP_TLS_MODE")}
	if c.TLSMode == "" {
		c.TLSMode = "starttls"
	}
	if v := os.Getenv("SMTP_PORT"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("invalid SMTP_PORT")
		}
		c.Port = n
	}
	for _, v := range strings.Split(os.Getenv("SMTP_TO"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			c.To = append(c.To, v)
		}
	}
	if c.TLSMode != "starttls" && c.TLSMode != "tls" {
		return c, fmt.Errorf("SMTP_TLS_MODE must be starttls or tls")
	}
	return c, nil
}
func (c SMTPConfig) Validate() error {
	if c.Host == "" || strings.ContainsAny(c.Host, "\r\n /:") || c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("SMTP_HOST and valid SMTP_PORT required")
	}
	if c.TLSMode != "starttls" && c.TLSMode != "tls" {
		return fmt.Errorf("TLS mode invalid")
	}
	for _, v := range append([]string{c.From}, c.To...) {
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("invalid email address")
		}
		a, e := mail.ParseAddress(v)
		if e != nil || a.Address != v {
			return fmt.Errorf("email addresses must be plain mailbox addresses")
		}
	}
	if len(c.To) == 0 {
		return fmt.Errorf("SMTP_TO required")
	}
	if (c.Username == "") != (c.Password == "") {
		return fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD must both be configured")
	}
	return nil
}
func (c SMTPConfig) Send(ctx context.Context, subject, body string) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("invalid subject")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	conn, e := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if e != nil {
		return e
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	cfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	var wire net.Conn = conn
	if c.TLSMode == "tls" {
		tc := tls.Client(conn, cfg)
		if e = tc.HandshakeContext(ctx); e != nil {
			return e
		}
		wire = tc
	}
	client, e := smtp.NewClient(wire, c.Host)
	if e != nil {
		return e
	}
	defer client.Close()
	if c.TLSMode == "starttls" {
		if e = client.StartTLS(cfg); e != nil {
			return e
		}
	}
	if c.Username != "" {
		if e = client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); e != nil {
			return e
		}
	}
	if e = client.Mail(c.From); e != nil {
		return e
	}
	for _, to := range c.To {
		if e = client.Rcpt(to); e != nil {
			return e
		}
	}
	writer, e := client.Data()
	if e != nil {
		return e
	}
	message := "From: " + c.From + "\r\nTo: " + strings.Join(c.To, ", ") + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if _, e = writer.Write([]byte(message)); e != nil {
		writer.Close()
		return e
	}
	if e = writer.Close(); e != nil {
		return e
	}
	// DATA acknowledgement confirms acceptance; QUIT failure must not trigger a duplicate retry.
	_ = client.Quit()
	return nil
}
func (s *Service) ConfigureNotifications(c SMTPConfig, initial bool) error {
	if initial {
		if e := c.Validate(); e != nil {
			return e
		}
	}
	s.notify = &notifier{config: c, send: c.Send}
	_, e := s.db.Exec(`CREATE TABLE IF NOT EXISTS service_settings(id INTEGER PRIMARY KEY CHECK(id=1),auto_notify INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS notification_state(target_id INTEGER PRIMARY KEY REFERENCES targets(id),state_key TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS notification_attempts(id INTEGER PRIMARY KEY,target_id INTEGER NOT NULL,created_at INTEGER NOT NULL,state_key TEXT NOT NULL,sent INTEGER NOT NULL);`)
	if e != nil {
		return e
	}
	_, e = s.db.Exec(`INSERT OR IGNORE INTO service_settings(id,auto_notify)VALUES(1,?)`, initial)
	if e != nil {
		return e
	}
	enabled, e := s.autoNotify()
	if e != nil {
		return e
	}
	if enabled {
		return c.Validate()
	}
	return nil
}
func (s *Service) autoNotify() (bool, error) {
	var on bool
	e := s.db.QueryRow(`SELECT auto_notify FROM service_settings WHERE id=1`).Scan(&on)
	return on, e
}
func notificationKey(t Target, r Result) string {
	if !r.OK {
		return "down"
	}
	if t.Kind == "ssl" && r.Expires > 0 && r.Expires-time.Now().Unix() < 30*86400 {
		return fmt.Sprintf("expiry:%d", r.Expires)
	}
	return "healthy"
}
func (s *Service) Notify(ctx context.Context, t Target, r Result) error {
	if s.notify == nil {
		return nil
	}
	s.notify.mu.Lock()
	defer s.notify.mu.Unlock()
	on, e := s.autoNotify()
	if e != nil || !on {
		return e
	}
	key := notificationKey(t, r)
	var prior string
	e = s.db.QueryRow(`SELECT state_key FROM notification_state WHERE target_id=?`, t.ID).Scan(&prior)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if key == prior || prior == "" && key == "healthy" {
		if prior == "" {
			_, e = s.db.Exec(`INSERT OR IGNORE INTO notification_state(target_id,state_key)VALUES(?,?)`, t.ID, key)
			return e
		}
		return nil
	}
	subject := "[Babyone Check] " + key + " target #" + strconv.FormatInt(t.ID, 10)
	body := fmt.Sprintf("Project: %d\nTarget: %s\nAddress: %s\nState: %s\nChecked at: %s\nHTTP status: %d\nLatency: %d ms\nError: %s\n", t.ProjectID, t.Name, t.Address, key, time.Unix(r.At, 0).UTC().Format(time.RFC3339), r.Status, r.Latency, r.Error)
	if r.Expires > 0 {
		body += "Certificate expires: " + time.Unix(r.Expires, 0).UTC().Format(time.RFC3339) + "\n"
	}
	e = s.notify.send(ctx, subject, body)
	_, auditErr := s.db.Exec(`INSERT INTO notification_attempts(target_id,created_at,state_key,sent)VALUES(?,?,?,?)`, t.ID, time.Now().Unix(), key, e == nil)
	if e != nil {
		return fmt.Errorf("SMTP notification failed")
	}
	if auditErr != nil {
		return auditErr
	}
	_, e = s.db.Exec(`INSERT INTO notification_state(target_id,state_key)VALUES(?,?) ON CONFLICT(target_id) DO UPDATE SET state_key=excluded.state_key`, t.ID, key)
	return e
}
func (s *Service) configRoutes(a *gin.RouterGroup) {
	a.GET("/settings", func(c *gin.Context) {
		if s.notify == nil {
			fail(c, 503, "notifications not configured")
			return
		}
		on, e := s.autoNotify()
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		c.JSON(200, gin.H{"auto_notify": on, "smtp_configured": s.notify.config.Validate() == nil, "ssl_warning_days": 30})
	})
	a.PATCH("/settings", func(c *gin.Context) {
		if s.notify == nil {
			fail(c, 503, "notifications not configured")
			return
		}
		var b struct {
			AutoNotify *bool `json:"auto_notify"`
		}
		if c.ShouldBindJSON(&b) != nil || b.AutoNotify == nil {
			fail(c, 400, "auto_notify boolean required")
			return
		}
		if *b.AutoNotify {
			if s.notify.config.Validate() != nil {
				fail(c, 400, "configure valid SMTP settings in .env first")
				return
			}
		}
		s.notify.mu.Lock()
		_, e := s.db.Exec(`UPDATE service_settings SET auto_notify=? WHERE id=1`, *b.AutoNotify)
		s.notify.mu.Unlock()
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		c.JSON(200, gin.H{"auto_notify": *b.AutoNotify})
	})
}
