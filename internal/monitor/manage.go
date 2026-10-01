package monitor

import (
	"database/sql"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
	"time"
)

func (s *Service) monitor(id int64) (Target, error) {
	var t Target
	err := s.db.QueryRow(`SELECT id,name,kind,address,interval_seconds,timeout_seconds,expected_status,enabled,description FROM targets WHERE id=? AND deleted=0`, id).Scan(&t.ID, &t.Name, &t.Kind, &t.Address, &t.Interval, &t.Timeout, &t.Expected, &t.Enabled, &t.Description)
	return t, err
}
func (s *Service) editMonitor(c *gin.Context) {
	tid := id(c, "target")
	if tid == 0 {
		return
	}
	t, err := s.monitor(tid)
	if err == sql.ErrNoRows {
		fail(c, 404, "monitor not found")
		return
	}
	if err != nil {
		fail(c, 500, "database error")
		return
	}
	var b struct {
		Name, Description, Kind, Address *string
		Interval                         *int64 `json:"interval_seconds"`
		Timeout                          *int64 `json:"timeout_seconds"`
		Expected                         *int   `json:"expected_status"`
		Enabled                          *bool  `json:"enabled"`
	}
	if c.ShouldBindJSON(&b) != nil {
		fail(c, 400, "invalid JSON")
		return
	}
	if b.Name != nil {
		t.Name = strings.TrimSpace(*b.Name)
	}
	if b.Description != nil {
		t.Description = *b.Description
	}
	if b.Kind != nil {
		t.Kind = *b.Kind
	}
	if b.Address != nil {
		t.Address = strings.TrimSpace(*b.Address)
	}
	if b.Interval != nil {
		t.Interval = *b.Interval
	}
	if b.Timeout != nil {
		t.Timeout = *b.Timeout
	}
	if b.Expected != nil {
		t.Expected = *b.Expected
	}
	if b.Enabled != nil {
		t.Enabled = *b.Enabled
	}
	if err = Validate(t); err != nil {
		fail(c, 400, err.Error())
		return
	}
	if _, err = s.db.Exec(`UPDATE targets SET name=?,description=?,kind=?,address=?,interval_seconds=?,timeout_seconds=?,expected_status=?,enabled=? WHERE id=? AND deleted=0`, t.Name, t.Description, t.Kind, t.Address, t.Interval, t.Timeout, t.Expected, t.Enabled, tid); err != nil {
		fail(c, 409, "name already exists or database error")
		return
	}
	s.resetSchedule(tid)
	c.JSON(200, t)
}
func (s *Service) monitorRoutes(a *gin.RouterGroup) {
	a.GET("/monitors", func(c *gin.Context) {
		kind, ok := queryKind(c)
		if !ok {
			return
		}
		ts, e := s.Targets(kind)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		c.JSON(200, gin.H{"data": ts})
	})
	a.POST("/monitors", func(c *gin.Context) {
		t := Target{Kind: "http", Interval: 60, Timeout: 10, Expected: 200, Enabled: true}
		if c.ShouldBindJSON(&t) != nil {
			fail(c, 400, "invalid JSON")
			return
		}
		t.Name = strings.TrimSpace(t.Name)
		t.Address = strings.TrimSpace(t.Address)
		if e := Validate(t); e != nil {
			fail(c, 400, e.Error())
			return
		}
		res, e := s.db.Exec(`INSERT INTO targets(name,description,kind,address,interval_seconds,timeout_seconds,expected_status,enabled)VALUES(?,?,?,?,?,?,?,?)`, t.Name, t.Description, t.Kind, t.Address, t.Interval, t.Timeout, t.Expected, t.Enabled)
		if e != nil {
			fail(c, 409, "name already exists or database error")
			return
		}
		t.ID, _ = res.LastInsertId()
		c.JSON(201, t)
	})
	a.PATCH("/monitors/:target", s.editMonitor)
	a.DELETE("/monitors/:target", func(c *gin.Context) {
		tid := id(c, "target")
		if tid == 0 {
			return
		}
		res, e := s.db.Exec(`UPDATE targets SET deleted=1,enabled=0 WHERE id=? AND deleted=0`, tid)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			fail(c, 404, "monitor not found")
			return
		}
		s.resetSchedule(tid)
		c.JSON(200, gin.H{"id": tid, "deleted": true})
	})
	a.GET("/monitors/timeline", s.timelines)
}
func (s *Service) timelines(c *gin.Context) {
	values := strings.Split(c.Query("ids"), ",")
	if len(values) > 100 {
		fail(c, 400, "at most 100 monitors")
		return
	}
	out := []gin.H{}
	now := time.Now().Unix()
	for _, v := range values {
		tid, e := strconv.ParseInt(v, 10, 64)
		if e != nil || tid < 1 {
			fail(c, 400, "invalid monitor IDs")
			return
		}
		var interval int64
		if e = s.db.QueryRow(`SELECT interval_seconds FROM targets WHERE id=?`, tid).Scan(&interval); e != nil {
			fail(c, 404, "monitor not found")
			return
		}
		if interval < 1 {
			interval = 60
		}
		// The overview is deliberately a compact 30-slot strip. Each slot is
		// exactly one configured check interval, so hover/click always describes
		// the same real time window the monitor actually uses.
		end := (now/interval + 1) * interval
		start := end - interval*30
		rows, e := s.db.Query(`SELECT (checked_at / ?)*?,COUNT(*),SUM(ok),AVG(latency_ms) FROM results WHERE target_id=? AND checked_at>=? AND checked_at<? GROUP BY 1 ORDER BY 1`, interval, interval, tid, start, end)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		for rows.Next() {
			var at, n, good int64
			var latency float64
			if rows.Scan(&at, &n, &good, &latency) != nil {
				rows.Close()
				fail(c, 500, "database error")
				return
			}
			out = append(out, gin.H{"target_id": tid, "start": at, "samples": n, "successful": good, "avg_latency_ms": latency, "bucket_seconds": interval, "from": start, "to": end})
		}
		if rows.Err() != nil {
			rows.Close()
			fail(c, 500, "database error")
			return
		}
		rows.Close()
		// Metadata row keeps empty timelines renderable without inventing a global range.
		out = append(out, gin.H{"target_id": tid, "meta": true, "bucket_seconds": interval, "from": start, "to": end})
	}
	c.JSON(200, gin.H{"data": out})
}
