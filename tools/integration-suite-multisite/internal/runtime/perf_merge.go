package runtime

// Merge combines two PerformanceStores deterministically per §5.4.
// - latest: later RanAt wins.
// - best: more-favorable verdict; tie → earlier RanAt.
// - worst: less-favorable verdict; tie → later RanAt.
// - One-sided case: take that side whole.
func Merge(a, b *PerformanceStore) *PerformanceStore {
	out := NewPerformanceStore()
	if a == nil {
		a = NewPerformanceStore()
	}
	if b == nil {
		b = NewPerformanceStore()
	}
	// Prefer the more recent LastRunAt for the merged store.
	if a.LastRunAt >= b.LastRunAt {
		out.LastRunAt = a.LastRunAt
	} else {
		out.LastRunAt = b.LastRunAt
	}
	for id, ea := range a.Cases {
		if eb, ok := b.Cases[id]; ok {
			out.Cases[id] = mergeEntry(ea, eb)
		} else {
			out.Cases[id] = ea
		}
	}
	for id, eb := range b.Cases {
		if _, ok := a.Cases[id]; !ok {
			out.Cases[id] = eb
		}
	}
	return out
}

func mergeEntry(a, b *CaseEntry) *CaseEntry {
	out := &CaseEntry{}
	// Latest: later RanAt wins.
	switch {
	case a.Latest != nil && b.Latest != nil:
		if a.Latest.RanAt >= b.Latest.RanAt {
			out.Latest = a.Latest
		} else {
			out.Latest = b.Latest
		}
	case a.Latest != nil:
		out.Latest = a.Latest
	default:
		out.Latest = b.Latest
	}
	// Best: more favorable; tie → earlier RanAt.
	out.Best = pickBest(a.Best, b.Best)
	// Worst: less favorable; tie → later RanAt.
	out.Worst = pickWorst(a.Worst, b.Worst)
	return out
}

func pickBest(a, b *CaseSummary) *CaseSummary {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if verdictBetter(a.Verdict, b.Verdict) {
		return a
	}
	if verdictBetter(b.Verdict, a.Verdict) {
		return b
	}
	// tie → earlier RanAt
	if a.RanAt <= b.RanAt {
		return a
	}
	return b
}

func pickWorst(a, b *CaseSummary) *CaseSummary {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if verdictBetter(b.Verdict, a.Verdict) {
		return a
	}
	if verdictBetter(a.Verdict, b.Verdict) {
		return b
	}
	// tie → later RanAt
	if a.RanAt >= b.RanAt {
		return a
	}
	return b
}
