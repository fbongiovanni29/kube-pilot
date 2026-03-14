# Add rate limiting to web-gateway

## Title
Add per-IP rate limiting to web-gateway

## Body

The web-gateway is currently open to unlimited requests. We need basic rate limiting to prevent abuse.

**Requirements:**

- Add a token-bucket rate limiter to the web-gateway
- Limit each client IP to 100 requests per minute
- Return HTTP 429 (Too Many Requests) with a JSON body `{"error":"rate limit exceeded"}` when the limit is hit
- Include `X-RateLimit-Remaining` and `X-RateLimit-Reset` headers in all responses
- The rate limiter should wrap all routes (both proxied and the root status endpoint)

**Implementation notes:**

- Use an in-memory map of IP -> bucket. Clean up stale entries every 5 minutes to avoid unbounded memory growth.
- Extract client IP from `X-Forwarded-For` header first, falling back to `r.RemoteAddr`.
- This is a code change to the existing `web-gateway` repo, not a new service.

**Testing:**

- Verify that 100 rapid requests from the same IP succeed
- Verify that request 101 returns 429
- Verify that after waiting 60 seconds, requests succeed again

**Labels:** `enhancement`, `security`
