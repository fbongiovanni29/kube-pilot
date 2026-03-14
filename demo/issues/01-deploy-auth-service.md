# Deploy auth-service to the cluster

## Title
Deploy auth-service from source

## Body

We need the `auth-service` running in our Kubernetes cluster. This is the authentication backend for CloudDesk.

**What needs to happen:**

1. Clone the `clouddesk/auth-service` repo
2. Build the container image from the Dockerfile
3. Push the image to the cluster registry
4. Create a Kubernetes Deployment and Service in the `clouddesk` namespace
5. The service should be accessible within the cluster at `auth-service.clouddesk.svc:8081`

**Acceptance criteria:**

- `GET /verify` returns 401 (confirming the service is running and rejecting unauthenticated requests)
- `POST /login` with `{"username":"admin","password":"demo"}` returns a valid JWT token
- The token from login passes `GET /verify` with `Authorization: Bearer <token>`

**Environment:**
- Set `AUTH_SECRET_KEY` to a random 32-character string via a Kubernetes Secret

**Labels:** `feature`, `deployment`
