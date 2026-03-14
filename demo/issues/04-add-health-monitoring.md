# Add health endpoints to all services

## Title
Add /healthz endpoints and a health monitoring dashboard

## Body

We need standardized health checks across all CloudDesk services so Kubernetes can properly manage them and we can monitor their status.

**Requirements for each service:**

Add a `GET /healthz` endpoint to `auth-service`, `docs-api`, and `web-gateway` that returns:

```json
{
  "status": "healthy",
  "service": "<service-name>",
  "uptime_seconds": 1234,
  "version": "1.0.0"
}
```

- Track uptime from process start using `time.Since`.
- Read version from a `VERSION` environment variable (default `"dev"`).
- Return HTTP 200 for healthy, HTTP 503 if the service detects an internal problem.

**For web-gateway specifically:**

The `/healthz` endpoint should also check connectivity to upstream services (auth-service and docs-api) and include their status:

```json
{
  "status": "healthy",
  "service": "web-gateway",
  "uptime_seconds": 1234,
  "version": "1.0.0",
  "upstreams": {
    "auth-service": "healthy",
    "docs-api": "unhealthy"
  }
}
```

If any upstream is unhealthy, the overall status should be `"degraded"` (not `"unhealthy"` — the gateway itself is still serving).

**For notifications-worker:**

Since this is not an HTTP server, add a health file approach: write a timestamp to `/tmp/healthz` on each successful poll loop iteration. Configure a Kubernetes liveness probe that checks the file's age.

**Kubernetes changes:**

- Add `livenessProbe` and `readinessProbe` to all Deployments pointing to `/healthz`
- For notifications-worker, use an exec probe that checks `/tmp/healthz` file age

**Labels:** `enhancement`, `observability`, `multi-repo`
