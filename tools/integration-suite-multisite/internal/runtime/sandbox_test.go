package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSandboxDeps_PerSiteMongoMap(t *testing.T) {
	// Compile-time: SandboxDeps must expose a per-site Mongo map and
	// a per-site AuthURL map; the single fields are gone.
	var d SandboxDeps
	_ = d.MongoBySite   // map[string]*mongo.Database
	_ = d.AuthURLBySite // map[string]string

	// Cassandra is shared, stays single:
	_ = d.Cassandra

	assert.True(t, true, "compile-time check only")
}
