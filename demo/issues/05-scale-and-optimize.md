# Scale and optimize CloudDesk for production traffic

## Title
Add horizontal scaling and resource limits to CloudDesk services

## Body

CloudDesk is getting real traffic and we need to tune it for production reliability.

**Resource limits:**

Add resource requests and limits to all Deployments:

| Service               | CPU Request | CPU Limit | Memory Request | Memory Limit |
|-----------------------|-------------|-----------|----------------|--------------|
| web-gateway           | 100m        | 500m      | 64Mi           | 128Mi        |
| auth-service          | 50m         | 250m      | 32Mi           | 64Mi         |
| docs-api              | 100m        | 500m      | 64Mi           | 256Mi        |
| notifications-worker  | 25m         | 100m      | 16Mi           | 32Mi         |

**Horizontal Pod Autoscaler:**

Create HPAs for the HTTP-serving services (not notifications-worker):

- `web-gateway`: min 2, max 10, target 70% CPU
- `auth-service`: min 2, max 5, target 80% CPU
- `docs-api`: min 1, max 5, target 70% CPU

**Pod Disruption Budgets:**

Add PDBs to ensure availability during rolling updates:

- `web-gateway`: minAvailable 1
- `auth-service`: minAvailable 1

**Anti-affinity:**

Add pod anti-affinity rules to `web-gateway` and `auth-service` so replicas spread across nodes. Use preferred (not required) anti-affinity to avoid scheduling failures on small clusters.

**Verification:**

- All pods should start successfully with the new resource limits
- HPA should report current metrics (requires metrics-server)
- Run a load test and confirm web-gateway scales up
- Verify PDBs block draining a node if it would take the last replica

**Labels:** `operations`, `scaling`, `production`
