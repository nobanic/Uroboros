"""Pure-ASGI bearer-auth middleware for the sidecar's Streamable HTTP transport.

This is the piece that actually ENFORCES auth on the FastMCP request path (the gap
noted in server.py): it wraps the ASGI app returned by `mcp.streamable_http_app()`
and rejects any HTTP request whose `Authorization: Bearer <token>` does not match,
before the request reaches the MCP handler.

Factored out of server.py so it is unit-testable WITHOUT importing the `mcp` SDK
(C3): the decision logic is `arsenal.auth.check_bearer`, already tested; this module
only adds the ASGI plumbing, driven directly by the tests with fake scope/receive/send.

Non-HTTP scopes (lifespan, websocket) pass through untouched — auth is HTTP-only.
"""

from arsenal.auth import check_bearer

_UNAUTHORIZED_BODY = b"unauthorized"


class BearerAuthMiddleware:
    def __init__(self, app, token):
        self.app = app
        self.token = token

    async def __call__(self, scope, receive, send):
        if scope.get("type") != "http":
            await self.app(scope, receive, send)
            return
        headers = dict(scope.get("headers") or [])
        auth = headers.get(b"authorization", b"").decode("latin-1")
        if not check_bearer(auth, self.token):
            await send({
                "type": "http.response.start",
                "status": 401,
                "headers": [(b"content-type", b"text/plain; charset=utf-8"),
                            (b"www-authenticate", b"Bearer")],
            })
            await send({"type": "http.response.body", "body": _UNAUTHORIZED_BODY})
            return
        await self.app(scope, receive, send)
