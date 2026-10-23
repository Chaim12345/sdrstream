#!/usr/bin/env python3
"""Pinned APK v2 signer-certificate fingerprint extractor.

Every signer comparison for sdrstream builds MUST use this script so all
reported fingerprints are computed by one pinned method. Prints the
SHA-256 (and SHA-1/MD5 for cross-tool checks) of the first signer's
signing certificate, byte-pair colon format, matching `apksigner verify
--print-certs` output.

Usage: apk-signer-fp.py <apk> [<apk>...]
"""
import hashlib
import struct
import sys

V2_BLOCK_ID = 0x7109871A
MAGIC = b"APK Sig Block 42"


def first_signer_cert(path):
    data = open(path, "rb").read()
    eocd = data.rfind(b"PK\x05\x06")
    if eocd < 0:
        raise SystemExit(f"{path}: no ZIP end-of-central-directory")
    cd_off = struct.unpack("<I", data[eocd + 16 : eocd + 20])[0]
    if data[cd_off - 16 : cd_off] != MAGIC:
        raise SystemExit(f"{path}: no APK Signature Scheme v2 block")
    size = struct.unpack("<Q", data[cd_off - 24 : cd_off - 16])[0]
    pos, end = cd_off - 8 - size + 8, cd_off - 24
    while pos < end:
        plen = struct.unpack("<Q", data[pos : pos + 8])[0]
        pid = struct.unpack("<I", data[pos + 8 : pos + 12])[0]
        if pid == V2_BLOCK_ID:
            val = data[pos + 12 : pos + 8 + plen]
            slen = struct.unpack("<I", val[:4])[0]
            signer = val[4 : 4 + slen]
            sdlen = struct.unpack("<I", signer[:4])[0]
            sd = signer[4 : 4 + sdlen]
            dlen = struct.unpack("<I", sd[:4])[0]
            r = 4 + dlen
            clen = struct.unpack("<I", sd[r : r + 4])[0]
            certs = sd[r + 4 : r + 4 + clen]
            clen2 = struct.unpack("<I", certs[:4])[0]
            return certs[4 : 4 + clen2]
        pos += 8 + plen
    raise SystemExit(f"{path}: v2 block has no signer")


def colon(h):
    return ":".join(h[i : i + 2] for i in range(0, len(h), 2))


for apk in sys.argv[1:]:
    cert = first_signer_cert(apk)
    print(apk)
    print("  SHA-256:", colon(hashlib.sha256(cert).hexdigest().upper()))
    print("  SHA-1:  ", colon(hashlib.sha1(cert).hexdigest().upper()))
    print("  MD5:    ", colon(hashlib.md5(cert).hexdigest().upper()))
