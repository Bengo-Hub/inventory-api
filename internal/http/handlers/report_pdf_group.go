package handlers

import "sort"

// groupByCategory buckets rows by a category-name key, preserving first-seen order for named
// categories (sorted alphabetically) with an "Uncategorized" bucket always sorted last. Shared by
// ProductsExportPDF and StockExportPDF's optional ?group_by=category grouping.
func groupByCategory[T any](rows []T, categoryName func(T) string) (order []string, groups map[string][]T) {
	groups = make(map[string][]T)
	seen := make(map[string]bool)
	for _, row := range rows {
		name := categoryName(row)
		if name == "" {
			name = "Uncategorized"
		}
		if !seen[name] {
			seen[name] = true
			order = append(order, name)
		}
		groups[name] = append(groups[name], row)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i] == "Uncategorized" {
			return false
		}
		if order[j] == "Uncategorized" {
			return true
		}
		return order[i] < order[j]
	})
	return order, groups
}
