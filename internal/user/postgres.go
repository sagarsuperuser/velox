package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Create(ctx context.Context, u User) (User, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return User{}, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	id := u.ID
	if id == "" {
		id = postgres.NewID("vlx_usr")
	}
	status := u.Status
	if status == "" {
		status = StatusActive
	}
	email := strings.ToLower(strings.TrimSpace(u.Email))

	var ph sql.NullString
	if u.PasswordHash != "" {
		ph = sql.NullString{String: u.PasswordHash, Valid: true}
	}
	var verified sql.NullTime
	if u.EmailVerifiedAt != nil {
		verified = sql.NullTime{Time: *u.EmailVerifiedAt, Valid: true}
	}

	err = tx.QueryRowContext(ctx,
		`INSERT INTO users (id, email, display_name, status, password_hash, email_verified_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, created_at, updated_at`,
		id, email, u.DisplayName, status, ph, verified,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	u.Email = email
	u.Status = status
	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("commit: %w", err)
	}
	return u, nil
}

func (s *PostgresStore) GetByEmail(ctx context.Context, email string) (User, error) {
	return s.queryOne(ctx,
		`SELECT id, email, display_name, status, password_hash, email_verified_at, created_at, updated_at
		 FROM users WHERE email = $1`,
		strings.ToLower(strings.TrimSpace(email)))
}

func (s *PostgresStore) GetByID(ctx context.Context, id string) (User, error) {
	return s.queryOne(ctx,
		`SELECT id, email, display_name, status, password_hash, email_verified_at, created_at, updated_at
		 FROM users WHERE id = $1`,
		id)
}

func (s *PostgresStore) queryOne(ctx context.Context, query string, arg any) (User, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return User{}, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	var u User
	var ph sql.NullString
	var verified sql.NullTime
	err = tx.QueryRowContext(ctx, query, arg).Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.Status, &ph, &verified, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("query user: %w", err)
	}
	if ph.Valid {
		u.PasswordHash = ph.String
	}
	if verified.Valid {
		t := verified.Time
		u.EmailVerifiedAt = &t
	}
	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("commit: %w", err)
	}
	return u, nil
}

func (s *PostgresStore) SetPassword(ctx context.Context, userID, hash string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`,
		hash, userID)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) MarkEmailVerified(ctx context.Context, userID string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx,
		`UPDATE users SET email_verified_at = $1, updated_at = now() WHERE id = $2`,
		at, userID)
	if err != nil {
		return fmt.Errorf("update verified: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) AddMembership(ctx context.Context, m Membership) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	role := m.Role
	if role == "" {
		role = RoleOwner
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO user_tenants (user_id, tenant_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = EXCLUDED.role, updated_at = now()`,
		m.UserID, m.TenantID, role)
	if err != nil {
		return fmt.Errorf("insert membership: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresStore) ListMemberships(ctx context.Context, userID string) ([]Membership, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx,
		`SELECT user_id, tenant_id, role, created_at
		 FROM user_tenants WHERE user_id = $1 ORDER BY created_at`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("query memberships: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.UserID, &m.TenantID, &m.Role, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s *PostgresStore) IssueResetToken(ctx context.Context, tokenHash, userID string, expiresAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx,
		`INSERT INTO password_reset_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt)
	if err != nil {
		return fmt.Errorf("insert reset token: %w", err)
	}
	return tx.Commit()
}

// ConsumeResetToken atomically marks a token consumed and returns its user.
// A token that does not exist, has expired, or has already been consumed
// returns ErrResetInvalid — callers should treat these identically to avoid
// leaking state via error differences.
func (s *PostgresStore) ConsumeResetToken(ctx context.Context, tokenHash string, now time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	var userID string
	err = tx.QueryRowContext(ctx,
		`UPDATE password_reset_tokens
		 SET consumed_at = $1
		 WHERE token_hash = $2 AND consumed_at IS NULL AND expires_at > $1
		 RETURNING user_id`,
		now, tokenHash,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrResetInvalid
	}
	if err != nil {
		return "", fmt.Errorf("consume reset token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return userID, nil
}

// ListMembersForTenant joins user_tenants with users so the handler can
// render the members page without a follow-up per-row lookup.
func (s *PostgresStore) ListMembersForTenant(ctx context.Context, tenantID string) ([]Member, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx,
		`SELECT u.id, u.email, u.display_name, ut.role, ut.created_at
		 FROM user_tenants ut JOIN users u ON u.id = ut.user_id
		 WHERE ut.tenant_id = $1
		 ORDER BY ut.created_at ASC`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("query members: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.DisplayName, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s *PostgresStore) RemoveMembership(ctx context.Context, userID, tenantID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx,
		`DELETE FROM user_tenants WHERE user_id = $1 AND tenant_id = $2`,
		userID, tenantID)
	if err != nil {
		return fmt.Errorf("delete membership: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) CountOwnersForTenant(ctx context.Context, tenantID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	var n int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_tenants WHERE tenant_id = $1 AND role = 'owner'`,
		tenantID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count owners: %w", err)
	}
	return n, tx.Commit()
}

// --- invitations -----------------------------------------------------------

func (s *PostgresStore) CreateInvitation(ctx context.Context, inv Invitation, tokenHash string) (Invitation, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return Invitation{}, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	id := inv.ID
	if id == "" {
		id = postgres.NewID("vlx_inv")
	}
	role := inv.Role
	if role == "" {
		role = RoleMember
	}
	email := strings.ToLower(strings.TrimSpace(inv.Email))

	err = tx.QueryRowContext(ctx,
		`INSERT INTO member_invitations
		   (id, tenant_id, email, token_hash, invited_by_user_id, role, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at`,
		id, inv.TenantID, email, tokenHash, inv.InvitedByUserID, role, inv.ExpiresAt,
	).Scan(&inv.ID, &inv.CreatedAt)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return Invitation{}, ErrPendingInvite
		}
		return Invitation{}, fmt.Errorf("insert invitation: %w", err)
	}
	inv.Email = email
	inv.Role = role
	if err := tx.Commit(); err != nil {
		return Invitation{}, fmt.Errorf("commit: %w", err)
	}
	return inv, nil
}

func (s *PostgresStore) GetInvitationByHash(ctx context.Context, tokenHash string) (Invitation, error) {
	return s.scanInvitation(ctx,
		`SELECT id, tenant_id, email, invited_by_user_id, role, expires_at,
		        accepted_at, revoked_at, created_at
		 FROM member_invitations WHERE token_hash = $1`,
		tokenHash)
}

func (s *PostgresStore) GetInvitationByID(ctx context.Context, id string) (Invitation, error) {
	return s.scanInvitation(ctx,
		`SELECT id, tenant_id, email, invited_by_user_id, role, expires_at,
		        accepted_at, revoked_at, created_at
		 FROM member_invitations WHERE id = $1`,
		id)
}

func (s *PostgresStore) scanInvitation(ctx context.Context, query string, arg any) (Invitation, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return Invitation{}, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	var inv Invitation
	var accepted, revoked sql.NullTime
	err = tx.QueryRowContext(ctx, query, arg).Scan(
		&inv.ID, &inv.TenantID, &inv.Email, &inv.InvitedByUserID, &inv.Role,
		&inv.ExpiresAt, &accepted, &revoked, &inv.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, ErrInvitationInvalid
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("query invitation: %w", err)
	}
	if accepted.Valid {
		t := accepted.Time
		inv.AcceptedAt = &t
	}
	if revoked.Valid {
		t := revoked.Time
		inv.RevokedAt = &t
	}
	return inv, tx.Commit()
}

// ListInvitationsForTenant returns invitations joined with the inviter's
// email for display. Both pending and historical rows are returned; the
// handler decides which to show.
func (s *PostgresStore) ListInvitationsForTenant(ctx context.Context, tenantID string) ([]Invitation, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx,
		`SELECT i.id, i.tenant_id, i.email, i.invited_by_user_id, u.email, i.role,
		        i.expires_at, i.accepted_at, i.revoked_at, i.created_at
		 FROM member_invitations i
		 LEFT JOIN users u ON u.id = i.invited_by_user_id
		 WHERE i.tenant_id = $1
		 ORDER BY i.created_at DESC`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("query invitations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Invitation
	for rows.Next() {
		var inv Invitation
		var inviterEmail sql.NullString
		var accepted, revoked sql.NullTime
		if err := rows.Scan(
			&inv.ID, &inv.TenantID, &inv.Email, &inv.InvitedByUserID, &inviterEmail, &inv.Role,
			&inv.ExpiresAt, &accepted, &revoked, &inv.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		if inviterEmail.Valid {
			inv.InvitedByEmail = inviterEmail.String
		}
		if accepted.Valid {
			t := accepted.Time
			inv.AcceptedAt = &t
		}
		if revoked.Valid {
			t := revoked.Time
			inv.RevokedAt = &t
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s *PostgresStore) MarkInvitationAccepted(ctx context.Context, id string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx,
		`UPDATE member_invitations SET accepted_at = $1
		 WHERE id = $2 AND accepted_at IS NULL AND revoked_at IS NULL`,
		at, id)
	if err != nil {
		return fmt.Errorf("mark accepted: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInvitationConsumed
	}
	return tx.Commit()
}

func (s *PostgresStore) RevokeInvitation(ctx context.Context, id string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx,
		`UPDATE member_invitations SET revoked_at = $1
		 WHERE id = $2 AND accepted_at IS NULL AND revoked_at IS NULL`,
		at, id)
	if err != nil {
		return fmt.Errorf("revoke invitation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInvitationConsumed
	}
	return tx.Commit()
}
