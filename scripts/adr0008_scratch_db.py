"""Connection-free allowlist for the ADR 0008 offline scratch drivers."""
import os
import re
from urllib.parse import parse_qsl, quote, unquote, urlsplit


def scratch_url(raw, database="postgres", environ=None):
    """Reject routing overrides before any subprocess, then rebuild the DSN.

    Do not inherit libpq defaults, service files, options, sockets or caller
    database paths. Only credentials and the explicit local TCP endpoint survive.
    """
    env = os.environ if environ is None else environ
    if any(key.upper().startswith("PG") for key in env):
        raise ValueError("PG environment overrides are forbidden for scratch verification")
    try:
        parsed = urlsplit(raw)
        query = parse_qsl(parsed.query, strict_parsing=True)
        valid = (parsed.scheme in ("postgres", "postgresql")
                 and parsed.hostname == "localhost" and parsed.port == 54329
                 and not parsed.fragment
                 and all(key == "sslmode" and value == "disable" for key, value in query)
                 and re.fullmatch(r"postgres|om_9c2c_[0-9a-f]{32}", database))
        if not valid:
            raise ValueError()
        credentials = ""
        if parsed.username is not None:
            credentials = quote(unquote(parsed.username), safe="")
            if parsed.password is not None:
                credentials += ":" + quote(unquote(parsed.password), safe="")
            credentials += "@"
        return f"postgresql://{credentials}localhost:54329/{database}?sslmode=disable"
    except (ValueError, UnicodeError):
        raise ValueError("only explicit shared test Postgres localhost:54329 without overrides is permitted") from None
