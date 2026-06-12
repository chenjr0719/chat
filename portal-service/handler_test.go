package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errtest"
	"github.com/hmchangw/chat/pkg/model"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
)

// fakeValidator implements TokenValidator for testing.
type fakeValidator struct {
	account string
	name    string
	expired bool
	invalid bool
}

func (f *fakeValidator) Validate(_ context.Context, _ string) (pkgoidc.Claims, error) {
	if f.expired {
		return pkgoidc.Claims{}, pkgoidc.ErrTokenExpired
	}
	if f.invalid {
		return pkgoidc.Claims{}, fmt.Errorf("oidc token verification failed: invalid signature")
	}
	return pkgoidc.Claims{PreferredUsername: f.account, Name: f.name}, nil
}

func setupRouter(t *testing.T, h *PortalHandler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerRoutes(r, h)
	return r
}

func postLookup(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/lookup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

var testSite = &site{
	ID:             "site-a",
	AuthServiceURL: "https://auth.site-a.example.com",
	NATSURL:        "wss://nats.site-a.example.com",
}

func TestHandleLookup_HappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().FindUserByAccount(gomock.Any(), "alice").
		Return(&model.User{ID: "u-alice", Account: "alice", SiteID: "site-a", EmployeeID: "E001"}, nil)
	store.EXPECT().FindSiteByID(gomock.Any(), "site-a").Return(testSite, nil)

	h := NewPortalHandler(&fakeValidator{account: "alice"}, store, false, "site-local")
	w := postLookup(t, setupRouter(t, h), `{"ssoToken":"valid-token"}`)

	require.Equal(t, http.StatusOK, w.Code)
	var resp lookupResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, lookupResponse{
		Account:        "alice",
		EmployeeID:     "E001",
		AuthServiceURL: "https://auth.site-a.example.com",
		NATSURL:        "wss://nats.site-a.example.com",
		SiteID:         "site-a",
	}, resp)
}

func TestHandleLookup_InvalidAccountFormat(t *testing.T) {
	// Same token invariant auth-service enforces at minting — refusing here
	// keeps the portal from blessing an account the next step must reject.
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl) // no EXPECT — must not be touched

	t.Run("prod: dotted claim refused", func(t *testing.T) {
		h := NewPortalHandler(&fakeValidator{account: "john.doe"}, store, false, "site-local")
		w := postLookup(t, setupRouter(t, h), `{"ssoToken":"valid-token"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
	})
	t.Run("dev: wildcard body refused", func(t *testing.T) {
		h := NewPortalHandler(nil, store, true, "site-local")
		w := postLookup(t, setupRouter(t, h), `{"account":"mal*ory"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
	})
}

func TestNewPortalHandler_NilValidatorPanics(t *testing.T) {
	ctrl := gomock.NewController(t)
	assert.Panics(t, func() { NewPortalHandler(nil, NewMockDirectoryStore(ctrl), false, "site-local") })
}

func TestHandleLookup_TokenErrors(t *testing.T) {
	tests := []struct {
		name       string
		validator  *fakeValidator
		wantReason errcode.Reason
	}{
		{"expired token", &fakeValidator{expired: true}, errcode.AuthTokenExpired},
		{"invalid token", &fakeValidator{invalid: true}, errcode.AuthInvalidToken},
		{"blank account claim", &fakeValidator{}, errcode.AuthInvalidToken},
		// name is a user-editable display claim — it must never become the principal.
		{"name-only claim refused", &fakeValidator{name: "Alice W"}, errcode.AuthInvalidToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockDirectoryStore(ctrl) // no EXPECT — must not be touched

			h := NewPortalHandler(tt.validator, store, false, "site-local")
			w := postLookup(t, setupRouter(t, h), `{"ssoToken":"tok"}`)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeUnauthenticated)
			errtest.AssertReason(t, w.Body.Bytes(), tt.wantReason)
		})
	}
}

func TestHandleLookup_MissingBody(t *testing.T) {
	ctrl := gomock.NewController(t)
	h := NewPortalHandler(&fakeValidator{account: "alice"}, NewMockDirectoryStore(ctrl), false, "site-local")
	router := setupRouter(t, h)

	for _, tt := range []struct{ name, body string }{
		{"empty object", `{}`},
		{"empty body", ``},
		{"wrong field", `{"account":"alice"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := postLookup(t, router, tt.body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
			errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
		})
	}
}

func TestHandleLookup_AccountNotProvisioned(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().FindUserByAccount(gomock.Any(), "mallory").
		Return(nil, fmt.Errorf("find user: %w", ErrUserNotFound))

	h := NewPortalHandler(&fakeValidator{account: "mallory"}, store, false, "site-local")
	w := postLookup(t, setupRouter(t, h), `{"ssoToken":"tok"}`)

	assert.Equal(t, http.StatusForbidden, w.Code)
	errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeForbidden)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.PortalAccountNotProvisioned)
}

func TestHandleLookup_StoreErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(store *MockDirectoryStore)
	}{
		{"user query fails", func(store *MockDirectoryStore) {
			store.EXPECT().FindUserByAccount(gomock.Any(), "alice").
				Return(nil, errors.New("mongo down"))
		}},
		{"site missing for known user", func(store *MockDirectoryStore) {
			store.EXPECT().FindUserByAccount(gomock.Any(), "alice").
				Return(&model.User{Account: "alice", SiteID: "site-gone"}, nil)
			store.EXPECT().FindSiteByID(gomock.Any(), "site-gone").
				Return(nil, fmt.Errorf("find site: %w", ErrSiteNotFound))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockDirectoryStore(ctrl)
			tt.setup(store)

			h := NewPortalHandler(&fakeValidator{account: "alice"}, store, false, "site-local")
			w := postLookup(t, setupRouter(t, h), `{"ssoToken":"tok"}`)

			assert.Equal(t, http.StatusInternalServerError, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeInternal)
			assert.NotContains(t, w.Body.String(), "mongo down", "raw cause must not leak")
		})
	}
}

func TestHandleLookup_DevMode(t *testing.T) {
	t.Run("known account resolves normally", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockDirectoryStore(ctrl)
		store.EXPECT().FindUserByAccount(gomock.Any(), "alice").
			Return(&model.User{Account: "alice", SiteID: "site-a", EmployeeID: "E001"}, nil)
		store.EXPECT().FindSiteByID(gomock.Any(), "site-a").Return(testSite, nil)

		h := NewPortalHandler(nil, store, true, "site-local")
		w := postLookup(t, setupRouter(t, h), `{"account":"alice"}`)
		require.Equal(t, http.StatusOK, w.Code)
		var resp lookupResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "site-a", resp.SiteID)
	})

	t.Run("unknown account falls back to the dev site", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockDirectoryStore(ctrl)
		store.EXPECT().FindUserByAccount(gomock.Any(), "newdev").
			Return(nil, fmt.Errorf("find user: %w", ErrUserNotFound))
		store.EXPECT().FindSiteByID(gomock.Any(), "site-local").Return(&site{
			ID: "site-local", AuthServiceURL: "http://localhost:8080", NATSURL: "ws://localhost:9222",
		}, nil)

		h := NewPortalHandler(nil, store, true, "site-local")
		w := postLookup(t, setupRouter(t, h), `{"account":"newdev"}`)
		require.Equal(t, http.StatusOK, w.Code)
		var resp lookupResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, lookupResponse{
			Account: "newdev", EmployeeID: "",
			AuthServiceURL: "http://localhost:8080", NATSURL: "ws://localhost:9222",
			SiteID: "site-local",
		}, resp)
	})

	t.Run("fallback site unseeded is internal", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockDirectoryStore(ctrl)
		store.EXPECT().FindUserByAccount(gomock.Any(), "newdev").
			Return(nil, fmt.Errorf("find user: %w", ErrUserNotFound))
		store.EXPECT().FindSiteByID(gomock.Any(), "site-local").
			Return(nil, fmt.Errorf("find site: %w", ErrSiteNotFound))

		h := NewPortalHandler(nil, store, true, "site-local")
		w := postLookup(t, setupRouter(t, h), `{"account":"newdev"}`)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("missing account is bad request", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		h := NewPortalHandler(nil, NewMockDirectoryStore(ctrl), true, "site-local")
		w := postLookup(t, setupRouter(t, h), `{}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
	})
}

func TestHandleHealth(t *testing.T) {
	ctrl := gomock.NewController(t)
	h := NewPortalHandler(&fakeValidator{}, NewMockDirectoryStore(ctrl), false, "site-local")
	router := setupRouter(t, h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}
