package monitor

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"sync"
	"time"
)

type Service struct {
	db         *sql.DB
	notify     *notifier
	scheduleMu sync.Mutex
	schedule   map[int64]scheduleEntry
}
type Target struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Address     string `json:"address"`
	Interval    int64  `json:"interval_seconds"`
	Timeout     int64  `json:"timeout_seconds"`
	Expected    int    `json:"expected_status"`
	Enabled     bool   `json:"enabled"`
}
type Result struct {
	ID       int64  `json:"id"`
	TargetID int64  `json:"target_id"`
	At       int64  `json:"checked_at"`
	OK       bool   `json:"ok"`
	Latency  int64  `json:"latency_ms"`
	Status   int    `json:"http_status"`
	Error    string `json:"error"`
	Expires  int64  `json:"expires_at"`
}

func Open(path string) (*Service, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var legacy int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&legacy); err != nil {
		db.Close()
		return nil, err
	}
	if legacy > 0 {
		db.Close()
		return nil, fmt.Errorf("old database schema: use a new DB_PATH (monitor-v2.db) or remove the old database before starting")
	}
	_, err = db.Exec(`PRAGMA journal_mode=WAL;PRAGMA busy_timeout=5000;PRAGMA foreign_keys=ON;
 CREATE TABLE IF NOT EXISTS targets(id INTEGER PRIMARY KEY,name TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',kind TEXT NOT NULL CHECK(kind IN ('http','ssl')),address TEXT NOT NULL,interval_seconds INTEGER NOT NULL,timeout_seconds INTEGER NOT NULL,expected_status INTEGER NOT NULL,enabled INTEGER NOT NULL,deleted INTEGER NOT NULL DEFAULT 0);
 CREATE UNIQUE INDEX IF NOT EXISTS targets_name_active ON targets(name) WHERE deleted=0;
 CREATE TABLE IF NOT EXISTS results(id INTEGER PRIMARY KEY,target_id INTEGER NOT NULL REFERENCES targets(id),checked_at INTEGER NOT NULL,ok INTEGER NOT NULL,latency_ms INTEGER NOT NULL,http_status INTEGER NOT NULL,error TEXT NOT NULL,expires_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS results_target_time ON results(target_id,checked_at,id);
 CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Service{db: db, schedule: make(map[int64]scheduleEntry)}, nil
}
func (s *Service) Close() error { return s.db.Close() }
func (s *Service) Targets(kinds ...string) ([]Target, error) {
	q := `SELECT id,name,description,kind,address,interval_seconds,timeout_seconds,expected_status,enabled FROM targets WHERE deleted=0`
	args := []any{}
	if len(kinds) > 0 && kinds[0] != "" {
		q += ` AND kind=?`
		args = append(args, kinds[0])
	}
	q += ` ORDER BY id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		var t Target
		if err = rows.Scan(&t.ID, &t.Name, &t.Description, &t.Kind, &t.Address, &t.Interval, &t.Timeout, &t.Expected, &t.Enabled); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Service) Save(r Result) error {
	_, e := s.db.Exec(`INSERT INTO results(target_id,checked_at,ok,latency_ms,http_status,error,expires_at)VALUES(?,?,?,?,?,?,?)`, r.TargetID, r.At, r.OK, r.Latency, r.Status, r.Error, r.Expires)
	return e
}
func (s *Service) Latest(id int64) (*Result, error) {
	var r Result
	e := s.db.QueryRow(`SELECT id,target_id,checked_at,ok,latency_ms,http_status,error,expires_at FROM results WHERE target_id=? ORDER BY checked_at DESC,id DESC LIMIT 1`, id).Scan(&r.ID, &r.TargetID, &r.At, &r.OK, &r.Latency, &r.Status, &r.Error, &r.Expires)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	return &r, e
}
func (s *Service) Seed() error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRow(`SELECT COUNT(*) FROM metadata WHERE key='seeded'`).Scan(&count); e != nil {
		return e
	}
	if count > 0 {
		return nil
	}
	for _, name := range []string{"babycare", "music", "yolo"} {
		if _, e = tx.Exec(`INSERT INTO targets(name,kind,address,interval_seconds,timeout_seconds,expected_status,enabled)VALUES(?,'http',?,60,10,200,1)`, name, "https://baby.hanlinbo.cn/"+name+"/api/health"); e != nil {
			return e
		}
	}
	for _, host := range []string{"www.hanlinbo.cn", "www.hanxiaoxin.cn", "baby.hanlinbo.cn"} {
		if _, e = tx.Exec(`INSERT INTO targets(name,kind,address,interval_seconds,timeout_seconds,expected_status,enabled)VALUES(?,'ssl',?,86400,10,200,1)`, host, host); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(`INSERT INTO metadata(key,value)VALUES('seeded','1')`); e != nil {
		return e
	}
	return tx.Commit()
}
func stale(t Target, r *Result) bool {
	return r == nil || time.Now().Unix()-r.At > t.Interval*2+t.Timeout
}
