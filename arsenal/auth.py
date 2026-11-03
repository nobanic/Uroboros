"""Bearer-token auth for the sidecar + token redaction for logs (C6). Stdlib-only."""

import hmac
import re

_BEARER = "Bearer "


def check_bearer(header_value, expected):
    """Constant-time check of an `Authorization: Bearer <token>` header.
    Empty/None expected rejects everything (fail-closed on misconfig)."""
    if not expected:
        return False
    if not header_value or not header_value.startswith(_BEARER):
        return False
    token = header_value[len(_BEARER):]
    return hmac.compare_digest(token, expected)


_TOKEN_RE = re.compile(r"(Bearer\s+|ak_)[A-Za-z0-9._\-]+")


def redact(s):
    """Mask bearer tokens / ak_ keys in any string before it is logged (C6)."""
    return _TOKEN_RE.sub("REDACTED", str(s))
