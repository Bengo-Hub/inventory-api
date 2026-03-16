# Inventory Service - Apache Superset Integration

## Overview

The Inventory service integrates with the centralized Apache Superset instance for BI dashboards, analytics, and reporting. Superset is deployed as a centralized service accessible to all BengoBox services.

---

## Architecture

### Service Configuration

**Environment Variables**:
- `SUPERSET_BASE_URL` - Superset service URL
- `SUPERSET_ADMIN_USERNAME` - Admin username (K8s secret)
- `SUPERSET_ADMIN_PASSWORD` - Admin password (K8s secret)
- `SUPERSET_API_VERSION` - API version (default: v1)

**Authentication**:
- Admin credentials used for backend-to-Superset communication
- User authentication via JWT tokens passed to Superset for SSO
- Guest tokens generated for embedded dashboards

---

## Integration Methods

### 1. REST API Client

Backend uses Go HTTP client configured for Superset REST API calls.

**Base Configuration**:
- Base URL: `SUPERSET_BASE_URL/api/v1`
- Default headers: `Content-Type: application/json`
- Authentication: Bearer token from Superset login endpoint
- Retry policy: Exponential backoff (3 retries)
- Circuit breaker: Opens after 5 consecutive failures

**Key API Endpoints**:

**Authentication**:
- `POST /api/v1/security/login` - Login with admin credentials
- `POST /api/v1/security/refresh` - Refresh access token
- `POST /api/v1/security/guest_token/` - Generate guest token for embedding

**Data Sources**:
- `GET /api/v1/database/` - List all data sources
- `POST /api/v1/database/` - Create new data source
- `PUT /api/v1/database/{id}` - Update data source

**Dashboards**:
- `GET /api/v1/dashboard/` - List all dashboards
- `POST /api/v1/dashboard/` - Create new dashboard
- `GET /api/v1/dashboard/{id}` - Get dashboard details

### 2. Database Direct Connection

Superset connects directly to PostgreSQL database via read-only user for data access.

**Connection Configuration**:
- Database type: PostgreSQL
- Connection string: Provided to Superset via data source API
- Read-only user: `superset_readonly` (created in PostgreSQL)
- Permissions: SELECT only on all tables, no write access
- SSL: Required for production connections

**Read-Only User Setup**:
- Create `superset_readonly` role in PostgreSQL
- Grant CONNECT on database
- Grant USAGE on schema
- Grant SELECT on all tables
- Set default privileges for future tables

**Connection String** (for Superset):
```
postgresql://superset_readonly:password@postgresql.infra.svc.cluster.local:5432/inventory_db?sslmode=require
```

---

## Pre-Built Dashboards

### 1. Stock Status Dashboard

**Charts**:
- Stock levels by warehouse (bar chart)
- Low stock items (table)
- Stock value by category (pie chart)
- Stock turnover rate (line chart)
- ABC analysis (table)

**Filters**:
- Date range
- Warehouse selection
- Item category

**Data Source**: `inventory_balances`, `items`, `warehouses` tables

### 2. Purchase Order Dashboard

**Charts**:
- PO status breakdown (pie chart)
- PO value over time (line chart)
- Supplier performance (table)
- Lead time analysis (bar chart)
- PO approval rate (metric)

**Filters**:
- Date range
- Supplier selection
- Status

**Data Source**: `purchase_orders`, `purchase_order_lines`, `suppliers` tables

### 3. Inventory Movements Dashboard

**Charts**:
- Movement volume by type (bar chart)
- Transfer efficiency (line chart)
- Cycle count accuracy (metric)
- Adjustment trends (line chart)
- Movement velocity (metric)

**Filters**:
- Date range
- Warehouse selection
- Movement type

**Data Source**: `inventory_ledger_entries`, `transfer_orders`, `stock_adjustments` tables

### 4. Valuation & Cost Dashboard

**Charts**:
- Inventory value by warehouse (bar chart)
- Cost trends (line chart)
- Valuation method distribution (pie chart)
- Cost per unit trends (line chart)
- Total inventory value (metric)

**Filters**:
- Date range
- Warehouse selection
- Valuation method

**Data Source**: `inventory_snapshots`, `inventory_ledger_entries` tables

### 5. Compliance & Traceability Dashboard

**Charts**:
- Lot/batch tracking coverage (metric)
- Expiry alerts (table)
- Temperature compliance (line chart)
- Recall events (bar chart)
- Compliance score (metric)

**Filters**:
- Date range
- Warehouse selection
- Compliance type

**Data Source**: `lot_batches`, `compliance_events`, `temperature_logs` tables

---

## Implementation Details

### Initialization Process

1. Authenticate with Superset using admin credentials
2. Create/update data source pointing to PostgreSQL
3. Create/update dashboards for each module:
   - Stock Status Dashboard
   - Purchase Order Dashboard
   - Inventory Movements Dashboard
   - Valuation & Cost Dashboard
   - Compliance & Traceability Dashboard
4. Log warnings for dashboard creation failures (non-blocking)

### Dashboard Bootstrap

**Backend Endpoint**: `GET /api/v1/dashboards/{module}/embed`

**Process**:
1. Extract tenant ID from context
2. Get dashboard ID for module from Superset
3. Generate guest token with Row-Level Security (RLS) clause filtering by tenant_id
4. Construct embed URL with dashboard ID and guest token
5. Return embed URL with expiration time (5 minutes)

### Row-Level Security (RLS)

**Implementation**:
- Guest tokens include RLS clauses
- RLS filters data by `tenant_id`
- Each tenant sees only their data

---

## Error Handling

### Retry Logic

**Retry Policy**:
- Maximum 3 retry attempts
- Exponential backoff (1s, 2s, 4s delays)
- Retry on 5xx errors or network failures
- Return response on success or after max retries

### Circuit Breaker

**Implementation**:
- Opens after 5 consecutive failures
- Half-open after 60 seconds
- Closes on successful request

### Fallback Strategies

**Superset Unavailable**:
- Return cached dashboard URLs (if available)
- Show static dashboard images
- Log error for monitoring
- Alert operations team

---

## Monitoring

### Metrics

**Integration-Specific Metrics**:
- Superset API call latency (p50, p95, p99)
- Dashboard creation/update success rates
- Guest token generation latency
- Data source connection health

**Prometheus Metrics**:
- `superset_api_call_duration_seconds` - Histogram of API call durations (labeled by endpoint, status)
- `superset_dashboard_views_total` - Counter of dashboard views (labeled by dashboard, tenant)

### Alerts

**Alert Conditions**:
- Superset service unavailability
- High API call failure rate (>5%)
- Dashboard creation failures
- Data source connection failures

---

## Security Considerations

### Authentication & Authorization

- Admin credentials stored in K8s secrets
- Guest tokens expire after 5 minutes
- RLS ensures tenant data isolation
- JWT tokens validated for SSO

### Data Privacy

- Read-only database user
- RLS filters enforce tenant isolation
- Sensitive data masked in logs
- PII data excluded from dashboards (if applicable)

---

## References

- [Apache Superset REST API Documentation](https://superset.apache.org/docs/api)
- [Superset Deployment Guide](../../devops-k8s/docs/superset-deployment.md)
- [Ordering-Backend Superset Integration](../../../ordering-service/ordering-backend/docs/superset-integration.md)

