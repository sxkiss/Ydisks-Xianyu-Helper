package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"crypto/rand"
	"encoding/base64"
)

// SessionTTL 是 24 小时的会话有效期。
const SessionTTL = 24 * time.Hour

// Sessions 会话表操作（HttpOnly Cookie 会话）。
type Sessions struct {
	DB      *sql.DB
	Dialect Dialect
}

// Create 为已认证用户创建会话，返回 session_id。
func (s *Sessions) Create(ctx context.Context, u *User) (string, error) {
	// sessionID、err 用于本次流程后续判断的会话ID、err
	sessionID, err := randomSessionID()
	if err != nil {
		return "", fmt.Errorf("生成 session id: %w", err)
	}
	// now 用于本次流程后续判断的now
	now := time.Now().Unix()
	// expires 用于本次流程后续判断的expires
	expires := now + int64(SessionTTL.Seconds())
	// isAdmin 用于本次流程后续判断的isAdmin
	isAdmin := 0
	if u.IsAdmin {
		isAdmin = 1
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO sessions (session_id, user_id, username, is_admin, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID, u.ID, u.Username, isAdmin, expires, now)
	if err != nil {
		return "", fmt.Errorf("写入 session: %w", err)
	}
	return sessionID, nil
}

// CreateVerified 在同一事务中锁定用户、确认认证代次并创建会话，避免改密与旧密码验证交错签发旧会话。
func (s *Sessions) CreateVerified(ctx context.Context, u *User, expectedVersion int64) (string, error) {
	if s == nil || s.DB == nil || u == nil {
		return "", errors.New("认证会话数据库适配器未初始化")
	}
	// tx 和 err 保存认证会话事务及其启动错误。
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if s.Dialect == DialectSQLite || s.Dialect == "" {
		// lockResult 通过无业务变化的更新先取得用户写锁，SQLite 不允许先读后升级写事务。
		lockResult, lockErr := tx.ExecContext(ctx, `UPDATE users SET auth_version=auth_version WHERE id=?`, u.ID)
		if lockErr != nil {
			return "", lockErr
		}
		// affected 和 affectedErr 保存用户行锁更新影响行数及读取错误。
		if affected, affectedErr := lockResult.RowsAffected(); affectedErr != nil || affected == 0 {
			if affectedErr != nil {
				return "", affectedErr
			}
			return "", ErrNotFound
		}
	}
	// username 保存事务内当前用户名投影。
	var username string
	// isActive 和 isAdmin 保存事务内用户启用与管理员投影。
	var isActive, isAdmin int
	// authVersion 保存事务内认证代次。
	var authVersion int64
	// userQuery 保存按数据库方言选择的用户锁定查询。
	userQuery := `SELECT username, is_active, is_admin, auth_version FROM users WHERE id=?`
	if s.Dialect != DialectSQLite && s.Dialect != "" {
		userQuery += ` FOR UPDATE`
	}
	// err 保存事务内用户投影读取错误。
	if err := tx.QueryRowContext(ctx, userQuery, u.ID).Scan(&username, &isActive, &isAdmin, &authVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if isActive == 0 || authVersion != expectedVersion {
		return "", ErrStaleLogin
	}
	// sessionID 和 err 保存新会话 ID 及随机生成错误。
	sessionID, err := randomSessionID()
	if err != nil {
		return "", fmt.Errorf("生成 session id: %w", err)
	}
	// now 和 expires 保存会话创建时间与本地过期秒数。
	now := time.Now().Unix()
	// expires 保存会话过期秒数。
	expires := now + int64(SessionTTL.Seconds())
	// admin 保存数据库兼容格式的管理员标志。
	admin := 0
	if isAdmin != 0 {
		admin = 1
	}
	// err 保存事务内会话插入错误。
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (session_id, user_id, username, is_admin, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`, sessionID, u.ID, username, admin, expires, now); err != nil {
		return "", fmt.Errorf("写入 session: %w", err)
	}
	// err 保存事务提交错误。
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return sessionID, nil
}

// Get 取会话；过期或不存在返回 ErrNotFound，并清理过期记录。
func (s *Sessions) Get(ctx context.Context, sessionID string) (*Session, error) {
	if sessionID == "" {
		return nil, ErrNotFound
	}
	// sess 用于本次流程后续判断的sess
	var sess Session
	// isAdmin 用于本次流程后续判断的isAdmin
	var isAdmin int
	// err 用于本次流程后续判断的err
	err := s.DB.QueryRowContext(ctx,
		`SELECT s.session_id, s.user_id, u.username, u.is_admin, s.expires_at
		   FROM sessions s
		   JOIN users u ON u.id=s.user_id
		  WHERE s.session_id=? AND u.is_active=1`,
		sessionID).Scan(&sess.SessionID, &sess.UserID, &sess.Username, &isAdmin, &sess.ExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sess.IsAdmin = isAdmin != 0
	if sess.ExpiresAt <= time.Now().Unix() {
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE session_id=?`, sessionID)
		return nil, ErrNotFound
	}
	return &sess, nil
}

// Delete 删除会话（登出）。
func (s *Sessions) Delete(ctx context.Context, sessionID string) error {
	// err 用于本次流程后续判断的err
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE session_id=?`, sessionID)
	return err
}

// DeleteExpired 清理所有过期会话（可由定时任务调用）。
func (s *Sessions) DeleteExpired(ctx context.Context) (int64, error) {
	// res、err 用于本次流程后续判断的res、err
	res, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	// n 用于本次流程后续判断的n
	n, _ := res.RowsAffected()
	return n, nil
}

// randomSessionID 生成 URL 安全的随机会话 ID。
func randomSessionID() (string, error) {
	// b 用于本次流程后续判断的b
	b := make([]byte, 32)
	if // err 用于本次流程后续判断的err
	_, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
