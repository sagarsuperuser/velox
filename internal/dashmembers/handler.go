// Package dashmembers is the HTTP layer for managing a tenant's team —
// listing active members, issuing invitations, revoking pending invites,
// and removing members. All routes are session-scoped and tenant-scoped:
// the session.Middleware populates the auth context, and the handler
// reads tenant+user ids from it rather than trusting query params.
//
// The invite email is delivered by dashauth.Handler.SendInvite so the
// accept-URL template and tenant-name lookup stay in one place.
package dashmembers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/user"
)

// InviteDispatcher sends the invite email once the service has minted a
// token. Implemented by *dashauth.Handler so the accept-URL template and
// tenant-name lookup don't need to be duplicated here.
type InviteDispatcher interface {
	SendInvite(ctx context.Context, tenantID, toEmail, inviterEmail, rawToken string) error
}

type Handler struct {
	users    *user.Service
	inviter  InviteDispatcher
	now      func() time.Time
}

func NewHandler(users *user.Service, inviter InviteDispatcher) *Handler {
	return &Handler{
		users:   users,
		inviter: inviter,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// Routes returns the session-scoped members routes. Mount under /v1/members
// inside the session.Middleware group.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Post("/invite", h.invite)
	r.Delete("/invitations/{id}", h.revokeInvite)
	r.Delete("/{userID}", h.removeMember)
	return r
}

// --- responses -------------------------------------------------------------

type memberView struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	JoinedAt    string `json:"joined_at"`
}

type invitationView struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	Role           string `json:"role"`
	Status         string `json:"status"` // pending | accepted | revoked | expired
	InvitedByEmail string `json:"invited_by_email,omitempty"`
	ExpiresAt      string `json:"expires_at"`
	CreatedAt      string `json:"created_at"`
	AcceptedAt     string `json:"accepted_at,omitempty"`
	RevokedAt      string `json:"revoked_at,omitempty"`
}

type listResp struct {
	Members     []memberView     `json:"members"`
	Invitations []invitationView `json:"invitations"`
}

// --- handlers --------------------------------------------------------------

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "no tenant in session")
		return
	}

	members, err := h.users.ListMembers(r.Context(), tenantID)
	if err != nil {
		slog.Error("dashmembers: list members failed", "err", err, "tenant_id", tenantID)
		respond.InternalError(w, r)
		return
	}
	invitations, err := h.users.ListInvitations(r.Context(), tenantID)
	if err != nil {
		slog.Error("dashmembers: list invitations failed", "err", err, "tenant_id", tenantID)
		respond.InternalError(w, r)
		return
	}

	now := h.now()
	resp := listResp{
		Members:     make([]memberView, 0, len(members)),
		Invitations: make([]invitationView, 0, len(invitations)),
	}
	for _, m := range members {
		resp.Members = append(resp.Members, memberView{
			UserID:      m.UserID,
			Email:       m.Email,
			DisplayName: m.DisplayName,
			Role:        m.Role,
			JoinedAt:    m.JoinedAt.Format(time.RFC3339),
		})
	}
	for _, inv := range invitations {
		v := invitationView{
			ID:             inv.ID,
			Email:          inv.Email,
			Role:           inv.Role,
			Status:         inv.Status(now),
			InvitedByEmail: inv.InvitedByEmail,
			ExpiresAt:      inv.ExpiresAt.Format(time.RFC3339),
			CreatedAt:      inv.CreatedAt.Format(time.RFC3339),
		}
		if inv.AcceptedAt != nil {
			v.AcceptedAt = inv.AcceptedAt.Format(time.RFC3339)
		}
		if inv.RevokedAt != nil {
			v.RevokedAt = inv.RevokedAt.Format(time.RFC3339)
		}
		resp.Invitations = append(resp.Invitations, v)
	}
	respond.JSON(w, r, http.StatusOK, resp)
}

type inviteReq struct {
	Email string `json:"email"`
}

func (h *Handler) invite(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	actorID := auth.UserID(r.Context())
	if tenantID == "" || actorID == "" {
		respond.Unauthorized(w, r, "no session")
		return
	}

	var req inviteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	email := strings.TrimSpace(req.Email)
	if email == "" {
		respond.ValidationField(w, r, "email", "email is required")
		return
	}

	inv, rawToken, err := h.users.Invite(r.Context(), tenantID, actorID, email)
	if err != nil {
		switch {
		case errors.Is(err, user.ErrAlreadyMember):
			respond.ConflictField(w, r, "email", "this email is already a member of the workspace")
		case errors.Is(err, user.ErrPendingInvite):
			respond.ConflictField(w, r, "email", "a pending invitation already exists for this email")
		default:
			slog.Error("dashmembers: invite failed", "err", err, "tenant_id", tenantID)
			respond.InternalError(w, r)
		}
		return
	}

	// Fetch the inviter's email for the email body. A lookup failure is
	// non-fatal — the invite row is already persisted and the email can
	// still be sent with a blank inviter.
	inviterEmail := ""
	if u, err := h.users.GetByID(r.Context(), actorID); err == nil {
		inviterEmail = u.Email
	}

	if err := h.inviter.SendInvite(r.Context(), tenantID, email, inviterEmail, rawToken); err != nil {
		// Don't fail the request — the row exists and can be resent later.
		slog.Error("dashmembers: enqueue invite email failed", "err", err, "invitation_id", inv.ID)
	}

	respond.JSON(w, r, http.StatusCreated, invitationView{
		ID:             inv.ID,
		Email:          inv.Email,
		Role:           inv.Role,
		Status:         "pending",
		InvitedByEmail: inviterEmail,
		ExpiresAt:      inv.ExpiresAt.Format(time.RFC3339),
		CreatedAt:      inv.CreatedAt.Format(time.RFC3339),
	})
}

func (h *Handler) revokeInvite(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "no tenant in session")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		respond.BadRequest(w, r, "invitation id required")
		return
	}

	if err := h.users.RevokeInvitation(r.Context(), tenantID, id); err != nil {
		switch {
		case errors.Is(err, user.ErrInvitationInvalid), errors.Is(err, user.ErrInvitationConsumed):
			respond.NotFound(w, r, "invitation not found or already consumed")
		default:
			slog.Error("dashmembers: revoke invitation failed", "err", err, "invitation_id", id)
			respond.InternalError(w, r)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	actorID := auth.UserID(r.Context())
	if tenantID == "" || actorID == "" {
		respond.Unauthorized(w, r, "no session")
		return
	}
	targetID := chi.URLParam(r, "userID")
	if targetID == "" {
		respond.BadRequest(w, r, "user id required")
		return
	}

	if err := h.users.RemoveMember(r.Context(), tenantID, actorID, targetID); err != nil {
		switch {
		case errors.Is(err, user.ErrSelfRemoval):
			respond.Conflict(w, r, "you cannot remove yourself from the workspace — transfer ownership first")
		case errors.Is(err, user.ErrLastOwner):
			respond.Conflict(w, r, "cannot remove the last owner of the workspace")
		case errors.Is(err, user.ErrNotFound):
			respond.NotFound(w, r, "member not found")
		default:
			slog.Error("dashmembers: remove member failed", "err", err, "target_user_id", targetID)
			respond.InternalError(w, r)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
