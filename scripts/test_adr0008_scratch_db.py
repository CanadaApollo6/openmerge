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

    def test_ci_port(self):
        ci_url = BASE.replace(":54329", ":5432")
        self.assertRaises(ValueError, scratch_url, ci_url, environ={})
        self.assertRaises(ValueError, scratch_url, ci_url, environ={"GITHUB_ACTIONS": "true"})
        self.assertRaises(ValueError, scratch_url, ci_url, environ={"CI": "true"})
        self.assertEqual(scratch_url(ci_url, environ={"GITHUB_ACTIONS": "true", "CI": "true"}),
                         "postgresql://postgres@localhost:5432/postgres?sslmode=disable")
        for value in ("1", "TRUE", "True", "yes", ""):
            self.assertRaises(ValueError, scratch_url, ci_url, environ={"GITHUB_ACTIONS": value, "CI": "true"})
            self.assertRaises(ValueError, scratch_url, ci_url, environ={"GITHUB_ACTIONS": "true", "CI": value})

    def test_ci_mode_keeps_every_bypass_rejection(self):
        ci = {"GITHUB_ACTIONS": "true", "CI": "true"}
        for base in (BASE, BASE.replace(":54329", ":5432")):
            for query in ("host=/tmp/review-nonexistent", "hostaddr=127.0.0.1", "port=54329", "dbname=production_example",
                          "service=live", "servicefile=/tmp/service", "options=-csearch_path=public",
                          "target_session_attrs=read-write", "%68ost=/tmp/socket", "sslmode=require", "sslmode=disable&host=localhost"):
                with self.subTest(base=base, query=query):
                    self.assertRaises(ValueError, scratch_url, base + "?" + query, environ=ci)
            for key in ("PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGSERVICE", "PGSERVICEFILE", "PGOPTIONS",
                        "PGTARGETSESSIONATTRS", "PGPASSWORD", "pgport"):
                with self.subTest(base=base, env=key):
                    self.assertRaises(ValueError, scratch_url, base, environ={**ci, key: "override"})
        for host in ("127.0.0.1:5432", "localhost:5433", "db:5432", "%2Ftmp:5432", "localhost:5432,other:5432"):
            with self.subTest(host=host):
                self.assertRaises(ValueError, scratch_url, BASE.replace("localhost:54329", host), environ=ci)
        self.assertRaises(ValueError, scratch_url, BASE, "production", environ=ci)

    def test_bypasses_refused_before_subprocess(self):
        attacks = [BASE + "?" + query for query in (
            "host=/tmp/review-nonexistent&port=5432&dbname=production_example",
            "host=localhost", "hostaddr=127.0.0.1", "port=5432", "dbname=production_example",
            "service=live", "servicefile=/tmp/service", "options=-csearch_path=public",
            "target_session_attrs=read-write", "host=", "hostaddr=", "port=", "dbname=", "service=", "options=", "target_session_attrs=", "%68ost=/tmp/socket", "sslmode=disable&host=localhost",
        )] + [BASE.replace("localhost:54329", host) for host in ("localhost:5433", "%2Ftmp:54329", "localhost:54329,other:5432")]
        attacks.append(BASE.replace(":54329", ":5432"))
        env_attacks = ["PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGSERVICE", "PGSERVICEFILE",
                       "PGOPTIONS", "PGTARGETSESSIONATTRS", "PGPASSWORD"]
        for driver in ("check-adr0008-9c-2c-restore.py", "verify-adr0008-9c-2c.py", "verify-adr0008-9c-2d.py", "verify-adr0008-9c-3.py", "verify-adr0008-10a-2a.py"):
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
