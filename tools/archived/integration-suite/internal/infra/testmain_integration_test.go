//go:build integration

package infra

import (
	"testing"

	"github.com/hmchangw/chat/pkg/testutil"
)

// TestMain is required by CLAUDE.md §4 for any integration test package.
// internal/infra owns its own containers per test via t.Cleanup, so no
// shared-container teardown is needed — testutil.RunTests still wraps
// m.Run for the standard exit-code path.
func TestMain(m *testing.M) { testutil.RunTests(m) }
