#!/usr/bin/env python3
"""Run the local Compose stack with a public OAuth origin and console logs."""

import argparse
import os
from pathlib import Path
import secrets
import subprocess
from urllib.parse import urlsplit


def public_origin(value):
    try:
        url = urlsplit(value)
        port = url.port
    except ValueError as exc:
        raise argparse.ArgumentTypeError("Use a valid public HTTPS origin") from exc
    if (url.scheme != "https" or not url.hostname or url.username is not None
            or url.password is not None or url.path not in ("", "/")
            or url.query or url.fragment or port not in (None, 443)
            or url.hostname in ("localhost", "127.0.0.1", "::1")
            or any(c.isspace() for c in value)):
        raise argparse.ArgumentTypeError("Use only the public HTTPS origin, without /mcp, credentials, or query parameters")
    return value.rstrip("/")


def debug_environment(origin):
    env = os.environ.copy()
    env.update({
        "MCP_OAUTH_ISSUER_URL": origin,
        "MCP_RESOURCE_URL": origin + "/mcp",
        # Shared across the containers in this invocation, never printed or saved.
        "MCP_OAUTH_INTERNAL_SECRET": secrets.token_urlsafe(48),
        "MCP_USERNAME_LOOKUP_SECRET": secrets.token_urlsafe(48),
    })
    return env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("origin", type=public_origin, help="HTTPS origin printed by your tunnel")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    if not (root / ".env").is_file():
        parser.error("Create .env from .env.example and configure local secrets first")
    print("Starting LOCAL data stack. Do not configure production database credentials.", flush=True)
    print("Claude connector URL: " + args.origin + "/mcp", flush=True)
    print("Watch for 'connector request completed' events; do not share other raw logs.", flush=True)
    try:
        result = subprocess.run(
            ["docker", "compose", "--profile", "app", "up", "--build"],
            cwd=root, env=debug_environment(args.origin), check=False,
        )
        return result.returncode
    except FileNotFoundError:
        parser.error("Docker Compose is required")
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
