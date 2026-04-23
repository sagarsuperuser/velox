// Package dashauth is the HTTP layer for dashboard (email+password) auth.
// It coordinates the user store, session store, and email outbox to
// implement login, logout, and password-reset flows. The session-auth
// middleware and the mode-toggle endpoint live here too so the full
// "dashboard account lifecycle" is readable in one place.
package dashauth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/session"
	"github.com/sagarsuperuser/velox/internal/user"
)

// EmailNotifier is the narrow interface dashauth needs from the outbox —
// password-reset and team-invite delivery. Kept local so the package can
// stay test-friendly without pulling in the full EmailDeliverer surface.
type EmailNotifier interface {
	SendPasswordReset(tenantID, to, displayName, resetURL string) error
	SendMemberInvite(tenantID, to, inviterEmail, tenantName, acceptURL string) error
}

// TenantLookup is the narrow view of tenant.Service dashauth needs: pull a
// tenant's display name for outgoing emails and for the invite-preview
// screen. Kept local to avoid a cross-domain import.
type TenantLookup interface {
	Name(ctx context.Context, tenantID string) (string, error)
}

type Handler struct {
	users     *user.Service
	sessions  *session.Service
	tenants   TenantLookup
	email     EmailNotifier
	resetURL  string // frontend URL that consumes the reset token
	inviteURL string // frontend URL that consumes the invite token
	cookie    CookieConfig
}

// CookieConfig centralises session cookie attributes. Secure is pinned
// by APP_ENV: off for local (HTTP), on for staging/production (HTTPS).
// SameSite=Lax keeps the cookie attached across top-level navigation
// (OK for a first-party dashboard) while blocking most cross-site CSRF.
type CookieConfig struct {
	Domain   string
	Secure   bool
	SameSite http.SameSite
	Path     string
}

// DefaultCookieConfig reads environment flags and returns a sensible
// default. Tests override fields directly.
func DefaultCookieConfig() CookieConfig {
	secure := strings.EqualFold(os.Getenv("APP_ENV"), "production") ||
		strings.EqualFold(os.Getenv("APP_ENV"), "staging")
	return CookieConfig{
		Domain:   os.Getenv("VELOX_COOKIE_DOMAIN"),
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	}
}

// NewHandler wires the services. resetURL is a template of the form
// "https://dashboard.velox.dev/reset?token=%s" — the raw token gets
// placed into the last segment. Pass empty string to fall back to a
// default derived from VELOX_DASHBOARD_URL. inviteURL follows the same
// convention. tenants may be nil in tests that don't exercise the
// invite flow; production must supply it.
func NewHandler(users *user.Service, sessions *session.Service, tenants TenantLookup, email EmailNotifier, resetURL, inviteURL string, cookie CookieConfig) *Handler {
	base := strings.TrimRight(os.Getenv("VELOX_DASHBOARD_URL"), "/")
	if base == "" {
		base = "http://localhost:5173"
	}
	if resetURL == "" {
		resetURL = base + "/reset-password?token=%s"
	}
	if inviteURL == "" {
		inviteURL = base + "/accept-invite?token=%s"
	}
	return &Handler{
		users:     users,
		sessions:  sessions,
		tenants:   tenants,
		email:     email,
		resetURL:  resetURL,
		inviteURL: inviteURL,
		cookie:    cookie,
	}
}

// Routes returns the public auth routes — login, logout, password-reset,
// invite preview and acceptance. Mount under /v1/auth. None of these
// require an existing session; that's precisely why they're outside the
// /v1 tenant-scoped subtree.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/login", h.login)
	r.Post("/logout", h.logout)
	r.Post("/password-reset-request", h.resetRequest)
	r.Post("/password-reset-confirm", h.resetConfirm)
	r.Get("/invite/{token}", h.invitePreview)
	r.Post("/accept-invite", h.acceptInvite)
	return r
}

// SessionRoutes returns the session-scoped routes (mode toggle, whoami).
// Mount under /v1/session with session.Middleware applied.
func (h *Handler) SessionRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.whoami)
	r.Patch("/", h.patchSession)
	return r
}

// --- handlers ---------------------------------------------------------------

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type sessionResp struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	TenantID  string    `json:"tenant_id"`
	Livemode  bool      `json:"livemode"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Email) == "" || req.Password == "" {
		respond.ValidationField(w, r, "email", "email and password are required")
		return
	}

	u, err := h.users.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, user.ErrInvalidPassword):
			respond.Unauthorized(w, r, "invalid email or password")
		case errors.Is(err, user.ErrDisabled):
			respond.Forbidden(w, r, "account disabled")
		default:
			slog.Error("dashauth: login failed", "err", err)
			respond.InternalError(w, r)
		}
		return
	}

	m, err := h.users.PrimaryTenant(r.Context(), u.ID)
	if err != nil {
		if errors.Is(err, user.ErrMembershipMissing) {
			respond.Forbidden(w, r, "account has no tenant membership — contact your administrator")
			return
		}
		slog.Error("dashauth: primary tenant lookup failed", "err", err, "user_id", u.ID)
		respond.InternalError(w, r)
		return
	}

	sess, err := h.sessions.Issue(r.Context(), u.ID, m.TenantID, r.UserAgent(), clientIP(r))
	if err != nil {
		slog.Error("dashauth: session issue failed", "err", err, "user_id", u.ID)
		respond.InternalError(w, r)
		return
	}

	h.setSessionCookie(w, sess.ID, sess.ExpiresAt)
	respond.JSON(w, r, http.StatusOK, sessionResp{
		UserID:    u.ID,
		Email:     u.Email,
		TenantID:  m.TenantID,
		Livemode:  sess.Livemode,
		ExpiresAt: sess.ExpiresAt,
	})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(session.CookieName)
	if err == nil && c.Value != "" {
		_ = h.sessions.Revoke(r.Context(), session.HashID(c.Value))
	}
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type resetRequestReq struct {
	Email string `json:"email"`
}

func (h *Handler) resetRequest(w http.ResponseWriter, r *http.Request) {
	var req resetRequestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Email) == "" {
		respond.ValidationField(w, r, "email", "email is required")
		return
	}

	u, rawToken, err := h.users.RequestPasswordReset(r.Context(), req.Email)
	if err != nil {
		slog.Error("dashauth: reset-request failed", "err", err)
		// Still respond success to avoid enumeration — the error is logged.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if rawToken != "" {
		// Non-empty means a user matched and a token was minted. Queue email
		// via the tenant associated with their primary membership.
		m, err := h.users.PrimaryTenant(r.Context(), u.ID)
		if err == nil && h.email != nil {
			resetURL := strings.Replace(h.resetURL, "%s", rawToken, 1)
			if err := h.email.SendPasswordReset(m.TenantID, u.Email, u.DisplayName, resetURL); err != nil {
				slog.Error("dashauth: enqueue reset email failed", "err", err)
			}
		} else if h.email == nil {
			slog.Warn("dashauth: reset email not enqueued (no email notifier)",
				"user_id", u.ID, "reset_url_template", h.resetURL)
		}
	}

	// Always 202 regardless of whether a user matched — enumeration resistance.
	w.WriteHeader(http.StatusAccepted)
}

type resetConfirmReq struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func (h *Handler) resetConfirm(w http.ResponseWriter, r *http.Request) {
	var req resetConfirmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if req.Token == "" || req.Password == "" {
		respond.ValidationField(w, r, "token", "token and password are required")
		return
	}

	err := h.users.ConsumeReset(r.Context(), req.Token, req.Password, h.sessions.RevokeAllForUser)
	if err != nil {
		switch {
		case errors.Is(err, user.ErrResetInvalid):
			respond.Unauthorized(w, r, "reset link is invalid or has expired — request a new one")
		default:
			if strings.Contains(err.Error(), "password") {
				respond.ValidationField(w, r, "password", err.Error())
				return
			}
			slog.Error("dashauth: reset-confirm failed", "err", err)
			respond.InternalError(w, r)
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- session-scoped endpoints ----------------------------------------------

type patchSessionReq struct {
	Livemode *bool `json:"livemode"`
}

func (h *Handler) whoami(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	tenantID := auth.TenantID(r.Context())
	u, err := h.users.GetByID(r.Context(), userID)
	if err != nil {
		respond.InternalError(w, r)
		return
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{
		"user_id":   u.ID,
		"email":     u.Email,
		"tenant_id": tenantID,
		"livemode":  auth.Livemode(r.Context()),
	})
}

// --- invite acceptance -----------------------------------------------------

type invitePreviewResp struct {
	Email           string `json:"email"`
	TenantID        string `json:"tenant_id"`
	TenantName      string `json:"tenant_name"`
	NeedsNewAccount bool   `json:"needs_new_account"`
	InvitedByEmail  string `json:"invited_by_email,omitempty"`
	ExpiresAt       string `json:"expires_at"`
}

// invitePreview is the pre-accept read. The UI uses it to decide between
// "create your account" and "sign in with your existing password" copy.
// All invalid states collapse to 404 — a targeted error would let a URL
// scanner learn that an invitation ID exists.
func (h *Handler) invitePreview(w http.ResponseWriter, r *http.Request) {
	rawToken := chi.URLParam(r, "token")
	if rawToken == "" {
		respond.NotFound(w, r, "invitation not found")
		return
	}
	inv, err := h.users.PreviewInvitation(r.Context(), rawToken)
	if err != nil {
		switch {
		case errors.Is(err, user.ErrInvitationInvalid), errors.Is(err, user.ErrInvitationConsumed):
			respond.NotFound(w, r, "invitation is invalid, expired, or already used")
		default:
			slog.Error("dashauth: invite preview failed", "err", err)
			respond.InternalError(w, r)
		}
		return
	}

	tenantName := ""
	if h.tenants != nil {
		if n, err := h.tenants.Name(r.Context(), inv.TenantID); err == nil {
			tenantName = n
		}
	}

	_, err = h.users.GetByEmailOrNotFound(r.Context(), inv.Email)
	needsNew := errors.Is(err, user.ErrNotFound)

	respond.JSON(w, r, http.StatusOK, invitePreviewResp{
		Email:           inv.Email,
		TenantID:        inv.TenantID,
		TenantName:      tenantName,
		NeedsNewAccount: needsNew,
		InvitedByEmail:  inv.InvitedByEmail,
		ExpiresAt:       inv.ExpiresAt.Format(time.RFC3339),
	})
}

type acceptInviteReq struct {
	Token       string `json:"token"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// acceptInvite consumes the invitation, ensures a user exists for the
// invited email, adds the tenant membership, and issues a session cookie.
// On existing-account accept the caller MUST supply their current password;
// the service layer rejects wrong passwords as ErrInvalidPassword.
func (h *Handler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req acceptInviteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if req.Token == "" || req.Password == "" {
		respond.ValidationField(w, r, "token", "token and password are required")
		return
	}

	userID, tenantID, err := h.users.AcceptInvitation(r.Context(), req.Token, req.Password, req.DisplayName)
	if err != nil {
		switch {
		case errors.Is(err, user.ErrInvitationInvalid), errors.Is(err, user.ErrInvitationConsumed):
			respond.NotFound(w, r, "invitation is invalid, expired, or already used")
		case errors.Is(err, user.ErrInvalidPassword):
			respond.Unauthorized(w, r, "invalid password for existing account")
		case errors.Is(err, user.ErrDisabled):
			respond.Forbidden(w, r, "account disabled")
		default:
			if strings.Contains(err.Error(), "password") {
				respond.ValidationField(w, r, "password", err.Error())
				return
			}
			slog.Error("dashauth: accept-invite failed", "err", err)
			respond.InternalError(w, r)
		}
		return
	}

	sess, err := h.sessions.Issue(r.Context(), userID, tenantID, r.UserAgent(), clientIP(r))
	if err != nil {
		slog.Error("dashauth: accept-invite session issue failed", "err", err, "user_id", userID)
		respond.InternalError(w, r)
		return
	}
	u, err := h.users.GetByID(r.Context(), userID)
	if err != nil {
		slog.Error("dashauth: accept-invite user lookup failed", "err", err, "user_id", userID)
		respond.InternalError(w, r)
		return
	}

	h.setSessionCookie(w, sess.ID, sess.ExpiresAt)
	respond.JSON(w, r, http.StatusOK, sessionResp{
		UserID:    u.ID,
		Email:     u.Email,
		TenantID:  tenantID,
		Livemode:  sess.Livemode,
		ExpiresAt: sess.ExpiresAt,
	})
}

// SendInvite is a helper for dashmembers to dispatch the invite email using
// dashauth's URL template + tenant lookup. Keeping this here (rather than in
// dashmembers) avoids duplicating inviteURL and the tenant-name fetch.
func (h *Handler) SendInvite(ctx context.Context, tenantID, toEmail, inviterEmail, rawToken string) error {
	if h.email == nil {
		slog.Warn("dashauth: invite email not enqueued (no email notifier)",
			"to", toEmail, "invite_url_template", h.inviteURL)
		return nil
	}
	tenantName := tenantID
	if h.tenants != nil {
		if n, err := h.tenants.Name(ctx, tenantID); err == nil && n != "" {
			tenantName = n
		}
	}
	url := strings.Replace(h.inviteURL, "%s", rawToken, 1)
	return h.email.SendMemberInvite(tenantID, toEmail, inviterEmail, tenantName, url)
}

func (h *Handler) patchSession(w http.ResponseWriter, r *http.Request) {
	var req patchSessionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if req.Livemode == nil {
		respond.ValidationField(w, r, "livemode", "livemode is required")
		return
	}

	sid := auth.SessionID(r.Context())
	if sid == "" {
		respond.Unauthorized(w, r, "no session in context")
		return
	}
	if err := h.sessions.SetLivemode(r.Context(), sid, *req.Livemode); err != nil {
		slog.Error("dashauth: set livemode failed", "err", err)
		respond.InternalError(w, r)
		return
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{
		"livemode": *req.Livemode,
	})
}

// --- helpers ---------------------------------------------------------------

func (h *Handler) setSessionCookie(w http.ResponseWriter, raw string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     session.CookieName,
		Value:    raw,
		Path:     h.cookie.Path,
		Domain:   h.cookie.Domain,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: h.cookie.SameSite,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     session.CookieName,
		Value:    "",
		Path:     h.cookie.Path,
		Domain:   h.cookie.Domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: h.cookie.SameSite,
	})
}

// clientIP pulls the caller's IP, preferring X-Forwarded-For (set by the
// RealIP middleware upstream). The stored value is informational — session
// rotation isn't bound to IP for usability reasons (mobile/Wi-Fi switching).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if comma := strings.IndexByte(xff, ','); comma > 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}
