package seedeffect

// RegisterBuiltins wires the Phase 3 production effects into the
// supplied Registry. Called once at runner startup (next to verb /
// matcher / reader / mishap registrations).
//
// Today only `verified` is registered. Adding a new effect:
//  1. Create catalogs/seed-effects/<flag>.yaml declaring the effect.
//  2. Implement <Flag>Effect with Apply per the Effect contract.
//  3. Register it here.
//  4. Add tests covering the new effect's mutation.
func RegisterBuiltins(r *Registry) {
	r.Register("verified", &VerifiedEffect{})
}
