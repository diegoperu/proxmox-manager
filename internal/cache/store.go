package cache

import (
	"database/sql"
	"encoding/json"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal=WAL&_timeout=5000&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	return s, s.migrate()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS api_cache (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	);
	`)
	return err
}

func (s *Store) CacheSet(key string, value interface{}, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO api_cache(key,value,expires_at) VALUES(?,?,?)`,
		key, string(b), time.Now().Add(ttl).Unix(),
	)
	return err
}

func (s *Store) CacheGet(key string, dest interface{}) bool {
	var value string
	var exp int64
	if err := s.db.QueryRow(`SELECT value, expires_at FROM api_cache WHERE key=?`, key).Scan(&value, &exp); err != nil {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}
	return json.Unmarshal([]byte(value), dest) == nil
}

func (s *Store) Close() error { return s.db.Close() }
