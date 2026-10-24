"""Focused operator-class sweep for PR 10b-2b; run explicitly, not in CI."""
import json
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).parent
SOURCE = (ROOT / 'verify-audit-anchor.py').read_text()
# Each replacement is a single-site mutation. Equivalent cases are explicit.
CASES = [
    ('directory guard deleted', '    require(not directory.is_symlink() and directory.is_dir())', '', None),
    ('directory symlink guard deleted', 'not directory.is_symlink() and directory.is_dir()', 'directory.is_dir()', None),
    ('directory conjunction weakened', 'not directory.is_symlink() and directory.is_dir()', 'not directory.is_symlink() or directory.is_dir()', None),
    ('directory symlink negation', 'not directory.is_symlink()', 'directory.is_symlink()', None),
    ('directory type guard deleted', 'not directory.is_symlink() and directory.is_dir()', 'not directory.is_symlink()', 'Reading directory/manifest.json still rejects a non-directory path.'),
    ('hash syntax validation deleted', "digest(manifest['selection_sha256'])", "manifest['selection_sha256']", 'Exact lowercase hexdigest equality rejects every noncanonical digest too.'),
    ('hash field changed', "manifest['selection_sha256']", "manifest['manifest_sha256']", None),
    ('hash update deleted', '            selection_hash.update(raw)', '', None),
    ('hash excludes LF', 'selection_hash.update(raw)', 'selection_hash.update(raw[:-1])', None),
    ('hash algorithm changed', 'hashlib.sha256()', 'hashlib.sha512()', None),
    ('hash check deleted', '    require(selection_hash.hexdigest() == expected)', '', None),
    ('hash comparison inverted', 'selection_hash.hexdigest() == expected', 'selection_hash.hexdigest() != expected', None),
    ('hash comparison forced true', 'selection_hash.hexdigest() == expected', 'True', None),
    ('hash comparison forced false', 'selection_hash.hexdigest() == expected', 'False', None),
    ('CRLF check deleted', "            require(not raw.endswith(b'\\r\\n'))", '', None),
    ('CRLF negation deleted', "not raw.endswith(b'\\r\\n')", "raw.endswith(b'\\r\\n')", None),
    ('CRLF suffix changed', "not raw.endswith(b'\\r\\n')", "not raw.endswith(b'\\n')", None),
    ('message tiebreak deleted', 'ORDER BY q,id,message', 'ORDER BY q,id', None),
    ('message tiebreak reversed', 'ORDER BY q,id,message', 'ORDER BY q,id,message DESC', None),
]


def main():
    results = []
    with tempfile.TemporaryDirectory(prefix='anchor-mutants-') as temporary:
        root = Path(temporary)
        test = root / 'test_verify_audit_anchor.py'
        test.write_bytes((ROOT / test.name).read_bytes())
        mutant = root / 'verify-audit-anchor.py'
        for name, old, new, equivalent in CASES:
            assert SOURCE.count(old) == 1, (name, SOURCE.count(old))
            changed = SOURCE.replace(old, new)
            compile(changed, str(mutant), 'exec')
            mutant.write_text(changed)
            run = subprocess.run([sys.executable, str(test)], capture_output=True, text=True)
            killed = run.returncode != 0
            assert killed == (equivalent is None), (name, run.stdout, run.stderr)
            results.append(dict(mutation=name, result='killed' if killed else 'equivalent', reason=equivalent))
    print(json.dumps(results, indent=2))


if __name__ == '__main__':
    main()
