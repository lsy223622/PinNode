package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func runMaintenance(args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("需要维护命令：admin-reset-password 或 backup")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(output)
	var username, destination string
	var passwordStdin bool
	switch args[0] {
	case "admin-reset-password":
		flags.StringVar(&username, "username", "", "已有管理员账号")
		flags.BoolVar(&passwordStdin, "password-stdin", false, "从标准输入读取新密码")
	case "backup":
		flags.StringVar(&destination, "output", "", "新 SQLite 快照的路径")
	default:
		return errors.New("未知维护命令；可用命令：admin-reset-password、backup")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("维护命令不接受位置参数")
	}
	if args[0] == "admin-reset-password" && (!passwordStdin || username == "") {
		return errors.New("需要 --username 和 --password-stdin；密码不能作为命令行参数")
	}
	if args[0] == "backup" && destination == "" {
		return errors.New("需要 --output 指定新快照路径")
	}
	databasePath := getenv("PINNODE_DATABASE_PATH", "data/pinnode.db")
	info, err := os.Stat(databasePath)
	if err != nil {
		return fmt.Errorf("读取已有数据库: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("维护命令需要普通 SQLite 数据库文件路径")
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return err
	}
	// Maintenance must not migrate the source before an upgrade backup.
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	defer store.Close()
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		return err
	}
	switch args[0] {
	case "admin-reset-password":
		username, err = normalizeAdminUsername(username)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(input, 1027))
		if err != nil {
			return errors.New("读取新密码失败")
		}
		password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		if err := validateAdminPassword(password); err != nil {
			return err
		}
		passwordHash, err := hashPassword(password)
		if err != nil {
			return err
		}
		if err := store.ResetAdminPassword(username, passwordHash, time.Now()); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "管理员密码已更新，已有管理员登录会话已撤销。")
		return err
	case "backup":
		if err := store.Backup(destination); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "SQLite 一致性快照已创建；请另外备份实例根密钥。")
		return err
	}
	return nil
}

func (s *Store) ResetAdminPassword(username, passwordHash string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE admin_users SET password_hash = ?, failed_attempts = 0,
		locked_until = NULL, updated_at = ? WHERE username = ? COLLATE NOCASE`, passwordHash, toMillis(now), username)
	if err != nil {
		return fmt.Errorf("更新管理员密码: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("管理员账号不存在")
	}
	if _, err := tx.Exec(`DELETE FROM admin_sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Backup(destination string) error {
	path, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	// Reserve the output with owner-only permissions; VACUUM INTO accepts an
	// empty file but must never replace an existing backup, database, or key.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建快照文件: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("创建 SQLite 一致性快照: %w", err)
	}
	return nil
}
