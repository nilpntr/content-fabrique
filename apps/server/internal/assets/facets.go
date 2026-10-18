package assets

import (
	"context"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/taxonomy"
)

// The clip labels (package taxonomy) as the library sees them: the filters of GET /v1/assets and the counts of GET /v1/assets/facets.

// ValueCount is one value of a facet and how many clips have it.
type ValueCount struct {
	Value string
	Label string
	Count int
}

// FacetCounts is one facet with the values the clips have.
type FacetCounts struct {
	Facet  string
	Label  string
	Values []ValueCount
}

// Facets counts the labels of the clips of a purpose that match the search words. The filter narrows the counts like a list request,
// except that each facet's own filter is left out of its counts: with body=back, the body counts still list every body part (so the
// creator can switch), and the angle counts are those of the back clips. Only values with a count are returned, and only facets with
// a value, in the vocabulary's order.
func (s *Service) Facets(ctx context.Context, who Actor, purpose, query string, filter FacetFilter) ([]FacetCounts, *Error) {
	if purpose == "" {
		purpose = store.PurposeFootage
	}
	if e := checkSearch(purpose, query); e != nil {
		return nil, e
	}
	want, e := filter.stored(s.tax)
	if e != nil {
		return nil, e
	}
	rows, err := s.store.ListAssetFacets(ctx, who.Workspace, purpose, store.SearchWords(query))
	if err != nil {
		return nil, s.internal(ctx, "list asset facets", err)
	}
	return CountFacets(s.tax, rows, want), nil
}

// CountFacets counts the labels of rows (each the stored labels of one clip) under the filter want (stored labels), leaving each
// facet's own filter out of its counts.
func CountFacets(tax *taxonomy.Taxonomy, rows [][]string, want []string) []FacetCounts {
	filters := map[string][]string{} // facet -> the stored labels it filters on
	for _, w := range want {
		if r, ok := taxonomy.ParseRef(w); ok {
			filters[r.Facet] = append(filters[r.Facet], w)
		}
	}
	counts := map[taxonomy.Ref]int{}
	for _, row := range rows {
		set := tax.Normalize(row)
		labels := set.Strings()
		for _, fc := range tax.Facets {
			// The row counts for this facet when it passes every other facet's filter.
			pass := true
			for f, list := range filters {
				if f == fc.ID {
					continue
				}
				for _, w := range list {
					if !slices.Contains(labels, w) {
						pass = false
					}
				}
			}
			if !pass {
				continue
			}
			for _, r := range set {
				if r.Facet == fc.ID {
					counts[r]++
				}
			}
		}
	}
	var out []FacetCounts
	for _, fc := range tax.Facets {
		f := FacetCounts{Facet: fc.ID, Label: fc.Label}
		for _, v := range fc.Values {
			if n := counts[taxonomy.Ref{Facet: fc.ID, Value: v.ID}]; n > 0 {
				f.Values = append(f.Values, ValueCount{Value: v.ID, Label: v.Label, Count: n})
			}
		}
		if len(f.Values) > 0 {
			out = append(out, f)
		}
	}
	return out
}
