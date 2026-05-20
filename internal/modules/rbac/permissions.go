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

	// users
	PermUsersView   = "inventory.users.view"
	PermUsersAdd    = "inventory.users.add"
	PermUsersChange = "inventory.users.change"
	PermUsersDelete = "inventory.users.delete"
	PermUsersManage = "inventory.users.manage"
)

// Role codes seeded per tenant.
const (
	RoleInventoryAdmin    = "inventory_admin"
	RoleWarehouseManager  = "warehouse_manager"
	RoleStockClerk        = "stock_clerk"
	RoleViewer            = "viewer"
)
