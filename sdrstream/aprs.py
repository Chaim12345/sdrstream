"""Live traffic from the APRS-IS backbone.

APRS-IS is the internet backbone of the amateur packet network: every IGate
relays over-the-air APRS packets to it, so a plain TCP connection carries
live positions, weather reports, telemetry and messages from stations around
the world — the same traffic the ISS digipeater on 437.825 MHz forwards.
No account is needed to listen: the login line is a courtesy and a receive
passcode of -1 works. The server accepts filter expressions on the login
line (``r/lat/lon/radius`` for an area, ``b/call`` for one callsign's
traffic, ``t/w`` for weather only — the syntax is the APRS-IS filter
language) so the firehose can be narrowed to what you want.
"""

from __future__ import annotations

import re
import socket
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Iterator, List, Optional

APRS_IS_HOSTS = ("rotate.aprs.net", "noam.aprs2.net", "euro.aprs2.net")
APRS_IS_PORT = 14580

# Uncompressed position: 4-digit latitude, 5-digit longitude, the classic
# APRS formats, followed by the symbol table character.
_POSITION = re.compile(
    r"(\d{2})(\d{2})\.(\d{2})([NS])([/\\A-Za-z])(\d{3})(\d{2})\.(\d{2})([EW])")


@dataclass
class AprsPacket:
    """One packet heard on the backbone."""

    raw: str
    source: str
    destination: str = ""
    path: str = ""
    kind: str = "other"  # position, weather, message, telemetry, status, object
    latitude: Optional[float] = None
    longitude: Optional[float] = None
    comment: str = ""
    received_at: Optional[datetime] = None

    def summary(self) -> str:
        where = ""
        if self.latitude is not None and self.longitude is not None:
            where = "  %.4f, %.4f" % (self.latitude, self.longitude)
        text = (self.comment[:40] + "...") if len(self.comment) > 40 \
            else self.comment
        return "%-9s %-9s %-9s %s%s" % (
            self.source, self.kind, self.destination or "-", where,
            ("  " + text) if text else "")


def parse_packet(line: str) -> Optional[AprsPacket]:
    """Parse one APRS-IS line; comment lines and noise return None."""
    line = line.strip()
    if not line or line.startswith("#"):
        return None
    if ">" not in line:
        return None
    # SOURCE>DEST,PATH,PATH*:payload — the payload starts at the first
    # colon after the digipeater path.
    header, _, payload = line.partition(">")
    if ":" not in payload:
        return None
    route, _, payload = payload.partition(":")
    first, _, path = route.partition(",")
    packet = AprsPacket(raw=line, source=header, destination=first,
                       path=path.strip())
    packet.kind = _classify(payload)
    packet.latitude, packet.longitude = _position(payload)
    packet.comment = payload.strip()
    return packet


def _classify(payload: str) -> str:
    if not payload:
        return "other"
    head = payload[0]
    if head == "_":
        return "weather"
    # A weather report may open with the station position and continue
    # with the underscore-delimited met data, so look past it too.
    if head in "!=" and "_" in payload[:30]:
        return "weather"
    if head in "!=":
        return "position"
    if head == ":":
        return "message"
    if payload.startswith("T#"):
        return "telemetry"
    if head == ">":
        return "status"
    if head in ";)":
        return "object"
    if payload.startswith("$G"):  # NMEA strings some IGates forward
        return "nmea"
    return "other"


def _position(payload: str):
    match = _POSITION.search(payload)
    if match is None:
        return None, None
    lat_deg, lat_min, lat_hund, lat_hem = match.group(1, 2, 3, 4)
    lon_deg, lon_min, lon_hund, lon_hem = match.group(6, 7, 8, 9)
    latitude = int(lat_deg) + int(lat_min) / 60.0 + int(lat_hund) / 6000.0
    longitude = int(lon_deg) + int(lon_min) / 60.0 + int(lon_hund) / 60000.0
    if lat_hem == "S":
        latitude = -latitude
    if lon_hem == "W":
        longitude = -longitude
    return latitude, longitude


def login_line(callsign: str = "N0CALL", filter_spec: Optional[str] = None) -> str:
    """The APRS-IS login: a callsign, the receive passcode, and a filter."""
    line = "# login %s pass -1" % callsign
    if filter_spec:
        line += " filter %s" % filter_spec
    return line + "\r\n"


def listen(callsign: str = "N0CALL",
           filter_spec: Optional[str] = None,
           max_packets: int = 10,
           seconds: float = 30.0,
           host: str = APRS_IS_HOSTS[0],
           port: int = APRS_IS_PORT,
           timeout: float = 10.0) -> List[AprsPacket]:
    """Collect up to *max_packets* packets, or until *seconds* pass."""
    packets: List[AprsPacket] = []
    for packet in stream(callsign, filter_spec, seconds, host, port, timeout):
        packets.append(packet)
        if len(packets) >= max_packets:
            break
    return packets


def stream(callsign: str = "N0CALL",
           filter_spec: Optional[str] = None,
           seconds: float = 30.0,
           host: str = APRS_IS_HOSTS[0],
           port: int = APRS_IS_PORT,
           timeout: float = 10.0) -> Iterator[AprsPacket]:
    """Yield packets from the live backbone until *seconds* elapse.

    The connection is closed on exit; a broken stream ends the iterator
    rather than raising, so a partial collection is still returned.
    """
    deadline = datetime.now(timezone.utc).timestamp() + seconds
    try:
        with socket.create_connection((host, port), timeout=timeout) as sock:
            sock.settimeout(max(1.0, min(timeout, 5.0)))
            sock.sendall(login_line(callsign, filter_spec).encode("ascii"))
            buffer = b""
            while datetime.now(timezone.utc).timestamp() < deadline:
                try:
                    chunk = sock.recv(4096)
                except socket.timeout:
                    continue
                if not chunk:
                    break
                buffer += chunk
                while b"\r\n" in buffer:
                    line, _, buffer = buffer.partition(b"\r\n")
                    packet = parse_packet(line.decode("ascii", "replace"))
                    if packet is not None:
                        packet.received_at = datetime.now(timezone.utc)
                        yield packet
    except OSError:
        return
