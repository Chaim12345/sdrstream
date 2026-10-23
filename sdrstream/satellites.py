"""Amateur-radio satellite catalogue.

Frequencies are in Hz. ``downlink`` is the frequency a receiver should tune to;
``uplink`` is informational (this library only listens). ``tone_hz`` is the
CTCSS tone a transmitter needs, which is irrelevant for receiving but useful
context. ``mode`` names the modulation used for reception.

Only satellites whose signals a public WebSDR/OpenWebRX receiver can actually
hear are useful here, so entries are annotated with the band their downlink
occupies. Anything in the 2 m (144-146 MHz) or 70 cm (430-440 MHz) amateur
allocations is within reach of the VHF/UHF receivers found by
``tools/probe_servers.py``.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Dict, List, Optional


@dataclass(frozen=True)
class Satellite:
    """A satellite that transmits in an amateur band."""

    name: str
    norad_id: int
    downlink_hz: int
    mode: str
    uplink_hz: Optional[int] = None
    tone_hz: Optional[float] = None
    status: str = "active"
    notes: str = ""
    #: Extra downlinks worth trying (beacons, telemetry, second channel).
    beacons: tuple = ()
    #: CelesTrak element group carrying this satellite ("amateur" default).
    group: str = "amateur"

    @property
    def downlink_khz(self) -> float:
        return self.downlink_hz / 1000.0

    @property
    def band(self) -> str:
        if 144_000_000 <= self.downlink_hz <= 148_000_000:
            return "2m"
        if 430_000_000 <= self.downlink_hz <= 440_000_000:
            return "70cm"
        if 1_240_000_000 <= self.downlink_hz <= 1_300_000_000:
            return "23cm"
        if 136_000_000 <= self.downlink_hz <= 138_000_000:
            return "apt"
        if 28_000_000 <= self.downlink_hz <= 29_700_000:
            return "10m"
        return "other"

    @property
    def needs_doppler(self) -> bool:
        """Doppler is significant enough to retune on UHF and above."""
        return self.downlink_hz >= 400_000_000


#: 137 MHz weather-satellite downlinks. NOAA 15/18/19 were decommissioned in
#: 2025 (NOAA OSPO) but their APT transmitters stayed on; notes carry the
#: official status and the decoder confirms what is actually on the air.
WEATHER_SATELLITES: Dict[str, Satellite] = {
    s.name: s
    for s in [
        Satellite(
            "NOAA 15", 25338, downlink_hz=137_620_000, mode="FM", group="weather",
            notes="Decommissioned 2025-08-19 per NOAA OSPO; APT still reported on air. APT 137.620 MHz, wide FM.",
        ),
        Satellite(
            "NOAA 18", 28654, downlink_hz=137_912_500, mode="FM", group="weather",
            notes="Decommissioned 2025-06-06 per NOAA OSPO; APT still reported on air. APT 137.9125 MHz, wide FM.",
        ),
        Satellite(
            "NOAA 19", 33591, downlink_hz=137_100_000, mode="FM", group="weather",
            notes="Decommissioned 2025-08-13 per NOAA OSPO; APT still reported on air. APT 137.100 MHz, wide FM.",
        ),
    ]
}

#: Satellites most likely to be heard. Frequencies follow AMSAT's published
#: coordination data; check the live TLE set for which are still transmitting.
SATELLITES: Dict[str, Satellite] = {
    s.name: s
    for s in [
        Satellite(
            "ISS",
            25544,
            downlink_hz=145_800_000,
            uplink_hz=145_990_000,
            tone_hz=67.0,
            mode="FM",
            notes="Voice repeater (region 2/3 uplink); also SSTV and APRS.",
            beacons=(145_825_000, 145_800_000, 437_800_000),
        ),
        Satellite(
            "SO-50",
            27607,
            downlink_hz=436_795_000,
            uplink_hz=145_850_000,
            tone_hz=67.0,
            mode="FM",
            notes="Most reliable FM bird; 74.4 Hz tone to arm for 10 min.",
            beacons=(436_795_000,),
        ),
        Satellite(
            "AO-91",
            43017,
            downlink_hz=145_960_000,
            uplink_hz=435_250_000,
            tone_hz=67.0,
            mode="FM",
            notes="Fox-1B. Downlink on 2 m so Doppler is mild.",
            beacons=(145_960_000,),
        ),
        Satellite(
            "AO-92",
            43137,
            downlink_hz=145_880_000,
            uplink_hz=435_350_000,
            tone_hz=67.0,
            mode="FM",
            status="decayed",
            notes="Fox-1D. No current TLE; re-entered.",
            beacons=(145_880_000,),
        ),
        Satellite(
            "AO-73",
            39444,
            downlink_hz=145_960_000,
            uplink_hz=435_150_000,
            mode="USB",
            notes="FUNcube-1 linear transponder; inverting. Telemetry 145.935.",
            beacons=(145_935_000, 145_960_000),
        ),
        Satellite(
            "RS-44",
            44909,
            downlink_hz=435_620_000,
            uplink_hz=145_965_000,
            mode="USB",
            notes="Linear transponder, inverting. Beacon near 435.610.",
            beacons=(435_610_000,),
        ),
        Satellite(
            "CAS-4B",
            42759,
            downlink_hz=145_925_000,
            uplink_hz=435_280_000,
            mode="USB",
            status="decayed",
            notes="CAS-4B. No current TLE; re-entered.",
            beacons=(145_925_000,),
        ),
        Satellite(
            "CAS-4A",
            42761,
            downlink_hz=145_855_000,
            uplink_hz=435_220_000,
            mode="USB",
            status="decayed",
            notes="CAS-4A. No current TLE; re-entered.",
            beacons=(145_855_000,),
        ),
        Satellite(
            "PO-101",
            43678,
            downlink_hz=145_900_000,
            uplink_hz=437_500_000,
            mode="FM",
            notes="Diwata-2; FM voice repeater, scheduling varies.",
            beacons=(145_900_000,),
        ),
        Satellite(
            "AO-7",
            7530,
            downlink_hz=145_975_000,
            uplink_hz=432_150_000,
            mode="USB",
            notes="Launched 1974, still working in sunlight. Mode B.",
            beacons=(145_975_000,),
        ),
        Satellite(
            "AO-27",
            22825,
            downlink_hz=436_795_000,
            uplink_hz=145_850_000,
            mode="FM",
            notes="EYESAT A. FM repeater; intermittently active.",
            beacons=(436_795_000,),
        ),
        Satellite(
            "FO-29",
            24278,
            downlink_hz=435_800_000,
            uplink_hz=145_950_000,
            mode="USB",
            notes="JAS-2. Linear transponder, inverting; long-lived. CW beacon 435.795.",
            beacons=(435_795_000,),
        ),
        Satellite(
            "AO-85",
            40967,
            downlink_hz=145_980_000,
            uplink_hz=435_170_000,
            tone_hz=67.0,
            mode="FM",
            notes="Fox-1A. FM repeater, intermittent.",
            beacons=(145_980_000,),
        ),
        Satellite(
            "AO-95",
            43770,
            downlink_hz=145_915_000,
            uplink_hz=435_300_000,
            mode="FM",
            notes="Fox-1Cliff. FM repeater.",
            beacons=(145_915_000,),
        ),
        Satellite(
            "JO-97",
            43803,
            downlink_hz=145_840_000,
            uplink_hz=435_110_000,
            mode="USB",
            notes="JY1SAT. Linear transponder, inverting; also telemetry.",
            beacons=(145_840_000,),
        ),
        Satellite(
            "CAS-9",
            50466,
            downlink_hz=145_870_000,
            uplink_hz=435_210_000,
            mode="USB",
            notes="XW-3. Linear transponder, inverting.",
            beacons=(145_870_000,),
        ),
        Satellite(
            "IO-117",
            53109,
            downlink_hz=435_310_000,
            uplink_hz=145_925_000,
            mode="USB",
            notes="GreenCube. 9k6 digipeater; downlink 435.310.",
            beacons=(435_310_000,),
        ),
        Satellite(
            "AO-123",
            61781,
            downlink_hz=435_400_000,
            uplink_hz=145_850_000,
            tone_hz=67.0,
            mode="FM",
            notes="ASRTU-1. FM repeater.",
            beacons=(435_400_000,),
        ),
        Satellite(
            "SONATE-2",
            59112,
            downlink_hz=145_880_000,
            uplink_hz=435_950_000,
            mode="FM",
            notes="FM repeater and APRS digipeater.",
            beacons=(145_880_000,),
        ),
    ]
}

#: Receiver passband windows (Hz) that can hear the satellites above.
SAT_BANDS_HZ = {
    "2m": (144_000_000, 148_000_000),
    "70cm": (430_000_000, 440_000_000),
    "23cm": (1_240_000_000, 1_300_000_000),
}


def by_band(band: str) -> List[Satellite]:
    """Return the satellites whose downlink falls in *band*."""
    return [s for s in SATELLITES.values() if s.band == band]


def by_name(name: str) -> Satellite:
    """Look up a satellite, accepting the common short names.

    Searches the weather table too: NOAA 15/18/19 live there, and a pass
    recording must not fail just because the bird is not an amateur sat.
    """
    key = name.strip().upper()
    tables = (SATELLITES, WEATHER_SATELLITES)
    for table in tables:
        if key in table:
            return table[key]
    for table in tables:
        for sat in table.values():
            if key == sat.name.upper() or key in sat.name.upper():
                return sat
    known = sorted(list(SATELLITES) + list(WEATHER_SATELLITES))
    raise KeyError("unknown satellite %r; known: %s" % (name, ", ".join(known)))
