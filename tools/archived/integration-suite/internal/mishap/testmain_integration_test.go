//go:build integration

package mishap

import (
	"testing"

	"github.com/hmchangw/chat/pkg/testutil"
)

// TestMain is required by CLAUDE.md §4 for any integration test package.
// The chaos integration tests start their own Toxiproxy container per
// test via t.Cleanup, so no shared-container teardown is needed —
// testutil.RunTests still wraps m.Run for the standard exit-code path.
func TestMain(m *testing.M) { testutil.RunTests(m) }
