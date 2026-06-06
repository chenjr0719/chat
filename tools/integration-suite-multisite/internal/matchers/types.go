// Package matchers holds typed predicate functions used inside a
// scenario's reads. Each matcher takes observed and expected values
// and returns whether they "match" plus a human-readable reason on
// mismatch.
package matchers

// Result is the outcome of a single match call.
type Result struct {
	Matched bool
	Reason  string // populated on mismatch
}

// Matcher is the interface every matcher implements.
type Matcher interface {
	Match(observed, expected any) Result
}
