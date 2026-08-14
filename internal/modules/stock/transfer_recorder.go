package stock

import (
	"context"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/modules/transfers"
)

// TransferRecorder is implemented by transfers.Service (wired via WithTransferRecorder), mirroring
// the existing reverse-direction StockCascader interface transfers.Service itself depends on — so
// a warehouse-to-warehouse move made through BulkAdjustStock gets the exact same transfer_number/
// audit-trail/Transfers-list visibility a manually-created transfer gets. Safe to depend on
// transfers directly here: transfers never imports stock, so there is no import cycle.
type TransferRecorder interface {
	RecordCompletedTransfer(ctx context.Context, tenantID, sourceWarehouseID, destWarehouseID uuid.UUID, origin string, lines []transfers.CompletedTransferLine, initiatedBy uuid.UUID, notes string) (*ent.StockTransfer, error)
}

// WithTransferRecorder wires the transfer-audit recorder into the service. Optional — when unset,
// BulkAdjustStock's destination-warehouse moves still move stock exactly as before, just without a
// StockTransfer audit record (best-effort, never blocks the move itself).
func (s *Service) WithTransferRecorder(tr TransferRecorder) *Service {
	s.transferRecorder = tr
	return s
}
