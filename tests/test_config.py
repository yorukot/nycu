#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "lib"))
from _paths import config_path, setting
spec = importlib.util.spec_from_file_location("migrate_config", ROOT / "scripts/migrate_config.py")
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class ConfigTest(unittest.TestCase):
    def test_path_precedence(self):
        env = {"XDG_CONFIG_HOME": "/example/config"}
        self.assertEqual(config_path("portal", env), Path("/example/config/nycu/portal.json"))
        self.assertEqual(config_path("e3p", env), Path("/example/config/nycu/e3p.json"))
        env.update(UTILS_NYCU_CONFIG="/legacy/portal", UTILS_E3P_CONFIG="/legacy/e3p")
        self.assertEqual(config_path("portal", env), Path("/legacy/portal"))
        self.assertEqual(config_path("e3p", env), Path("/legacy/e3p"))
        env.update(NYCU_PORTAL_CONFIG="/new/portal", NYCU_E3P_CONFIG="/new/e3p")
        self.assertEqual(config_path("portal", env), Path("/new/portal"))
        self.assertEqual(config_path("e3p", env), Path("/new/e3p"))

    def test_legacy_token_override(self):
        self.assertEqual(setting("TOKEN", env={"UTILS_E3P_TOKEN": "legacy"}), "legacy")
        self.assertEqual(setting("TOKEN", env={"UTILS_E3P_TOKEN": "legacy", "NYCU_E3P_TOKEN": "new"}), "new")

    def test_migration_preserves_data_and_permissions_without_resurrecting_logout(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            env = {"XDG_CONFIG_HOME": tmp}
            (root / "utils").mkdir()
            data = b'{"token":"synthetic-test-token","userid":123}'
            for name in ["nycu", "e3p"]: (root / "utils" / (name + ".json")).write_bytes(data)
            results = migration.migrate(env)
            self.assertEqual([r["status"] for r in results], ["migrated", "migrated"])
            self.assertNotIn("synthetic-test-token", json.dumps(results))
            for service in ["portal", "e3p"]:
                file = config_path(service, env)
                self.assertEqual(file.read_bytes(), data)
                self.assertEqual(file.stat().st_mode & 0o777, 0o600)
            backups = list((root / "nycu/migration-backups").iterdir())
            self.assertEqual(len(backups), 2)
            self.assertTrue(all(p.stat().st_mode & 0o777 == 0o600 for p in backups))
            migration.migrate(env)
            self.assertEqual(len(list((root / "nycu/migration-backups").iterdir())), 2)
            config_path("e3p", env).unlink()
            migration.migrate(env)
            self.assertFalse(config_path("e3p", env).exists())

    def test_explicit_paths_are_not_moved(self):
        with tempfile.TemporaryDirectory() as tmp:
            result = migration.migrate({"XDG_CONFIG_HOME": tmp, "UTILS_E3P_CONFIG": "/custom/e3p", "NYCU_PORTAL_CONFIG": "/custom/portal"})
            self.assertTrue(all(r["status"] == "explicit path retained" for r in result))


if __name__ == "__main__": unittest.main()
