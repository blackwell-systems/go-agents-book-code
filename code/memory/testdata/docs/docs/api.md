# The JSON API

Every request needs an API key in the Authorization header:
`Authorization: Bearer <key>`. Keys are created with
`shortlink keys create` on the server; a read key can only make GET
requests (see docs/api-keys.md).

## Create a link

POST /api/links with a JSON body:

    {"url": "https://example.com/a/long/path", "expires_at": "2027-01-01T00:00:00Z"}

expires_at and slug are optional; slug sets a custom code (see
docs/short-codes.md). The response is 201 Created with the new link:

    {"code": "x7Kp2", "short_url": "https://sl.example/x7Kp2"}

## Read and delete a link

GET /api/links/{code} returns the link and its total clicks.
DELETE /api/links/{code} removes it; its short URL then returns 404.

## Errors

Errors come back as JSON with an error field:

- 400: the body was not valid, or the target was refused.
- 401: the API key was missing, wrong, or revoked.
- 403: the key's scope doesn't allow the request.
- 409: the slug is already in use.
- 429: the client is over its rate limit (see rate-limits.md).
