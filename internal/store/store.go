// Package store holds Bragi's database queries.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique constraint fails.
var ErrConflict = errors.New("already exists")

// Store wraps the database handle.
type Store struct {
	DB *sql.DB
}

// New returns a Store for d.
func New(d *sql.DB) *Store { return &Store{DB: d} }

const timeFormat = "2006-01-02T15:04:05Z"

func now() string { return time.Now().UTC().Format(timeFormat) }

func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}

// ---- Users and sessions ----

// User is a Bragi account.
type User struct {
	ID             int64
	Username       string
	PasswordHash   string
	Role           string
	APIKey         string
	JellyfinUserID string
	AutoApprove    bool
	CreatedAt      string
}

const userCols = `id, username, COALESCE(password_hash, ''), role, api_key, COALESCE(jellyfin_user_id, ''), auto_approve, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.APIKey, &u.JellyfinUserID, &u.AutoApprove, &u.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}

// CountUsers returns how many users exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateFirstAdmin creates the admin account only if no user exists yet.
// It returns ErrConflict if setup already happened.
func (s *Store) CreateFirstAdmin(ctx context.Context, username, passwordHash, apiKey string) (*User, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, api_key, auto_approve)
		SELECT ?, ?, 'admin', ?, 1 WHERE NOT EXISTS (SELECT 1 FROM users)`, username, passwordHash, apiKey)
	if err != nil {
		return nil, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrConflict
	}
	id, _ := res.LastInsertId()
	return s.GetUser(ctx, id)
}

// GetUser loads a user by id.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// GetUserByUsername loads a user by name, case-insensitively.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
}

// GetUserByAPIKey loads the user that owns apiKey.
func (s *Store) GetUserByAPIKey(ctx context.Context, apiKey string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE api_key = ?`, apiKey))
}

// SetAPIKey replaces a user's API key.
func (s *Store) SetAPIKey(ctx context.Context, userID int64, apiKey string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET api_key = ? WHERE id = ?`, apiKey, userID)
	return mapErr(err)
}

// CreateSession stores a session token hash for a user.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID int64, expires time.Time) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now()); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		tokenHash, userID, expires.UTC().Format(timeFormat))
	return err
}

// GetSessionUser returns the user of an unexpired session.
func (s *Store) GetSessionUser(ctx context.Context, tokenHash string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT u.id, u.username, COALESCE(u.password_hash, ''), u.role, u.api_key,
		COALESCE(u.jellyfin_user_id, ''), u.auto_approve, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, now()))
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// ---- Settings ----

// GetSetting returns a setting's value, or "" if unset.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---- Quality presets ----

// QualityPreset is an ordered list of acceptable qualities plus a size cap.
type QualityPreset struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Qualities   []string `json:"qualities"`
	MaxMBPerMin float64  `json:"maxMbPerMin"`
	Builtin     bool     `json:"builtin"`
}

// ListQualityPresets returns all presets.
func (s *Store) ListQualityPresets(ctx context.Context) ([]QualityPreset, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, description, qualities, max_mb_per_min, builtin FROM quality_presets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QualityPreset{}
	for rows.Next() {
		var p QualityPreset
		var q string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &q, &p.MaxMBPerMin, &p.Builtin); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(q), &p.Qualities); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- Libraries ----

// Library is a root folder for one media type.
type Library struct {
	ID              int64  `json:"id"`
	MediaType       string `json:"mediaType"`
	Name            string `json:"name"`
	Path            string `json:"path"`
	QualityPresetID *int64 `json:"qualityPresetId"`
	CreatedAt       string `json:"createdAt"`
}

const libraryCols = `id, media_type, name, path, quality_preset_id, created_at`

func scanLibrary(row interface{ Scan(...any) error }) (*Library, error) {
	var l Library
	var preset sql.NullInt64
	if err := row.Scan(&l.ID, &l.MediaType, &l.Name, &l.Path, &preset, &l.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	if preset.Valid {
		l.QualityPresetID = &preset.Int64
	}
	return &l, nil
}

// ListLibraries returns all libraries.
func (s *Store) ListLibraries(ctx context.Context) ([]Library, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+libraryCols+` FROM libraries ORDER BY media_type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Library{}
	for rows.Next() {
		l, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// GetLibrary loads one library.
func (s *Store) GetLibrary(ctx context.Context, id int64) (*Library, error) {
	return scanLibrary(s.DB.QueryRowContext(ctx, `SELECT `+libraryCols+` FROM libraries WHERE id = ?`, id))
}

// CreateLibrary inserts a library.
func (s *Store) CreateLibrary(ctx context.Context, l Library) (*Library, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO libraries (media_type, name, path, quality_preset_id) VALUES (?, ?, ?, ?)`,
		l.MediaType, l.Name, l.Path, l.QualityPresetID)
	if err != nil {
		return nil, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return s.GetLibrary(ctx, id)
}

// UpdateLibrary changes a library's name, path and preset.
func (s *Store) UpdateLibrary(ctx context.Context, l Library) (*Library, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE libraries SET media_type = ?, name = ?, path = ?, quality_preset_id = ? WHERE id = ?`,
		l.MediaType, l.Name, l.Path, l.QualityPresetID, l.ID)
	if err != nil {
		return nil, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetLibrary(ctx, l.ID)
}

// DeleteLibrary removes a library. Files on disk are not touched.
func (s *Store) DeleteLibrary(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM libraries WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Indexers and download clients ----

// IntegrationTable picks which integration table a query targets.
type IntegrationTable string

// The two integration tables. Only these constants are ever interpolated into SQL.
const (
	Indexers        IntegrationTable = "indexers"
	DownloadClients IntegrationTable = "download_clients"
)

// Integration is a configured indexer or download client.
type Integration struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	BaseURL   string `json:"baseUrl"`
	APIKey    string `json:"apiKey"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Category  string `json:"category"`
	Enabled   bool   `json:"enabled"`
	Priority  int    `json:"priority"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

const integrationCols = `id, name, kind, base_url, api_key, username, password, category, enabled, priority, created_at, updated_at`

func scanIntegration(row interface{ Scan(...any) error }) (*Integration, error) {
	var i Integration
	if err := row.Scan(&i.ID, &i.Name, &i.Kind, &i.BaseURL, &i.APIKey, &i.Username, &i.Password, &i.Category,
		&i.Enabled, &i.Priority, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &i, nil
}

// ListIntegrations returns every row of table t.
func (s *Store) ListIntegrations(ctx context.Context, t IntegrationTable) ([]Integration, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+integrationCols+` FROM `+string(t)+` ORDER BY priority, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Integration{}
	for rows.Next() {
		i, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// GetIntegration loads one row of table t.
func (s *Store) GetIntegration(ctx context.Context, t IntegrationTable, id int64) (*Integration, error) {
	return scanIntegration(s.DB.QueryRowContext(ctx, `SELECT `+integrationCols+` FROM `+string(t)+` WHERE id = ?`, id))
}

// CreateIntegration inserts a row into table t.
func (s *Store) CreateIntegration(ctx context.Context, t IntegrationTable, i Integration) (*Integration, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO `+string(t)+`
		(name, kind, base_url, api_key, username, password, category, enabled, priority)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.Name, i.Kind, i.BaseURL, i.APIKey, i.Username, i.Password, i.Category, i.Enabled, i.Priority)
	if err != nil {
		return nil, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return s.GetIntegration(ctx, t, id)
}

// UpdateIntegration replaces a row of table t.
func (s *Store) UpdateIntegration(ctx context.Context, t IntegrationTable, i Integration) (*Integration, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE `+string(t)+` SET
		name = ?, kind = ?, base_url = ?, api_key = ?, username = ?, password = ?, category = ?,
		enabled = ?, priority = ?, updated_at = ? WHERE id = ?`,
		i.Name, i.Kind, i.BaseURL, i.APIKey, i.Username, i.Password, i.Category, i.Enabled, i.Priority, now(), i.ID)
	if err != nil {
		return nil, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetIntegration(ctx, t, i.ID)
}

// DeleteIntegration removes a row of table t.
func (s *Store) DeleteIntegration(ctx context.Context, t IntegrationTable, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM `+string(t)+` WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
