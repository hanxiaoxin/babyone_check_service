package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func Validate(t Target) error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 100 {
		return fmt.Errorf("name required, max 100 characters")
	}
	if t.Interval < 10 || t.Interval > 2592000 || t.Timeout < 1 || t.Timeout > 60 || t.Timeout >= t.Interval {
		return fmt.Errorf("interval must be 10..2592000, timeout 1..60 and smaller than interval")
	}
	if t.Kind == "http" {
		u, e := url.Parse(t.Address)
		if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return fmt.Errorf("valid http/https URL without credentials required")
		}
		if t.Expected < 100 || t.Expected > 599 {
			return fmt.Errorf("expected_status must be 100..599")
		}
	} else if t.Kind == "ssl" {
		if strings.ContainsAny(t.Address, "/: ?#@") || t.Address == "" {
			return fmt.Errorf("ssl address must be a hostname without scheme or port")
		}
	} else {
		return fmt.Errorf("kind must be http or ssl")
	}
	return nil
}
func Check(ctx context.Context, t Target) Result {
	start := time.Now()
	r := Result{TargetID: t.ID, At: start.Unix()}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(t.Timeout)*time.Second)
	defer cancel()
	var err error
	if t.Kind == "http" {
		tr := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: time.Duration(t.Timeout) * time.Second}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, "GET", t.Address, nil)
		if err == nil {
			var resp *http.Response
			resp, err = client.Do(req)
			if err == nil {
				r.Status = resp.StatusCode
				resp.Body.Close()
				r.OK = r.Status == t.Expected
				if !r.OK {
					err = fmt.Errorf("unexpected HTTP status: %d", r.Status)
				}
			}
		}
	} else {
		d := tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{ServerName: t.Address, MinVersion: tls.VersionTLS12}}
		var conn net.Conn
		conn, err = d.DialContext(ctx, "tcp", net.JoinHostPort(t.Address, "443"))
		if err == nil {
			state := conn.(*tls.Conn).ConnectionState()
			r.Expires = state.PeerCertificates[0].NotAfter.Unix()
			r.OK = true
			conn.Close()
		}
		// If verification fails, retrieve the presented expiry for diagnosis; OK remains false.
		if err != nil && ctx.Err() == nil {
			d.Config.InsecureSkipVerify = true
			conn, e := d.DialContext(ctx, "tcp", net.JoinHostPort(t.Address, "443"))
			if e == nil {
				state := conn.(*tls.Conn).ConnectionState()
				if len(state.PeerCertificates) > 0 {
					r.Expires = state.PeerCertificates[0].NotAfter.Unix()
				}
				conn.Close()
			}
		}
	}
	if err != nil {
		r.Error = err.Error()
	}
	r.Latency = time.Since(start).Milliseconds()
	return r
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	next := map[int64]time.Time{}
	defer log.Print("scheduler stopped")
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			targets, err := s.Targets(0)
			if err != nil {
				log.Printf("scheduler: %v", err)
				continue
			}
			sem := make(chan struct{}, 8)
			var wg sync.WaitGroup
			for _, t := range targets {
				if !t.Enabled || now.Before(next[t.ID]) {
					continue
				}
				next[t.ID] = now.Add(time.Duration(t.Interval) * time.Second)
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					wg.Wait()
					return
				}
				wg.Add(1)
				go func(t Target) {
					defer wg.Done()
					defer func() { <-sem }()
					r := Check(ctx, t)
					if ctx.Err() == nil {
						if err := s.Save(r); err != nil {
							log.Printf("save result: %v", err)
						} else if err := s.Notify(ctx, t, r); err != nil {
							log.Printf("notify target %d: %v", t.ID, err)
						}
					}
				}(t)
			}
			wg.Wait()
		}
	}
}
