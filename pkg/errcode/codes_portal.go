package errcode

// Reasons emitted by portal-service and the auth-service minting gate.
const (
	// PortalAccountNotProvisioned: authenticated but not in the directory (portal lookup / auth minting gate).
	PortalAccountNotProvisioned Reason = "account_not_provisioned"
)
