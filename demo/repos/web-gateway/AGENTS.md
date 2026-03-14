# web-gateway — Agent Instructions

## Overview

API gateway for CloudDesk. Reverse proxies incoming requests to the appropriate backend service based on URL path prefix.

## Build

```bash
go build -o web-gateway .
```

## Test

```bash
go test ./...
```

When writing tests, use `httptest.NewServer` to create mock backends. Verify that requests to `/auth/*` are proxied to the auth service and `/api/docs*` to the docs API. Test the root `/` endpoint returns the service status JSON.

## Run

```bash
AUTH_SERVICE_URL=http://auth:8081 \
DOCS_API_URL=http://docs:8082 \
./web-gateway
# Listens on :8080
```

## Conventions

- Standard library only — uses `net/http/httputil.ReverseProxy`.
- Path-based routing: the prefix is stripped before forwarding to the upstream.
- Upstream errors return a JSON `{"error":"upstream unavailable"}` with status 502.
- The root path `/` returns a JSON status response listing available routes.
- All configuration via environment variables.

## Environment Variables

| Variable            | Default                  | Description           |
|---------------------|--------------------------|-----------------------|
| `AUTH_SERVICE_URL`  | `http://localhost:8081`  | auth-service base URL |
| `DOCS_API_URL`      | `http://localhost:8082`  | docs-api base URL     |

## Deployment

- Container listens on port 8080.
- This is the public-facing entry point. All other services should be cluster-internal only.
- Readiness check: `GET /` returning 200.
- Configure upstream URLs to point to Kubernetes service DNS names (e.g., `http://auth-service.clouddesk.svc:8081`).
