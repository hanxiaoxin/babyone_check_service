package monitor

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SMTPConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	TLSMode  string   `json:"tls_mode"`
}
type notifier struct {
	config      SMTPConfig
	warningDays int
	mu          sync.Mutex
	lastTest    time.Time
	send        func(context.Context, string, string) error
}

func (c SMTPConfig) Validate() error {
	if c.Host == "" || strings.ContainsAny(c.Host, "\r\n /:") || c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("SMTP server and valid port required")
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
		return fmt.Errorf("at least one recipient required")
	}
	if (c.Username == "") != (c.Password == "") {
		return fmt.Errorf("SMTP username and password must both be configured")
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
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	conn, e := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if e != nil {
		return fmt.Errorf("connect %s: %w", addr, e)
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
			return fmt.Errorf("TLS handshake: %w", e)
		}
		wire = tc
	}
	client, e := smtp.NewClient(wire, c.Host)
	if e != nil {
		return fmt.Errorf("SMTP handshake: %w", e)
	}
	defer client.Close()
	if c.TLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("STARTTLS: server does not advertise STARTTLS; use TLS mode for implicit TLS ports such as 465")
		}
		if e = client.StartTLS(cfg); e != nil {
			return fmt.Errorf("STARTTLS: %w", e)
		}
	}
	if c.Username != "" {
		if e = client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); e != nil {
			return fmt.Errorf("SMTP authentication: %w", e)
		}
	}
	if e = client.Mail(c.From); e != nil {
		return fmt.Errorf("MAIL FROM %s: %w", c.From, e)
	}
	for _, to := range c.To {
		if e = client.Rcpt(to); e != nil {
			return fmt.Errorf("RCPT TO %s: %w", to, e)
		}
	}
	writer, e := client.Data()
	if e != nil {
		return fmt.Errorf("SMTP DATA: %w", e)
	}
	message, e := buildEmail(c, subject, body)
	if e != nil {
		writer.Close()
		return e
	}
	if _, e = writer.Write(message); e != nil {
		writer.Close()
		return fmt.Errorf("write message: %w", e)
	}
	if e = writer.Close(); e != nil {
		return fmt.Errorf("SMTP DATA acknowledgement: %w", e)
	}
	// DATA acknowledgement confirms acceptance; QUIT failure must not trigger a duplicate retry.
	_ = client.Quit()
	return nil
}
func smtpDebugInfo(c SMTPConfig) gin.H {
	return gin.H{
		"host":            c.Host,
		"port":            c.Port,
		"tls_mode":        c.TLSMode,
		"username":        c.Username,
		"username_length": len(c.Username),
		"password_set":    c.Password != "",
		"password_length": len(c.Password),
		"from":            c.From,
		"to":              append([]string(nil), c.To...),
	}
}

func smtpPublicError(err error) string {
	if err == nil {
		return "unknown error"
	}
	msg := strings.TrimSpace(err.Error())
	// SMTP server replies and network/TLS errors are useful for diagnosing settings,
	// but keep the API response single-line and bounded.
	msg = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(msg)
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return msg
}

func (s *Service) ConfigureNotifications(c SMTPConfig, initial bool) error {
	if initial {
		if e := c.Validate(); e != nil {
			return e
		}
	}
	s.notify = &notifier{config: c, send: c.Send}
	_, e := s.db.Exec(`CREATE TABLE IF NOT EXISTS service_settings(id INTEGER PRIMARY KEY CHECK(id=1),auto_notify INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS ssl_warning_settings(id INTEGER PRIMARY KEY CHECK(id=1),days INTEGER NOT NULL CHECK(days BETWEEN 1 AND 365));
 INSERT OR IGNORE INTO ssl_warning_settings(id,days)VALUES(1,1);
 CREATE TABLE IF NOT EXISTS smtp_settings(id INTEGER PRIMARY KEY CHECK(id=1),config TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS notification_state(target_id INTEGER PRIMARY KEY REFERENCES targets(id),state_key TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS notification_attempts(id INTEGER PRIMARY KEY,target_id INTEGER NOT NULL,created_at INTEGER NOT NULL,state_key TEXT NOT NULL,sent INTEGER NOT NULL);`)
	if e != nil {
		return e
	}
	_, e = s.db.Exec(`INSERT OR IGNORE INTO service_settings(id,auto_notify)VALUES(1,?)`, initial)
	if e != nil {
		return e
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = s.db.Exec(`INSERT OR IGNORE INTO smtp_settings(id,config)VALUES(1,?)`, string(raw)); err != nil {
		return err
	}
	if err = s.db.QueryRow(`SELECT config FROM smtp_settings WHERE id=1`).Scan(&raw); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if c.Port == 0 {
		c.Port = 465
	}
	if c.TLSMode == "" {
		c.TLSMode = "tls"
	}
	if err = s.db.QueryRow(`SELECT days FROM ssl_warning_settings WHERE id=1`).Scan(&s.notify.warningDays); err != nil {
		return err
	}
	s.notify.config, s.notify.send = c, c.Send
	enabled, err := s.autoNotify()
	if err != nil {
		return err
	}
	// Old installations may have enabled notifications before configuring SMTP in the UI.
	if enabled && c.Validate() != nil {
		_, err = s.db.Exec(`UPDATE service_settings SET auto_notify=0 WHERE id=1`)
	}
	return err
}

func (s *Service) autoNotify() (bool, error) {
	var on bool
	e := s.db.QueryRow(`SELECT auto_notify FROM service_settings WHERE id=1`).Scan(&on)
	return on, e
}
func notificationKey(t Target, r Result, warningDays int) string {
	if !r.OK {
		return "down"
	}
	if t.Kind == "ssl" && r.Expires > 0 && r.Expires-time.Now().Unix() <= int64(warningDays)*86400 {
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
	var active bool
	if e = s.db.QueryRow(`SELECT enabled AND deleted=0 FROM targets WHERE id=?`, t.ID).Scan(&active); e != nil {
		return e
	}
	if !active {
		return nil
	}
	key := notificationKey(t, r, s.notify.warningDays)
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
	label := "服务恢复正常"
	if key == "down" {
		label = "服务检测异常"
	} else if strings.HasPrefix(key, "expiry:") {
		label = "证书即将到期"
	}
	subject := "[Babyone Check] " + label + " · " + strings.NewReplacer("\r", " ", "\n", " ").Replace(t.Name)
	body := fmt.Sprintf("Target: %s\nAddress: %s\nState: %s\nChecked at: %s\nHTTP status: %d\nLatency: %d ms\nError: %s\n", t.Name, t.Address, label, time.Unix(r.At, 0).UTC().Format(time.RFC3339), r.Status, r.Latency, r.Error)
	if t.Description != "" {
		body += "Description: " + t.Description + "\n"
	}
	if r.Expires > 0 {
		body += "Certificate expires: " + time.Unix(r.Expires, 0).UTC().Format(time.RFC3339) + "\n"
		body += fmt.Sprintf("Remaining: %.1f 天\n", float64(r.Expires-time.Now().Unix())/86400)
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

// settingsResponse never returns the stored SMTP password.
func (s *Service) settingsResponse(on bool) gin.H {
	cfg := s.notify.config
	hasPassword := cfg.Password != ""
	cfg.Password = ""
	if cfg.To == nil {
		cfg.To = []string{}
	}
	return gin.H{"auto_notify": on, "smtp_configured": s.notify.config.Validate() == nil,
		"ssl_warning_days": s.notify.warningDays, "smtp": cfg, "smtp_password_set": hasPassword}
}
func (s *Service) configRoutes(a *gin.RouterGroup) {
	a.POST("/settings/test-mail", func(c *gin.Context) {
		if s.notify == nil {
			fail(c, 503, "notifications not configured")
			return
		}
		s.notify.mu.Lock()
		if s.notify.config.Validate() != nil {
			s.notify.mu.Unlock()
			fail(c, 400, "save valid SMTP settings first")
			return
		}
		if time.Since(s.notify.lastTest) < 30*time.Second {
			s.notify.mu.Unlock()
			fail(c, 429, "please wait 30 seconds before sending another test")
			return
		}
		send := s.notify.send
		cfg := s.notify.config
		s.notify.mu.Unlock()
		body := "Target: SMTP 配置测试\nState: 测试通知，无需开启自动通知\nSent at: " + time.Now().UTC().Format(time.RFC3339) + "\n如果收到此邮件，说明当前 SMTP 配置可正常发送通知。"
		if err := send(c.Request.Context(), "[Babyone Check] 测试邮件", body); err != nil {
			msg := "SMTP 测试失败：" + smtpPublicError(err)
			if strings.Contains(strings.ToLower(err.Error()), "authentication") || strings.Contains(err.Error(), "535") {
				msg += "；认证失败通常表示用户名或密码/授权码不匹配。126 邮箱通常应使用邮箱账号作为用户名，并使用客户端授权码而不是网页登录密码。"
			}
			c.JSON(502, gin.H{"error": msg, "smtp": smtpDebugInfo(cfg)})
			return
		}
		s.notify.mu.Lock()
		s.notify.lastTest = time.Now()
		s.notify.mu.Unlock()
		c.JSON(200, gin.H{"message": "test mail accepted by SMTP server"})
	})
	a.GET("/settings", func(c *gin.Context) {
		if s.notify == nil {
			fail(c, 503, "notifications not configured")
			return
		}
		s.notify.mu.Lock()
		defer s.notify.mu.Unlock()
		on, err := s.autoNotify()
		if err != nil {
			fail(c, 500, "database error")
			return
		}
		c.JSON(200, s.settingsResponse(on))
	})
	a.PATCH("/settings", func(c *gin.Context) {
		if s.notify == nil {
			fail(c, 503, "notifications not configured")
			return
		}
		var b struct {
			WarningDays   *int        `json:"ssl_warning_days"`
			AutoNotify    *bool       `json:"auto_notify"`
			SMTP          *SMTPConfig `json:"smtp"`
			ClearPassword bool        `json:"clear_smtp_password"`
		}
		if c.ShouldBindJSON(&b) != nil || (b.WarningDays == nil && b.AutoNotify == nil && b.SMTP == nil && !b.ClearPassword) {
			fail(c, 400, "notification settings required")
			return
		}
		s.notify.mu.Lock()
		defer s.notify.mu.Unlock()
		on, err := s.autoNotify()
		if err != nil {
			fail(c, 500, "database error")
			return
		}
		days := s.notify.warningDays
		if b.WarningDays != nil {
			days = *b.WarningDays
			if days < 1 || days > 365 {
				fail(c, 400, "ssl_warning_days must be 1..365")
				return
			}
		}
		if b.AutoNotify != nil {
			on = *b.AutoNotify
		}
		cfg := s.notify.config
		if b.SMTP != nil {
			cfg = *b.SMTP
			cfg.Host = strings.TrimSpace(cfg.Host)
			cfg.Username = strings.TrimSpace(cfg.Username)
			cfg.From = strings.TrimSpace(cfg.From)
			cfg.To = append([]string(nil), cfg.To...)
			for i := range cfg.To {
				cfg.To[i] = strings.TrimSpace(cfg.To[i])
			}
			if cfg.Password == "" {
				cfg.Password = s.notify.config.Password
			}
		}
		if b.ClearPassword {
			cfg.Password = ""
		}
		if b.SMTP != nil || b.ClearPassword || on {
			if err = cfg.Validate(); err != nil {
				fail(c, 400, err.Error())
				return
			}
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			fail(c, 500, "configuration error")
			return
		}
		tx, err := s.db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			fail(c, 500, "database error")
			return
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`UPDATE service_settings SET auto_notify=? WHERE id=1`, on); err == nil {
			_, err = tx.Exec(`UPDATE smtp_settings SET config=? WHERE id=1`, string(raw))
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE ssl_warning_settings SET days=? WHERE id=1`, days)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			fail(c, 500, "database error")
			return
		}
		s.notify.warningDays = days
		s.notify.config, s.notify.send = cfg, cfg.Send
		c.JSON(200, s.settingsResponse(on))
	})
}
