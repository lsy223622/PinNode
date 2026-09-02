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
	Hash         string
	CredentialID string
	CodeCipher   []byte
	CreatedAt    time.Time
	ExpiresAt    time.Time
	UsedAt       time.Time
	Config       SessionConfig
}

type AdminUser struct {
	ID             int64
	Username       string
	PasswordHash   string
	FailedAttempts int
	LockedUntil    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type AdminSession struct {
	TokenHash string
	AdminID   int64
	Username  string
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type TailscaleCredential struct {
	ID         string
	Name       string
	Kind       TailscaleCredentialKind
	Ciphertext []byte
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastUsedAt time.Time
}

type TailscaleCredentialKind string

const (
	TailscaleCredentialAPIToken    TailscaleCredentialKind = "api_token"
	TailscaleCredentialOAuthClient TailscaleCredentialKind = "oauth_client"
)

type SessionStatus string

const (
	SessionProvisioning  SessionStatus = "provisioning"
	SessionActive        SessionStatus = "active"
	SessionCleaning      SessionStatus = "cleaning"
	SessionStopped       SessionStatus = "stopped"
	SessionCleanupFailed SessionStatus = "cleanup_failed"
)

type Session struct {
	ID                    string
	TokenHash             string
	PairingCodeHash       string
	CredentialID          string
	AuthKeyID             string
	ProvisioningName      string
	Route                 string
	Routes                []string
	WiFiRoutes            []string
	Config                SessionConfig
	ConfigRevision        int64
	AppliedConfigRevision int64
	DeviceID              string
	ConnectedAt           time.Time
	CreatedAt             time.Time
	ProvisioningDeadline  time.Time
	ExpiresAt             time.Time
	LastSeenAt            time.Time
	SyncDeadline          time.Time
	Status                SessionStatus
	CleanupErr            string
	CleanupAfter          time.Time
	CleanupGeneration     int64
	CleanupLeaseUntil     time.Time
	StoppedAt             time.Time
	StopReason            string
	ClientStateJSON       string
	UpdatedAt             time.Time
}

type SessionStartReplay struct {
	RequestHash string
	SessionID   string
	Ciphertext  []byte
	ExpiresAt   time.Time
}

type Store struct {
	db *sql.DB
}

const defaultCleanupLeaseTTL = 5 * time.Minute

var ErrStaleCleanupClaim = errors.New("清理 claim 已失效")

type cleanupSummaryError struct {
	value string
}

func (e cleanupSummaryError) Error() string { return e.value }

func newCleanupSummaryError(value string) error {
	return cleanupSummaryError{value: value}
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
			credential_id TEXT,
			code_cipher BLOB,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			used_at INTEGER,
			config_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL,
			pairing_code_hash TEXT NOT NULL DEFAULT '',
			credential_id TEXT,
			auth_key_id TEXT NOT NULL,
			provisioning_name TEXT NOT NULL UNIQUE,
			route TEXT NOT NULL,
			routes_json TEXT NOT NULL,
			wifi_routes_json TEXT NOT NULL,
			config_json TEXT NOT NULL,
			config_revision INTEGER NOT NULL DEFAULT 1,
			applied_config_revision INTEGER NOT NULL DEFAULT 0,
			device_id TEXT UNIQUE,
			connected_at INTEGER,
			created_at INTEGER NOT NULL,
			provisioning_deadline INTEGER,
			expires_at INTEGER,
			last_seen_at INTEGER,
			sync_deadline INTEGER,
			status TEXT NOT NULL,
			cleanup_error TEXT NOT NULL DEFAULT '',
			cleanup_after INTEGER,
			cleanup_generation INTEGER NOT NULL DEFAULT 0,
			cleanup_lease_until INTEGER,
			stopped_at INTEGER,
			stop_reason TEXT NOT NULL DEFAULT '',
			client_state_json TEXT NOT NULL DEFAULT '{}',
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS sessions_active_created_idx
			ON sessions(created_at DESC) WHERE status <> 'stopped'`,
		`CREATE TABLE IF NOT EXISTS session_start_replays (
			idempotency_key_hash TEXT PRIMARY KEY,
			request_hash TEXT NOT NULL,
			session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id) ON DELETE CASCADE,
			response_cipher BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS session_start_replays_expiry_idx
			ON session_start_replays(expires_at)`,
		`CREATE TABLE IF NOT EXISTS admin_users (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			username TEXT NOT NULL COLLATE NOCASE UNIQUE,
			password_hash TEXT NOT NULL,
			failed_attempts INTEGER NOT NULL DEFAULT 0,
			locked_until INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS admin_sessions (
			token_hash TEXT PRIMARY KEY,
			admin_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
			csrf_token TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS admin_sessions_expiry_idx ON admin_sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS tailscale_credentials (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL COLLATE NOCASE UNIQUE,
			kind TEXT NOT NULL DEFAULT 'api_token',
			token_cipher BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			last_used_at INTEGER
		)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, unixepoch() * 1000)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (2, unixepoch() * 1000)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("迁移 SQLite 数据库: %w", err)
		}
	}
	if err := s.ensureColumn("pairing_codes", "credential_id", "credential_id TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("pairing_codes", "code_cipher", "code_cipher BLOB"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "credential_id", "credential_id TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "pairing_code_hash", "pairing_code_hash TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("tailscale_credentials", "kind", "kind TEXT NOT NULL DEFAULT 'api_token'"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "config_revision", "config_revision INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "applied_config_revision", "applied_config_revision INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "sync_deadline", "sync_deadline INTEGER"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "connected_at", "connected_at INTEGER"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "client_state_json", "client_state_json TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "cleanup_generation", "cleanup_generation INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "cleanup_lease_until", "cleanup_lease_until INTEGER"); err != nil {
		return err
	}
	hasHeartbeatDeadline, err := s.hasColumn("sessions", "heartbeat_deadline")
	if err != nil {
		return err
	}
	if hasHeartbeatDeadline {
		if _, err := s.db.Exec(`UPDATE sessions SET sync_deadline = heartbeat_deadline
			WHERE sync_deadline IS NULL AND heartbeat_deadline IS NOT NULL`); err != nil {
			return fmt.Errorf("迁移会话同步租约: %w", err)
		}
	}
	if _, err := s.db.Exec(`UPDATE sessions SET sync_deadline = NULL
		WHERE sync_deadline IS NOT NULL
		AND COALESCE(json_extract(config_json, '$.exitPolicy.onAppClose'), 0) != 1`); err != nil {
		return fmt.Errorf("规范会话同步租约: %w", err)
	}
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS sessions_reap_idx`); err != nil {
		return fmt.Errorf("迁移会话清理索引: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS sessions_reap_idx
		ON sessions(status, provisioning_deadline, expires_at, sync_deadline, cleanup_after, cleanup_lease_until)`); err != nil {
		return fmt.Errorf("创建会话清理索引: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (3, unixepoch() * 1000)`); err != nil {
		return fmt.Errorf("记录 SQLite 迁移: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (4, unixepoch() * 1000)`); err != nil {
		return fmt.Errorf("记录 SQLite 迁移: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (5, unixepoch() * 1000)`); err != nil {
		return fmt.Errorf("记录 SQLite 迁移: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (6, unixepoch() * 1000)`); err != nil {
		return fmt.Errorf("记录 SQLite 迁移: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (7, unixepoch() * 1000)`); err != nil {
		return fmt.Errorf("记录 SQLite 迁移: %w", err)
	}
	return nil
}

func (s *Store) ensureColumn(table, column, definition string) error {
	found, err := s.hasColumn(table, column)
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + definition); err != nil {
		return fmt.Errorf("添加 SQLite 列 %s.%s: %w", table, column, err)
	}
	return nil
}

func (s *Store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, fmt.Errorf("检查 SQLite 列 %s.%s: %w", table, column, err)
	}
	found := false
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return false, err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	return found, nil
}

func (s *Store) AdminExists() (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&count); err != nil {
		return false, fmt.Errorf("检查管理员账号: %w", err)
	}
	return count != 0, nil
}

func (s *Store) CreateAdmin(username, passwordHash string, now time.Time) (bool, error) {
	result, err := s.db.Exec(
		`INSERT INTO admin_users(id, username, password_hash, created_at, updated_at)
		 SELECT 1, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM admin_users)`,
		username, passwordHash, toMillis(now), toMillis(now),
	)
	if err != nil {
		return false, fmt.Errorf("创建管理员账号: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) GetAdminByUsername(username string) (AdminUser, bool, error) {
	var admin AdminUser
	var lockedUntil sql.NullInt64
	var createdAt, updatedAt int64
	err := s.db.QueryRow(
		`SELECT id, username, password_hash, failed_attempts, locked_until, created_at, updated_at
		 FROM admin_users WHERE username = ? COLLATE NOCASE`, username,
	).Scan(
		&admin.ID, &admin.Username, &admin.PasswordHash, &admin.FailedAttempts,
		&lockedUntil, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, false, nil
	}
	if err != nil {
		return AdminUser{}, false, fmt.Errorf("读取管理员账号: %w", err)
	}
	admin.LockedUntil = fromNullableMillis(lockedUntil)
	admin.CreatedAt = fromMillis(createdAt)
	admin.UpdatedAt = fromMillis(updatedAt)
	return admin, true, nil
}

func (s *Store) RecordAdminLoginFailure(adminID int64, now time.Time) (time.Time, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	var failures int
	if err := tx.QueryRow(`SELECT failed_attempts FROM admin_users WHERE id = ?`, adminID).Scan(&failures); err != nil {
		return time.Time{}, err
	}
	failures++
	var lockedUntil time.Time
	if failures > 3 {
		exponent := failures - 4
		if exponent > 9 {
			exponent = 9
		}
		lockedUntil = now.Add(time.Duration(1<<exponent) * time.Second)
	}
	if _, err := tx.Exec(
		`UPDATE admin_users SET failed_attempts = ?, locked_until = ?, updated_at = ? WHERE id = ?`,
		failures, nullableMillis(lockedUntil), toMillis(now), adminID,
	); err != nil {
		return time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return lockedUntil, nil
}

func (s *Store) ResetAdminLoginFailures(adminID int64, now time.Time) error {
	_, err := s.db.Exec(
		`UPDATE admin_users SET failed_attempts = 0, locked_until = NULL, updated_at = ? WHERE id = ?`,
		toMillis(now), adminID,
	)
	return err
}

func (s *Store) PutAdminSession(session AdminSession) error {
	_, err := s.db.Exec(
		`INSERT INTO admin_sessions(token_hash, admin_id, csrf_token, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		session.TokenHash, session.AdminID, session.CSRFToken,
		toMillis(session.CreatedAt), toMillis(session.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("保存管理员会话: %w", err)
	}
	return nil
}

func (s *Store) GetAdminSession(tokenHash string, now time.Time) (AdminSession, bool, error) {
	_, _ = s.db.Exec(`DELETE FROM admin_sessions WHERE expires_at <= ?`, toMillis(now))
	var session AdminSession
	var createdAt, expiresAt int64
	err := s.db.QueryRow(
		`SELECT s.token_hash, s.admin_id, u.username, s.csrf_token, s.created_at, s.expires_at
		 FROM admin_sessions s JOIN admin_users u ON u.id = s.admin_id
		 WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, toMillis(now),
	).Scan(
		&session.TokenHash, &session.AdminID, &session.Username, &session.CSRFToken,
		&createdAt, &expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminSession{}, false, nil
	}
	if err != nil {
		return AdminSession{}, false, fmt.Errorf("读取管理员会话: %w", err)
	}
	session.CreatedAt = fromMillis(createdAt)
	session.ExpiresAt = fromMillis(expiresAt)
	return session, true, nil
}

func (s *Store) DeleteAdminSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM admin_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) PutTailscaleCredential(credential TailscaleCredential) error {
	if credential.Kind == "" {
		credential.Kind = TailscaleCredentialAPIToken
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开始保存 Tailscale 凭据: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO tailscale_credentials(id, name, kind, token_cipher, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		credential.ID, credential.Name, credential.Kind, credential.Ciphertext,
		toMillis(credential.CreatedAt), toMillis(credential.UpdatedAt),
	); err != nil {
		return fmt.Errorf("保存 Tailscale 凭据: %w", err)
	}
	if _, err := tx.Exec(`UPDATE pairing_codes SET credential_id = ? WHERE credential_id IS NULL`, credential.ID); err != nil {
		return fmt.Errorf("迁移配对代码凭据: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET credential_id = ? WHERE credential_id IS NULL`, credential.ID); err != nil {
		return fmt.Errorf("迁移会话凭据: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 Tailscale 凭据: %w", err)
	}
	return nil
}

func (s *Store) ListTailscaleCredentials() ([]TailscaleCredential, error) {
	rows, err := s.db.Query(
		`SELECT id, name, kind, token_cipher, created_at, updated_at, last_used_at
		 FROM tailscale_credentials ORDER BY name COLLATE NOCASE`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var credentials []TailscaleCredential
	for rows.Next() {
		credential, err := scanTailscaleCredential(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	return credentials, rows.Err()
}

func (s *Store) GetTailscaleCredential(id string) (TailscaleCredential, bool, error) {
	credential, err := scanTailscaleCredential(s.db.QueryRow(
		`SELECT id, name, kind, token_cipher, created_at, updated_at, last_used_at
		 FROM tailscale_credentials WHERE id = ?`, id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return TailscaleCredential{}, false, nil
	}
	if err != nil {
		return TailscaleCredential{}, false, err
	}
	return credential, true, nil
}

func (s *Store) TouchTailscaleCredential(id string, now time.Time) error {
	_, err := s.db.Exec(`UPDATE tailscale_credentials SET last_used_at = ? WHERE id = ?`, toMillis(now), id)
	return err
}

func scanTailscaleCredential(scanner rowScanner) (TailscaleCredential, error) {
	var credential TailscaleCredential
	var createdAt, updatedAt int64
	var lastUsedAt sql.NullInt64
	if err := scanner.Scan(
		&credential.ID, &credential.Name, &credential.Kind, &credential.Ciphertext,
		&createdAt, &updatedAt, &lastUsedAt,
	); err != nil {
		return TailscaleCredential{}, err
	}
	credential.CreatedAt = fromMillis(createdAt)
	credential.UpdatedAt = fromMillis(updatedAt)
	credential.LastUsedAt = fromNullableMillis(lastUsedAt)
	return credential, nil
}

func (s *Store) PutCode(hash string, expiresAt time.Time) error {
	return s.PutCodeWithConfig(hash, expiresAt, DefaultSessionConfig())
}

func (s *Store) PutCodeWithConfig(hash string, expiresAt time.Time, config SessionConfig) error {
	return s.PutCodeWithConfigAt(hash, time.Now(), expiresAt, config)
}

func (s *Store) PutCodeWithConfigAt(hash string, createdAt, expiresAt time.Time, config SessionConfig) error {
	return s.PutCodeWithCredentialAt(hash, "", createdAt, expiresAt, config)
}

func (s *Store) PutCodeWithCredentialAt(hash, credentialID string, createdAt, expiresAt time.Time, config SessionConfig) error {
	return s.PutCodeWithCredentialAtAndCipher(hash, credentialID, createdAt, expiresAt, config, nil)
}

func (s *Store) PutCodeWithCredentialAtAndCipher(
	hash, credentialID string,
	createdAt, expiresAt time.Time,
	config SessionConfig,
	codeCipher []byte,
) error {
	encoded, err := json.Marshal(cloneSessionConfig(config))
	if err != nil {
		return fmt.Errorf("编码配对配置: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO pairing_codes(hash, credential_id, code_cipher, created_at, expires_at, config_json)
		 VALUES (?, NULLIF(?, ''), ?, ?, ?, ?)`,
		hash, credentialID, codeCipher, toMillis(createdAt), toMillis(expiresAt), string(encoded),
	)
	if err != nil {
		return fmt.Errorf("保存配对代码: %w", err)
	}
	return nil
}

func (s *Store) GetPairingCode(hash string) (PairingCode, bool, error) {
	code, err := scanPairingCode(s.db.QueryRow(
		`SELECT hash, COALESCE(credential_id, ''), code_cipher, created_at, expires_at, used_at, config_json
		 FROM pairing_codes WHERE hash = ?`, hash,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PairingCode{}, false, nil
	}
	if err != nil {
		return PairingCode{}, false, err
	}
	return code, true, nil
}

func (s *Store) ListPendingPairingCodes(now time.Time) ([]PairingCode, error) {
	rows, err := s.db.Query(
		`SELECT hash, COALESCE(credential_id, ''), code_cipher, created_at, expires_at, used_at, config_json
		 FROM pairing_codes WHERE used_at IS NULL AND expires_at > ? ORDER BY created_at ASC`,
		toMillis(now),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var codes []PairingCode
	for rows.Next() {
		code, err := scanPairingCode(rows)
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

func scanPairingCode(scanner rowScanner) (PairingCode, error) {
	var code PairingCode
	var createdAt, expiresAt int64
	var usedAt sql.NullInt64
	var config string
	if err := scanner.Scan(
		&code.Hash, &code.CredentialID, &code.CodeCipher, &createdAt, &expiresAt, &usedAt, &config,
	); err != nil {
		return PairingCode{}, err
	}
	if err := json.Unmarshal([]byte(config), &code.Config); err != nil {
		return PairingCode{}, fmt.Errorf("解析配对配置: %w", err)
	}
	code.Config = cloneSessionConfig(code.Config)
	code.CreatedAt = fromMillis(createdAt)
	code.ExpiresAt = fromMillis(expiresAt)
	code.UsedAt = fromNullableMillis(usedAt)
	return code, nil
}

func (s *Store) ConsumeCode(hash string, now time.Time) (bool, error) {
	_, _, _, ok, err := s.RedeemCodeWithCredential(hash, now)
	return ok, err
}

func (s *Store) GetRedeemableCodeWithCredential(hash string, now time.Time) (SessionConfig, time.Time, string, bool, error) {
	var createdAt int64
	var encoded string
	var credentialID sql.NullString
	err := s.db.QueryRow(
		`SELECT created_at, config_json, credential_id FROM pairing_codes
		 WHERE hash = ? AND used_at IS NULL AND expires_at > ?`,
		hash, toMillis(now),
	).Scan(&createdAt, &encoded, &credentialID)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionConfig{}, time.Time{}, "", false, nil
	}
	if err != nil {
		return SessionConfig{}, time.Time{}, "", false, fmt.Errorf("读取配对配置: %w", err)
	}
	var config SessionConfig
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		return SessionConfig{}, time.Time{}, "", false, fmt.Errorf("解析配对配置: %w", err)
	}
	return cloneSessionConfig(config), fromMillis(createdAt), credentialID.String, true, nil
}

func (s *Store) RedeemCode(hash string, now time.Time) (SessionConfig, bool, error) {
	config, _, _, ok, err := s.RedeemCodeWithCredential(hash, now)
	return config, ok, err
}

func (s *Store) RedeemCodeDetails(hash string, now time.Time) (SessionConfig, time.Time, bool, error) {
	config, createdAt, _, ok, err := s.RedeemCodeWithCredential(hash, now)
	return config, createdAt, ok, err
}

func (s *Store) RedeemCodeWithCredential(hash string, now time.Time) (SessionConfig, time.Time, string, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return SessionConfig{}, time.Time{}, "", false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(
		`UPDATE pairing_codes SET used_at = ?
		 WHERE hash = ? AND used_at IS NULL AND expires_at > ?`,
		toMillis(now), hash, toMillis(now),
	)
	if err != nil {
		return SessionConfig{}, time.Time{}, "", false, fmt.Errorf("消费配对代码: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return SessionConfig{}, time.Time{}, "", false, err
	}
	var createdAt int64
	var encoded string
	var credentialID sql.NullString
	if err := tx.QueryRow(
		`SELECT created_at, config_json, credential_id FROM pairing_codes WHERE hash = ?`, hash,
	).Scan(&createdAt, &encoded, &credentialID); err != nil {
		return SessionConfig{}, time.Time{}, "", false, fmt.Errorf("读取配对配置: %w", err)
	}
	var config SessionConfig
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		return SessionConfig{}, time.Time{}, "", false, fmt.Errorf("解析配对配置: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SessionConfig{}, time.Time{}, "", false, err
	}
	return cloneSessionConfig(config), fromMillis(createdAt), credentialID.String, true, nil
}

func (s *Store) PutSession(session Session) error {
	return insertSession(s.db, session)
}

func (s *Store) CreateSessionFromCode(
	codeHash string,
	now time.Time,
	session Session,
	idempotencyKeyHash string,
	requestHash string,
	responseCipher []byte,
	replayExpiresAt time.Time,
) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(
		`UPDATE pairing_codes SET used_at = ?
		 WHERE hash = ? AND used_at IS NULL AND expires_at > ?`,
		toMillis(now), codeHash, toMillis(now),
	)
	if err != nil {
		return false, fmt.Errorf("消费配对代码: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return false, err
	}
	if err := insertSession(tx, session); err != nil {
		return false, err
	}
	if _, err := tx.Exec(
		`INSERT INTO session_start_replays(
			idempotency_key_hash, request_hash, session_id, response_cipher, created_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
		idempotencyKeyHash, requestHash, session.ID, responseCipher,
		toMillis(now), toMillis(replayExpiresAt),
	); err != nil {
		return false, fmt.Errorf("保存会话创建重放记录: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) GetSessionStartReplay(idempotencyKeyHash string) (SessionStartReplay, bool, error) {
	var replay SessionStartReplay
	var expiresAt int64
	err := s.db.QueryRow(
		`SELECT request_hash, session_id, response_cipher, expires_at
		 FROM session_start_replays WHERE idempotency_key_hash = ?`,
		idempotencyKeyHash,
	).Scan(&replay.RequestHash, &replay.SessionID, &replay.Ciphertext, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionStartReplay{}, false, nil
	}
	if err != nil {
		return SessionStartReplay{}, false, fmt.Errorf("读取会话创建重放记录: %w", err)
	}
	replay.ExpiresAt = fromMillis(expiresAt)
	return replay, true, nil
}

func (s *Store) DeleteSessionStartReplay(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM session_start_replays WHERE session_id = ?`, sessionID)
	return err
}

func (s *Store) DeleteExpiredSessionStartReplays(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM session_start_replays WHERE expires_at <= ?`, toMillis(now))
	return err
}

type sqlExecer interface {
	Exec(string, ...any) (sql.Result, error)
}

func insertSession(execer sqlExecer, session Session) error {
	routes, wifiRoutes, config, err := encodeSessionJSON(session)
	if err != nil {
		return err
	}
	clientState := strings.TrimSpace(session.ClientStateJSON)
	if clientState == "" {
		clientState = "{}"
	}
	if !json.Valid([]byte(clientState)) {
		return errors.New("客户端状态不是有效 JSON")
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = session.CreatedAt
	}
	if session.ConfigRevision <= 0 {
		session.ConfigRevision = 1
	}
	_, err = execer.Exec(
		`INSERT INTO sessions(
			id, token_hash, pairing_code_hash, credential_id, auth_key_id, provisioning_name, route, routes_json,
			wifi_routes_json, config_json, config_revision, applied_config_revision,
			device_id, connected_at, created_at, provisioning_deadline, expires_at, last_seen_at, sync_deadline,
			status, cleanup_error, cleanup_after, cleanup_generation, cleanup_lease_until,
			stopped_at, stop_reason, client_state_json, updated_at
		) VALUES (
			?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?,
			?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?
		)`,
		session.ID, session.TokenHash, session.PairingCodeHash, session.CredentialID, session.AuthKeyID,
		session.ProvisioningName, session.Route, routes, wifiRoutes, config, session.ConfigRevision,
		session.AppliedConfigRevision, session.DeviceID, nullableMillis(session.ConnectedAt),
		toMillis(session.CreatedAt), nullableMillis(session.ProvisioningDeadline),
		nullableMillis(session.ExpiresAt), nullableMillis(session.LastSeenAt),
		nullableMillis(session.SyncDeadline), session.Status, session.CleanupErr,
		nullableMillis(session.CleanupAfter), session.CleanupGeneration,
		nullableMillis(session.CleanupLeaseUntil), nullableMillis(session.StoppedAt),
		session.StopReason, clientState, toMillis(session.UpdatedAt),
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

func (s *Store) AttachDevice(id, deviceID string, now, syncDeadline time.Time) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE sessions
		 SET device_id = ?, status = ?, connected_at = ?, last_seen_at = ?, sync_deadline = ?,
		     cleanup_error = '', cleanup_after = NULL, updated_at = ?
		 WHERE id = ? AND status = ? AND device_id IS NULL`,
		deviceID, SessionActive, toMillis(now), toMillis(now), nullableMillis(syncDeadline), toMillis(now),
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
		_, err := s.TouchSession(id, now, syncDeadline)
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
		 SET device_id = NULL, status = ?, last_seen_at = NULL, sync_deadline = NULL,
		     updated_at = ?
		 WHERE id = ? AND device_id = ? AND status = ?`,
		SessionProvisioning, toMillis(now), id, deviceID, SessionActive,
	)
	return err
}

func (s *Store) TouchSession(id string, now, deadline time.Time) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE sessions SET last_seen_at = ?, sync_deadline = ?, updated_at = ?
		 WHERE id = ? AND status = ?`,
		toMillis(now), nullableMillis(deadline), toMillis(now), id, SessionActive,
	)
	if err != nil {
		return false, fmt.Errorf("记录会话同步: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) SyncSession(id string, now, deadline time.Time, appliedRevision int64) (bool, error) {
	return s.SyncSessionWithState(id, now, deadline, appliedRevision, "")
}

func (s *Store) SyncSessionWithState(
	id string,
	now, deadline time.Time,
	appliedRevision int64,
	clientStateJSON string,
) (bool, error) {
	if appliedRevision < 0 {
		return false, errors.New("客户端配置 revision 不能为负数")
	}
	clientStateJSON = strings.TrimSpace(clientStateJSON)
	if clientStateJSON == "" {
		clientStateJSON = "{}"
	}
	if !json.Valid([]byte(clientStateJSON)) {
		return false, errors.New("客户端状态不是有效 JSON")
	}
	result, err := s.db.Exec(
		`UPDATE sessions
		 SET last_seen_at = ?, sync_deadline = ?,
		     applied_config_revision = CASE
		         WHEN applied_config_revision < ? THEN ?
		         ELSE applied_config_revision
		     END,
		     client_state_json = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND config_revision >= ?`,
		toMillis(now), nullableMillis(deadline), appliedRevision, appliedRevision,
		clientStateJSON, toMillis(now),
		id, SessionActive, appliedRevision,
	)
	if err != nil {
		return false, fmt.Errorf("同步会话状态: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) BeginCleanup(id string, now time.Time, force bool, reason string) (Session, bool, error) {
	return s.BeginCleanupWithLease(id, now, force, reason, defaultCleanupLeaseTTL)
}

func (s *Store) BeginCleanupWithLease(
	id string,
	now time.Time,
	force bool,
	reason string,
	leaseTTL time.Duration,
) (Session, bool, error) {
	if leaseTTL <= 0 {
		leaseTTL = defaultCleanupLeaseTTL
	}
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
	if session.Status == SessionStopped {
		return session, false, nil
	}
	if session.Status == SessionCleaning {
		if !session.CleanupLeaseUntil.IsZero() && now.Before(session.CleanupLeaseUntil) {
			return session, false, nil
		}
	} else if !force && !session.reapable(now) {
		return Session{}, false, nil
	}
	if reason == "" {
		reason = session.StopReason
		if reason == "" {
			reason = session.reapReason(now)
		}
	}
	nextGeneration := session.CleanupGeneration + 1
	if nextGeneration <= 0 {
		nextGeneration = 1
	}
	leaseUntil := now.Add(leaseTTL)
	result, err := tx.Exec(
		`UPDATE sessions SET status = ?, cleanup_error = '', cleanup_after = NULL,
			 cleanup_generation = ?, cleanup_lease_until = ?, stop_reason = ?, updated_at = ?
		 WHERE id = ? AND status = ?
		   AND (status <> ? OR cleanup_lease_until IS NULL OR cleanup_lease_until <= ?)`,
		SessionCleaning, nextGeneration, toMillis(leaseUntil), reason, toMillis(now),
		id, session.Status, SessionCleaning, toMillis(now),
	)
	if err != nil {
		return Session{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Session{}, false, err
	}
	if count != 1 {
		_ = tx.Rollback()
		current, found, readErr := s.GetSession(id)
		if readErr != nil || !found {
			return current, false, readErr
		}
		return current, false, nil
	}
	if err := tx.Commit(); err != nil {
		return Session{}, false, err
	}
	session.Status = SessionCleaning
	session.StopReason = reason
	session.CleanupErr = ""
	session.CleanupAfter = time.Time{}
	session.CleanupGeneration = nextGeneration
	session.CleanupLeaseUntil = leaseUntil
	session.UpdatedAt = now
	return session, true, nil
}

func (s *Store) FinishCleanupClaim(
	id string,
	generation int64,
	now time.Time,
	cleanupErr error,
) (bool, error) {
	if generation <= 0 {
		return false, ErrStaleCleanupClaim
	}
	if cleanupErr == nil {
		result, err := s.db.Exec(
			`UPDATE sessions SET status = ?, cleanup_error = '', cleanup_after = NULL,
				 cleanup_lease_until = NULL, stopped_at = ?, updated_at = ?
			 WHERE id = ? AND status = ? AND cleanup_generation = ?
			   AND cleanup_lease_until IS NOT NULL AND cleanup_lease_until > ?`,
			SessionStopped, toMillis(now), toMillis(now), id, SessionCleaning,
			generation, toMillis(now),
		)
		if err != nil {
			return false, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if count != 1 {
			return false, ErrStaleCleanupClaim
		}
		return true, nil
	}
	result, err := s.db.Exec(
		`UPDATE sessions SET status = ?, cleanup_error = ?, cleanup_after = ?,
		 cleanup_lease_until = NULL, updated_at = ?
		 WHERE id = ? AND status = ? AND cleanup_generation = ?
		   AND cleanup_lease_until IS NOT NULL AND cleanup_lease_until > ?`,
		SessionCleanupFailed, sanitizeCleanupError(cleanupErr), toMillis(now.Add(30*time.Second)),
		toMillis(now), id, SessionCleaning, generation, toMillis(now),
	)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count != 1 {
		return false, ErrStaleCleanupClaim
	}
	return true, nil
}

func (s *Store) ReapableSessions(now time.Time) ([]Session, error) {
	rows, err := s.db.Query(
		sessionSelect+` WHERE
		 (status = ? AND provisioning_deadline IS NOT NULL AND provisioning_deadline <= ?)
		 OR (status = ? AND (
		     (expires_at IS NOT NULL AND expires_at <= ?)
			     OR (sync_deadline IS NOT NULL AND sync_deadline <= ?
		         AND COALESCE(json_extract(config_json, '$.exitPolicy.onAppClose'), 0) = 1)
			  ))
			 OR (status = ? AND (cleanup_after IS NULL OR cleanup_after <= ?))
			 OR (status = ? AND (cleanup_lease_until IS NULL OR cleanup_lease_until <= ?))
		 ORDER BY updated_at ASC`,
		SessionProvisioning, toMillis(now), SessionActive, toMillis(now), toMillis(now),
		SessionCleanupFailed, toMillis(now), SessionCleaning, toMillis(now),
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

func (s *Store) ListActiveSessions() ([]Session, error) {
	rows, err := s.db.Query(sessionSelect+` WHERE status <> ? ORDER BY created_at DESC`, SessionStopped)
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
	id, token_hash, COALESCE(pairing_code_hash, ''), COALESCE(credential_id, ''), auth_key_id, provisioning_name, route, routes_json,
	wifi_routes_json, config_json, config_revision, applied_config_revision,
	COALESCE(device_id, ''), connected_at, created_at, provisioning_deadline, expires_at, last_seen_at, sync_deadline,
	status, cleanup_error, cleanup_after, cleanup_generation, cleanup_lease_until,
	stopped_at, stop_reason, client_state_json, updated_at
	FROM sessions`

type rowScanner interface {
	Scan(...any) error
}

func scanSession(scanner rowScanner) (Session, error) {
	var session Session
	var routes, wifiRoutes, config string
	var provisioningDeadline, expiresAt, lastSeenAt, syncDeadline sql.NullInt64
	var connectedAt, cleanupAfter, cleanupLeaseUntil, stoppedAt sql.NullInt64
	var clientState string
	var createdAt, updatedAt int64
	err := scanner.Scan(
		&session.ID, &session.TokenHash, &session.PairingCodeHash, &session.CredentialID, &session.AuthKeyID, &session.ProvisioningName,
		&session.Route, &routes, &wifiRoutes, &config, &session.ConfigRevision,
		&session.AppliedConfigRevision, &session.DeviceID, &connectedAt, &createdAt,
		&provisioningDeadline, &expiresAt, &lastSeenAt, &syncDeadline,
		&session.Status, &session.CleanupErr, &cleanupAfter, &session.CleanupGeneration,
		&cleanupLeaseUntil, &stoppedAt,
		&session.StopReason, &clientState, &updatedAt,
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
	session.Routes = append([]string{}, session.Routes...)
	session.WiFiRoutes = append([]string{}, session.WiFiRoutes...)
	session.Config = cloneSessionConfig(session.Config)
	session.ConnectedAt = fromNullableMillis(connectedAt)
	session.CreatedAt = fromMillis(createdAt)
	session.ProvisioningDeadline = fromNullableMillis(provisioningDeadline)
	session.ExpiresAt = fromNullableMillis(expiresAt)
	session.LastSeenAt = fromNullableMillis(lastSeenAt)
	session.SyncDeadline = fromNullableMillis(syncDeadline)
	session.CleanupAfter = fromNullableMillis(cleanupAfter)
	session.CleanupLeaseUntil = fromNullableMillis(cleanupLeaseUntil)
	session.StoppedAt = fromNullableMillis(stoppedAt)
	if strings.TrimSpace(clientState) == "" || !json.Valid([]byte(clientState)) {
		clientState = "{}"
	}
	session.ClientStateJSON = clientState
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
	config, err := json.Marshal(cloneSessionConfig(session.Config))
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
			(s.Config.ExitPolicy.OnAppClose && !s.SyncDeadline.IsZero() &&
				!now.Before(s.SyncDeadline))
	case SessionCleanupFailed:
		return s.CleanupAfter.IsZero() || !now.Before(s.CleanupAfter)
	case SessionCleaning:
		return s.CleanupLeaseUntil.IsZero() || !now.Before(s.CleanupLeaseUntil)
	default:
		return false
	}
}

func (s Session) reapReason(now time.Time) string {
	if s.Status == SessionProvisioning {
		return "provisioning_timeout"
	}
	if s.Status == SessionCleanupFailed {
		if s.StopReason != "" {
			return s.StopReason
		}
		return "cleanup_retry"
	}
	if s.Status == SessionCleaning {
		return "cleanup_recovery"
	}
	if !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt) {
		return "expired"
	}
	return "sync_timeout"
}

func sanitizeCleanupError(err error) string {
	var summary cleanupSummaryError
	if errors.As(err, &summary) {
		value := strings.TrimSpace(summary.value)
		if value != "" {
			if len(value) > 512 {
				return value[:512]
			}
			return value
		}
	}
	var apiErr *HTTPError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("Tailscale API HTTP %d", apiErr.StatusCode)
	}
	return "cleanup failed"
}

func cloneSessionConfig(config SessionConfig) SessionConfig {
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
