package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewTraceparent generates a fresh W3C traceparent header value:
//
//	00-<32hex trace id>-<16hex span id>-01
func NewTraceparent() string {
	var traceID [16]byte
	var spanID [8]byte
	_, _ = rand.Read(traceID[:])
	_, _ = rand.Read(spanID[:])
	return fmt.Sprintf("00-%s-%s-01",
		hex.EncodeToString(traceID[:]),
		hex.EncodeToString(spanID[:]))
}
