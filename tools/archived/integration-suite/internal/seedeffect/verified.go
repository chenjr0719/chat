package seedeffect

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/nats-io/nkeys"
)

// MintNATSIdentity mints a fresh NATS nkey + JWT for the given account
// via auth-service /auth (dev-mode bypass). Returns the JWT, the nkey
// seed (private), or an error.
//
// Phase 3.8: moved out of VerifiedEffect.Apply so Sandbox.Setup can call
// it UNCONDITIONALLY for every seeded user. Withholding the network
// identity ("eve_unverified" had no JWT) collapsed the negative-case
// assertion into a connection error instead of an authorization gate —
// not a meaningful test. The "verified" flag now lives only on the
// SeedUser + Mongo profile doc; every seeded user gets a working
// credential so scenarios can exercise real backend logic.
func MintNATSIdentity(ctx context.Context, account, authURL string) (jwt, nkeySeed string, err error) {
	if authURL == "" {
		return "", "", fmt.Errorf("MintNATSIdentity: authURL is required")
	}

	kp, err := nkeys.CreateUser()
	if err != nil {
		return "", "", fmt.Errorf("nkey create: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", "", fmt.Errorf("nkey pub: %w", err)
	}
	seedBytes, err := kp.Seed()
	if err != nil {
		return "", "", fmt.Errorf("nkey seed: %w", err)
	}

	httpClient := resty.New().SetTimeout(5 * time.Second)
	resp, err := httpClient.R().SetContext(ctx).
		SetBody(map[string]string{"account": account, "natsPublicKey": pub}).
		SetHeader("Content-Type", "application/json").
		Post(authURL + "/auth")
	if err != nil {
		return "", "", fmt.Errorf("auth-service POST: %w", err)
	}
	if resp.StatusCode() != 200 {
		return "", "", fmt.Errorf("auth-service /auth: status %d body %s", resp.StatusCode(), string(resp.Body()))
	}
	var body struct {
		NATSJwt string `json:"natsJwt"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		return "", "", fmt.Errorf("auth-service /auth: parse: %w", err)
	}
	if body.NATSJwt == "" {
		return "", "", fmt.Errorf("auth-service /auth: empty natsJwt")
	}
	return body.NATSJwt, string(seedBytes), nil
}

// VerifiedEffect marks the seed user as verified. Phase 3.8 split: the
// NATS credential mint moved out (every seeded user now gets one
// unconditionally via Sandbox.Setup → MintNATSIdentity). What stays on
// this effect is the verified status itself — recorded on
// SeedUser.Verified and persisted into the Mongo user-profile doc so
// future scenarios can target a profile-driven authorization gate
// (rather than the synthetic "no credential" rejection v2 relied on).
type VerifiedEffect struct{}

// Apply per the Effect contract: set u.Verified=true.
func (e *VerifiedEffect) Apply(_ context.Context, u *SeedUser, _ Deps) error {
	u.Verified = true
	return nil
}
