# Fix docs-api crash on PUT with empty body

## Title
docs-api panics on PUT /docs/:id with empty request body

## Body

**Bug report:**

Sending a PUT request to `/docs/:id` with an empty body causes the docs-api to panic and crash. The pod restarts but any in-memory documents are lost.

**Steps to reproduce:**

```bash
# Create a document first
curl -X POST http://docs-api:8082/docs \
  -H "Content-Type: application/json" \
  -d '{"title":"Test","body":"Hello"}'

# Note the returned ID, then send an empty PUT
curl -X PUT http://docs-api:8082/docs/<id>
```

**Expected:** Return 400 Bad Request with an error message.

**Actual:** The process panics with a nil pointer dereference and the pod restarts.

**Root cause hint:** The `handleDocByID` function calls `json.NewDecoder(r.Body).Decode()` without checking whether the body is nil or empty. When the request has no body, `r.Body` may be `http.NoBody` and the decoder returns an EOF error, but the function flow continues with zero-value fields. The real crash happens because the error path doesn't return early in all cases.

**Fix:**

- Add a nil/empty body check before decoding
- Return 400 with `{"error":"request body required"}` if the body is missing or empty
- Add a test case covering this scenario

**Labels:** `bug`, `crash`
