package userauth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
)

const cookieName = "velox_session"

// Handler serves the /v1/auth routes (no auth middleware required).
type Handler struct {
	svc *Service
}

// NewHandler creates a new auth Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes returns the auth routes to be mounted at /v1/auth.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/register", h.register)
	r.Post("/login", h.login)
	r.Post("/logout", h.logout)
	r.Post("/forgot-password", h.forgotPassword)
	r.Post("/reset-password", h.resetPassword)
	r.Get("/me", h.me)
	return r
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	// Registration requires an existing API key (to know which tenant to add the user to)
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		// Try extracting from request body if no auth context
		var input struct {
			TenantID string `json:"tenant_id"`
			Email    string `json:"email"`
			Password string `json:"password"`
			Name     string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
		if input.TenantID == "" {
			respond.Unauthorized(w, r, "tenant_id is required for registration")
			return
		}
		user, err := h.svc.Register(r.Context(), input.TenantID, input.Email, input.Password, input.Name)
		if err != nil {
			respond.FromError(w, r, err, "user")
			return
		}
		respond.JSON(w, r, http.StatusCreated, map[string]any{"user": user})
		return
	}

	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	user, err := h.svc.Register(r.Context(), tenantID, input.Email, input.Password, input.Name)
	if err != nil {
		respond.FromError(w, r, err, "user")
		return
	}

	respond.JSON(w, r, http.StatusCreated, map[string]any{"user": user})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	if input.Email == "" || input.Password == "" {
		respond.BadRequest(w, r, "email and password are required")
		return
	}

	sessionToken, user, err := h.svc.Login(r.Context(), input.Email, input.Password)
	if err != nil {
		respond.Unauthorized(w, r, "invalid email or password")
		return
	}

	setSessionCookie(w, r, sessionToken)
	respond.JSON(w, r, http.StatusOK, map[string]any{"user": user})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		// No session cookie — still clear it and return success
		clearSessionCookie(w, r)
		respond.JSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	if err := h.svc.Logout(r.Context(), cookie.Value); err != nil {
		slog.Error("logout error", "error", err)
	}

	clearSessionCookie(w, r)
	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	resetToken, err := h.svc.ForgotPassword(r.Context(), input.Email)
	if err != nil {
		slog.Error("forgot password error", "error", err)
	}

	// In dev mode, log the reset token to the console (no email service in dev)
	if resetToken != "" {
		slog.Info("password reset token generated (dev mode)",
			"email", input.Email,
			"reset_token", resetToken,
			"reset_url", "/reset-password?token="+resetToken,
		)
	}

	// Always return success to prevent email enumeration
	respond.JSON(w, r, http.StatusOK, map[string]string{
		"message": "If an account exists with that email, a password reset link has been sent.",
	})
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	if input.Token == "" || input.Password == "" {
		respond.BadRequest(w, r, "token and password are required")
		return
	}

	if err := h.svc.ResetPassword(r.Context(), input.Token, input.Password); err != nil {
		respond.FromError(w, r, err, "password reset")
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{
		"message": "Password has been reset. Please log in with your new password.",
	})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		respond.Unauthorized(w, r, "not authenticated")
		return
	}

	user, err := h.svc.ValidateSession(r.Context(), cookie.Value)
	if err != nil {
		respond.Unauthorized(w, r, "invalid or expired session")
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"user": user})
}

// setSessionCookie sets the httpOnly session cookie.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := r.TLS != nil || os.Getenv("APP_ENV") == "production"
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
	})
}

// clearSessionCookie removes the session cookie.
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil || os.Getenv("APP_ENV") == "production"
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1, // delete
	})
}
