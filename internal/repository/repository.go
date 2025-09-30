package repository

import (
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

type ProxyModel struct {
	ID        int64
	Username  string
	Password  string
	Target    string
	CreatedAt string
}

type IProxyRepository interface {
	Create(username, password, target string) (*ProxyModel, error)
	Update(username, password, target string) error
	Delete(username string) error
	FindByUsername(username string) (*ProxyModel, error)
	FindAll() ([]*ProxyModel, error)
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_username ON proxies(username);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_target ON proxies(target);
	`

	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	return &SQLiteRepository{db: db}, nil
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
		"SELECT id, username, password, target, created_at FROM proxies WHERE username = ?",
		username,
	).Scan(&model.ID, &model.Username, &model.Password, &model.Target, &model.CreatedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query proxy: %w", err)
	}

	return &model, nil
}

func (r *SQLiteRepository) FindAll() ([]*ProxyModel, error) {
	rows, err := r.db.Query("SELECT id, username, password, target, created_at FROM proxies")
	if err != nil {
		return nil, fmt.Errorf("failed to query proxies: %w", err)
	}
	defer rows.Close()

	var models []*ProxyModel
	for rows.Next() {
		var model ProxyModel
		if err := rows.Scan(&model.ID, &model.Username, &model.Password, &model.Target, &model.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan proxy: %w", err)
		}
		models = append(models, &model)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return models, nil
}

func (r *SQLiteRepository) Close() error {
	return r.db.Close()
}
