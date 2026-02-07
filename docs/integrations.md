# Inventory Service - Integration Guide

## Overview

This document provides detailed integration information for all external services and systems integrated with the Inventory service, including internal BengoBox microservices and external third-party services.

---

## Table of Contents

1. [Internal BengoBox Service Integrations](#internal-bengobox-service-integrations)
2. [External Third-Party Integrations](#external-third-party-integrations)
3. [Integration Patterns](#integration-patterns)
4. [Two-Tier Configuration Management](#two-tier-configuration-management)
5. [Event-Driven Architecture](#event-driven-architecture)
6. [Integration Security](#integration-security)
7. [Error Handling & Resilience](#error-handling--resilience)

---

## Internal BengoBox Service Integrations

### Auth Service

**Integration Type**: OAuth2/OIDC + Events + REST

**Use Cases**:
- User authentication and authorization
- JWT token validation
- User identity synchronization
- Tenant/outlet discovery

**Architecture**:
- Uses `shared/auth-client` v0.1.0 library for JWT validation
- All protected `/v1/{tenant}` routes require valid Bearer tokens

**Events Consumed**:
- `auth.tenant.created` - Initialize tenant in inventory system
- `auth.tenant.updated` - Update tenant metadata
- `auth.tenant.synced` - Sync tenant metadata
- `auth.outlet.created` - Create outlet reference
- `auth.outlet.updated` - Update outlet metadata

### Cafe Backend

**Integration Type**: REST API + Events (NATS)

**Use Cases**:
- Menu availability queries
- Stock reservation for orders
- Order consumption events
- Recipe/BOM explosion

**REST API Usage**:
- `GET /v1/{tenant}/inventory/items/{sku}` - Get stock availability
- `POST /v1/{tenant}/inventory/reservations` - Reserve stock for order
- `GET /v1/{tenant}/inventory/recipes/{id}` - Get recipe details

**Events Consumed**:
- `cafe.order.placed` - Reserve stock
- `cafe.order.cancelled` - Release stock reservation
- `cafe.order.completed` - Consume stock

**Events Published**:
- `inventory.stock.updated` - Stock level changed
- `inventory.stock.low` - Low stock alert
- `inventory.reservation.confirmed` - Reservation confirmed

### POS Service

**Integration Type**: REST API + Events (NATS)

**Use Cases**:
- Catalog sync (read-only cache)
- Real-time sales consumption
- Stock warnings
- Offline queue reconciliation

**REST API Usage**:
- `GET /v1/{tenant}/inventory/items` - Get catalog items
- `POST /v1/{tenant}/inventory/consumption` - Record sales consumption
- `GET /v1/{tenant}/inventory/availability` - Check stock availability

**Events Consumed**:
- `pos.order.completed` - Consume stock
- `pos.catalog.sync.requested` - Trigger catalog sync

**Events Published**:
- `inventory.catalog.updated` - Catalog changes
- `inventory.stock.warning` - Stock warning

### Logistics Service

**Integration Type**: REST API + Events (NATS)

**Use Cases**:
- Transfer task creation
- Pick-pack confirmation
- Zone/branch availability queries
- Dispatch preferences

**REST API Usage**:
- `GET /v1/{tenant}/inventory/availability?zone={zone}&branch={branch}` - Zone/branch availability
- `GET /v1/{tenant}/policies/dispatch-preferences` - Dispatch preferences
- `POST /v1/{tenant}/inventory/transfers` - Create transfer order

**Events Consumed**:
- `logistics.transfer.shipped` - Mark transfer as shipped
- `logistics.transfer.delivered` - Mark transfer as received

**Events Published**:
- `inventory.transfer.created` - Transfer order created
- `inventory.transfer.completed` - Transfer completed

### Treasury App

**Integration Type**: REST API + Events (NATS)

**Use Cases**:
- Cost accounting
- Invoice matching
- Subscription entitlement enforcement
- GL account mapping

**REST API Usage**:
- `POST /api/v1/{tenant}/expenses` - Export inventory costs
- `GET /api/v1/{tenant}/invoices` - Match supplier invoices
- `GET /api/v1/{tenant}/subscriptions/{id}/entitlements` - Check feature access

**Events Consumed**:
- `treasury.invoice.received` - Match supplier invoice
- `treasury.subscription.updated` - Update entitlements

**Events Published**:
- `inventory.cost.allocated` - Cost allocation event
- `inventory.usage.metered` - Usage tracking for billing

### Notifications Service

**Integration Type**: Events (NATS) + REST API

**Use Cases**:
- Low stock alerts
- PO approval notifications
- Expiry warnings
- Cycle count assignments

**REST API Usage**:
- `POST /v1/{tenantId}/notifications/messages` - Send notification

**Events Published**:
- `inventory.stock.low` - Low stock alert
- `inventory.po.approved` - PO approval notification
- `inventory.expiry.warning` - Expiry warning
- `inventory.cycle_count.assigned` - Cycle count assignment

### IoT Service

**Integration Type**: Events (NATS) + REST API

**Use Cases**:
- Temperature sensor data
- Humidity monitoring
- Compliance alerts

**Events Consumed**:
- `iot.temperature.reading` - Temperature reading
- `iot.humidity.reading` - Humidity reading

**Events Published**:
- `inventory.temperature.alert` - Temperature threshold breach
- `inventory.compliance.hold` - Compliance hold triggered

---

## External Third-Party Integrations

### Supplier APIs

**Purpose**: Automated PO submission, ASN ingestion

**Configuration** (Tier 1):
- API credentials: Stored encrypted at rest
- Endpoint URLs: Stored encrypted

**Use Cases**:
- Automated PO submission
- ASN (Advanced Shipping Notice) ingestion
- Supplier catalog sync

### Ecommerce Platforms (Future)

**Purpose**: Storefront inventory updates, order reservations

**Configuration** (Tier 1):
- Platform API keys: Stored encrypted
- Webhook secrets: Stored encrypted

**Use Cases**:
- Shopify integration
- Amazon integration
- Catalog sync
- Order reservations

### WMS/ERP Systems (Future)

**Purpose**: Third-party WMS/ERP connectors

**Configuration** (Tier 1):
- System credentials: Stored encrypted
- Integration endpoints: Stored encrypted

**Use Cases**:
- Data synchronization
- Order fulfillment
- Inventory reconciliation

---

## Integration Patterns

### 1. REST API Pattern (Synchronous)

**Use Case**: Immediate stock queries, reservation requests

**Implementation**:
- HTTP client with retry logic
- Circuit breaker pattern
- Request timeout (5 seconds default)
- Idempotency keys for mutations

### 2. Event-Driven Pattern (Asynchronous)

**Use Case**: Stock adjustments, PO status, consumption events

**Transport**: NATS JetStream

**Flow**:
1. Service publishes event to NATS
2. Subscriber services consume event
3. Process event and update local state
4. Publish response events if needed

**Reliability**:
- At-least-once delivery
- Event deduplication via event_id
- Retry on failure
- Dead letter queue for failed events

### 3. Webhook Pattern (Callbacks)

**Use Case**: External provider callbacks, supplier ASN

**Implementation**:
- Webhook endpoints in inventory service
- Signature verification (HMAC-SHA256)
- Retry logic for failed deliveries
- Idempotency handling

### 4. Streaming Pattern (Real-Time)

**Use Case**: Real-time stock updates to POS kiosks

**Implementation**:
- Server-Sent Events (SSE) for stock feeds
- WebSocket for bidirectional communication
- Automatic reconnection on failure

---

## Two-Tier Configuration Management

### Tier 1: Developer/Superuser Configuration

**Visibility**: Only developers and superusers

**Configuration Items**:
- Supplier API credentials
- Ecommerce platform API keys
- WMS/ERP integration credentials
- Database credentials
- Encryption keys

**Storage**:
- Encrypted at rest in database (AES-256-GCM)
- K8s secrets for runtime
- Vault for production secrets

### Tier 2: Business User Configuration

**Visibility**: Normal system users (tenant admins)

**Configuration Items**:
- Replenishment rules
- Dispatch preferences
- Warehouse settings
- Notification preferences

**Storage**:
- Plain text in database (non-sensitive)
- Tenant-specific configuration tables

---

## Event-Driven Architecture

### Event Catalog

#### Outbound Events (Published by Inventory Service)

**inventory.stock.updated**
```json
{
  "event_id": "uuid",
  "event_type": "inventory.stock.updated",
  "tenant_id": "tenant-uuid",
  "timestamp": "2024-12-05T10:30:00Z",
  "data": {
    "item_id": "item-uuid",
    "warehouse_id": "warehouse-uuid",
    "on_hand": 100,
    "available": 95,
    "reserved": 5
  }
}
```

**inventory.transfer.created**
```json
{
  "event_id": "uuid",
  "event_type": "inventory.transfer.created",
  "tenant_id": "tenant-uuid",
  "timestamp": "2024-12-05T10:30:00Z",
  "data": {
    "transfer_id": "transfer-uuid",
    "from_warehouse_id": "warehouse-uuid",
    "to_warehouse_id": "warehouse-uuid",
    "items": [...]
  }
}
```

#### Inbound Events (Consumed by Inventory Service)

**cafe.order.placed**
```json
{
  "event_id": "uuid",
  "event_type": "cafe.order.placed",
  "tenant_id": "tenant-uuid",
  "timestamp": "2024-12-05T10:30:00Z",
  "data": {
    "order_id": "order-uuid",
    "items": [...]
  }
}
```

---

## Integration Security

### Authentication

**JWT Tokens**:
- Validated via `shared/auth-client` library
- JWKS from auth-service
- Token claims include tenant_id for scoping

**API Keys** (Service-to-Service):
- Stored in K8s secrets
- Rotated quarterly

### Authorization

**Tenant Isolation**:
- All requests scoped by tenant_id
- Provider credentials isolated per tenant
- Data isolation enforced at database level

### Secrets Management

**Encryption**:
- Secrets encrypted at rest (AES-256-GCM)
- Decrypted only when used
- Key rotation every 90 days

### Webhook Security

**Signature Verification**:
- HMAC-SHA256 signatures
- Secret shared via K8s secret
- Timestamp validation (5-minute window)
- Nonce validation (prevent replay attacks)

---

## Error Handling & Resilience

### Retry Policies

**Exponential Backoff**:
- Initial delay: 1 second
- Max delay: 30 seconds
- Max retries: 3

### Circuit Breaker

**Implementation**:
- Opens after 5 consecutive failures
- Half-open after 60 seconds
- Closes on successful request

### Monitoring

**Metrics**:
- API call latency (p50, p95, p99)
- API call success/failure rates
- Event publishing success rates
- Stock query performance

**Alerts**:
- High failure rate (>5%)
- Service unavailability
- Event delivery failures
- Low stock thresholds

---

## References

- [Auth Service Integration](../auth-service/auth-service/docs/integrations.md)
- [Cafe Backend Integration](../Cafe/cafe-backend/docs/integrations.md)
- [Logistics Service Integration](../logistics-service/logistics-api/docs/integrations.md)
- [Treasury App Integration](../finance-service/treasury-api/docs/integrations.md)

