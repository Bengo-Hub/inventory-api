package items

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
)

// MaxImageMatchNames caps one match request. The bulk image dialog sends file names in pages
// of this size, so a 1,000 photo folder is a handful of requests.
const MaxImageMatchNames = 100

// Match kinds, best first. A file named after a SKU or barcode is certain; an exact name is
// near certain; a similar name needs a person to confirm it.
const (
	ImageMatchSKU     = "sku"
	ImageMatchBarcode = "barcode"
	ImageMatchExact   = "exact"
	ImageMatchSimilar = "similar"
	ImageMatchNone    = "none"
)

// imageMatchCandidates is how many items one file name is compared against: the best match
// plus a few alternatives the person can pick from.
const imageMatchCandidates = 4

// ImageMatchCandidate is one item a file could belong to.
type ImageMatchCandidate struct {
	ItemID   uuid.UUID `json:"item_id"`
	SKU      string    `json:"sku"`
	Name     string    `json:"name"`
	IsActive bool      `json:"is_active"`
	HasImage bool      `json:"has_image"`
	ImageURL string    `json:"image_url,omitempty"`
	Score    float64   `json:"score"`
}

// ImageMatchResult is the suggested item for one uploaded file name.
type ImageMatchResult struct {
	FileName     string                `json:"file_name"`
	Kind         string                `json:"kind"`
	Match        *ImageMatchCandidate  `json:"match,omitempty"`
	Alternatives []ImageMatchCandidate `json:"alternatives"`
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// imageQuery turns a file name into the text compared with item names:
// "Bread_tins_600ml.JPG" becomes "bread tins 600ml".
func imageQuery(fileName string) string {
	return normalizedName(fileStem(fileName))
}

// fileStem is the file name without folders or extension, as typed, for SKU and barcode lookups.
func fileStem(fileName string) string {
	base := path.Base(strings.ReplaceAll(fileName, `\`, "/"))
	return strings.TrimSpace(strings.TrimSuffix(base, path.Ext(base)))
}

// MatchItemImages suggests the item each file name belongs to. Order of preference: a stem that
// is the item's SKU, then its barcode (one query for the whole page), then the most similar names
// through the trigram index on items.name (one indexed query per remaining file). Inactive items
// are offered too so a photo can be attached before an item goes back on sale.
func (s *Service) MatchItemImages(ctx context.Context, tenantID uuid.UUID, fileNames []string) ([]ImageMatchResult, error) {
	if len(fileNames) > MaxImageMatchNames {
		return nil, fmt.Errorf("items: at most %d file names per request", MaxImageMatchNames)
	}
	stems := make([]string, 0, len(fileNames))
	for _, f := range fileNames {
		if st := fileStem(f); st != "" {
			stems = append(stems, st)
		}
	}
	byCode := map[string]*ent.Item{}
	if len(stems) > 0 {
		coded, err := s.client.Item.Query().
			Where(item.TenantID(tenantID), item.Or(item.SkuIn(stems...), item.BarcodeIn(stems...))).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("items: match by sku or barcode: %w", err)
		}
		for _, it := range coded {
			byCode["sku:"+strings.ToLower(it.Sku)] = it
			if it.Barcode != "" {
				byCode["barcode:"+strings.ToLower(it.Barcode)] = it
			}
		}
	}

	out := make([]ImageMatchResult, 0, len(fileNames))
	for _, f := range fileNames {
		res := ImageMatchResult{FileName: f, Kind: ImageMatchNone, Alternatives: []ImageMatchCandidate{}}
		stem := strings.ToLower(fileStem(f))
		if it, ok := byCode["sku:"+stem]; ok {
			c := s.imageCandidate(it, 1)
			res.Kind, res.Match = ImageMatchSKU, &c
			out = append(out, res)
			continue
		}
		if it, ok := byCode["barcode:"+stem]; ok {
			c := s.imageCandidate(it, 1)
			res.Kind, res.Match = ImageMatchBarcode, &c
			out = append(out, res)
			continue
		}
		q := imageQuery(f)
		if q == "" {
			out = append(out, res)
			continue
		}
		similar, err := s.similarItems(ctx, tenantID, q)
		if err != nil {
			return nil, err
		}
		for i, it := range similar {
			c := s.imageCandidate(it, trigramSimilarity(q, it.Name))
			if i == 0 {
				res.Match = &c
				res.Kind = ImageMatchSimilar
				if normalizedName(it.Name) == q {
					res.Kind = ImageMatchExact
				}
				continue
			}
			res.Alternatives = append(res.Alternatives, c)
		}
		out = append(out, res)
	}
	return out, nil
}

// similarItems returns the tenant's items whose names are trigram-similar to q, most similar
// first. `name % q` is answered by the item_name GIN trigram index (pg_trgm's default 0.3
// threshold); the ORDER BY ranks only those few rows.
func (s *Service) similarItems(ctx context.Context, tenantID uuid.UUID, q string) ([]*ent.Item, error) {
	found, err := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			func(sel *entsql.Selector) {
				sel.Where(entsql.P(func(b *entsql.Builder) {
					b.Ident(sel.C(item.FieldName)).WriteString(" % ").Arg(q)
				}))
			},
		).
		Order(func(sel *entsql.Selector) {
			// ExprFunc continues the query's argument numbering; OrderExprFunc would restart at $1.
			sel.OrderExpr(entsql.ExprFunc(func(b *entsql.Builder) {
				b.WriteString("similarity(").Ident(sel.C(item.FieldName)).WriteString(", ").Arg(q).WriteString(") DESC")
			}))
		}).
		Limit(imageMatchCandidates).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: match by name: %w", err)
	}
	return found, nil
}

func (s *Service) imageCandidate(it *ent.Item, score float64) ImageMatchCandidate {
	c := ImageMatchCandidate{
		ItemID:   it.ID,
		SKU:      it.Sku,
		Name:     it.Name,
		IsActive: it.IsActive,
		HasImage: it.ImageURL != "",
		Score:    score,
	}
	if c.HasImage {
		c.ImageURL = s.mediaURL(it.ImageURL)
	}
	return c
}

// normalizedName lowercases and turns every run of non letters or digits into one space.
func normalizedName(name string) string {
	return strings.TrimSpace(nonAlnum.ReplaceAllString(strings.ToLower(name), " "))
}

// trigramSimilarity reproduces pg_trgm's similarity(): each word is padded with two spaces in
// front and one behind, cut into three-letter pieces, and the score is shared pieces over all
// distinct pieces. It gives the dialog the same number the database ranked by.
func trigramSimilarity(a, b string) float64 {
	ta, tb := trigrams(a), trigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	shared := 0
	for t := range ta {
		if _, ok := tb[t]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(ta)+len(tb)-shared)
}

func trigrams(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for w := range strings.FieldsSeq(normalizedName(s)) {
		p := "  " + w + " "
		for i := 0; i+3 <= len(p); i++ {
			out[p[i:i+3]] = struct{}{}
		}
	}
	return out
}
