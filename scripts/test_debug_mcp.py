import argparse
import os
import unittest
from unittest.mock import patch

from debug_mcp import debug_environment, public_origin


class DebugMCPTests(unittest.TestCase):
    def test_valid_origin(self):
        self.assertEqual(public_origin("https://example.trycloudflare.com/"), "https://example.trycloudflare.com")

    def test_reject_invalid_origins(self):
        for value in ["http://example.com", "https://localhost", "https://example.com/mcp", "https://user:pass@example.com", "https://example.com?q=x", "https://example.com/#x", "https://example.com:8082", "https://example.com:bad", "https://example.com\n"]:
            with self.subTest(value=value), self.assertRaises(argparse.ArgumentTypeError):
                public_origin(value)

    def test_environment_is_private_and_consistent(self):
        with patch.dict(os.environ, {"JWT_SECRET": "test-only", "MCP_RESOURCE_URL": "https://old.example/mcp"}):
            env = debug_environment("https://debug.example")
            self.assertEqual(env["MCP_RESOURCE_URL"], "https://debug.example/mcp")
            self.assertEqual(env["MCP_OAUTH_ISSUER_URL"], "https://debug.example")
            self.assertEqual(env["JWT_SECRET"], "test-only")
            self.assertGreaterEqual(len(env["MCP_OAUTH_INTERNAL_SECRET"]), 32)
            self.assertNotEqual(env["MCP_OAUTH_INTERNAL_SECRET"], env["MCP_USERNAME_LOOKUP_SECRET"])
            self.assertEqual(os.environ["MCP_RESOURCE_URL"], "https://old.example/mcp")


if __name__ == "__main__":
    unittest.main()
