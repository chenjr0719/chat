package pollers

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/msgbucket"
)

func TestDecodeJSONRow_HappyPath(t *testing.T) {
	got, err := decodeJSONRow(`{"room_id":"r-1","bucket":1700000000000,"msg":"hello"}`)
	require.NoError(t, err)
	assert.Equal(t, "r-1", got["room_id"])
	assert.Equal(t, float64(1700000000000), got["bucket"]) // JSON numbers → float64
	assert.Equal(t, "hello", got["msg"])
}

func TestDecodeJSONRow_EmptyRejected(t *testing.T) {
	_, err := decodeJSONRow("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestDecodeJSONRow_MalformedJSON(t *testing.T) {
	_, err := decodeJSONRow("{not json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// TestCassandraSelectPoller_BucketAtMatchesProductionSizer regresses
// the bucket-math contract: the poller's Sizer must produce identical
// values to the production msgbucket.Sizer the message-worker uses.
// Drift here would make scenario assertions race the producer's
// partition selection.
func TestCassandraSelectPoller_BucketAtMatchesProductionSizer(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	p := NewCassandraSelectPoller(nil, time.Now(), sizer)

	at := time.Date(2026, 5, 31, 12, 34, 56, 0, time.UTC)
	want := sizer.Of(at)
	got := p.BucketAt(at)
	assert.Equal(t, want, got)
	assert.Greater(t, got, int64(0))
}

// TestCassandraSelectPoller_MissingQueryReturnsNil — Phase 4.0
// universal primitive: missing args.query surfaces as slog warn +
// nil, never panics.
func TestCassandraSelectPoller_MissingQueryReturnsNil(t *testing.T) {
	p := NewCassandraSelectPoller(nil, time.Now(), msgbucket.New(24*time.Hour))
	events := p.PollFn("", map[string]any{}, "")()
	assert.Nil(t, events)
}

// TestBuildCassandraParams_DefaultsStartTimeForSingleQuestionMark —
// regresses the convention that the common "WHERE created_at >= ?"
// pattern auto-binds startTime when args.params is absent.
func TestBuildCassandraParams_DefaultsStartTimeForSingleQuestionMark(t *testing.T) {
	startTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := buildCassandraParams(map[string]any{}, startTime, "SELECT * FROM t WHERE created_at >= ?")
	require.Len(t, got, 1)
	assert.Equal(t, startTime, got[0])
}

// TestBuildCassandraParams_NoQuestionMarksNoBind — queries without
// placeholders MUST NOT bind a phantom startTime (would break
// constant queries like "SELECT count(*) FROM t").
func TestBuildCassandraParams_NoQuestionMarksNoBind(t *testing.T) {
	got := buildCassandraParams(map[string]any{}, time.Now(), "SELECT count(*) FROM t")
	assert.Nil(t, got)
}

// TestBuildCassandraParams_ExplicitParamsWin — author-supplied
// args.params overrides the default startTime binding.
func TestBuildCassandraParams_ExplicitParamsWin(t *testing.T) {
	got := buildCassandraParams(map[string]any{"params": []any{"r-1", "bucket-1"}}, time.Now(), "SELECT * FROM t WHERE room_id = ? AND bucket = ?")
	require.Len(t, got, 2)
	assert.Equal(t, "r-1", got[0])
	assert.Equal(t, "bucket-1", got[1])
}

// TestBuildCassandraParams_MultipleQuestionMarksNoDefaultBind — a
// multi-placeholder query without explicit args.params returns nil
// because there's no sensible single-value default. The poller's
// SELECT will then error out at gocql binding, which surfaces as a
// slog warning + empty result — a loud-fail design choice.
func TestBuildCassandraParams_MultipleQuestionMarksNoDefaultBind(t *testing.T) {
	got := buildCassandraParams(map[string]any{}, time.Now(), "SELECT * FROM t WHERE a = ? AND b = ?")
	assert.Nil(t, got, "ambiguous bind context → caller must supply params explicitly")
	_ = strings.Count
}
