package main

import (
	"context"
	"errors"

	"github.com/hmchangw/chat/pkg/model"
)

var (
	ErrUserNotFound = errors.New("user not found") // FindUserByAccount: no directory entry
	ErrSiteNotFound = errors.New("site not found") // FindSiteByID: no sites entry
)

//go:generate mockgen -source=store.go -destination=mock_store_test.go -package=main

// site is one row of the ops-owned sites collection: a site's connection URLs.
type site struct {
	ID             string `json:"siteId"         bson:"_id"`
	AuthServiceURL string `json:"authServiceUrl" bson:"authServiceUrl"`
	NATSURL        string `json:"natsUrl"        bson:"natsUrl"`
}

// DirectoryStore reads the account→site directory and site coordinates.
type DirectoryStore interface {
	FindUserByAccount(ctx context.Context, account string) (*model.User, error)
	FindSiteByID(ctx context.Context, siteID string) (*site, error)
	EnsureIndexes(ctx context.Context) error
}
