package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/inventory-service/internal/ent"
	entdir "github.com/bengobox/inventory-service/internal/ent/druginteractionrule"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	"github.com/google/uuid"
)

// registerPharmacyInteractionRoutes wires the drug-class resolution + interaction-check
// endpoint pos-api's pharmacy module calls S2S before allowing a prescription to be approved.
// Kept on InventoryExtrasHandler (already DI-wired into both tenant route groups) rather than
// a new handler, to avoid a router.New()/app.go constructor-signature change for one endpoint.
func (h *InventoryExtrasHandler) registerPharmacyInteractionRoutes(r chi.Router) {
	r.Post("/inventory/items/check-interactions", h.CheckDrugInteractions)
}

type resolvedDrugItem struct {
	ItemID           string `json:"item_id,omitempty"`
	SKU              string `json:"sku"`
	DrugClass        string `json:"drug_class,omitempty"`
	ActiveIngredient string `json:"active_ingredient,omitempty"`
	GenericName      string `json:"generic_name,omitempty"`
}

type drugInteractionFinding struct {
	ClassA                 string `json:"class_a"`
	ClassB                 string `json:"class_b"`
	SKUA                   string `json:"sku_a"`
	SKUB                   string `json:"sku_b"`
	Severity               string `json:"severity"`
	Description            string `json:"description,omitempty"`
	ClinicalRecommendation string `json:"clinical_recommendation,omitempty"`
}

type allergyMatch struct {
	SKU              string `json:"sku"`
	ActiveIngredient string `json:"active_ingredient,omitempty"`
	AllergyFlag      string `json:"allergy_flag"`
}

type checkInteractionsRequest struct {
	SKUs         []string `json:"skus,omitempty"`
	ItemIDs      []string `json:"item_ids,omitempty"`
	AllergyFlags []string `json:"allergy_flags,omitempty"`
}

type checkInteractionsResponse struct {
	Resolved       []resolvedDrugItem       `json:"resolved"`
	Interactions   []drugInteractionFinding `json:"interactions"`
	AllergyMatches []allergyMatch           `json:"allergy_matches"`
}

// CheckDrugInteractions handles POST /inventory/items/check-interactions. It resolves each
// SKU's drug_class/active_ingredient, cross-joins every distinct class pair present against
// DrugInteractionRule (tenant override ∪ platform-global), and flags any SKU whose
// active_ingredient/drug_class matches a caller-supplied allergy flag. inventory-api owns both
// Item classification and DrugInteractionRule, so the match happens here rather than pos-api
// replicating either table (service-data-ownership rule).
func (h *InventoryExtrasHandler) CheckDrugInteractions(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req checkInteractionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if len(req.SKUs) == 0 && len(req.ItemIDs) == 0 {
		writeJSON(w, http.StatusOK, checkInteractionsResponse{
			Resolved:       []resolvedDrugItem{},
			Interactions:   []drugInteractionFinding{},
			AllergyMatches: []allergyMatch{},
		})
		return
	}

	idPredicates := make([]predicate.Item, 0, 2)
	if len(req.SKUs) > 0 {
		idPredicates = append(idPredicates, entitem.SkuIn(req.SKUs...))
	}
	if len(req.ItemIDs) > 0 {
		ids := make([]uuid.UUID, 0, len(req.ItemIDs))
		for _, s := range req.ItemIDs {
			if id, err := uuid.Parse(s); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			idPredicates = append(idPredicates, entitem.IDIn(ids...))
		}
	}
	if len(idPredicates) == 0 {
		writeJSON(w, http.StatusOK, checkInteractionsResponse{
			Resolved:       []resolvedDrugItem{},
			Interactions:   []drugInteractionFinding{},
			AllergyMatches: []allergyMatch{},
		})
		return
	}

	items, err := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.Or(idPredicates...)).
		All(r.Context())
	if err != nil {
		h.log.Error("check drug interactions: item lookup failed")
		writeError(w, http.StatusInternalServerError, "LOOKUP_FAILED", "Failed to resolve items")
		return
	}

	resolved := make([]resolvedDrugItem, 0, len(items))
	for _, it := range items {
		resolved = append(resolved, resolvedDrugItem{
			ItemID:           it.ID.String(),
			SKU:              it.Sku,
			DrugClass:        it.DrugClass,
			ActiveIngredient: it.ActiveIngredient,
			GenericName:      it.GenericName,
		})
	}

	// Load every active rule visible to this tenant (own overrides ∪ platform-global) once;
	// the pair-count here is always small (a handful of dispensed lines), so an in-memory
	// join is simpler and cheaper than one query per pair.
	rules, err := h.orm.DrugInteractionRule.Query().
		Where(
			entdir.IsActive(true),
			entdir.Or(
				entdir.TenantID(tenantID),
				entdir.And(entdir.IsGlobal(true), entdir.TenantID(uuid.Nil)),
			),
		).
		All(r.Context())
	if err != nil {
		h.log.Error("check drug interactions: rule lookup failed")
		writeError(w, http.StatusInternalServerError, "LOOKUP_FAILED", "Failed to resolve interaction rules")
		return
	}
	ruleMap := make(map[string]*ent.DrugInteractionRule, len(rules))
	for _, ru := range rules {
		ruleMap[pairKey(ru.ClassA, ru.ClassB)] = ru
	}

	interactions := make([]drugInteractionFinding, 0)
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			if a.DrugClass == "" || b.DrugClass == "" || a.DrugClass == b.DrugClass {
				continue
			}
			ru, ok := ruleMap[pairKey(a.DrugClass, b.DrugClass)]
			if !ok {
				continue
			}
			interactions = append(interactions, drugInteractionFinding{
				ClassA:                 ru.ClassA,
				ClassB:                 ru.ClassB,
				SKUA:                   a.Sku,
				SKUB:                   b.Sku,
				Severity:               string(ru.Severity),
				Description:            ru.Description,
				ClinicalRecommendation: ru.ClinicalRecommendation,
			})
		}
	}

	allergyMatches := make([]allergyMatch, 0)
	if len(req.AllergyFlags) > 0 {
		flags := make(map[string]string, len(req.AllergyFlags)) // lowercased -> original
		for _, f := range req.AllergyFlags {
			flags[strings.ToLower(strings.TrimSpace(f))] = f
		}
		for _, it := range items {
			// Match against drug_class, active_ingredient, AND generic_name — a patient's
			// declared allergy is rarely the drug class (e.g. "penicillin" is an active
			// ingredient/generic name, not the class "antibiotic"), so checking drug_class
			// alone under-detects real allergy conflicts.
			var matchedFlag string
			for _, candidate := range [...]string{it.DrugClass, it.ActiveIngredient, it.GenericName} {
				if candidate == "" {
					continue
				}
				if orig, ok := flags[strings.ToLower(candidate)]; ok {
					matchedFlag = orig
					break
				}
			}
			if matchedFlag == "" {
				continue
			}
			allergyMatches = append(allergyMatches, allergyMatch{
				SKU:              it.Sku,
				ActiveIngredient: it.ActiveIngredient,
				AllergyFlag:      matchedFlag,
			})
		}
	}

	writeJSON(w, http.StatusOK, checkInteractionsResponse{
		Resolved:       resolved,
		Interactions:   interactions,
		AllergyMatches: allergyMatches,
	})
}

// pairKey builds a stable, order-independent lookup key for a class pair.
func pairKey(a, b string) string {
	if strings.Compare(a, b) > 0 {
		a, b = b, a
	}
	return a + "|" + b
}
