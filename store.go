package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Entry struct {
	Date      string `json:"date"`
	Body      string `json:"body"`
	UpdatedAt string `json:"updated_at"`
}

// Value is nil for a plain "done" check and set for measured habits (steps, minutes).
type HabitLog struct {
	Date      string   `json:"date"`
	Habit     string   `json:"habit"`
	Value     *float64 `json:"value"`
	UpdatedAt string   `json:"updated_at"`
}

type Store struct{ db *sql.DB }

var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS entries (
	date       TEXT PRIMARY KEY,
	body       TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS habit_logs (
	date       TEXT NOT NULL,
	habit      TEXT NOT NULL,
	value      REAL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (date, habit)
);`

func OpenStore(path string) (*Store, error) {
	// The driver reads "?" as the start of DSN options and "file:" as a URI, so such paths would open a different file.
	if strings.Contains(path, "?") || strings.HasPrefix(path, "file:") || path == ":memory:" {
		return nil, fmt.Errorf("HIBI_DB must be a plain file path: %q", path)
	}
	// The diary is private: create the file owner-only before SQLite creates it with the umask.
	// Chmod as well, so a file created earlier with a looser mode is tightened too; via the descriptor, so a swapped path cannot redirect it.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0o600)
	f.Close()
	if err != nil {
		return nil, err
	}
	// busy_timeout covers writers outside this process (an sqlite3 .backup run).
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// One connection serializes writes, so concurrent requests queue in Go instead of failing with SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Store) PutEntry(date, body string) (Entry, error) {
	e := Entry{Date: date, Body: body, UpdatedAt: now()}
	_, err := s.db.Exec(`INSERT INTO entries (date, body, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(date) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
		e.Date, e.Body, e.UpdatedAt)
	return e, err
}

func (s *Store) GetEntry(date string) (Entry, error) {
	var e Entry
	err := s.db.QueryRow(`SELECT date, body, updated_at FROM entries WHERE date = ?`, date).
		Scan(&e.Date, &e.Body, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// ListEntries returns entries in [from, to], newest first. ISO dates sort lexically.
func (s *Store) ListEntries(from, to string) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT date, body, updated_at FROM entries
		WHERE date >= ? AND date <= ? ORDER BY date DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Date, &e.Body, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) PutHabit(date, habit string, value *float64) (HabitLog, error) {
	h := HabitLog{Date: date, Habit: habit, Value: value, UpdatedAt: now()}
	_, err := s.db.Exec(`INSERT INTO habit_logs (date, habit, value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(date, habit) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		h.Date, h.Habit, h.Value, h.UpdatedAt)
	return h, err
}

func (s *Store) DeleteEntry(date string) error {
	return deleted(s.db.Exec(`DELETE FROM entries WHERE date = ?`, date))
}

func (s *Store) DeleteHabit(date, habit string) error {
	return deleted(s.db.Exec(`DELETE FROM habit_logs WHERE date = ? AND habit = ?`, date, habit))
}

func deleted(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListHabits(date string) ([]HabitLog, error) {
	rows, err := s.db.Query(`SELECT date, habit, value, updated_at FROM habit_logs
		WHERE date = ? ORDER BY habit`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HabitLog{}
	for rows.Next() {
		var h HabitLog
		if err := rows.Scan(&h.Date, &h.Habit, &h.Value, &h.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
