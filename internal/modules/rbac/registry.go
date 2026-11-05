package rbac

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// registryRole is one role in the catalogue pushed to auth-api's role registry.
type registryRole struct {
	RoleCode    string `json:"role_code"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

// assignableRoles are the inventory roles a tenant admin can give from Accounts. inventory_admin is
// left out: the admin role already maps to it. viewer and accountant are in the catalogue already,
// scoped to pos, and a push here would move them.
var assignableRoles = []registryRole{
	{RoleCode: RoleWarehouseManager, Name: "Warehouse Manager", Description: "Purchases, goods received, items, stock and stock counts.", Scope: ServiceTag},
	{RoleCode: RoleStockClerk, Name: "Stock Clerk", Description: "Stock movements and counts. Can view purchases but not raise them.", Scope: ServiceTag},
}

var registryHTTPClient = &http.Client{Timeout: 15 * time.Second}

// PushRolesToAuthRegistry publishes the assignable roles to auth-api so Accounts can list them and
// count them as inventory roles. Auth upserts by role_code, so it is safe on every start. Best-effort:
// the caller logs a failure, and an unset URL or key does nothing.
func PushRolesToAuthRegistry(ctx context.Context, authURL, apiKey string) error {
	authURL, apiKey = strings.TrimSpace(authURL), strings.TrimSpace(apiKey)
	if authURL == "" || apiKey == "" {
		return nil
	}
	body, err := json.Marshal(map[string]any{"service": ServiceTag, "roles": assignableRoles})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(authURL, "/")+"/api/v1/s2s/roles/sync", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := registryHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("role registry sync: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("role registry sync: HTTP %d", resp.StatusCode)
	}
	return nil
}
