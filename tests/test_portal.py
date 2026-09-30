#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
import base64
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
import _nycu_portal as portal


class PortalRefreshTest(unittest.TestCase):
    def test_timeout_message_reauthenticates_once(self):
        timeout = b'{"status":"false","message":"token timeout."}'
        ok = b'{"status":"true"}'
        with patch.object(portal, "token", return_value=("old", "cache")), patch.object(portal, "_post_form", side_effect=[timeout, ok]), patch.object(portal, "login", return_value="new") as login, patch.object(portal, "_save_token") as save:
            self.assertEqual(portal.api("getUserStatus"), {"status": "true"})
            login.assert_called_once()
            save.assert_called_once_with("new")
            self.assertTrue(portal.last_call_relogged())

    def test_repeated_timeout_does_not_loop(self):
        timeout = b'{"status":"false","message":"token timeout."}'
        with patch.object(portal, "token", return_value=("old", "cache")), patch.object(portal, "_post_form", return_value=timeout) as post, patch.object(portal, "login", return_value="new") as login, patch.object(portal, "_save_token"):
            with self.assertRaises(portal.PortalError): portal.api("getUserStatus")
            login.assert_called_once()
            self.assertEqual(post.call_count, 2)

    def test_expired_jwt_is_not_reused(self):
        payload = base64.urlsafe_b64encode(json.dumps({"exp": 1}).encode()).decode().rstrip("=")
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / "portal.json"
            p.write_text(json.dumps({"token": "header." + payload + ".signature"}))
            with patch.object(portal, "_config_path", return_value=p), patch.object(portal, "login", return_value="fresh") as login, patch.object(portal, "_save_token"):
                self.assertEqual(portal.token(), ("fresh", "login"))
                login.assert_called_once()

    def test_relay_timeout_refreshes_once_before_returning_form(self):
        link = {"syslink": [{"sysURL": "https://timeclock.example.test"}]}
        form = '<form action="https://timeclock.example.test"><input name="jwt" value="synthetic"></form>'
        with patch.object(portal, "api", return_value=link), patch.object(portal, "token", return_value=("cached", "cache")) as token, patch.object(portal, "_post_form", side_effect=[b"token timeout.", form.encode()]), patch.object(portal, "_post_form_full", return_value=(b"landing", "https://timeclock.example.test")):
            _, html, _ = portal.relay("example")
            self.assertEqual(html, "landing")
            self.assertEqual(sum(call.kwargs.get("force") is True for call in token.call_args_list), 1)

    def test_campus_restriction_does_not_reauthenticate(self):
        with patch.object(portal, "api", return_value={"syslink": [{"sysURL": "https://timeclock.example.test"}]}), patch.object(portal, "token", return_value=("cached", "cache")) as token, patch.object(portal, "_post_form", return_value=b"vpn-required"):
            with self.assertRaisesRegex(portal.PortalError, "requires a campus IP"):
                portal.relay("example")
            token.assert_called_once_with()


if __name__ == "__main__": unittest.main()
