package main

// This file is the faceted counting shared by the connection and usage
// queries: a filter group lists and counts its options over the records
// that pass every other condition, so choosing in one group narrows the
// others but not itself.

// facetAll is the facetMiss of a record that passes every condition;
// facetNone that of a record failing two or more, which counts nowhere.
const (
	facetAll  = -1
	facetNone = -2
)

// facetMiss evaluates the n conditions of a record, pass(i) reporting
// whether it passes condition i: facetAll when it passes them all, the
// index of the only one it fails, else facetNone. A record counts toward
// the group of condition i when facetCounts(miss, i).
func facetMiss(n int, pass func(i int) bool) int {
	miss := facetAll
	for i := range n {
		if pass(i) {
			continue
		}
		if miss != facetAll {
			return facetNone
		}
		miss = i
	}
	return miss
}

// facetCounts reports whether a record of facetMiss miss counts toward the
// options of condition i: it passes every condition but, at most, i. A
// group without a condition of its own passes i < 0.
func facetCounts(miss, i int) bool {
	return miss == facetAll || i >= 0 && miss == i
}
