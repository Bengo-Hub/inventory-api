package rbac

// Permission codes follow the inventory.{module}.{action} pattern.
// These match what's seeded in cmd/seed/main.go and stored in inventory_permissions.
const (
	// items
	PermItemsView      = "inventory.items.view"
	PermItemsAdd       = "inventory.items.add"
	PermItemsChange    = "inventory.items.change"
	PermItemsDelete    = "inventory.items.delete"
	PermItemsManage    = "inventory.items.manage"

	// variants
	PermVariantsView   = "inventory.variants.view"
	PermVariantsAdd    = "inventory.variants.add"
	PermVariantsChange = "inventory.variants.change"
	PermVariantsDelete = "inventory.variants.delete"
	PermVariantsManage = "inventory.variants.manage"

	// categories
	PermCategoriesView   = "inventory.categories.view"
	PermCategoriesAdd    = "inventory.categories.add"
	PermCategoriesChange = "inventory.categories.change"
	PermCategoriesDelete = "inventory.categories.delete"
	PermCategoriesManage = "inventory.categories.manage"

	// warehouses
	PermWarehousesView   = "inventory.warehouses.view"
	PermWarehousesAdd    = "inventory.warehouses.add"
	PermWarehousesChange = "inventory.warehouses.change"
	PermWarehousesDelete = "inventory.warehouses.delete"
	PermWarehousesManage = "inventory.warehouses.manage"

	// stock
	PermStockView      = "inventory.stock.view"
	PermStockViewOwn   = "inventory.stock.view_own"
	PermStockAdd       = "inventory.stock.add"
	PermStockChange    = "inventory.stock.change"
	PermStockChangeOwn = "inventory.stock.change_own"
	PermStockDelete    = "inventory.stock.delete"
	PermStockManage    = "inventory.stock.manage"
	PermStockManageOwn = "inventory.stock.manage_own"

	// recipes
	PermRecipesView   = "inventory.recipes.view"
	PermRecipesAdd    = "inventory.recipes.add"
	PermRecipesChange = "inventory.recipes.change"
	PermRecipesDelete = "inventory.recipes.delete"
	PermRecipesManage = "inventory.recipes.manage"

	// consumptions
	PermConsumptionsView      = "inventory.consumptions.view"
	PermConsumptionsViewOwn   = "inventory.consumptions.view_own"
	PermConsumptionsAdd       = "inventory.consumptions.add"
	PermConsumptionsChange    = "inventory.consumptions.change"
	PermConsumptionsChangeOwn = "inventory.consumptions.change_own"
	PermConsumptionsDelete    = "inventory.consumptions.delete"
	PermConsumptionsDeleteOwn = "inventory.consumptions.delete_own"
	PermConsumptionsManage    = "inventory.consumptions.manage"
	PermConsumptionsManageOwn = "inventory.consumptions.manage_own"

	// reservations
	PermReservationsView      = "inventory.reservations.view"
	PermReservationsViewOwn   = "inventory.reservations.view_own"
	PermReservationsAdd       = "inventory.reservations.add"
	PermReservationsChange    = "inventory.reservations.change"
	PermReservationsChangeOwn = "inventory.reservations.change_own"
	PermReservationsDelete    = "inventory.reservations.delete"
	PermReservationsDeleteOwn = "inventory.reservations.delete_own"
	PermReservationsManage    = "inventory.reservations.manage"
	PermReservationsManageOwn = "inventory.reservations.manage_own"

	// units
	PermUnitsView   = "inventory.units.view"
	PermUnitsAdd    = "inventory.units.add"
	PermUnitsChange = "inventory.units.change"
	PermUnitsDelete = "inventory.units.delete"
	PermUnitsManage = "inventory.units.manage"

	// config
	PermConfigView   = "inventory.config.view"
	PermConfigChange = "inventory.config.change"
	PermConfigManage = "inventory.config.manage"

	// settings (tenant inventory settings: general, stock policy, modules, tax/compliance)
	PermSettingsView   = "inventory.settings.view"
	PermSettingsAdd    = "inventory.settings.add"
	PermSettingsChange = "inventory.settings.change"
	PermSettingsDelete = "inventory.settings.delete"
	PermSettingsManage = "inventory.settings.manage"

	// users
	PermUsersView   = "inventory.users.view"
	PermUsersAdd    = "inventory.users.add"
	PermUsersChange = "inventory.users.change"
	PermUsersDelete = "inventory.users.delete"
	PermUsersManage = "inventory.users.manage"

	// tickets (event seat selling + check-in)
	PermTicketsView   = "inventory.tickets.view"
	PermTicketsAdd    = "inventory.tickets.add"
	PermTicketsChange = "inventory.tickets.change"
	PermTicketsDelete = "inventory.tickets.delete"
	PermTicketsManage = "inventory.tickets.manage"

	// procurement (requisitions, POs, goods receipts, returns, contracts, suppliers)
	PermProcurementView   = "inventory.procurement.view"
	PermProcurementAdd    = "inventory.procurement.add"
	PermProcurementChange = "inventory.procurement.change"
	PermProcurementDelete = "inventory.procurement.delete"
	PermProcurementManage = "inventory.procurement.manage"

	// manufacturing (production batches, BOM, QC)
	PermManufacturingView   = "inventory.manufacturing.view"
	PermManufacturingAdd    = "inventory.manufacturing.add"
	PermManufacturingChange = "inventory.manufacturing.change"
	PermManufacturingDelete = "inventory.manufacturing.delete"
	PermManufacturingManage = "inventory.manufacturing.manage"

	// assets (fixed-asset register + lifecycle)
	PermAssetsView   = "inventory.assets.view"
	PermAssetsAdd    = "inventory.assets.add"
	PermAssetsChange = "inventory.assets.change"
	PermAssetsDelete = "inventory.assets.delete"
	PermAssetsManage = "inventory.assets.manage"

	// approvals (approval-matrix rules; the approve/reject act itself is role-gated, not permission-gated)
	PermApprovalsView   = "inventory.approvals.view"
	PermApprovalsAdd    = "inventory.approvals.add"
	PermApprovalsChange = "inventory.approvals.change"
	PermApprovalsDelete = "inventory.approvals.delete"
	PermApprovalsManage = "inventory.approvals.manage"

	// audit (centralized sensitive-action trail — read-only API)
	PermAuditView = "inventory.audit.view"

	// stock counts (cycle/physical counts with variance approval)
	PermStockCountView    = "inventory.stock_count.view"
	PermStockCountAdd     = "inventory.stock_count.add"
	PermStockCountChange  = "inventory.stock_count.change"
	PermStockCountApprove = "inventory.stock_count.approve"
)

// Role codes seeded per tenant.
const (
	RoleInventoryAdmin    = "inventory_admin"
	RoleWarehouseManager  = "warehouse_manager"
	RoleStockClerk        = "stock_clerk"
	RoleViewer            = "viewer"
)
