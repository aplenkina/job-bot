// Package store — хранилище на SQLite (чистый Go, без CGO).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/anastasia/job-bot/internal/model"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS vacancies (
	source       TEXT NOT NULL,
	external_id  TEXT NOT NULL,
	url          TEXT NOT NULL,
	title        TEXT NOT NULL,
	company      TEXT NOT NULL,
	description  TEXT NOT NULL DEFAULT '',
	location     TEXT NOT NULL DEFAULT '',
	remote       INTEGER NOT NULL DEFAULT 0,
	salary_from  INTEGER NOT NULL DEFAULT 0,
	salary_to    INTEGER NOT NULL DEFAULT 0,
	currency     TEXT NOT NULL DEFAULT '',
	language     TEXT NOT NULL DEFAULT '',
	published_at INTEGER NOT NULL DEFAULT 0,
	status       TEXT NOT NULL,
	score        INTEGER NOT NULL DEFAULT 0,
	notes        TEXT NOT NULL DEFAULT '',
	reason       TEXT NOT NULL DEFAULT '',
	note         TEXT NOT NULL DEFAULT '',
	letter       TEXT NOT NULL DEFAULT '',
	fingerprint  TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL,
	PRIMARY KEY (source, external_id)
);
CREATE INDEX IF NOT EXISTS idx_vacancies_status ON vacancies(status);

CREATE TABLE IF NOT EXISTS kv (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// Open открывает (или создаёт) базу и применяет схему.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite: один писатель
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// Миграция базы, созданной на этапе 1: добавляем новые колонки, если их ещё нет.
	for _, col := range []string{"notes", "reason", "note", "letter", "fingerprint"} {
		_, err := db.Exec(`ALTER TABLE vacancies ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate %s: %w", col, err)
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_vacancies_fp ON vacancies(fingerprint)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create fingerprint index: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Insert сохраняет вакансию. Возвращает false, если такая уже есть (дубликат).
func (s *Store) Insert(ctx context.Context, v model.Vacancy, status model.Status, notes, reason string) (bool, error) {
	now := time.Now().Unix()
	fp := ""
	if strings.HasPrefix(v.Source, "tg:") { // дубли ищем только среди постов из Telegram
		fp = v.Fingerprint()
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM vacancies WHERE fingerprint=? LIMIT 1`, fp).Scan(&one)
		if err == nil {
			return false, nil // такой пост уже есть (кросспост)
		}
		if err != sql.ErrNoRows {
			return false, fmt.Errorf("check duplicate: %w", err)
		}
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO vacancies
		(source, external_id, url, title, company, description, location, remote,
		 salary_from, salary_to, currency, language, published_at, status, notes, reason, fingerprint, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.Source, v.ExternalID, v.URL, v.Title, v.Company, v.Description, v.Location, b2i(v.Remote),
		v.SalaryFrom, v.SalaryTo, v.Currency, v.Language, v.PublishedAt.Unix(), string(status), notes, reason, fp, now, now)
	if err != nil {
		return false, fmt.Errorf("insert vacancy: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Row — вакансия из базы вместе с внутренним id и пометками.
type Row struct {
	ID     int64
	Notes  string // пометки фильтров («ЗП не указана»)
	Score  int    // оценка соответствия 0..100
	Note   string // объяснение оценки
	Letter string // черновик письма
	model.Vacancy
}

// ListByStatus возвращает вакансии со статусом st, сначала самые свежие.
func (s *Store) ListByStatus(ctx context.Context, st model.Status, limit int) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rowid, source, external_id, url, title, company, description, location, remote,
		       salary_from, salary_to, currency, language, published_at, notes, score, note, letter
		FROM vacancies WHERE status=? ORDER BY published_at DESC LIMIT ?`, string(st), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var remote int
		var pub int64
		if err := rows.Scan(&r.ID, &r.Source, &r.ExternalID, &r.URL, &r.Title, &r.Company, &r.Description,
			&r.Location, &remote, &r.SalaryFrom, &r.SalaryTo, &r.Currency, &r.Language, &pub, &r.Notes, &r.Score, &r.Note, &r.Letter); err != nil {
			return nil, err
		}
		r.Remote = remote == 1
		r.PublishedAt = time.Unix(pub, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveMatch сохраняет оценку и письмо.
func (s *Store) SaveMatch(ctx context.Context, id int64, score int, note, letter string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE vacancies SET score=?, note=?, letter=?, updated_at=? WHERE rowid=?`,
		score, note, letter, time.Now().Unix(), id)
	return err
}

// Reject переводит вакансию в filtered с причиной.
func (s *Store) Reject(ctx context.Context, id int64, score int, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE vacancies SET status=?, score=?, reason=?, updated_at=? WHERE rowid=?`,
		string(model.StatusFiltered), score, reason, time.Now().Unix(), id)
	return err
}

// SetStatusByID меняет статус по внутреннему id.
func (s *Store) SetStatusByID(ctx context.Context, id int64, st model.Status) error {
	_, err := s.db.ExecContext(ctx, `UPDATE vacancies SET status=?, updated_at=? WHERE rowid=?`,
		string(st), time.Now().Unix(), id)
	return err
}

// SetStatus меняет статус вакансии.
func (s *Store) SetStatus(ctx context.Context, source, id string, st model.Status) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE vacancies SET status=?, updated_at=? WHERE source=? AND external_id=?`,
		string(st), time.Now().Unix(), source, id)
	return err
}

// Unpark возвращает в работу (new) отложенные вакансии, где компания встречается как отдельное слово
// в названии компании, заголовке или начале текста: у постов из Telegram название компании не выделено.
func (s *Store) Unpark(ctx context.Context, company string) (int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT rowid, company, title, description FROM vacancies WHERE status=?`, string(model.StatusParked))
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var v model.Vacancy
		if err := rows.Scan(&id, &v.Company, &v.Title, &v.Description); err != nil {
			rows.Close()
			return 0, err
		}
		if v.Mentions(company) {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	now := time.Now().Unix()
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE vacancies SET status=?, updated_at=? WHERE rowid=?`,
			string(model.StatusNew), now, id); err != nil {
			return 0, err
		}
	}
	return int64(len(ids)), nil
}

// Counts возвращает число вакансий по статусам.
func (s *Store) Counts(ctx context.Context) (map[model.Status]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM vacancies GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[model.Status]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[model.Status(st)] = n
	}
	return out, rows.Err()
}

// Paused — стоит ли бот на паузе.
func (s *Store) Paused(ctx context.Context) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key='paused'`).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v == "1", err
}

func (s *Store) SetPaused(ctx context.Context, p bool) error {
	v := "0"
	if p {
		v = "1"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO kv(key,value) VALUES('paused',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, v)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
