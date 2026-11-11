package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// cancelDuplicatePendingApprovals keeps the newest pending approval request per tenant, module
// and object and cancels the older ones. It must run before Schema.Create builds the partial
// unique index approvalrequest_pending_unique, which cannot be created while duplicates exist.
// Idempotent: once no duplicates remain it updates nothing. Superseding the older pending
// request is what Submit always did, so this only finishes work the old code left behind.
const cancelDuplicatePendingApprovals = `
UPDATE approval_requests ar
   SET status = 'cancelled', decided_at = now(), updated_at = now()
 WHERE ar.status = 'pending'
   AND EXISTS (
       SELECT 1 FROM approval_requests o
        WHERE o.tenant_id = ar.tenant_id
          AND o.module = ar.module
          AND o.object_id = ar.object_id
          AND o.status = 'pending'
          AND (o.created_at > ar.created_at OR (o.created_at = ar.created_at AND o.id > ar.id)))`

// PreMigrate runs the targeted data fixes the live schema diff needs before it can apply. The
// versioned .sql files are not executed by Schema.Create, so data steps live here. Safe on a fresh
// database (the table may not exist yet).
func PreMigrate(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('approval_requests') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("premigrate: check approval_requests: %w", err)
	}
	if !exists {
		return nil
	}
	if _, err := db.ExecContext(ctx, cancelDuplicatePendingApprovals); err != nil {
		return fmt.Errorf("premigrate: cancel duplicate pending approvals: %w", err)
	}
	return nil
}
