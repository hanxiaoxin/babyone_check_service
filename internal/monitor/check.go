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
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 100 || len(t.Description) > 2000 {
		return fmt.Errorf("name required, max 100 UTF-8 bytes; description max 2000 bytes")
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

type scheduleEntry struct {
	Next     time.Time
	Running  bool
	Interval int64
}

func (s *Service) scheduleStatus(id int64) (int64, bool) {
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	entry := s.schedule[id]
	if entry.Next.IsZero() {
		return 0, entry.Running
	}
	return entry.Next.Unix(), entry.Running
}
func (s *Service) resetSchedule(id int64) {
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	entry := s.schedule[id]
	entry.Next = time.Now()
	s.schedule[id] = entry
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	defer func() { wg.Wait(); log.Print("scheduler stopped") }()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			targets, err := s.Targets()
			if err != nil {
				log.Printf("scheduler: %v", err)
				continue
			}
			alive := make(map[int64]bool)
			for _, t := range targets {
				alive[t.ID] = true
				s.scheduleMu.Lock()
				entry := s.schedule[t.ID]
				if !t.Enabled {
					if !entry.Running {
						delete(s.schedule, t.ID)
					}
					s.scheduleMu.Unlock()
					continue
				}
				if entry.Interval != t.Interval {
					entry.Next = now
					entry.Interval = t.Interval
				}
				if entry.Running || now.Before(entry.Next) {
					s.schedule[t.ID] = entry
					s.scheduleMu.Unlock()
					continue
				}
				select {
				case sem <- struct{}{}:
				default:
					s.schedule[t.ID] = entry
					s.scheduleMu.Unlock()
					continue
				}
				entry.Running = true
				entry.Next = now.Add(time.Duration(t.Interval) * time.Second)
				s.schedule[t.ID] = entry
				s.scheduleMu.Unlock()
				wg.Add(1)
				go func(t Target) {
					defer wg.Done()
					defer func() {
						<-sem
						s.scheduleMu.Lock()
						e, ok := s.schedule[t.ID]
						if ok {
							e.Running = false
							s.schedule[t.ID] = e
						}
						s.scheduleMu.Unlock()
					}()
					r := Check(ctx, t)
					if ctx.Err() != nil {
						return
					}
					// Discard in-flight results if the target was edited, paused or deleted.
					current, e := s.monitor(t.ID)
					if e != nil || !current.Enabled || current.Address != t.Address || current.Kind != t.Kind || current.Expected != t.Expected {
						return
					}
					if e = s.Save(r); e != nil {
						log.Printf("save result: %v", e)
					} else if e = s.Notify(ctx, current, r); e != nil {
						log.Printf("notify target %d: %v", t.ID, e)
					}
				}(t)
			}
			s.scheduleMu.Lock()
			for id := range s.schedule {
				if !alive[id] {
					delete(s.schedule, id)
				}
			}
			s.scheduleMu.Unlock()
		}
	}
}
