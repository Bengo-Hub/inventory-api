# Database Maintenance Procedures

This document records operational procedures for maintaining the Inventory Service database within the Kubernetes cluster.

## Database Reset Procedures

### 1. Identify Resources
- **PostgreSQL Pod:** `postgresql-0` (Namespace: `infra`)
- **Inventory API Deployment:** `inventory-api` (Namespace: `inventory`)
- **Database Name:** `inventory`
- **Database User:** `inventory_user`

### 2. Preparation: Scale Down Inventory API
```powershell
kubectl scale deployment inventory-api -n inventory --replicas=0
```

### 3. Terminate Active Sessions
```powershell
kubectl exec postgresql-0 -n infra -- env PGPASSWORD='Vertex2020!' psql -h 127.0.0.1 -U admin_user -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='inventory' AND pid<>pg_backend_pid();"
```

### 4. Drop and Recreate Database
```powershell
kubectl exec postgresql-0 -n infra -- /bin/bash -c "export PGPASSWORD='Vertex2020!'; dropdb -h 127.0.0.1 -U admin_user inventory --if-exists; createdb -h 127.0.0.1 -U admin_user inventory"
```

### 5. Fix Database Ownership
```powershell
kubectl exec postgresql-0 -n infra -- /bin/bash -c "export PGPASSWORD='Vertex2020!'; psql -h 127.0.0.1 -U admin_user -d postgres -c 'ALTER DATABASE inventory OWNER TO inventory_user;'"
```

### 6. Restore Inventory API Deployment
```powershell
kubectl rollout restart deployment inventory-api -n inventory
```

### 7. Run Seed (after deployment)
The seed binary is included in the container at `/app/seed`. It can be run via:
```powershell
kubectl exec <inventory-api-pod> -n inventory -- /app/seed
```

Or wait for the seed init container if enabled in Helm values.

### 8. Verification
```powershell
kubectl get pods -n inventory
kubectl exec postgresql-0 -n infra -- env PGPASSWORD='Vertex2020!' psql -h 127.0.0.1 -U admin_user -d inventory -c "SELECT COUNT(*) FROM units; SELECT COUNT(*) FROM items; SELECT COUNT(*) FROM item_categories;"
```

## Seeded Data

The seed script (`cmd/seed/main.go`) creates:
- **15 global units** (PIECE, CUP, KG, GRAM, LITRE, ML, etc.)
- **22 item categories** (Hot Beverages, Cold Beverages, Pastries, etc.)
- **~50 menu items** (Espresso, Cappuccino, Croissants, etc.)
- **1 default warehouse** (MAIN)
- **Inventory balances** for all items

Seed is idempotent and can be run multiple times safely.

## Common Issues

### Empty API Responses
If endpoints return empty arrays:
1. Check if seed has been run: `SELECT COUNT(*) FROM units;`
2. Check tenant exists: `SELECT * FROM tenants;`
3. Check pod logs: `kubectl logs <pod> -n inventory --tail=50`

### Tenant Resolution Failure
If endpoints return `INVALID_TENANT`:
1. The slug-to-UUID resolver queries auth-api
2. Check auth-api connectivity from inventory pod
3. Verify tenant exists in auth-api: `curl auth-api.auth.svc.cluster.local:4101/api/v1/tenants/by-slug/urban-loft`
