package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	entinvuser "github.com/bengobox/inventory-service/internal/ent/inventoryuser"
)

// POSPinLookup reads a member's PIN hash from pos-api. An empty hash means they have no POS PIN.
type POSPinLookup func(ctx context.Context, tenantID, authUserID uuid.UUID) (string, error)

// SetPOSPinLookup lets a member newly given inventory keep the PIN they already use at the till.
func (c *AuthEventsConsumer) SetPOSPinLookup(fn POSPinLookup) { c.posPin = fn }

var posPinHTTPClient = &http.Client{Timeout: 10 * time.Second}

// NewPOSPinLookup reads the hash over pos-api's internal-key route.
func NewPOSPinLookup(posURL, apiKey string) POSPinLookup {
	base := strings.TrimRight(strings.TrimSpace(posURL), "/")
	return func(ctx context.Context, tenantID, authUserID uuid.UUID) (string, error) {
		if base == "" || apiKey == "" {
			return "", nil
		}
		url := fmt.Sprintf("%s/api/v1/s2s/%s/staff/%s/pin-hash", base, tenantID, authUserID)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("X-API-Key", apiKey)
		resp, err := posPinHTTPClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return "", nil
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("pos pin lookup: HTTP %d", resp.StatusCode)
		}
		var body struct {
			PinHash string `json:"pin_hash"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return "", err
		}
		return body.PinHash, nil
	}
}

// adoptPOSPin gives a member with no inventory PIN the one they use at the till, so someone newly
// given inventory does not need a second PIN. It never replaces a PIN, and a failure only leaves
// the member to have one set in Accounts: it must not fail the user sync.
func (c *AuthEventsConsumer) adoptPOSPin(ctx context.Context, tenantID, authUserID uuid.UUID) {
	if c.posPin == nil || c.orm == nil {
		return
	}
	missing, err := c.orm.InventoryUser.Query().
		Where(entinvuser.TenantID(tenantID), entinvuser.AuthServiceUserID(authUserID), entinvuser.PinHashIsNil()).
		Exist(ctx)
	if err != nil || !missing {
		return
	}
	hash, err := c.posPin(ctx, tenantID, authUserID)
	if err != nil {
		c.log.Warn("pos pin lookup failed", zap.String("user_id", authUserID.String()), zap.Error(err))
		return
	}
	if hash == "" {
		return
	}
	if err := c.orm.InventoryUser.Update().
		Where(entinvuser.TenantID(tenantID), entinvuser.AuthServiceUserID(authUserID), entinvuser.PinHashIsNil()).
		SetPinHash(hash).Exec(ctx); err != nil {
		c.log.Warn("pos pin not stored", zap.String("user_id", authUserID.String()), zap.Error(err))
	}
}

// followPOSPinChange keeps a member's one PIN the same in both services when it changes for POS. It
// only touches someone already in inventory: a POS PIN never makes a person an inventory user.
func (c *AuthEventsConsumer) followPOSPinChange(ctx context.Context, tenantID, authUserID uuid.UUID, pinHash string) error {
	if c.orm == nil || tenantID == uuid.Nil || pinHash == "" {
		return nil
	}
	return c.orm.InventoryUser.Update().
		Where(entinvuser.TenantID(tenantID), entinvuser.AuthServiceUserID(authUserID)).
		SetPinHash(pinHash).SetPinFailedAttempts(0).ClearPinLockedUntil().Exec(ctx)
}
