package triage

// PairScore measures a way of putting crash reports together against
// the causes they really have, over every pair of reports: precision
// is the share of the pairs it puts together that share a cause, and
// recall is the share of the pairs that share a cause that it puts
// together. cause maps each report's ID to its cause. The chapter's
// tests and the evidence harness both measure with it.
func PairScore(reports []Report, cause map[string]string,
	together func(a, b Report) bool) (precision, recall float64) {
	tp, fp, fn := 0, 0, 0
	for i, a := range reports {
		for _, b := range reports[i+1:] {
			same, got := cause[a.ID] == cause[b.ID], together(a, b)
			switch {
			case same && got:
				tp++
			case got:
				fp++
			case same:
				fn++
			}
		}
	}
	return float64(tp) / float64(max(tp+fp, 1)),
		float64(tp) / float64(max(tp+fn, 1))
}
