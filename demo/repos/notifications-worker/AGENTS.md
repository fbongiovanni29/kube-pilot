# notifications-worker — Agent Instructions

## Overview

Background worker for CloudDesk. Polls a notification queue and dispatches webhook calls to external targets.

## Build

```bash
go build -o notifications-worker .
```

## Test

```bash
go test ./...
```

For tests, use `httptest.NewServer` to mock both the queue endpoint and the webhook target. Verify that the worker correctly handles empty queues, valid notifications, and webhook failures.

## Run

```bash
QUEUE_URL=http://queue:9090/queue/notifications \
WEBHOOK_URL=http://hooks:9091/webhook \
./notifications-worker
```

## Conventions

- Standard library only — no external dependencies.
- Long-running process with a poll loop (not an HTTP server).
- Errors are logged but do not crash the process. The worker keeps polling.
- Each notification is dispatched independently. One webhook failure does not block others.

## Environment Variables

| Variable      | Default                                      | Description                  |
|---------------|----------------------------------------------|------------------------------|
| `QUEUE_URL`   | `http://localhost:9090/queue/notifications`   | URL to poll for new notifications |
| `WEBHOOK_URL` | `http://localhost:9091/webhook`               | URL to POST webhook payloads |

## Deployment

- No port exposed — this is a worker, not a server.
- Deploy as a Kubernetes Deployment with 1 replica (scaling beyond 1 risks duplicate dispatches without a distributed lock).
- Liveness check: the process stays alive as long as the poll loop is running. Use a process-level liveness probe.
- Configure `QUEUE_URL` and `WEBHOOK_URL` via environment variables or a ConfigMap.
