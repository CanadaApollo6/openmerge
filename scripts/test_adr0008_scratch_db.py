#!/usr/bin/env python3
"""Connection-free bypass regressions for scratch driver entry points."""
import importlib.util
import os
from pathlib import Path
import sys
import unittest
from unittest.mock import patch


sys.dont_write_bytecode = True
from adr0008_scratch_db import scratch_url

BASE = "postgres://postgres@localhost:54329/postgres"


class ScratchGuardTest(unittest.TestCase):
    def test_rebuilt_destination(self):
        self.assertEqual(scratch_url(BASE + "?sslmode=disable", environ={}),
                         "postgresql://postgres@localhost:54329/postgres?sslmode=disable")
        self.assertEqual(scratch_url(BASE.rsplit("/", 1)[0] + "/ignored", environ={}),
                         scratch_url(BASE, environ={}))

    def test_bypasses_refused_before_subprocess(self):
        attacks = [BASE + "?" + query for query in (
            "host=/tmp/review-nonexistent&port=5432&dbname=production_example",
            "host=localhost", "hostaddr=127.0.0.1", "port=5432", "dbname=production_example",
            "service=live", "servicefile=/tmp/service", "options=-csearch_path=public",
            "target_session_attrs=read-write", "host=", "hostaddr=", "port=", "dbname=", "service=", "options=", "target_session_attrs=", "%68ost=/tmp/socket", "sslmode=disable&host=localhost",
        )] + [BASE.replace("localhost:54329", host) for host in ("localhost:5432", "%2Ftmp:54329", "localhost:54329,other:5432")]
        env_attacks = ["PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGSERVICE", "PGSERVICEFILE",
                       "PGOPTIONS", "PGTARGETSESSIONATTRS", "PGPASSWORD"]
        for driver in ("check-adr0008-9c-2c-restore.py", "verify-adr0008-9c-2c.py", "verify-adr0008-9c-2d.py"):
            spec = importlib.util.spec_from_file_location("driver", Path(__file__).with_name(driver))
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            args = [driver, "--out", str(Path.home() / ".cache/guard-self-test")]
            args += ["--binary", "bin/openmerge"] if driver.startswith("check-") else ["--baseline", "."]
            for raw, extra in [(raw, {}) for raw in attacks] + [(BASE, {key: "override"}) for key in env_attacks]:
                with self.subTest(driver=driver, raw=raw, env=extra), patch.dict(os.environ, {"OPENMERGE_TEST_DATABASE_URL": raw, **extra}, clear=True), patch.object(sys, "argv", args), patch("subprocess.run") as run, patch("subprocess.check_output") as output:
                    with self.assertRaises(ValueError):
                        module.main()
                    run.assert_not_called()
                    output.assert_not_called()


if __name__ == "__main__":
    unittest.main()
