package monitor

import (
	"crypto/subtle"
	"database/sql"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func fail(c *gin.Context, status int, msg string) { c.AbortWithStatusJSON(status, gin.H{"error": msg}) }
func id(c *gin.Context, key string) int64 {
	v, e := strconv.ParseInt(c.Param(key), 10, 64)
	if e != nil || v < 1 {
		fail(c, 400, "invalid ID")
		return 0
	}
	return v
}
func (s *Service) Router(token, origin string) *gin.Engine {
	r := gin.New()
	// Remove the credential before Gin logging/recovery can inspect the URL.
	r.Use(func(c *gin.Context) {
		query := c.Request.URL.Query()
		values := query["token"]
		if len(values) == 1 {
			c.Set("query_token", values[0])
		}
		query.Del("token")
		c.Request.URL.RawQuery = query.Encode()
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	})
	r.Use(gin.Logger(), gin.Recovery())
	r.Use(func(c *gin.Context) {
		if origin == "*" || (origin != "" && c.GetHeader("Origin") == origin) {
			c.Header("Access-Control-Allow-Origin", origin)
			if origin != "*" {
				c.Header("Vary", "Origin")
			}
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		}
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})
	r.GET("/health", func(c *gin.Context) {
		if s.db.PingContext(c.Request.Context()) != nil {
			fail(c, 503, "database unavailable")
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	a := r.Group("/api/v1")
	a.Use(func(c *gin.Context) {
		provided := c.GetString("query_token")
		if auth := c.GetHeader("Authorization"); auth != "" {
			// A supplied header takes precedence; invalid headers cannot fall back to URL tokens.
			if !strings.HasPrefix(auth, "Bearer ") {
				fail(c, 401, "unauthorized")
				return
			}
			provided = strings.TrimPrefix(auth, "Bearer ")
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			fail(c, 401, "unauthorized")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
		c.Next()
	})
	s.configRoutes(a)
	a.GET("/projects", func(c *gin.Context) {
		rows, e := s.db.Query(`SELECT id,name,description FROM projects ORDER BY id`)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		defer rows.Close()
		out := []Project{}
		for rows.Next() {
			var p Project
			if rows.Scan(&p.ID, &p.Name, &p.Description) != nil {
				fail(c, 500, "database error")
				return
			}
			out = append(out, p)
		}
		if rows.Err() != nil {
			fail(c, 500, "database error")
			return
		}
		c.JSON(200, gin.H{"data": out})
	})
	a.POST("/projects", func(c *gin.Context) {
		var p Project
		if c.ShouldBindJSON(&p) != nil || p.Name == "" || len(p.Name) > 100 || len(p.Description) > 2000 {
			fail(c, 400, "name required, max 100 bytes; description max 2000 bytes")
			return
		}
		res, e := s.db.Exec(`INSERT INTO projects(name,description)VALUES(?,?)`, p.Name, p.Description)
		if e != nil {
			fail(c, 409, "could not create project; name must be unique")
			return
		}
		p.ID, _ = res.LastInsertId()
		c.JSON(201, p)
	})
	a.PATCH("/projects/:project", func(c *gin.Context) {
		pid := id(c, "project")
		if pid == 0 {
			return
		}
		var b struct {
			Description *string `json:"description"`
		}
		if c.ShouldBindJSON(&b) != nil || b.Description == nil || len(*b.Description) > 2000 {
			fail(c, 400, "description required, max 2000 bytes")
			return
		}
		res, e := s.db.Exec(`UPDATE projects SET description=? WHERE id=?`, *b.Description, pid)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			fail(c, 404, "project not found")
			return
		}
		var p Project
		if e = s.db.QueryRow(`SELECT id,name,description FROM projects WHERE id=?`, pid).Scan(&p.ID, &p.Name, &p.Description); e != nil {
			fail(c, 500, "database error")
			return
		}
		c.JSON(200, p)
	})
	a.GET("/projects/:project/targets", func(c *gin.Context) {
		p := id(c, "project")
		if p == 0 {
			return
		}
		kind, ok := queryKind(c)
		if !ok {
			return
		}
		ts, e := s.Targets(p, kind)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		c.JSON(200, gin.H{"data": ts})
	})
	a.POST("/projects/:project/targets", func(c *gin.Context) {
		p := id(c, "project")
		if p == 0 {
			return
		}
		t := Target{Interval: 60, Timeout: 10, Expected: 200, Enabled: true}
		if c.ShouldBindJSON(&t) != nil {
			fail(c, 400, "invalid JSON")
			return
		}
		t.ProjectID = p
		if e := Validate(t); e != nil {
			fail(c, 400, e.Error())
			return
		}
		res, e := s.db.Exec(`INSERT INTO targets(project_id,name,kind,address,interval_seconds,timeout_seconds,expected_status,enabled)VALUES(?,?,?,?,?,?,?,?)`, p, t.Name, t.Kind, t.Address, t.Interval, t.Timeout, t.Expected, t.Enabled)
		if e != nil {
			fail(c, 409, "project missing or target name already exists")
			return
		}
		t.ID, _ = res.LastInsertId()
		c.JSON(201, t)
	})
	a.PATCH("/targets/:target", func(c *gin.Context) {
		tid := id(c, "target")
		if tid == 0 {
			return
		}
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if c.ShouldBindJSON(&body) != nil || body.Enabled == nil {
			fail(c, 400, "enabled boolean required")
			return
		}
		res, e := s.db.Exec(`UPDATE targets SET enabled=? WHERE id=?`, *body.Enabled, tid)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			fail(c, 404, "target not found")
			return
		}
		c.JSON(200, gin.H{"id": tid, "enabled": *body.Enabled})
	})
	a.GET("/status", func(c *gin.Context) { s.statusPage(c, 0) })
	a.GET("/projects/:project/status", func(c *gin.Context) {
		p := id(c, "project")
		if p > 0 {
			s.statusPage(c, p)
		}
	})
	a.GET("/targets/:target/history", func(c *gin.Context) {
		tid := id(c, "target")
		if tid == 0 {
			return
		}
		from, to, ok := window(c)
		if !ok {
			return
		}
		limit := 100
		if v := c.Query("limit"); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || n > 1000 {
				fail(c, 400, "limit must be 1..1000")
				return
			}
			limit = n
		}
		before := int64(9223372036854775807)
		if v := c.Query("before_id"); v != "" {
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n < 1 {
				fail(c, 400, "invalid before_id")
				return
			}
			before = n
		}
		rows, e := s.db.Query(`SELECT id,target_id,checked_at,ok,latency_ms,http_status,error,expires_at FROM results WHERE target_id=? AND checked_at>=? AND checked_at<? AND id<? ORDER BY id DESC LIMIT ?`, tid, from, to, before, limit)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		defer rows.Close()
		out := []Result{}
		for rows.Next() {
			var v Result
			if rows.Scan(&v.ID, &v.TargetID, &v.At, &v.OK, &v.Latency, &v.Status, &v.Error, &v.Expires) != nil {
				fail(c, 500, "database error")
				return
			}
			out = append(out, v)
		}
		if rows.Err() != nil {
			fail(c, 500, "database error")
			return
		}
		var cursor any
		if len(out) == limit {
			cursor = out[len(out)-1].ID
		}
		c.JSON(200, gin.H{"data": out, "next_before_id": cursor})
	})
	a.GET("/targets/:target/stats", func(c *gin.Context) {
		tid := id(c, "target")
		if tid == 0 {
			return
		}
		from, to, ok := window(c)
		if !ok {
			return
		}
		bucket := int64(3600)
		if v := c.Query("bucket_seconds"); v != "" {
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n < 60 || n > 86400 {
				fail(c, 400, "bucket_seconds must be 60..86400")
				return
			}
			bucket = n
		}
		if (to-from)/bucket > 10000 {
			fail(c, 400, "too many buckets")
			return
		}
		var total, success int64
		var avg sql.NullFloat64
		e := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(ok),0),AVG(latency_ms) FROM results WHERE target_id=? AND checked_at>=? AND checked_at<?`, tid, from, to).Scan(&total, &success, &avg)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		rows, e := s.db.Query(`SELECT (checked_at / ?)*?,COUNT(*),SUM(ok),AVG(latency_ms) FROM results WHERE target_id=? AND checked_at>=? AND checked_at<? GROUP BY 1 ORDER BY 1`, bucket, bucket, tid, from, to)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		defer rows.Close()
		out := []gin.H{}
		for rows.Next() {
			var at, n, good int64
			var latency float64
			if rows.Scan(&at, &n, &good, &latency) != nil {
				fail(c, 500, "database error")
				return
			}
			out = append(out, gin.H{"start": at, "samples": n, "successful": good, "availability_percent": float64(good) * 100 / float64(n), "avg_latency_ms": latency})
		}
		if rows.Err() != nil {
			fail(c, 500, "database error")
			return
		}
		var pct, latency any
		if total > 0 {
			pct = float64(success) * 100 / float64(total)
			latency = avg.Float64
		}
		c.JSON(200, gin.H{"from": from, "to": to, "samples": total, "successful": success, "availability_percent": pct, "avg_latency_ms": latency, "bucket_seconds": bucket, "buckets": out, "method": "successful_samples / total_samples"})
	})
	return r
}
func window(c *gin.Context) (int64, int64, bool) {
	now := time.Now().Unix()
	from, to := now-86400, now+1
	for key, dst := range map[string]*int64{"from": &from, "to": &to} {
		if v := c.Query(key); v != "" {
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil {
				fail(c, 400, "from/to must be Unix seconds")
				return 0, 0, false
			}
			*dst = n
		}
	}
	if from < 0 || to <= from || to-from > 366*86400 {
		fail(c, 400, "time range must be positive and at most 366 days")
		return 0, 0, false
	}
	return from, to, true
}

// statusPage paginates targets in SQL rather than loading the entire inventory.
func (s *Service) statusPage(c *gin.Context, project int64) {
	page, size := int64(1), int64(20)
	for key, dst := range map[string]*int64{"page": &page, "page_size": &size} {
		if value, exists := c.GetQuery(key); exists {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 1 {
				fail(c, 400, key+" must be a positive integer")
				return
			}
			*dst = n
		}
	}
	if size > 100 || page > 1000000000 {
		fail(c, 400, "page_size must be 1..100; page must be 1..1000000000")
		return
	}
	kind, ok := queryKind(c)
	if !ok {
		return
	}
	where := ""
	args := []any{}
	if project > 0 {
		where = " WHERE project_id=?"
		args = append(args, project)
	}
	if kind != "" {
		if where == "" {
			where = " WHERE kind=?"
		} else {
			where += " AND kind=?"
		}
		args = append(args, kind)
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		fail(c, 500, "database error")
		return
	}
	defer tx.Rollback()
	var total int64
	if err = tx.QueryRowContext(c.Request.Context(), "SELECT COUNT(*) FROM targets"+where, args...).Scan(&total); err != nil {
		fail(c, 500, "database error")
		return
	}
	pageArgs := append(append([]any{}, args...), size, (page-1)*size)
	rows, err := tx.QueryContext(c.Request.Context(), `SELECT id,project_id,name,kind,address,interval_seconds,timeout_seconds,expected_status,enabled FROM targets`+where+` ORDER BY id LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		fail(c, 500, "database error")
		return
	}
	targets := []Target{}
	for rows.Next() {
		var t Target
		if err = rows.Scan(&t.ID, &t.ProjectID, &t.Name, &t.Kind, &t.Address, &t.Interval, &t.Timeout, &t.Expected, &t.Enabled); err != nil {
			rows.Close()
			fail(c, 500, "database error")
			return
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		fail(c, 500, "database error")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(c, 500, "database error")
		return
	}
	out := []gin.H{}
	for _, t := range targets {
		latest, e := s.Latest(t.ID)
		if e != nil {
			fail(c, 500, "database error")
			return
		}
		state := "unknown"
		if !t.Enabled {
			state = "paused"
		} else if !stale(t, latest) {
			state = "down"
			if latest.OK {
				state = "up"
			}
		}
		v := gin.H{"target": t, "state": state, "latest": latest}
		if latest != nil && latest.Expires > 0 {
			remaining := latest.Expires - time.Now().Unix()
			v["expires_in_seconds"] = remaining
			v["expiry_warning"] = remaining < 30*86400
		}
		out = append(out, v)
	}
	c.JSON(200, gin.H{"data": out, "page": page, "page_size": size, "total": total, "total_pages": (total + size - 1) / size})
}

func queryKind(c *gin.Context) (string, bool) {
	values, exists := c.Request.URL.Query()["kind"]
	if !exists {
		return "", true
	}
	if len(values) != 1 || (values[0] != "http" && values[0] != "ssl") {
		fail(c, 400, "kind must be http or ssl; omit it to query all")
		return "", false
	}
	return values[0], true
}
