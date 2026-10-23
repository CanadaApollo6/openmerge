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
        query = parse_qsl(parsed.query, strict_parsing=True, keep_blank_values=True)
        allowed_ports = (54329,)
        if env.get("GITHUB_ACTIONS") == "true" and env.get("CI") == "true":
            allowed_ports = (54329, 5432)
        valid = (parsed.scheme in ("postgres", "postgresql")
                 and parsed.hostname == "localhost" and parsed.port in allowed_ports
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
        return f"postgresql://{credentials}localhost:{parsed.port}/{database}?sslmode=disable"
    except (ValueError, UnicodeError):
        raise ValueError("only explicit shared test Postgres localhost:54329 is permitted; localhost:5432 requires GitHub Actions CI") from None
