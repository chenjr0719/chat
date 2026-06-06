package verbs

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

// credentialAuthOpt picks the right NATS auth option based on which
// credential fields are populated. See Credential's godoc for the
// user-level vs service-level split.
func credentialAuthOpt(c Credential) (nats.Option, error) {
	if c.CredsFile != "" {
		return nats.UserCredentials(c.CredsFile), nil
	}
	if c.JWT != "" && c.NkeySeed != "" {
		return nats.UserJWTAndSeed(c.JWT, c.NkeySeed), nil
	}
	return nil, fmt.Errorf("missing credential for %q (no JWT+NkeySeed and no CredsFile)", c.Account)
}
