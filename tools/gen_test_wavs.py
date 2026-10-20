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

# PSK31 Varicode: character -> bit pattern (MSB first). Space is "1".
VARICODE = {
" ": "1",
"!": "111111111",
""": "101011111",
"#": "111110101",
"$": "111011011",
"%": "1011010101",
"&": "1010111011",
""": "101111111",
"(": "11111011",
")": "11110111",
"*": "101101111",
"+": "111011111",
",": "1110101",
"-": "110101",
".": "1010111",
"/": "110101111",
"0": "10110111",
"1": "10111101",
"2": "11101101",
"3": "11111111",
"4": "101110111",
"5": "101011011",
"6": "101101011",
"7": "110101101",
"8": "110101011",
"9": "110110111",
":": "11110101",
";": "110111101",
"<": "111101101",
"=": "1010101",
">": "111010111",
"?": "1010101111",
"@": "1010111101",
"A": "1111101",
"B": "11101011",
"C": "10101101",
"D": "10110101",
"E": "1110111",
"F": "11011011",
"G": "11111101",
"H": "101010101",
"I": "1111111",
"J": "111111101",
"K": "101111101",
"L": "11010111",
"M": "10111011",
"N": "11011101",
"O": "10101011",
"P": "11010101",
"Q": "111011101",
"R": "10101111",
"S": "1101111",
"T": "1101101",
"U": "101010111",
"V": "110110101",
"W": "101011101",
"X": "101110101",
"Y": "101111011",
"Z": "1010101101",
"[": "111110111",
"\\": "111101111",
"]": "111111011",
"^": "1010111111",
"_": "101101101",
"`": "1011011111",
"a": "1011",
"b": "1011111",
"c": "101111",
"d": "101101",
"e": "11",
"f": "111101",
"g": "1011011",
"h": "101011",
"i": "1101",
"j": "111101011",
"k": "10111111",
"l": "11011",
"m": "111011",
"n": "1111",
"o": "111",
"p": "111111",
"q": "110111111",
"r": "10101",
"s": "10111",
"t": "101",
"u": "110111",
"v": "1111011",
"w": "1101011",
"x": "11011111",
"y": "1011101",
"z": "111010101",
"{": "1010110111",
"|": "110111011",
"}": "1010110101",
"~": "1011010111",
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


DTMF_ROWS = {0: 697, 1: 770, 2: 852, 3: 941}
DTMF_COLS = {0: 1209, 1: 1336, 2: 1477, 3: 1633}
DTMF_KEY = ["123A", "456B", "789C", "*0#D"]


def make_dtmf(digits, tone_len=0.1, gap=0.05):
    """Render DTMF: each digit is one row tone plus one column tone."""
    samples = []
    for ch in digits:
        row = col = None
        for r in range(4):
            for c in range(4):
                if DTMF_KEY[r][c] == ch:
                    row, col = r, c
        if row is None:
            continue
        n = int(RATE * tone_len)
        for j in range(n):
            samples.append(0.4 * (math.sin(2 * math.pi * DTMF_ROWS[row] * j / RATE)
                                  + math.sin(2 * math.pi * DTMF_COLS[col] * j / RATE)))
        samples.extend([0.0] * int(RATE * gap))
    return [s + random.gauss(0, 0.01) for s in samples]


ITA2_LTRS = {0x00: None, 0x01: "E", 0x02: "\n", 0x03: "A", 0x04: " ", 0x05: "S",
             0x06: "I", 0x07: "U", 0x08: "\r", 0x09: "D", 0x0A: "R", 0x0B: "J",
             0x0C: "N", 0x0D: "F", 0x0E: "C", 0x0F: "K", 0x10: "T", 0x11: "Z",
             0x12: "L", 0x13: "W", 0x14: "H", 0x15: "Y", 0x16: "P", 0x17: "Q",
             0x18: "O", 0x19: "B", 0x1A: "G", 0x1C: "M", 0x1D: "X", 0x1E: "V"}
ITA2_FIGS = {0x01: "3", 0x03: "-", 0x04: " ", 0x05: "'", 0x06: "8", 0x07: "7",
             0x09: "$", 0x0A: "4", 0x0C: ",", 0x0D: "!", 0x0E: ":", 0x0F: "(",
             0x10: "5", 0x11: '"', 0x12: ")", 0x13: "2", 0x14: "#", 0x15: "6",
             0x16: "0", 0x17: "1", 0x18: "9", 0x19: "?", 0x1A: "&", 0x1C: ".",
             0x1D: "/", 0x1E: ";"}


def make_rtty(text, mark=2125.0, shift=170.0, baud=45.45):
    """Render standard RTTY: start bit (space), 5 data bits LSB first, stop."""
    space = mark - shift

    def char(code):
        out = [0]  # start bit
        for b in range(5):
            out.append((code >> b) & 1)
        out.extend([1, 1])  # two stop bits
        return out

    bits = [1] * 16  # idle marks so the receiver can lock before the first char
    figures = False
    for ch in text.upper():
        code = next((k for k, v in ITA2_LTRS.items() if v == ch), None)
        if code is not None:
            if figures:
                bits += char(0x1F)  # LTRS
                figures = False
            bits += char(code)
            continue
        code = next((k for k, v in ITA2_FIGS.items() if v == ch), None)
        if code is not None:
            if not figures:
                bits += char(0x1B)  # FIGS
                figures = True
            bits += char(code)
    samples = []
    phase = 0.0
    spb = RATE / baud
    idx = 0
    for bit in bits:
        f = mark if bit else space
        n = int(round((idx + 1) * spb)) - int(round(idx * spb))
        for _ in range(n):
            samples.append(0.6 * math.sin(phase))
            phase += 2 * math.pi * f / RATE
        idx += 1
    return [s + random.gauss(0, 0.02) for s in samples]


def make_eas(header="ZCZC-WXR-TOR-039173+0045-1421800-KCLE/NWS-", eom=True):
    """Render an EAS/SAME burst: 0xAB preamble then 8N1 ASCII at 520.83 baud."""
    mark, space = 2083.3, 1562.5
    baud = 520.83333

    def byte(b):
        out = [0]  # start bit (space)
        for i in range(8):
            out.append((b >> i) & 1)  # data bits LSB first
        out.append(1)  # stop bit (mark)
        return out

    bits = []
    for _ in range(16):
        bits += byte(0xAB)
    for ch in header:
        bits += byte(ord(ch) & 0xFF)
    if eom:
        for _ in range(16):
            bits += byte(0xAB)
        for ch in "NNNN":
            bits += byte(ord(ch) & 0xFF)
    samples = []
    phase = 0.0
    spb = RATE / baud
    idx = 0
    for bit in bits:
        f = mark if bit else space
        n = int(round((idx + 1) * spb)) - int(round(idx * spb))
        for _ in range(n):
            samples.append(0.6 * math.sin(phase))
            phase += 2 * math.pi * f / RATE
        idx += 1
    return [s + random.gauss(0, 0.02) for s in samples]


def make_sstv_vis(vis, mode_name="test"):
    """Render an SSTV VIS header: leader, break, start, 7 data bits, parity."""
    def tone(f, sec):
        out = []
        ph = 0.0
        for _ in range(int(RATE * sec)):
            out.append(0.6 * math.sin(ph))
            ph += 2 * math.pi * f / RATE
        return out
    samples = tone(1200.0, 0.3)   # leader
    samples += tone(1900.0, 0.01)  # break
    samples += tone(1100.0, 0.03)  # start bit (0)
    ones = 0
    for i in range(7):
        b = (vis >> i) & 1
        ones += b
        samples += tone(1300.0 if b else 1100.0, 0.03)
    samples += tone(1300.0 if ones % 2 else 1100.0, 0.03)  # even parity
    samples += tone(1200.0, 0.03)  # stop
    return [s + random.gauss(0, 0.01) for s in samples]


def _pocsag_parity_table():
    """10-bit BCH parity contributed by each of the 21 data bits."""
    poly = 0x769  # x^10+x^9+x^8+x^6+x^5+x^3+1
    tbl = []
    for databit in range(21):
        shreg = 1 << (databit + 10)
        for i in range(20, -1, -1):
            if shreg & (1 << (i + 10)):
                shreg ^= poly << i
        tbl.append(shreg & 0x3FF)
    return tbl


_POCSAG_PARITY = _pocsag_parity_table()


def pocsag_encode(data):
    """Encode a 21-bit data value into a 32-bit POCSAG codeword.

    Layout: data[31:11], BCH parity[10:1], even parity[0]. This mirrors
    multimon-ng's bch_pocsag_encode so the fixture matches the reference.
    """
    d = data & 0x1FFFFF
    parity = 0
    for i in range(21):
        if d & (1 << i):
            parity ^= _POCSAG_PARITY[i]
    cw = (d << 11) | (parity << 1)
    if bin(cw).count("1") % 2:
        cw |= 1
    return cw


def make_pocsag(address, message, function=3, baud=1200, numeric=False):
    """Render a POCSAG page: preamble, sync word, address and data words.

    POCSAG transmits only the top 18 bits of the address in the address
    codeword; the low 3 bits are implied by which of the eight frames the
    codeword sits in. This generator places the codeword accordingly, as a
    real pager would.
    """
    sync = 0x7CD215D8
    idle = 0x7A89C197
    # 21-bit data value = (top 18 address bits << 2) | function.
    addr_cw = pocsag_encode(((address >> 3) & 0x3FFFF) << 2 | (function & 3))
    if numeric:
        digits = "084 2.6]195-3U7["
        bits20 = []
        for ch in message:
            v = digits.index(ch) if ch in digits else 3
            for b in range(3, -1, -1):
                bits20.append((v >> b) & 1)
    else:
        bits20 = []
        for ch in message:
            v = ord(ch) & 0x7F
            rev = int(format(v, "07b")[::-1], 2)  # 7 bits, LSB first
            for b in range(6, -1, -1):
                bits20.append((rev >> b) & 1)
    while len(bits20) % 20:
        bits20.append(0)
    words = []
    for i in range(0, len(bits20), 20):
        val = 0
        for b in bits20[i:i + 20]:
            val = val << 1 | b
        words.append(pocsag_encode((1 << 20) | val))
    # The batch has 16 codeword slots (8 frames x 2). Put the address word in
    # the frame selected by the address's low three bits, then the message.
    # Messages longer than one batch continue in following batches.
    frame = address & 7
    slots = [idle] * 16
    slots[frame * 2] = addr_cw
    idx = frame * 2 + 1
    codewords = []
    for w in words:
        if idx >= 16:
            codewords.append(sync)
            codewords.extend(slots)
            slots = [idle] * 16
            idx = 0
        slots[idx] = w
        idx += 1
    codewords.append(sync)
    codewords.extend(slots)
    bits = [1, 0] * 288  # 576-bit preamble
    for cw in codewords:
        for b in range(31, -1, -1):
            bits.append((cw >> b) & 1)
    spb = RATE / baud
    samples = []
    idx = 0
    for bit in bits:
        n = int(round((idx + 1) * spb)) - int(round(idx * spb))
        samples.extend([0.6 if bit else -0.6] * n)
        idx += 1
    return [s + random.gauss(0, 0.02) for s in samples]


def make_psk31(text, center=1000.0):
    """Render BPSK31: 31.25 baud DBPSK, raised-cosine, Varicode."""
    spb = RATE / 31.25
    bits = []
    for ch in text:
        code = VARICODE.get(ch)
        if code is None:
            continue
        for b in code:
            bits.append(int(b))
        bits.extend([0, 0])
    bits.extend([1] * 8)  # idle so the final character is not truncated
    samples = []
    phase = 0.0
    bit_index = 0
    for bit in bits:
        start = int(round(bit_index * spb))
        end = int(round((bit_index + 1) * spb))
        n = end - start
        if bit == 0:
            phase += math.pi  # phase reversal at bit boundary
        step = 2 * math.pi * center / RATE
        for j in range(n):
            # Raised-cosine envelope across the bit (0 at edges).
            env = math.sin(math.pi * (j + 0.5) / n)
            samples.append(0.7 * env * math.cos(phase))
            phase += step
        bit_index += 1
    return [s + random.gauss(0, 0.01) for s in samples]


if __name__ == "__main__":
    write_wav("/tmp/test_aprs.wav", make_afsk([
        ax25_frame("APRS", 0, "M0XXX", 9, b">Hello from space! test 001"),
        ax25_frame("APRS", 0, "GB3RS", 7, b">second packet 002"),
    ]))
    write_wav("/tmp/test_morse.wav", make_morse("CQ DE RS44 SAT"))
    write_wav("/tmp/test_dtmf.wav", make_dtmf("0123456789*#ABCD"))
    write_wav("/tmp/test_rtty.wav", make_rtty("CQ DE RS44 RTTY TEST 599"))
    write_wav("/tmp/test_sstv.wav", make_sstv_vis(0x3C))  # Martin M2
    write_wav("/tmp/test_psk31.wav", make_psk31("CQ DE RS44 PSK31 TEST"))
    write_wav("/tmp/test_pocsag.wav", make_pocsag(1234567, "HELLO PAGER TEST 123"))
    write_wav("/tmp/test_eas.wav", make_eas())
    print(f"wrote test WAVs at {RATE} Hz")
