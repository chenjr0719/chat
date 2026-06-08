package pollers

import (
	"encoding/json"
	"fmt"
)

// decodeJSONRow unmarshals a single Cassandra `SELECT JSON *` row into
// a map[string]any. Pulled out as a free function so tests can exercise
// the row-shape contract without standing up a real Session.
//
// `SELECT JSON *` returns one row per result with a single column
// containing the row as a JSON object. Empty strings come back as
// `null` per gocql; the caller should treat nil as "no rows".
func decodeJSONRow(s string) (map[string]any, error) {
	if s == "" {
		return nil, fmt.Errorf("empty JSON row")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("unmarshal JSON row: %w", err)
	}
	return out, nil
}
