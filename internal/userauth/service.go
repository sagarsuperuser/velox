package userauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

const (
	bcryptCost       = 12
	sessionTokenLen  = 32 // bytes → 64 hex chars
	sessionDuration  = 30 * 24 * time.Hour
	resetTokenLen    = 32
	resetTokenExpiry = 1 * time.Hour
	minPasswordLen   = 8
)

// User represents a dashboard user account (password hash excluded).
type User struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// Session is an internal representation (not exposed via API).
type Session struct {
	ID        string
	UserID    string
	TenantID  string
	ExpiresAt time.Time
}

// Service handles user registration, login, session management, and password resets.
type Service struct {
	db *postgres.DB
}

// NewService creates a new userauth Service.
func NewService(db *postgres.DB) *Service {
	return &Service{db: db}
}

// Register creates a new user account for the given tenant.
func (s *Service) Register(ctx context.Context, tenantID, email, password, name string) (User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	name = strings.TrimSpace(name)

	if _, err := mail.ParseAddress(email); err != nil {
		return User{}, fmt.Errorf("invalid email address")
	}
	if len(password) < minPasswordLen {
		return User{}, fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}

	id := postgres.NewID("vlx_usr")
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return User{}, fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx,
		`INSERT INTO user_accounts (id, tenant_id, email, password_hash, name, role, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, 'admin', $6, $6)`,
		id, tenantID, email, string(hash), name, now)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return User{}, fmt.Errorf("a user with this email already exists")
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("commit: %w", err)
	}

	return User{
		ID:        id,
		TenantID:  tenantID,
		Email:     email,
		Name:      name,
		Role:      "admin",
		CreatedAt: now,
	}, nil
}

// Login authenticates a user by email and password, returning a session token and user.
// The raw session token is returned to set in the cookie; the DB stores SHA-256(token).
func (s *Service) Login(ctx context.Context, email, password string) (string, User, error) {
	email = strings.TrimSpace(strings.ToLower(email))

	// Bypass RLS — we need to find the user across tenants by email.
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return "", User{}, fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	var (
		user         User
		passwordHash string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT id, tenant_id, email, password_hash, name, role, created_at
		 FROM user_accounts WHERE email = $1`, email).
		Scan(&user.ID, &user.TenantID, &user.Email, &passwordHash, &user.Name, &user.Role, &user.CreatedAt)
	if err == sql.ErrNoRows {
		return "", User{}, fmt.Errorf("invalid email or password")
	}
	if err != nil {
		return "", User{}, fmt.Errorf("query user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return "", User{}, fmt.Errorf("invalid email or password")
	}

	// Generate session token
	tokenBytes := make([]byte, sessionTokenLen)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", User{}, fmt.Errorf("generate session token: %w", err)
	}
	rawToken := hex.EncodeToString(tokenBytes)
	tokenHash := hashToken(rawToken)

	sessionID := postgres.NewID("vlx_ses")
	expiresAt := time.Now().UTC().Add(sessionDuration)

	_, err = tx.ExecContext(ctx,
		`INSERT INTO user_sessions (id, user_id, tenant_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		sessionID, user.ID, user.TenantID, tokenHash, expiresAt)
	if err != nil {
		return "", User{}, fmt.Errorf("create session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", User{}, fmt.Errorf("commit: %w", err)
	}

	return rawToken, user, nil
}

// ValidateSession verifies a session token and returns the associated user.
// Called by middleware on every authenticated request.
func (s *Service) ValidateSession(ctx context.Context, sessionToken string) (User, error) {
	tokenHash := hashToken(sessionToken)

	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return User{}, fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	var user User
	err = tx.QueryRowContext(ctx,
		`SELECT u.id, u.tenant_id, u.email, u.name, u.role, u.created_at
		 FROM user_sessions s
		 JOIN user_accounts u ON u.id = s.user_id
		 WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash).
		Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Role, &user.CreatedAt)
	if err == sql.ErrNoRows {
		return User{}, fmt.Errorf("invalid or expired session")
	}
	if err != nil {
		return User{}, fmt.Errorf("query session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("commit: %w", err)
	}

	return user, nil
}

// Logout deletes the session identified by the given token.
func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	tokenHash := hashToken(sessionToken)

	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx,
		`DELETE FROM user_sessions WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return tx.Commit()
}

// ForgotPassword generates a password reset token for the given email.
// Always returns nil (no error) even if the email is not found, to prevent enumeration.
func (s *Service) ForgotPassword(ctx context.Context, email string) (string, error) {
	email = strings.TrimSpace(strings.ToLower(email))

	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	var userID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM user_accounts WHERE email = $1`, email).
		Scan(&userID)
	if err == sql.ErrNoRows {
		// Don't reveal that the email doesn't exist
		slog.Info("forgot password requested for unknown email", "email", email)
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("query user: %w", err)
	}

	// Generate reset token
	tokenBytes := make([]byte, resetTokenLen)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate reset token: %w", err)
	}
	rawToken := hex.EncodeToString(tokenBytes)
	tokenHash := hashToken(rawToken)

	tokenID := postgres.NewID("vlx_prt")
	expiresAt := time.Now().UTC().Add(resetTokenExpiry)

	_, err = tx.ExecContext(ctx,
		`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		tokenID, userID, tokenHash, expiresAt)
	if err != nil {
		return "", fmt.Errorf("create reset token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return rawToken, nil
}

// ResetPassword validates a reset token and updates the user's password.
// All existing sessions for the user are deleted (force re-login).
func (s *Service) ResetPassword(ctx context.Context, resetToken, newPassword string) error {
	if len(newPassword) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}

	tokenHash := hashToken(resetToken)

	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	var (
		tokenID string
		userID  string
		usedAt  sql.NullTime
	)
	err = tx.QueryRowContext(ctx,
		`SELECT id, user_id, used_at FROM password_reset_tokens
		 WHERE token_hash = $1 AND expires_at > now()`, tokenHash).
		Scan(&tokenID, &userID, &usedAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("invalid or expired reset token")
	}
	if err != nil {
		return fmt.Errorf("query reset token: %w", err)
	}
	if usedAt.Valid {
		return fmt.Errorf("reset token has already been used")
	}

	// Hash new password
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	// Update password
	_, err = tx.ExecContext(ctx,
		`UPDATE user_accounts SET password_hash = $1, updated_at = now() WHERE id = $2`,
		string(hash), userID)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	// Mark token as used
	_, err = tx.ExecContext(ctx,
		`UPDATE password_reset_tokens SET used_at = now() WHERE id = $1`, tokenID)
	if err != nil {
		return fmt.Errorf("mark token used: %w", err)
	}

	// Delete all sessions for this user (force re-login)
	_, err = tx.ExecContext(ctx,
		`DELETE FROM user_sessions WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}

	return tx.Commit()
}

// ValidateSessionForAuth implements auth.SessionValidator.
// Returns (userID, tenantID, error) for use by the auth middleware.
func (s *Service) ValidateSessionForAuth(ctx context.Context, sessionToken string) (string, string, error) {
	user, err := s.ValidateSession(ctx, sessionToken)
	if err != nil {
		return "", "", err
	}
	return user.ID, user.TenantID, nil
}

// hashToken returns the hex-encoded SHA-256 hash of a raw token string.
func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
