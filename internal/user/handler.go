package user

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/session"
)

// Handler wires the dashboard auth endpoints. ADR-011.
//
// Routes (mounted under /v1/auth):
//
//	POST /login                     — email + password → mint cookie
//	POST /logout                    — clear cookie + revoke session
//	POST /password-reset/request    — email → send reset link
//	POST /password-reset/confirm    — token + new password → set
type Handler struct {
	users            *Service
	sessions         *session.Service
	cookie           session.CookieConfig
	email            EmailSender // required — production always wires the adapter
	dashboardBaseURL string      // canonical dashboard origin for reset links; never from request headers. empty => reset emails disabled
}

// EmailSender is the narrow surface this handler uses to dispatch
// password-reset emails. Satisfied in production by an adapter over
// internal/email's Sender / OutboxSender. SMTP misconfiguration
// surfaces as a SendPasswordReset error logged here; the response to
// the user stays generic to prevent enumeration.
type EmailSender interface {
	SendPasswordReset(ctx context.Context, tenantID, email, resetLink string) error
}

// NewHandler wires the dependencies. emailSender is required —
// password-reset requests will nil-panic if it's nil. dashboardBaseURL is
// the canonical dashboard origin used to build password-reset links; it is
// never derived from the request (Host header) to prevent host-header
// poisoning / token theft. When empty, password-reset emails are not sent
// (requestPasswordReset fails safe). Set it to your dashboard URL — in
// split-origin dev (Vite on :5173 vs API on :8080) that's the SPA URL.
func NewHandler(users *Service, sessions *session.Service, cookie session.CookieConfig, emailSender EmailSender, dashboardBaseURL string) *Handler {
	return &Handler{
		users:            users,
		sessions:         sessions,
		cookie:           cookie,
		email:            emailSender,
		dashboardBaseURL: strings.TrimRight(strings.TrimSpace(dashboardBaseURL), "/"),
	}
}

// Routes returns the dashboard auth surface. Mount under /v1/auth.
// All routes are intentionally outside session middleware: they're
// either pre-session (login, password-reset) or take the cookie via
// r.Cookie directly (logout, mode).
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/login", h.login)
	r.Post("/logout", h.logout)
	r.Post("/mode", h.setMode)
	r.Post("/password-reset/request", h.requestPasswordReset)
	r.Post("/password-reset/confirm", h.confirmPasswordReset)
	// GET /password-reset/check?token=… — non-consuming validity probe
	// for the reset-password page on mount. 200 = renderable form, 422
	// = link is invalid/expired/used. Idempotent so it's a GET.
	r.Get("/password-reset/check", h.checkPasswordResetToken)
	return r
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResp struct {
	UserID    string `json:"user_id"`
	TenantID  string `json:"tenant_id"`
	Email     string `json:"email"`
	Livemode  bool   `json:"livemode"`
	ExpiresAt string `json:"expires_at"`
}

// login authenticates the operator's email + password and mints a
// session cookie. Failures collapse into a single 401 with a
// constant-time bcrypt check on the not-found path so we don't leak
// account existence via response timing.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if req.Email == "" {
		respond.ValidationField(w, r, "email", "email is required")
		return
	}
	if req.Password == "" {
		respond.ValidationField(w, r, "password", "password is required")
		return
	}

	u, tenants, err := h.users.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		// On bad creds, still record the attempt against the (possibly
		// non-existent) email for the lockout counter. Don't bother
		// recording on lockout — already locked.
		if errors.Is(err, ErrBadCredentials) {
			h.users.RecordFailedAttempt(r.Context(), req.Email)
			respond.Unauthorized(w, r, "invalid email or password")
			return
		}
		if errors.Is(err, ErrAccountLocked) {
			respond.Error(w, r, http.StatusTooManyRequests,
				"authentication_error", "account_locked",
				"too many failed attempts — try again in 15 minutes")
			return
		}
		respond.FromError(w, r, err, "user")
		return
	}

	// V1: each user belongs to exactly one tenant. The Authenticate
	// call already errored out on zero tenants. Pick the first.
	tenant := tenants[0]

	rawID, sess, err := h.sessions.Issue(r.Context(), session.IssueInput{
		UserID:    u.ID,
		TenantID:  tenant.TenantID,
		Livemode:  false, // dashboard sessions default to test mode; bearer for live
		UserAgent: r.UserAgent(),
		IP:        session.ClientIP(r),
	})
	if err != nil {
		slog.Error("session: issue failed", "err", err, "user_id", u.ID)
		respond.InternalError(w, r)
		return
	}

	h.cookie.SetCookie(w, rawID, sess.ExpiresAt)
	respond.JSON(w, r, http.StatusOK, loginResp{
		UserID:    u.ID,
		TenantID:  tenant.TenantID,
		Email:     u.Email,
		Livemode:  sess.Livemode,
		ExpiresAt: sess.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

type setModeReq struct {
	Livemode bool `json:"livemode"`
}

// setMode flips the active mode (test/live) on the cookie session.
// Same operator switches between modes without re-authenticating.
// Returns 401 if the cookie is missing or the session is gone.
func (h *Handler) setMode(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(session.CookieName)
	if err != nil || c.Value == "" {
		respond.Unauthorized(w, r, "missing session — sign in at /login")
		return
	}

	var req setModeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	if err := h.sessions.SetLivemode(r.Context(), c.Value, req.Livemode); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			respond.Unauthorized(w, r, "invalid or expired session")
			return
		}
		slog.Error("session: set livemode failed", "err", err)
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{
		"livemode": req.Livemode,
	})
}

// logout revokes the session row matching the cookie and clears the
// cookie on the response. Idempotent — missing cookie is a no-op.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(session.CookieName)
	if err == nil && c.Value != "" {
		if revokeErr := h.sessions.Revoke(r.Context(), c.Value); revokeErr != nil {
			slog.Error("session: revoke failed", "err", revokeErr)
		}
	}
	h.cookie.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type requestResetReq struct {
	Email string `json:"email"`
}

// requestPasswordReset issues a reset token (if the email matches a
// user) and emails the operator a reset link. Always returns 200 with
// a generic message — never confirms whether the email matched a
// user, to avoid account enumeration.
func (h *Handler) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req requestResetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if req.Email == "" {
		respond.ValidationField(w, r, "email", "email is required")
		return
	}

	plaintext, tenantID, err := h.users.IssueResetToken(r.Context(), req.Email)
	if err != nil {
		slog.Error("password reset issue failed", "err", err)
		// Generic response — don't expose internal failures
	}

	if plaintext != "" && tenantID != "" {
		// Send is best-effort: failure is logged but never surfaces to
		// the response, since the response shape is fixed at "if your
		// email is on file, you'll get a link" to avoid email
		// enumeration. SMTP misconfiguration shows up as a logged
		// SendPasswordReset error.
		resetLink, ok := h.buildResetLink(plaintext)
		if !ok {
			// DASHBOARD_BASE_URL is unset. Refuse to derive the link origin
			// from the request Host header — a poisoned Host would email the
			// victim a reset link pointing at an attacker domain, leaking the
			// token. Fail safe: don't send a poisonable link. Operator must
			// set DASHBOARD_BASE_URL to enable password-reset emails.
			slog.Error("password reset email not sent: DASHBOARD_BASE_URL is unset; refusing to build a reset link from request headers", "tenant_id", tenantID)
		} else if sendErr := h.email.SendPasswordReset(r.Context(), tenantID, req.Email, resetLink); sendErr != nil {
			slog.Error("password reset email send failed", "err", sendErr)
		}
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{
		"message": "if an account exists for that email, a password-reset link has been sent",
	})
}

type confirmResetReq struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// checkPasswordResetToken probes a reset token's validity without
// consuming it. 200 with {"valid": true} when the token is currently
// usable; 422 with the same shape (valid: false) when it's expired,
// already used, or unknown. The reset-password page hits this on
// mount so it can render "this link is no longer valid" instead of
// a form the user fills in only to be rejected at submit.
func (h *Handler) checkPasswordResetToken(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if err := h.users.CheckResetToken(r.Context(), token); err != nil {
		respond.JSON(w, r, http.StatusUnprocessableEntity, map[string]any{
			"valid":  false,
			"reason": "invalid_or_expired",
		})
		return
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"valid": true})
}

// confirmPasswordReset validates the reset token, applies the new
// password, and revokes every active session for the user (forces a
// fresh login). Revoking matters for the threat the reset is most
// often run against: a thief who already has a live velox_session
// cookie. Without the fan-out revoke the stolen cookie would ride out
// its 7-day TTL even after the operator resets. Surface the
// password-validation error inline (e.g. "must be at least 12
// characters") so the dashboard can highlight the field; collapse
// token failures into a single 422.
func (h *Handler) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req confirmResetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if req.Token == "" {
		respond.ValidationField(w, r, "token", "token is required")
		return
	}
	if req.Password == "" {
		respond.ValidationField(w, r, "password", "password is required")
		return
	}

	u, err := h.users.ConsumeResetToken(r.Context(), req.Token, req.Password)
	if err != nil {
		// errs.Invalid (password validation) and errs.NotFound (token
		// invalid/expired/used) both map cleanly; let respond.FromError
		// do the routing.
		respond.FromError(w, r, err, "password_reset")
		return
	}

	// Revoke any sessions minted before the reset — including one a
	// thief opened from a stolen cookie, which is exactly the case the
	// operator is resetting to shut down. Failure here is logged but
	// not surfaced: the password is already changed, so the reset
	// succeeded; we don't want a transient session-store error to make
	// the operator think it didn't.
	if revokeErr := h.sessions.RevokeAllForUser(r.Context(), u.ID); revokeErr != nil {
		slog.Error("session: revoke-all-for-user after password reset failed",
			"user_id", u.ID, "err", revokeErr)
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{
		"message": "password updated — sign in with your new password",
	})
}

// buildResetLink constructs the operator-facing URL for the reset
// confirmation page from the configured DASHBOARD_BASE_URL. It never
// derives the origin from the request (Host header, X-Forwarded-Proto):
// a password-reset link is a security-token carrier, and a poisoned Host
// header would let an attacker email the victim a link pointing at their
// own domain to capture the token. Returns ok=false when
// DASHBOARD_BASE_URL is unset so the caller can fail safe rather than
// send a poisonable link. Single-origin prod deployments still set
// DASHBOARD_BASE_URL to their canonical dashboard URL.
func (h *Handler) buildResetLink(token string) (string, bool) {
	if h.dashboardBaseURL == "" {
		return "", false
	}
	return h.dashboardBaseURL + "/reset-password?token=" + token, true
}
