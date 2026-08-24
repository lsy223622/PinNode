package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type PairingCode struct {
	Hash      string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    time.Time
	Config    RescueConfig
}

type SessionStatus string

const (
	SessionProvisioning  SessionStatus = "provisioning"
	SessionActive        SessionStatus = "active"
	SessionCleaning      SessionStatus = "cleaning"
	SessionStopped       SessionStatus = "stopped"
	SessionCleanupFailed SessionStatus = "cleanup_failed"
)

type Session struct {
	ID                   string
	TokenHash            string
	AuthKeyID            string
	ProvisioningName     string
	Route                string
	Routes               []string
	WiFiRoutes           []string
	Config               RescueConfig
	DeviceID             string
	CreatedAt            time.Time
	ProvisioningDeadline time.Time
	ExpiresAt            time.Time
	LastSeenAt           time.Time
	HeartbeatDeadline    time.Time
	Status               SessionStatus
	CleanupErr           string
	CleanupAfter         time.Time
	StoppedAt            time.Time
	StopReason           string
	UpdatedAt            time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore() *Store {
	store, err := OpenStore(":memory:")
	if err != nil {
		panic(err)
	}
	return store
}

func OpenStore(databasePath string) (*Store, error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, errors.New("数据库路径不能为空")
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS pairing_codes (
			hash TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			used_at INTEGER,
			config_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL,
			auth_key_id TEXT NOT NULL,
			provisioning_name TEXT NOT NULL UNIQUE,
			route TEXT NOT NULL,
			routes_json TEXT NOT NULL,
			wifi_routes_json TEXT NOT NULL,
			config_json TEXT NOT NULL,
			device_id TEXT UNIQUE,
			created_at INTEGER NOT NULL,
			provisioning_deadline INTEGER,
			expires_at INTEGER,
			last_seen_at INTEGER,
			heartbeat_deadline INTEGER,
			status TEXT NOT NULL,
			cleanup_error TEXT NOT NULL DEFAULT '',
			cleanup_after INTEGER,
			stopped_at INTEGER,
			stop_reason TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS sessions_reap_idx
			ON sessions(status, provisioning_deadline, expires_at, heartbeat_deadline, cleanup_after)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, unixepoch() * 1000)`,
		`UPDATE sessions SET heartbeat_deadline = NULL
			WHERE heartbeat_deadline IS NOT NULL
			AND COALESCE(json_extract(config_json, '$.exitPolicy.onAppClose'), 0) != 1`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (2, unixepoch() * 1000)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("迁移 SQLite 数据库: %w", err)
		}
	}
	return nil
}

func (s *Store) PutCode(hash string, expiresAt time.Time) error {
	return s.PutCodeWithConfig(hash, expiresAt, DefaultRescueConfig())
}

func (s *Store) PutCodeWithConfig(hash string, expiresAt time.Time, config RescueConfig) error {
	return s.PutCodeWithConfigAt(hash, time.Now(), expiresAt, config)
}

func (s *Store) PutCodeWithConfigAt(hash string, createdAt, expiresAt time.Time, config RescueConfig) error {
	encoded, err := json.Marshal(cloneRescueConfig(config))
	if err != nil {
		return fmt.Errorf("编码配对配置: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO pairing_codes(hash, created_at, expires_at, config_json) VALUES (?, ?, ?, ?)`,
		hash, toMillis(createdAt), toMillis(expiresAt), string(encoded),
	)
	if err != nil {
		return fmt.Errorf("保存配对代码: %w", err)
	}
	return nil
}

func (s *Store) ConsumeCode(hash string, now time.Time) (bool, error) {
	_, _, ok, err := s.RedeemCodeDetails(hash, now)
	return ok, err
}

func (s *Store) RedeemCode(hash string, now time.Time) (RescueConfig, bool, error) {
	config, _, ok, err := s.RedeemCodeDetails(hash, now)
	return config, ok, err
}

func (s *Store) RedeemCodeDetails(hash string, now time.Time) (RescueConfig, time.Time, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return RescueConfig{}, time.Time{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(
		`UPDATE pairing_codes SET used_at = ?
		 WHERE hash = ? AND used_at IS NULL AND expires_at > ?`,
		toMillis(now), hash, toMillis(now),
	)
	if err != nil {
		return RescueConfig{}, time.Time{}, false, fmt.Errorf("消费配对代码: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return RescueConfig{}, time.Time{}, false, err
	}
	var createdAt int64
	var encoded string
	if err := tx.QueryRow(
		`SELECT created_at, config_json FROM pairing_codes WHERE hash = ?`, hash,
	).Scan(&createdAt, &encoded); err != nil {
		return RescueConfig{}, time.Time{}, false, fmt.Errorf("读取配对配置: %w", err)
	}
	var config RescueConfig
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		return RescueConfig{}, time.Time{}, false, fmt.Errorf("解析配对配置: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RescueConfig{}, time.Time{}, false, err
	}
	return cloneRescueConfig(config), fromMillis(createdAt), true, nil
}

func (s *Store) PutSession(session Session) error {
	routes, wifiRoutes, config, err := encodeSessionJSON(session)
	if err != nil {
		return err
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = session.CreatedAt
	}
	_, err = s.db.Exec(
		`INSERT INTO sessions(
			id, token_hash, auth_key_id, provisioning_name, route, routes_json,
			wifi_routes_json, config_json, device_id, created_at,
			provisioning_deadline, expires_at, last_seen_at, heartbeat_deadline,
			status, cleanup_error, cleanup_after, stopped_at, stop_reason, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.TokenHash, session.AuthKeyID, session.ProvisioningName,
		session.Route, routes, wifiRoutes, config, session.DeviceID,
		toMillis(session.CreatedAt), nullableMillis(session.ProvisioningDeadline),
		nullableMillis(session.ExpiresAt), nullableMillis(session.LastSeenAt),
		nullableMillis(session.HeartbeatDeadline), session.Status, session.CleanupErr,
		nullableMillis(session.CleanupAfter), nullableMillis(session.StoppedAt),
		session.StopReason, toMillis(session.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("保存会话: %w", err)
	}
	return nil
}

func (s *Store) GetSession(id string) (Session, bool, error) {
	session, err := scanSession(s.db.QueryRow(sessionSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

func (s *Store) AttachDevice(id, deviceID string, now, heartbeatDeadline time.Time) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE sessions
		 SET device_id = ?, status = ?, last_seen_at = ?, heartbeat_deadline = ?,
		     cleanup_error = '', cleanup_after = NULL, updated_at = ?
		 WHERE id = ? AND status = ? AND device_id IS NULL`,
		deviceID, SessionActive, toMillis(now), nullableMillis(heartbeatDeadline), toMillis(now),
		id, SessionProvisioning,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return false, nil
		}
		return false, fmt.Errorf("绑定设备: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 1 {
		return true, nil
	}
	session, ok, err := s.GetSession(id)
	if err != nil || !ok {
		return false, err
	}
	if session.Status == SessionActive && session.DeviceID == deviceID {
		_, err := s.Heartbeat(id, now, heartbeatDeadline)
		return err == nil, err
	}
	return false, nil
}

func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

func (s *Store) DetachDevice(id, deviceID string, now time.Time) error {
	_, err := s.db.Exec(
		`UPDATE sessions
		 SET device_id = NULL, status = ?, last_seen_at = NULL, heartbeat_deadline = NULL,
		     updated_at = ?
		 WHERE id = ? AND device_id = ? AND status = ?`,
		SessionProvisioning, toMillis(now), id, deviceID, SessionActive,
	)
	return err
}

func (s *Store) Heartbeat(id string, now, deadline time.Time) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE sessions SET last_seen_at = ?, heartbeat_deadline = ?, updated_at = ?
		 WHERE id = ? AND status = ?`,
		toMillis(now), nullableMillis(deadline), toMillis(now), id, SessionActive,
	)
	if err != nil {
		return false, fmt.Errorf("记录心跳: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) BeginCleanup(id string, now time.Time, force bool, reason string) (Session, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, false, err
	}
	defer tx.Rollback()
	session, err := scanSession(tx.QueryRow(sessionSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	if session.Status == SessionStopped || session.Status == SessionCleaning {
		return Session{}, false, nil
	}
	if !force && !session.reapable(now) {
		return Session{}, false, nil
	}
	if reason == "" {
		reason = session.reapReason(now)
	}
	result, err := tx.Exec(
		`UPDATE sessions SET status = ?, cleanup_error = '', cleanup_after = NULL,
		 stop_reason = ?, updated_at = ? WHERE id = ? AND status = ?`,
		SessionCleaning, reason, toMillis(now), id, session.Status,
	)
	if err != nil {
		return Session{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return Session{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, false, err
	}
	session.Status = SessionCleaning
	session.StopReason = reason
	session.UpdatedAt = now
	return session, true, nil
}

func (s *Store) FinishCleanup(id string, now time.Time, cleanupErr error) error {
	if cleanupErr == nil {
		_, err := s.db.Exec(
			`UPDATE sessions SET status = ?, cleanup_error = '', cleanup_after = NULL,
			 stopped_at = ?, updated_at = ? WHERE id = ?`,
			SessionStopped, toMillis(now), toMillis(now), id,
		)
		return err
	}
	_, err := s.db.Exec(
		`UPDATE sessions SET status = ?, cleanup_error = ?, cleanup_after = ?,
		 updated_at = ? WHERE id = ?`,
		SessionCleanupFailed, cleanupErr.Error(), toMillis(now.Add(30*time.Second)),
		toMillis(now), id,
	)
	return err
}

func (s *Store) ReapableSessions(now time.Time) ([]Session, error) {
	rows, err := s.db.Query(
		sessionSelect+` WHERE
		 (status = ? AND provisioning_deadline IS NOT NULL AND provisioning_deadline <= ?)
		 OR (status = ? AND (
		     (expires_at IS NOT NULL AND expires_at <= ?)
		     OR (heartbeat_deadline IS NOT NULL AND heartbeat_deadline <= ?
		         AND COALESCE(json_extract(config_json, '$.exitPolicy.onAppClose'), 0) = 1)
		 ))
		 OR (status = ? AND (cleanup_after IS NULL OR cleanup_after <= ?))
		 ORDER BY updated_at ASC`,
		SessionProvisioning, toMillis(now), SessionActive, toMillis(now), toMillis(now),
		SessionCleanupFailed, toMillis(now),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []Session
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) ListSessions(limit int) ([]Session, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.Query(sessionSelect+` ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []Session
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

const sessionSelect = `SELECT
	id, token_hash, auth_key_id, provisioning_name, route, routes_json,
	wifi_routes_json, config_json, COALESCE(device_id, ''), created_at,
	provisioning_deadline, expires_at, last_seen_at, heartbeat_deadline,
	status, cleanup_error, cleanup_after, stopped_at, stop_reason, updated_at
	FROM sessions`

type rowScanner interface {
	Scan(...any) error
}

func scanSession(scanner rowScanner) (Session, error) {
	var session Session
	var routes, wifiRoutes, config string
	var provisioningDeadline, expiresAt, lastSeenAt, heartbeatDeadline sql.NullInt64
	var cleanupAfter, stoppedAt sql.NullInt64
	var createdAt, updatedAt int64
	err := scanner.Scan(
		&session.ID, &session.TokenHash, &session.AuthKeyID, &session.ProvisioningName,
		&session.Route, &routes, &wifiRoutes, &config, &session.DeviceID, &createdAt,
		&provisioningDeadline, &expiresAt, &lastSeenAt, &heartbeatDeadline,
		&session.Status, &session.CleanupErr, &cleanupAfter, &stoppedAt,
		&session.StopReason, &updatedAt,
	)
	if err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal([]byte(routes), &session.Routes); err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal([]byte(wifiRoutes), &session.WiFiRoutes); err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal([]byte(config), &session.Config); err != nil {
		return Session{}, err
	}
	session.CreatedAt = fromMillis(createdAt)
	session.ProvisioningDeadline = fromNullableMillis(provisioningDeadline)
	session.ExpiresAt = fromNullableMillis(expiresAt)
	session.LastSeenAt = fromNullableMillis(lastSeenAt)
	session.HeartbeatDeadline = fromNullableMillis(heartbeatDeadline)
	session.CleanupAfter = fromNullableMillis(cleanupAfter)
	session.StoppedAt = fromNullableMillis(stoppedAt)
	session.UpdatedAt = fromMillis(updatedAt)
	return session, nil
}

func encodeSessionJSON(session Session) (string, string, string, error) {
	routes, err := json.Marshal(append([]string{}, session.Routes...))
	if err != nil {
		return "", "", "", err
	}
	wifiRoutes, err := json.Marshal(append([]string{}, session.WiFiRoutes...))
	if err != nil {
		return "", "", "", err
	}
	config, err := json.Marshal(cloneRescueConfig(session.Config))
	if err != nil {
		return "", "", "", err
	}
	return string(routes), string(wifiRoutes), string(config), nil
}

func (s Session) reapable(now time.Time) bool {
	switch s.Status {
	case SessionProvisioning:
		return !s.ProvisioningDeadline.IsZero() && !now.Before(s.ProvisioningDeadline)
	case SessionActive:
		return (!s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt)) ||
			(s.Config.ExitPolicy.OnAppClose && !s.HeartbeatDeadline.IsZero() &&
				!now.Before(s.HeartbeatDeadline))
	case SessionCleanupFailed:
		return s.CleanupAfter.IsZero() || !now.Before(s.CleanupAfter)
	default:
		return false
	}
}

func (s Session) reapReason(now time.Time) string {
	if s.Status == SessionProvisioning {
		return "provisioning_timeout"
	}
	if s.Status == SessionCleanupFailed {
		return s.StopReason
	}
	if !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt) {
		return "expired"
	}
	return "heartbeat_timeout"
}

func cloneRescueConfig(config RescueConfig) RescueConfig {
	if config.AdvertiseRoutes == nil {
		config.AdvertiseRoutes = []string{}
	} else {
		config.AdvertiseRoutes = append([]string{}, config.AdvertiseRoutes...)
	}
	return config
}

func toMillis(value time.Time) int64 {
	return value.UTC().UnixMilli()
}

func nullableMillis(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return toMillis(value)
}

func fromMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}

func fromNullableMillis(value sql.NullInt64) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return fromMillis(value.Int64)
}
