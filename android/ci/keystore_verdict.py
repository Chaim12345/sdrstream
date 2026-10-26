#!/usr/bin/env python3
"""Verdict for the Keystore instrumented run. Usage: keystore_verdict.py fixed|baseline <gradle_rc>

Reads the instrumentation JUnit XML. An unrelated build, dependency or
emulator failure (no XML, wrong tests, other failures) is NEVER proof.
"""
import glob, sys, xml.etree.ElementTree as ET

variant, rc = sys.argv[1], int(sys.argv[2])
MSG = "put must succeed on a real AndroidKeyStore"
CLS = "com.chaim12345.sdrstream.SecretStoreInstrumentedTest"
POL = "com.chaim12345.sdrstream.KeystoreIvPolicyInstrumentedTest"
REQUIRED = {CLS: {"keystoreIsAvailable", "putGetClearRoundTrip", "plaintextNeverInPrefs"},
            POL: {"callerIvRejectedByKeystore", "providerIvAccepted"}}
BASELINE_MAY_FAIL = {(CLS, "putGetClearRoundTrip"), (CLS, "plaintextNeverInPrefs")}

def die(m):
    print("VERDICT FAIL:", m); sys.exit(1)

files = glob.glob("android/app/build/outputs/androidTest-results/**/TEST-*.xml", recursive=True)
if not files:
    die("no instrumentation XML found: unrelated build/emulator failure is not proof")
print("xml:", files)
cases = {}
for f in files:
    for tc in ET.parse(f).getroot().iter("testcase"):
        bad = tc.find("failure") if tc.find("failure") is not None else tc.find("error")
        skipped = tc.find("skipped") is not None
        msg = ""
        if bad is not None:
            msg = (bad.get("message") or "") + (bad.text or "")
        cases[(tc.get("classname"), tc.get("name"))] = ("skipped" if skipped else "fail" if bad is not None else "pass", msg)
for k, v in sorted(cases.items()):
    print(v[0], k[0].split(".")[-1], k[1])
for cls, names in REQUIRED.items():
    for n in names:
        if (cls, n) not in cases:
            die(f"required test did not execute: {cls}.{n}")
        if cases[(cls, n)][0] == "skipped":
            die(f"required test skipped: {cls}.{n}")
extra = [k for k in cases if k[0] not in REQUIRED]
fails = {k for k, v in cases.items() if v[0] == "fail"}
if variant == "fixed":
    if rc != 0: die(f"gradle rc={rc}")
    if fails: die(f"failures on fixed code: {sorted(fails)}")
    print("VERDICT OK: fixed passes all", len(cases), "tests incl. IV policy evidence")
else:
    if rc == 0: die("baseline unexpectedly passed")
    if (CLS, "putGetClearRoundTrip") not in fails:
        die("baseline did not fail putGetClearRoundTrip")
    for k in fails:
        if k not in BASELINE_MAY_FAIL: die(f"unexpected baseline failure: {k}")
        if MSG not in cases[k][1]: die(f"baseline failure for {k} lacks expected message")
    # policy tests must pass on baseline too: they prove the platform behaviour independent of the code under test
    print("VERDICT OK: baseline fails only", sorted(k[1] for k in fails), "with the put-must-succeed assertion; policy tests passed")
