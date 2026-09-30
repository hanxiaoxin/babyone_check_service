package monitor

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"time"
)

type Service struct {
	db     *sql.DB
	notify *notifier
}
type Project struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type Target struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Address   string `json:"address"`
	Interval  int64  `json:"interval_seconds"`
	Timeout   int64  `json:"timeout_seconds"`
	Expected  int    `json:"expected_status"`
	Enabled   bool   `json:"enabled"`
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
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
 CREATE TABLE IF NOT EXISTS projects(id INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE);
 CREATE TABLE IF NOT EXISTS targets(id INTEGER PRIMARY KEY,project_id INTEGER NOT NULL REFERENCES projects(id),name TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('http','ssl')),address TEXT NOT NULL,interval_seconds INTEGER NOT NULL,timeout_seconds INTEGER NOT NULL,expected_status INTEGER NOT NULL,enabled INTEGER NOT NULL,UNIQUE(project_id,name));
 CREATE TABLE IF NOT EXISTS results(id INTEGER PRIMARY KEY,target_id INTEGER NOT NULL REFERENCES targets(id),checked_at INTEGER NOT NULL,ok INTEGER NOT NULL,latency_ms INTEGER NOT NULL,http_status INTEGER NOT NULL,error TEXT NOT NULL,expires_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS results_target_time ON results(target_id,checked_at,id);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Service{db: db}, nil
}
func (s *Service) Close() error { return s.db.Close() }
func (s *Service) Targets(project int64) ([]Target, error) {
	q := `SELECT id,project_id,name,kind,address,interval_seconds,timeout_seconds,expected_status,enabled FROM targets`
	args := []any{}
	if project > 0 {
		q += ` WHERE project_id=?`
		args = append(args, project)
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
		if err = rows.Scan(&t.ID, &t.ProjectID, &t.Name, &t.Kind, &t.Address, &t.Interval, &t.Timeout, &t.Expected, &t.Enabled); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Service) Save(r Result) error {
	_, err := s.db.Exec(`INSERT INTO results(target_id,checked_at,ok,latency_ms,http_status,error,expires_at)VALUES(?,?,?,?,?,?,?)`, r.TargetID, r.At, r.OK, r.Latency, r.Status, r.Error, r.Expires)
	return err
}
func (s *Service) Latest(id int64) (*Result, error) {
	var r Result
	err := s.db.QueryRow(`SELECT id,target_id,checked_at,ok,latency_ms,http_status,error,expires_at FROM results WHERE target_id=? ORDER BY checked_at DESC,id DESC LIMIT 1`, id).Scan(&r.ID, &r.TargetID, &r.At, &r.OK, &r.Latency, &r.Status, &r.Error, &r.Expires)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}
func (s *Service) Seed() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, name := range []string{"babycare", "music", "yolo", "websites"} {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO projects(name)VALUES(?)`, name); err != nil {
			return err
		}
	}
	for _, name := range []string{"babycare", "music", "yolo"} {
		_, err = tx.Exec(`INSERT OR IGNORE INTO targets(project_id,name,kind,address,interval_seconds,timeout_seconds,expected_status,enabled) SELECT id,?,'http',?,60,10,200,1 FROM projects WHERE name=?`, name, "https://baby.hanlinbo.cn/"+name+"/api/health", name)
		if err != nil {
			return err
		}
	}
	for _, host := range []string{"www.hanlinbo.cn", "www.hanxiaoxin.cn", "baby.hanlinbo.cn"} {
		_, err = tx.Exec(`INSERT OR IGNORE INTO targets(project_id,name,kind,address,interval_seconds,timeout_seconds,expected_status,enabled) SELECT id,?,'ssl',?,86400,10,200,1 FROM projects WHERE name='websites'`, host, host)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func stale(t Target, r *Result) bool {
	return r == nil || time.Now().Unix()-r.At > t.Interval*2+t.Timeout
}
