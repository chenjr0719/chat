package main

import "context"

//go:generate mockgen -source=store.go -destination=mock_store_test.go -package=main

// ProvisionStore answers whether an account is provisioned for this site.
type ProvisionStore interface {
	// AccountProvisioned reports whether {account, siteID} exists in users.
	// Compound on purpose: the collection also holds other sites' users.
	AccountProvisioned(ctx context.Context, account, siteID string) (bool, error)
}
