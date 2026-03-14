# docs-api — Agent Instructions

## Overview

Document storage REST API for CloudDesk. Stores documents in-memory with full CRUD support.

## Build

```bash
go build -o docs-api .
```

## Test

```bash
go test ./...
```

No tests exist yet. When adding tests, use the standard `net/http/httptest` package. Test each endpoint (GET /docs, POST /docs, GET/PUT/DELETE /docs/:id) independently.

## Run

```bash
./docs-api
# Listens on :8082
```

## Conventions

- Standard library HTTP handlers only — no frameworks.
- JSON request/response bodies. Always set `Content-Type: application/json`.
- UUID v4 for document IDs (via `github.com/google/uuid`).
- Thread-safe access to the in-memory store using `sync.RWMutex`.
- Log to stdout with the `log` package.

## Deployment

- Container listens on port 8082.
- No environment variables required for basic operation.
- No persistent storage — data lives in process memory. Restarts clear all documents.
- Readiness check: `GET /docs` returning 200 means the service is ready.

## Known Issues

- The `/docs/` endpoint will panic if the request body is nil on a PUT. This is a known bug for the demo debugging exercise.
