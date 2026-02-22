package items

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
)

// StockAvailability matches the DTO expected by the ordering-backend client.
type StockAvailability struct {
	ItemID        uuid.UUID `json:"item_id"`
	SKU           string    `json:"sku"`
	WarehouseID   uuid.UUID `json:"warehouse_id"`
	OnHand        int       `json:"on_hand"`
	Available     int       `json:"available"`
	Reserved      int       `json:"reserved"`
	UnitOfMeasure string    `json:"unit_of_measure"`
	UpdatedAt     string    `json:"updated_at"`
}

// Service handles item-related business logic.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService creates a new items service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("items.service"),
	}
}

// GetStockAvailability returns stock availability for a single item by SKU within a tenant.
func (s *Service) GetStockAvailability(ctx context.Context, tenantID uuid.UUID, sku string) (*StockAvailability, error) {
	itm, err := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			item.Sku(sku),
			item.IsActive(true),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("items: item not found: sku=%s", sku)
		}
		return nil, fmt.Errorf("items: query item: %w", err)
	}

	bal, err := s.client.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemID(itm.ID),
		).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return &StockAvailability{
				ItemID:        itm.ID,
				SKU:           itm.Sku,
				WarehouseID:   uuid.Nil,
				OnHand:        0,
				Available:     0,
				Reserved:      0,
				UnitOfMeasure: itm.UnitOfMeasure,
				UpdatedAt:     itm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			}, nil
		}
		return nil, fmt.Errorf("items: query balance: %w", err)
	}

	return &StockAvailability{
		ItemID:        itm.ID,
		SKU:           itm.Sku,
		WarehouseID:   bal.WarehouseID,
		OnHand:        bal.OnHand,
		Available:     bal.Available,
		Reserved:      bal.Reserved,
		UnitOfMeasure: bal.UnitOfMeasure,
		UpdatedAt:     bal.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}, nil
}

// BulkAvailability returns stock availability for multiple items by SKU.
func (s *Service) BulkAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]StockAvailability, error) {
	items, err := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			item.SkuIn(skus...),
			item.IsActive(true),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query items: %w", err)
	}

	itemIDs := make([]uuid.UUID, len(items))
	itemMap := make(map[uuid.UUID]*ent.Item, len(items))
	for i, itm := range items {
		itemIDs[i] = itm.ID
		itemMap[itm.ID] = itm
	}

	balances, err := s.client.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemIDIn(itemIDs...),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query balances: %w", err)
	}

	balMap := make(map[uuid.UUID]*ent.InventoryBalance, len(balances))
	for _, b := range balances {
		balMap[b.ItemID] = b
	}

	result := make([]StockAvailability, 0, len(items))
	for _, itm := range items {
		avail := StockAvailability{
			ItemID:        itm.ID,
			SKU:           itm.Sku,
			UnitOfMeasure: itm.UnitOfMeasure,
			UpdatedAt:     itm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if bal, ok := balMap[itm.ID]; ok {
			avail.WarehouseID = bal.WarehouseID
			avail.OnHand = bal.OnHand
			avail.Available = bal.Available
			avail.Reserved = bal.Reserved
			avail.UpdatedAt = bal.UpdatedAt.Format("2006-01-02T15:04:05Z")
		}
		result = append(result, avail)
	}

	return result, nil
}
