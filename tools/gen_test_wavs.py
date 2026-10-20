#!/usr/bin/env python3
"""Generate synthetic AFSK/APRS and Morse WAVs for decoder tests.

Usage: python3 tools/gen_test_wavs.py [rate]  (default 22050)
"""
import math
import random
import struct
import sys

RATE = int(sys.argv[1]) if len(sys.argv) > 1 else 22050
random.seed(42)


def write_wav(path, samples):
    with open(path, "wb") as f:
        data = b"".join(struct.pack("<h", max(-32768, min(32767, int(s * 20000)))) for s in samples)
        f.write(b"RIFF")
        f.write(struct.pack("<I", 36 + len(data)))
        f.write(b"WAVEfmt ")
        f.write(struct.pack("<IHHIIHH", 16, 1, 1, RATE, RATE * 2, 2, 16))
        f.write(b"data")
        f.write(struct.pack("<I", len(data)))
        f.write(data)


def afsk_bit(bit, phase):
    freq = 2200.0 if bit else 1200.0
    return 2 * math.pi * freq / RATE, phase


def ax25_addr(call, ssid, last=False):
    """Encode one AX.25 address field: 6 chars shifted left one bit, plus a
    seventh byte holding the SSID and the end-of-address flag."""
    call = call.upper().ljust(6)[:6]
    out = bytearray()
    for ch in call:
        out.append(ord(ch) << 1)
    out.append((ssid << 1) | 0x60 | (1 if last else 0))
    return bytes(out)


def ax25_frame(dest, dssid, src, sssid, info):
    """A complete AX.25 UI frame body (no flags, no bit-stuffing)."""
    return ax25_addr(dest, dssid) + ax25_addr(src, sssid, last=True) + bytes([0x03, 0xF0]) + info


def hdlc_frame(payload):
    """Build an AX.25 HDLC bit stream: flags, payload, CRC, flags."""
    def crc16(data):
        crc = 0xFFFF
        for byte in data:
            crc ^= byte << 8
            for _ in range(8):
                crc = ((crc << 1) ^ 0x1021) & 0xFFFF if crc & 0x8000 else (crc << 1) & 0xFFFF
        return crc ^ 0xFFFF

    crc = crc16(payload)
    body = payload + bytes([crc & 0xFF, crc >> 8])
    bits = []
    for byte in body:
        for i in range(8):
            bits.append((byte >> i) & 1)
    # Bit-stuff after five consecutive 1s.
    stuffed = []
    ones = 0
    for b in bits:
        stuffed.append(b)
        if b:
            ones += 1
            if ones == 5:
                stuffed.append(0)
                ones = 0
        else:
            ones = 0
    flag = [0, 1, 1, 1, 1, 1, 1, 0]
    return flag * 20 + stuffed + flag * 4


def nrzi(bits):
    """NRZI encode: 0 bit = transition, 1 bit = hold."""
    out = []
    state = 1
    for b in bits:
        if b == 0:
            state ^= 1
        out.append(state)
    return out


def make_afsk(messages):
    """Render AFSK1200: 2200 Hz for a 1, 1200 Hz for a 0, at exact 1200 baud."""
    samples = []
    phase = 0.0
    samples_per_bit = RATE / 1200.0
    bit_index = 0
    for msg in messages:
        bits = hdlc_frame(msg)
        tones = nrzi(bits)
        for tone in tones:
            # Sample this bit by walking a global sample clock so the
            # fractional 18.375 samples/bit at 22050 Hz never accumulates.
            start = bit_index * samples_per_bit
            end = (bit_index + 1) * samples_per_bit
            n = int(round(end)) - int(round(start))
            step = 2 * math.pi * (1200.0 if tone else 2200.0) / RATE
            for _ in range(n):
                samples.append(0.6 * math.sin(phase))
                phase += step
            bit_index += 1
        samples.extend([0.0] * int(RATE * 0.3))
    return [s + random.gauss(0, 0.02) for s in samples]


MORSE = {
    "A": ".-", "B": "-...", "C": "-.-.", "D": "-..", "E": ".", "F": "..-.",
    "G": "--.", "H": "....", "I": "..", "J": ".---", "K": "-.-", "L": ".-..",
    "M": "--", "N": "-.", "O": "---", "P": ".--.", "Q": "--.-", "R": ".-.",
    "S": "...", "T": "-", "U": "..-", "V": "...-", "W": ".--", "X": "-..-",
    "Y": "-.--", "Z": "--..", "0": "-----", "1": ".----", "2": "..---",
    "3": "...--", "4": "....-", "5": ".....", "6": "-....", "7": "--...",
    "8": "---..", "9": "----.",
}


def make_morse(text, tone=700.0):
    unit = 0.06
    samples = []
    phase = 0.0
    for ch in text:
        if ch == " ":
            samples.extend([0.0] * int(RATE * unit * 3))
            continue
        for sym in MORSE.get(ch.upper(), ""):
            dur = unit * (3 if sym == "-" else 1)
            for _ in range(int(RATE * dur)):
                samples.append(0.7 * math.sin(phase))
                phase += 2 * math.pi * tone / RATE
            samples.extend([0.0] * int(RATE * unit))
        samples.extend([0.0] * int(RATE * unit * 2))
    return [s + random.gauss(0, 0.02) for s in samples]


if __name__ == "__main__":
    write_wav("/tmp/test_aprs.wav", make_afsk([
        ax25_frame("APRS", 0, "M0XXX", 9, b">Hello from space! test 001"),
        ax25_frame("APRS", 0, "GB3RS", 7, b">second packet 002"),
    ]))
    write_wav("/tmp/test_morse.wav", make_morse("CQ DE RS44 SAT"))
    print(f"wrote /tmp/test_aprs.wav and /tmp/test_morse.wav at {RATE} Hz")
