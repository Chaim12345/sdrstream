#!/usr/bin/env bash
# Orchestrates the two QA harnesses and owns the job verdict.
# The emulator-runner executes each script: line as a SEPARATE sh -c call, so
# shell variables cannot carry state between workflow lines - all verdict
# logic must live in this single bash process. Usage: android-ui-qa-run.sh <apk>
set -u
APK="${1:-fat.apk}"
QA1=0; QA2=0
timeout 700 bash -x qa-src/qa/android-ui-qa.sh "$APK" ui > /tmp/qa1.log 2>&1 || QA1=$?
cat /tmp/qa1.log | tee -a qa-results-trace.txt
if [ "$QA1" -eq 0 ]; then
  timeout 600 bash qa-src/qa/android-func-audit.sh cgo1 200 "$APK" > /tmp/qa2.log 2>&1 || QA2=$?
  cat /tmp/qa2.log | tee -a qa-results-trace.txt
else
  echo "skipping android-func-audit.sh: ui-qa failed (exit $QA1); device state unusable" | tee -a qa-results-trace.txt
fi

# Emulator crash evidence (host side): the emulator-runner keeps a sqlite crash
# db; a process-level qemu death is recorded there when the runner log is not.
mkdir -p qa-results
python3 - <<'PY' > qa-results/emu-crash-summary.txt 2>&1
import glob, os, sqlite3, traceback
dbs = sorted(glob.glob('/tmp/android-runner/emu-crash-*.db'))
if not dbs:
    print("no emu-crash db found under /tmp/android-runner")
for db in dbs:
    print("== %s (%d bytes, mtime %s) ==" % (db, os.path.getsize(db), __import__('datetime').datetime.utcfromtimestamp(os.path.getmtime(db)).isoformat()+'Z'))
    try:
        con = sqlite3.connect('file:%s?mode=ro' % db, uri=True)
        tables = [r[0] for r in con.execute("SELECT name FROM sqlite_master WHERE type='table'")]
        print("tables:", tables)
        for t in tables:
            try:
                n = con.execute('SELECT COUNT(*) FROM "%s"' % t).fetchone()[0]
                print("table %s rows=%d" % (t, n))
                for row in con.execute('SELECT * FROM "%s" ORDER BY rowid DESC LIMIT 5' % t):
                    print(str(row)[:2000])
            except Exception as e:
                print("table %s unreadable: %s" % (t, e))
        con.close()
    except Exception:
        traceback.print_exc()
PY

# Redaction BEFORE artifacts upload: device-generated passwords and any token
# patterns must not land in public artifacts. The secret values arrive via
# /tmp/qa-redact.list (mode 600, written by android-ui-qa.sh, deleted here).
python3 - <<'PY'
import glob, re
secrets = []
try:
    secrets = [l.rstrip('\n') for l in open('/tmp/qa-redact.list') if l.strip()]
except FileNotFoundError:
    pass
tok = re.compile(r'(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}')
files = ['qa-results-trace.txt'] + glob.glob('qa-results/**/*.txt', recursive=True) + glob.glob('qa-results/**/*.xml', recursive=True)
hits = 0
for f in files:
    try:
        data = open(f, errors='ignore').read()
    except OSError:
        continue
    orig = data
    for s in secrets:
        data = data.replace(s, '<pw>')
    data = tok.sub('<token>', data)
    if data != orig:
        open(f, 'w').write(data); hits += 1
print("redaction: %d listed secret(s), scrubbed %d of %d text/xml files" % (len(secrets), hits, len(files)))
PY
rm -f /tmp/qa-redact.list

echo "verdict: android-ui-qa.sh exit=$QA1 (gated); android-func-audit.sh exit=$QA2 (informational - no verdict semantics in that script yet)"
[ "$QA1" -eq 0 ]
