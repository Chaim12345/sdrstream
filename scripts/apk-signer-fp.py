#!/usr/bin/env python3
"""Pinned APK signer-certificate fingerprint method for sdrstream.

Uses androguard's APK signature parsing (v1/v2/v3) - a hand-rolled block
parser proved unreliable against multi-scheme APKs (mis-read certificates;
retired 2026-10-23). Cross-checked against `apksigner verify --print-certs`.
Prints package, versionCode and every scheme's signer cert digests.

Usage: apk-signer-fp.py <apk> [<apk>...]
Requires: pip install androguard
"""
import hashlib
import logging
import sys

logging.disable(logging.CRITICAL)
from androguard.core.apk import APK  # noqa: E402


def colon(h):
    return ":".join(h[i : i + 2].upper() for i in range(0, len(h), 2))


for path in sys.argv[1:]:
    a = APK(path)
    print(path)
    print("  package:", a.get_package())
    print("  versionCode:", a.get_androidversion_code())
    print("  signed v1/v2/v3:", a.is_signed_v1(), a.is_signed_v2(), a.is_signed_v3())
    for scheme, certs in (("v1", a.get_certificates_v1()), ("v2", a.get_certificates_v2()), ("v3", a.get_certificates_v3())):
        for c in certs or []:
            der = c.dump() if hasattr(c, "dump") else bytes(c)
            print(f"  {scheme} SHA-256:", colon(hashlib.sha256(der).hexdigest()))
