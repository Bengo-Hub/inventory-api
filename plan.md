# Inventory Service - Implementation Plan

## Executive Summary

**System Purpose**: Centralized inventory backbone for all BengoBox domains (cafe, POS, ecommerce, logistics) with real-time stock visibility. Harmonizes purchasing, warehouse operations, and fulfillment so downstream services consume accurate product and availability data.

**Key Capabilities**:
- Master data management (items, variants, BOMs, suppliers)
- Warehouse and location management
- Real-time inventory balances and valuation
- Purchase orders and replenishment
- Sales consumption and reservations
- Inventory movements and transfers
- Lot/batch tracking and compliance
- Multi-warehouse support

**Entity Ownership**: This service owns all catalog and inventory entities: items/SKUs, variants, categories, BOMs, suppliers, warehouses, inventory balances, purchase orders, transfer orders, reservations, and lot/batch tracking. **Inventory does NOT own**: users (references auth-service via `user_id`), orders (references from cafe/POS services), payments (uses treasury-api), logistics tasks (references from logistics-service).

---

## Technology Stack

### Core Framework
- **Language**: Go 1.22+
- **Architecture**: Clean/Hexagonal architecture
- **HTTP Router**: chi
- **API Documentation**: OpenAPI-first contracts
- **gRPC**: ConnectRPC for streaming (stock feeds)

### Data & Caching
- **Primary Database**: PostgreSQL 16+
- **ORM**: Ent (schema-as-code migrations)
- **Caching**: Redis 7+ for caching, rate limiting, reservation queues
- **Message Broker**: NATS JetStream or Kafka

### Supporting Libraries
- **Validation**: Custom validators
- **Logging**: zap (structured logging)
- **Tracing**: OpenTelemetry instrumentation
- **Metrics**: Prometheus

### DevOps & Observability
- **Containerization**: Multi-stage Docker builds
- **Orchestration**: Kubernetes (via centralized devops-k8s)
- **CI/CD**: GitHub Actions → ArgoCD
- **Monitoring**: Prometheus + Grafana, OpenTelemetry
- **APM**: Jaeger distributed tracing

---

## Domain Modules & Features

### 1. Master Data Management

**Inventory-Specific Features**:
- Items, variants, units of measure
- Packaging hierarchy, barcodes/PLUs
- BOM/recipe definitions
- Supplier catalogue and price lists

**Entities Owned**:
- `items` - Canonical item catalogue
- `item_categories` - Hierarchical category tree
- `item_variants` - Size/flavor/packaging variants
- `item_uoms` - Units of measure and conversions
- `item_boms` - Bill of materials/recipes
- `item_bom_components` - BOM components
- `item_suppliers` - Supplier sourcing data

**Integration Points**:
- **Ordering-Backend**: Menu items reference inventory SKUs (no duplication)
- **POS Service**: Catalog sync (read-only cache)

### 2. Warehouse & Location Management

**Inventory-Specific Features**:
- Warehouse definitions
- Storage locations (zone/aisle/bin, temperature)
- Stocking rules
- Multi-warehouse hierarchies

**Entities Owned**:
- `warehouses` - Warehouse definitions
- `warehouse_zones` - Logical partitioning
- `locations` - Storage bins/locations
- `warehouse_contacts` - Operational contacts

**Integration Points**:
- **auth-service**: Outlet registry (references only)
- **Logistics Service**: Warehouse availability queries

### 3. Inventory Balances & Valuation

**Inventory-Specific Features**:
- Real-time On Hand (OH), Available to Promise (ATP)
- Reserved, In Transit, Damaged, On Order
- Costing methods (FIFO, Weighted Average)
- Stock ledger for auditable adjustments

**Entities Owned**:
- `inventory_balances` - Rolling inventory totals
- `inventory_snapshots` - Periodic snapshots
- `inventory_ledger_entries` - Immutable movement log
- `lot_batches` - Traceability records
- `serial_numbers` - Serialized asset tracking

**Integration Points**:
- **Ordering-Backend**: Stock availability queries
- **POS Service**: Real-time stock checks
- **Logistics Service**: Zone/branch availability queries

### 4. Replenishment & Purchasing

**Inventory-Specific Features**:
- Purchase orders, quotes, requisitions
- Auto-replenishment rules (min/max, EOQ, safety stock)
- Vendor returns
- Drop-ship workflows

**Entities Owned**:
- `suppliers` - Supplier master data
- `purchase_orders` - Purchase order header
- `purchase_order_lines` - Line items
- `asn_receipts` - Advance shipping notices
- `goods_receipts` - Warehouse receipt transactions
- `goods_receipt_lines` - Detailed receipt lines
- `replenishment_rules` - Auto-replenishment settings
- `replenishment_suggestions` - Generated proposals

**Integration Points**:
- **treasury-api**: Invoice matching, cost accounting
- **Notifications Service**: PO approval alerts, low stock warnings

### 5. Sales & Consumption Integration

**Inventory-Specific Features**:
- Consume POS/delivery order events to decrement stock
- Recipe/BOM explosion for kitchen production
- Ecommerce fulfillment reservations
- Substitution support

**Entities Owned**:
- `inventory_reservations` - Soft/hard allocations
- `reservation_events` - Reservation lifecycle

**Integration Points**:
- **Ordering-Backend**: Order consumption events
- **POS Service**: Sales consumption events
- **Logistics Service**: Transfer task creation

### 6. Inventory Movements

**Inventory-Specific Features**:
- Goods receipt, put-away, picking, packing, staging
- Internal transfers
- Cycle counts, adjustments
- Waste/spoilage logging
- Quality holds

**Entities Owned**:
- `transfer_orders` - Inter-warehouse transfers
- `transfer_lines` - Transfer details
- `transfer_events` - State changes
- `stock_adjustments` - Manual adjustments
- `cycle_counts` - Cycle count campaigns
- `cycle_count_results` - Variance tracking

**Integration Points**:
- **Logistics Service**: Transfer task creation and completion

### 7. Reservations & Allocation

**Inventory-Specific Features**:
- Soft/hard allocations for sales orders
- Backorder management
- Substitution logic
- Urgent override approvals

**Entities Owned**:
- `inventory_reservations` - Allocations
- `reservation_events` - Reservation audit

**Integration Points**:
- **Ordering-Backend**: Order reservations
- **POS Service**: Sales reservations

### 8. Compliance & Audit

**Inventory-Specific Features**:
- Lot/batch tracking
- Expiry date management
- Temperature logs
- HACCP compliance
- Recall management

**Entities Owned**:
- `lot_batches` - Traceability records
- `compliance_events` - Recalls, expiry alerts
- `temperature_logs` - HACCP monitoring

**Integration Points**:
- **IoT Service**: Temperature sensor data

### 9. Analytics & Intelligence

**Inventory-Specific Features**:
- Stock status dashboards
- Inventory turn, fill rates, shrinkage
- PO/transfer/warehouse performance metrics
- Scheduled exports

**Entities Owned**:
- `inventory_snapshots` - Analytics snapshots
- `demand_forecasts` - Forecast results (future)

**Integration Points**:
- **Apache Superset**: BI dashboards and analytics

### 10. Subscription & Gating

**Inventory-Specific Features**:
- Plan-based feature access
- Usage tracking (warehouses, API calls)
- Advanced planning gating
- Multi-warehouse gating

**Entities Owned**:
- `subscription_entitlements` - Feature access control
- `inventory_usage_metrics` - Usage tracking

**Integration Points**:
- **treasury-api**: Subscription management

---

## Cross-Cutting Concerns

### Testing
- Go test suites with table-driven tests
- Testcontainers for integration testing
- Pact for contract tests
- Performance testing for high-volume operations

### Observability
- Structured logging (zap)
- Tracing via OpenTelemetry
- Metrics exported via Prometheus
- Distributed tracing via Tempo/Jaeger

### Security
- OWASP ASVS baseline
- TLS everywhere
- Secrets via Vault/Parameter Store
- Rate limiting & anomaly detection middleware
- JWT validation via auth-service

### Scalability
- Stateless HTTP layer
- Background workers via NATS/Redis streams
- Partitioned tables for ledger entries
- Caching strategy for hot data

### Data Modelling
- Ent schemas as single source of truth
- Tenant/outlet discovery webhooks
- Outbox pattern for reliable domain events (using `shared-events` library)
- Immutable ledger for audit trail

### Architecture Patterns Migration Status (January 2026)

| Pattern | Status | Library | Notes |
|---------|--------|---------|-------|
| Outbox Pattern | ✅ **Schema Ready** | `shared-events` v0.1.0 | Migration + repository created |
| Circuit Breaker | ⏳ **Dependency Ready** | `shared-service-client` v0.1.0 | Import and use in HTTP clients |
| Shared Middleware | ✅ **Completed** | `httpware` v0.1.1 | Migrated to shared package |
| JWT Validation | ✅ Implemented | `shared-auth-client` v0.2.0 | Production |
| Subscription Feature Gating | ⏳ **Planned** | `shared-auth-client` v0.2.0 | Use JWT claims for feature checks |
| Dual Auth (JWT + API Key) | ✅ Implemented | `shared-auth-client` v0.2.0 | SSO fully supports both |

**Migration Checklist:**
- [x] Add `github.com/Bengo-Hub/shared-events` dependency ✅ (Jan 2026)
- [x] Create `outbox_events` SQL migration ✅ (Jan 2026)
- [x] Create `internal/modules/outbox/repository.go` ✅ (Jan 2026)
- [ ] Replace direct NATS publish with `PublishWithOutbox`
- [ ] Add background publisher worker
- [ ] Add `github.com/Bengo-Hub/shared-service-client` dependency
- [ ] Replace direct HTTP calls with shared client
- [x] Add `github.com/Bengo-Hub/httpware` dependency ✅ (Jan 2026)
- [x] Replace local middleware with shared package ✅ (Jan 2026)
- [x] Upgrade to `shared-auth-client` v0.2.0 ✅ (Jan 2026)
- [ ] Add feature gating middleware for premium features

### Subscription Feature Gating (Pending auth-service Sprint 11)

Once auth-service embeds subscription data in JWT, apply feature gating:

```go
// In router.go - Gate premium features
r.Route("/warehouses", func(r chi.Router) {
    // Multi-warehouse requires Growth+ plan
    r.With(authclient.RequireFeature("multi_warehouse")).Post("/", handler.CreateWarehouse)
})

r.Route("/demand-forecast", func(r chi.Router) {
    r.Use(authclient.RequirePlan("PROFESSIONAL"))
    r.Get("/", handler.GetDemandForecast)
})
```

**Features to Gate:**
| Feature | Required Plan | Feature Code |
|---------|---------------|--------------|
| Multi-Warehouse | Growth+ | `multi_warehouse` |
| Demand Forecasting | Professional | `demand_forecast` |
| Advanced Lot Tracking | Growth+ | `lot_tracking` |
| Batch Costing | Professional | `batch_costing` |

---

## API & Protocol Strategy

- **REST-first**: Versioned routes (`/v1/{tenant}/inventory/items`), documented via OpenAPI
- **gRPC**: ConnectRPC for high-throughput streaming (stock feeds)
- **Webhooks**: Stock adjustments, PO status, replenishment events
- **SSE**: Real-time stock updates to POS kiosks
- **Idempotency**: Keys, correlation IDs, distributed tracing context propagation

---

## Compliance & Risk Controls

- Align with Kenya Data Protection Act: explicit consent flows, user data export/delete endpoints, audit logging
- HACCP compliance: temperature logging, traceability
- Financial compliance: cost accounting, GL mapping
- Disaster recovery playbook, RTO/RPO targets (<1 hour)

---

## Sprint Delivery Plan

See `docs/sprints/` folder for detailed sprint plans:
- Sprint 0: Foundations
- **Sprint 1 (CRITICAL)**: Authentication, RBAC & User Management ⏳ **MUST BE FIRST**
- Sprint 2: Master Data
- Sprint 3: Balances & Ledger
- Sprint 4: Purchasing & Replenishment
- Sprint 5: Sales Consumption & Reservations
- Sprint 6: Transfers & Movements
- Sprint 7: Lot/Expiry & Compliance
- Sprint 8: Integration Layer & Provider Configs
- Sprint 9: Analytics & Hardening
- Sprint 10: Subscription Enforcement
- Sprint 11: Launch & Support

**Note**: Sprint 1 (Auth/RBAC) must be completed before any domain features (Master Data, etc.) can be implemented. All endpoints require authentication.

---

## Runtime Ports & Environments

- **Local development**: Service runs on port **4104**
- **Cloud deployment**: All backend services listen on **port 4000** for consistency behind ingress controllers

---

## References

- [Integration Guide](docs/integrations.md)
- [Entity Relationship Diagram](docs/erd.md)
- [Superset Integration](docs/superset-integration.md)
- [Sprint Plans](docs/sprints/)
