# auth-service — Agent Instructions

## Overview

JWT-based authentication service for CloudDesk. Handles login, token verification, and session logout.

## Build

```bash
go build -o auth-service .
```

## Test

```bash
go test ./...
```

When writing tests, cover the full token lifecycle: create a token via `/login`, verify it via `/verify`, revoke it via `/logout`, then confirm `/verify` rejects it.

## Run

```bash
./auth-service
# Listens on :8081
```

## Conventions

- Standard library only — no external dependencies.
- HMAC-SHA256 signed JWTs with a configurable secret key.
- Tokens are tracked in an in-memory session map. Logout removes the token from the map.
- All endpoints accept and return JSON.
- Authentication is via `Authorization: Bearer <token>` header.
- Method enforcement: POST for `/login` and `/logout`, GET for `/verify`.

## Deployment

- Container listens on port 8081.
- Set `AUTH_SECRET_KEY` environment variable in production to override the default signing key.
- No persistent storage. Restarting the service invalidates all active sessions.
- Readiness check: `GET /verify` returning 401 (no token) confirms the service is running.

## Security Notes

- The default secret key is for demo purposes only. Always override via environment variable in production.
- Token expiry is set to 24 hours.
- No user database — any username/password combination is accepted. Plug in a real credential store for production.
