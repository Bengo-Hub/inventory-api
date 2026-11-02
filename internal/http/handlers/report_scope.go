package handlers

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
)

// reportScope is where a stock report looks: the warehouses it covers (nil means every warehouse)
// and a label naming them for the page and the exported document ("" for all locations).
type reportScope struct {
	warehouseIDs []uuid.UUID
	label        string
}

// reportScopeError is a request the scope cannot honour, answered with status and code.
type reportScopeError struct {
	status  int
	code    string
	message string
}

// reportOutlet is the outlet a stock report is scoped to: the caller's own outlet for branch staff
// (OutletContext pins it), else ?outlet_id=, else the top-nav X-Outlet-ID; nil means all outlets.
func reportOutlet(r *http.Request) *uuid.UUID {
	raw := invmiddleware.GetOutletID(r.Context())
	if !invmiddleware.IsRestrictedUser(r.Context()) {
		if q := r.URL.Query().Get("outlet_id"); q != "" {
			raw = q
		}
	}
	if id, err := uuid.Parse(raw); err == nil && id != uuid.Nil {
		return &id
	}
	return nil
}

// resolveReportScope scopes the Deadstock and Fast-Moving reports. ?warehouse_id= picks one
// location, and must be the tenant's and, when an outlet is selected, that outlet's. Otherwise the
// selected outlet covers the warehouses it owns (not the shared outlet-less ones, which would count
// a central store's stock at every outlet); an outlet owning none yields an empty, non-nil scope.
// No outlet and no warehouse means every warehouse.
func (h *InventoryHandler) resolveReportScope(r *http.Request, tenantID uuid.UUID) (reportScope, *reportScopeError) {
	ctx := r.Context()
	outletID := reportOutlet(r)
	if wid := r.URL.Query().Get("warehouse_id"); wid != "" {
		id, err := uuid.Parse(wid)
		if err != nil {
			return reportScope{}, &reportScopeError{http.StatusBadRequest, "INVALID_WAREHOUSE_ID", "Invalid warehouse_id"}
		}
		q := h.orm.Warehouse.Query().Where(entwarehouse.TenantID(tenantID), entwarehouse.ID(id))
		if outletID != nil {
			q = q.Where(entwarehouse.OutletID(*outletID))
		}
		wh, err := q.Only(ctx)
		if err != nil {
			return reportScope{}, &reportScopeError{http.StatusNotFound, "WAREHOUSE_NOT_FOUND", "Location not found at the selected outlet"}
		}
		return reportScope{warehouseIDs: []uuid.UUID{wh.ID}, label: wh.Name}, nil
	}
	if outletID == nil {
		return reportScope{}, nil
	}
	whs, err := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.OutletID(*outletID)).
		Order(entwarehouse.ByName()).
		All(ctx)
	if err != nil {
		return reportScope{}, &reportScopeError{http.StatusInternalServerError, "INTERNAL", "Failed to resolve the outlet's locations"}
	}
	scope := reportScope{warehouseIDs: make([]uuid.UUID, 0, len(whs))}
	names := make([]string, 0, len(whs))
	for _, wh := range whs {
		scope.warehouseIDs = append(scope.warehouseIDs, wh.ID)
		names = append(names, wh.Name)
	}
	scope.label = strings.Join(names, ", ")
	return scope, nil
}
