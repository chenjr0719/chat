package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errhttp"
	"github.com/hmchangw/chat/pkg/model"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
	"github.com/hmchangw/chat/pkg/subject"
)

// TokenValidator validates an SSO token and returns OIDC claims.
type TokenValidator interface {
	Validate(ctx context.Context, rawToken string) (pkgoidc.Claims, error)
}

type lookupRequest struct {
	SSOToken string `json:"ssoToken" binding:"required"`
}

type devLookupRequest struct {
	Account string `json:"account" binding:"required"`
}

type lookupResponse struct {
	Account        string `json:"account"`
	EmployeeID     string `json:"employeeId"`
	AuthServiceURL string `json:"authServiceUrl"`
	NATSURL        string `json:"natsUrl"`
	SiteID         string `json:"siteId"`
}

// PortalHandler resolves a user's home-site coordinates from the directory.
// Discovery only — the authoritative provisioning gate is auth-service.
type PortalHandler struct {
	validator         TokenValidator
	store             DirectoryStore
	devMode           bool
	devFallbackSiteID string
}

// NewPortalHandler creates a PortalHandler; devMode skips OIDC and enables the
// site fallback. Panics if validator is nil outside devMode — fail at startup.
func NewPortalHandler(validator TokenValidator, store DirectoryStore, devMode bool, devFallbackSiteID string) *PortalHandler {
	if !devMode && validator == nil {
		panic("portal handler: validator is required when devMode is false")
	}
	return &PortalHandler{
		validator:         validator,
		store:             store,
		devMode:           devMode,
		devFallbackSiteID: devFallbackSiteID,
	}
}

// HandleLookup validates the SSO token and resolves the account's home-site coordinates.
func (h *PortalHandler) HandleLookup(c *gin.Context) {
	if h.devMode {
		h.handleDevLookup(c)
		return
	}

	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	var req lookupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("ssoToken is required",
			errcode.WithReason(errcode.AuthMissingFields)))
		return
	}

	claims, err := h.validator.Validate(ctx, req.SSOToken)
	if err != nil {
		if errors.Is(err, pkgoidc.ErrTokenExpired) {
			errhttp.Write(ctx, c, errcode.Unauthenticated("SSO token has expired, please re-login",
				errcode.WithReason(errcode.AuthTokenExpired)))
			return
		}
		errhttp.Write(ctx, c, errcode.Unauthenticated("invalid SSO token",
			errcode.WithReason(errcode.AuthInvalidToken),
			errcode.WithCause(err)))
		return
	}

	account := claims.Account()
	if account == "" {
		errhttp.Write(ctx, c, errcode.Unauthenticated("token missing account claim",
			errcode.WithReason(errcode.AuthInvalidToken)))
		return
	}

	h.resolve(ctx, c, account, false)
}

// handleDevLookup accepts a raw account without OIDC, for local development.
func (h *PortalHandler) handleDevLookup(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	var req devLookupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("account is required",
			errcode.WithReason(errcode.AuthMissingFields)))
		return
	}
	h.resolve(ctx, c, req.Account, true)
}

// resolve maps account → user → site and writes the response. devFallback
// substitutes the dev site so local logins need no per-account seeding.
func (h *PortalHandler) resolve(ctx context.Context, c *gin.Context, account string, devFallback bool) {
	if !subject.IsValidAccountToken(account) {
		errhttp.Write(ctx, c, errcode.BadRequest("account must be a single NATS subject token (no '.', '*', '>' or whitespace)"))
		return
	}
	ctx = errcode.WithLogValues(ctx, "account", account)

	user, err := h.store.FindUserByAccount(ctx, account)
	switch {
	case err == nil:
	case errors.Is(err, ErrUserNotFound) && devFallback:
		user = &model.User{Account: account, SiteID: h.devFallbackSiteID}
	case errors.Is(err, ErrUserNotFound):
		errhttp.Write(ctx, c, errcode.Forbidden("account not provisioned for chat",
			errcode.WithReason(errcode.PortalAccountNotProvisioned)))
		return
	default:
		errhttp.Write(ctx, c, fmt.Errorf("find user by account: %w", err))
		return
	}

	st, err := h.store.FindSiteByID(ctx, user.SiteID)
	if err != nil {
		// Includes ErrSiteNotFound: a user pointing at an unconfigured site
		// is an ops data bug, not a client error.
		errhttp.Write(ctx, c, fmt.Errorf("find site %q: %w", user.SiteID, err))
		return
	}

	c.JSON(http.StatusOK, lookupResponse{
		Account:        user.Account,
		EmployeeID:     user.EmployeeID,
		AuthServiceURL: st.AuthServiceURL,
		NATSURL:        st.NATSURL,
		SiteID:         st.ID,
	})
}

func (h *PortalHandler) HandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
