package items

import (
	"strings"

	"github.com/google/uuid"
)

// ReportFilter narrows the stock reports (Valuation, Deadstock, Fast-Moving) to the items a user
// asked about. Empty fields filter nothing. Offset and Limit page the listed lines; totals always
// cover every matching item.
type ReportFilter struct {
	CategoryIDs []uuid.UUID
	BrandIDs    []uuid.UUID
	Search      string // matched against item name and SKU, case-insensitive
	Offset      int
	Limit       int
}

// maxReportLimit caps one page of report lines.
const maxReportLimit = 500

// page returns the offset and limit to use, with def when no limit was asked for.
func (f ReportFilter) page(def int) (offset, limit int) {
	offset, limit = f.Offset, f.Limit
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = def
	}
	if limit > maxReportLimit {
		limit = maxReportLimit
	}
	return offset, limit
}

// searchPattern is the ILIKE pattern for Search, or nil when there is no search. % and _ typed by
// the user are matched literally.
func (f ReportFilter) searchPattern() any {
	s := strings.TrimSpace(f.Search)
	if s == "" {
		return nil
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// uuidParam renders ids for a uuid[] SQL parameter: NULL when empty, so "($n::uuid[] IS NULL OR
// col = ANY($n))" keeps every row.
func uuidParam(ids []uuid.UUID) any {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// itemFilterSQL is the WHERE fragment applied to items aliased i. The parameter numbers are passed
// in so each report keeps its own numbering.
func itemFilterSQL(catParam, brandParam, searchParam string) string {
	return `(` + catParam + `::uuid[] IS NULL OR i.category_id = ANY(` + catParam + `::uuid[]))
  AND (` + brandParam + `::uuid[] IS NULL OR i.brand_id = ANY(` + brandParam + `::uuid[]))
  AND (` + searchParam + `::text IS NULL OR i.name ILIKE ` + searchParam + ` OR i.sku ILIKE ` + searchParam + `)`
}
