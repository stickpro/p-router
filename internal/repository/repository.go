package repository

import (
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

type ProxyModel struct {
	ID           int64
	Username     string
	Password     string
	Target       string
	FailedChecks int
	LastCheckAt  string
	CreatedAt    string
}

type IProxyRepository interface {
	Create(username, password, target string) (*ProxyModel, error)
	Update(username, password, target string) error
	Delete(username string) error
	FindByUsername(username string) (*ProxyModel, error)
	FindAll() ([]*ProxyModel, error)
	IncrementFailedChecks(username string) error
	ResetFailedChecks(username string) error
	Close() error
}

type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(dbPath string) (*SQLiteRepository, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	createTableSQL := `
	CREATE TABLE IF NOT EXISTS proxies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password TEXT NOT NULL,
		target TEXT NOT NULL,
		failed_checks INTEGER DEFAULT 0,
    	last_check_at DATETIME DEFAULT NULL, 
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_username ON proxies(username);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_target ON proxies(target);
	`

	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	if err := migrateProxiesTable(db); err != nil {
		db.Close()
		return nil, err
	}

	return &SQLiteRepository{db: db}, nil
}

func migrateProxiesTable(db *sql.DB) error {
	columns := map[string]bool{}

	rows, err := db.Query(`PRAGMA table_info(proxies);`)
	if err != nil {
		return fmt.Errorf("failed to get table info: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dflt_value sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt_value, &pk); err != nil {
			return fmt.Errorf("failed to scan table info: %w", err)
		}
		columns[name] = true
	}

	if !columns["failed_checks"] {
		if _, err := db.Exec(`ALTER TABLE proxies ADD COLUMN failed_checks INTEGER DEFAULT 0;`); err != nil {
			return fmt.Errorf("failed to add column failed_checks: %w", err)
		}
	}

	if !columns["last_check_at"] {
		if _, err := db.Exec(`ALTER TABLE proxies ADD COLUMN last_check_at DATETIME DEFAULT NULL;`); err != nil {
			return fmt.Errorf("failed to add column last_check_at: %w", err)
		}
	}

	return nil
}

func (r *SQLiteRepository) Create(username, password, target string) (*ProxyModel, error) {
	result, err := r.db.Exec(
		"INSERT INTO proxies (username, password, target) VALUES (?, ?, ?)",
		username, password, target,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert proxy: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get last insert id: %w", err)
	}

	return &ProxyModel{
		ID:       id,
		Username: username,
		Password: password,
		Target:   target,
	}, nil
}

func (r *SQLiteRepository) Update(username, password, target string) error {
	result, err := r.db.Exec(
		"UPDATE proxies SET password = ?, target = ? WHERE username = ?",
		password, target, username,
	)
	if err != nil {
		return fmt.Errorf("failed to update proxy: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("proxy with username %s not found", username)
	}

	return nil
}

func (r *SQLiteRepository) Delete(username string) error {
	result, err := r.db.Exec("DELETE FROM proxies WHERE username = ?", username)
	if err != nil {
		return fmt.Errorf("failed to delete proxy: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("proxy with username %s not found", username)
	}

	return nil
}

func (r *SQLiteRepository) FindByUsername(username string) (*ProxyModel, error) {
	var model ProxyModel
	err := r.db.QueryRow(
		"SELECT id, username, password, target, failed_checks, created_at FROM proxies WHERE username = ?",
		username,
	).Scan(&model.ID, &model.Username, &model.Password, &model.Target, &model.FailedChecks, &model.CreatedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query proxy: %w", err)
	}

	return &model, nil
}

func (r *SQLiteRepository) FindAll() ([]*ProxyModel, error) {
	rows, err := r.db.Query("SELECT id, username, password, target, failed_checks, created_at FROM proxies")
	if err != nil {
		return nil, fmt.Errorf("failed to query proxies: %w", err)
	}
	defer rows.Close()

	var models []*ProxyModel
	for rows.Next() {
		var model ProxyModel
		if err := rows.Scan(&model.ID, &model.Username, &model.Password, &model.Target, &model.FailedChecks, &model.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan proxy: %w", err)
		}
		models = append(models, &model)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return models, nil
}

func (r *SQLiteRepository) IncrementFailedChecks(username string) error {
	result, err := r.db.Exec(
		"UPDATE proxies SET failed_checks = failed_checks + 1, last_check_at = CURRENT_TIMESTAMP WHERE username = ?",
		username,
	)
	if err != nil {
		return fmt.Errorf("failed to increment failed checks: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("proxy with username %s not found", username)
	}

	return nil
}

func (r *SQLiteRepository) ResetFailedChecks(username string) error {
	result, err := r.db.Exec(
		"UPDATE proxies SET failed_checks = 0, last_check_at = CURRENT_TIMESTAMP WHERE username = ?",
		username,
	)
	if err != nil {
		return fmt.Errorf("failed to reset failed checks: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("proxy with username %s not found", username)
	}

	return nil
}

func (r *SQLiteRepository) Close() error {
	return r.db.Close()
}
