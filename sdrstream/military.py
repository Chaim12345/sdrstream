"""Government and military satellites whose downlinks public SDRs can reach.

This table exists because a lot of space hardware belongs to governments and
armed forces rather than to radio amateurs, and some of it transmits where a
public WebSDR or OpenWebRX receiver can hear it. What can be received varies
enormously, so every entry says plainly how much of the signal is readable:

``open``
    The downlink carries traffic that is not encrypted. This is rare, and it
    usually means a legacy system built before encryption was practical. The
    UHF SATCOM repeaters are the classic example.
``encrypted``
    The downlink is present and receivable, but its payload is encrypted. A
    public receiver can show that the satellite is transmitting, measure its
    frequency, watch it drift with Doppler and time its passes. It cannot read
    the content, and nothing here tries to.
``beacon``
    Only a carrier or an unmodulated beacon is receivable.

Nothing in this module decrypts anything, and there is no key material or
cryptanalysis anywhere in the package. Receiving a signal that is broadcast
over the air is generally lawful to listen to; what is not lawful in most
places is decrypting a protected service or using what you hear. That
distinction is the whole point of the ``encryption`` field.

Frequencies are in Hz and come from the published sources named in each entry,
chiefly the UHF-Satcom frequency lists, Matt Blaze's North America downlink
survey, and eoPortal's spacecraft descriptions. They are starting points: these
are old spacecraft, they drift, and a transponder that was busy last year may
be silent this year.

The ``reachable`` helper compares the table against a list of real receivers,
which is how you find out whether a given satellite is worth chasing from the
receivers you can actually see.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Dict, Iterable, List, Optional, Sequence, Tuple

#: UHF SATCOM occupies 240-270 MHz. The amateur 70 cm band stops at 440 MHz,
#: so receivers tagged for it do not automatically reach this.
UHF_SATCOM_LOW_HZ = 243_000_000
UHF_SATCOM_HIGH_HZ = 270_000_000


@dataclass(frozen=True)
class GovernmentSatellite:
    """A government or military spacecraft with a published downlink."""

    name: str
    operator: str
    downlink_hz: int
    mode: str
    #: "comms", "weather", "early-warning", "navigation" or "reconnaissance".
    category: str
    #: "open", "encrypted" or "beacon". See the module docstring.
    encryption: str
    notes: str
    source: str
    norad_id: Optional[int] = None
    #: Further downlinks, as ``(frequency_hz, mode)`` pairs.
    extra_downlinks: Tuple[Tuple[int, str], ...] = ()
    #: Constellations name a representative spacecraft rather than one object.
    constellation: bool = False

    @property
    def downlink_mhz(self) -> float:
        return self.downlink_hz / 1e6

    @property
    def band(self) -> str:
        """A coarse band name, matching the vocabulary used elsewhere."""
        if 136_000_000 <= self.downlink_hz <= 138_000_000:
            return "137 MHz"
        if 148_000_000 <= self.downlink_hz <= 152_000_000:
            return "150 MHz"
        if UHF_SATCOM_LOW_HZ <= self.downlink_hz <= UHF_SATCOM_HIGH_HZ:
            return "UHF SATCOM"
        if 380_000_000 <= self.downlink_hz <= 480_000_000:
            return "400-470 MHz"
        if 1_500_000_000 <= self.downlink_hz <= 1_600_000_000:
            return "L-band"
        if 2_200_000_000 <= self.downlink_hz <= 2_300_000_000:
            return "S-band"
        return "other"

    @property
    def is_open(self) -> bool:
        """Whether any of the downlink carries unencrypted traffic."""
        return self.encryption == "open"

    def all_downlinks(self) -> List[Tuple[int, str]]:
        """Every downlink, primary first."""
        return [(self.downlink_hz, self.mode)] + list(self.extra_downlinks)

    def summary(self) -> str:
        return "%-22s %9.4f MHz %-4s %-11s %s" % (
            self.name, self.downlink_mhz, self.mode, self.band,
            self.encryption)


def _sat(name, operator, downlink_hz, mode, category, encryption, notes,
         source, norad_id=None, extra=(), constellation=False):
    return GovernmentSatellite(
        name=name, operator=operator, downlink_hz=downlink_hz, mode=mode,
        category=category, encryption=encryption, notes=notes, source=source,
        norad_id=norad_id, extra_downlinks=extra, constellation=constellation)


#: The table. Ordered roughly by how rewarding reception is, so the open
#: legacy systems come first.
GOVERNMENT_SATELLITES: Dict[str, GovernmentSatellite] = {
    "FLTSATCOM": _sat(
        "FLTSATCOM", "US Navy", 255_550_000, "NFM", "comms", "open",
        "1970s-80s UHF repeater constellation. Built before practical "
        "encryption, so transponders still relay whatever reaches them, "
        "which in practice is often unlicensed third-party traffic. The most "
        "widely heard military downlink in the world. 255.550 is the famous "
        "channel; a 25 kHz transponder sits on FLTSATCOM 8 at 15W.",
        "mattblaze.org/misc/uhf-sats", extra=((255_250_000, "NFM"),
                                              (253_850_000, "NFM")),
        constellation=True),
    "UFO (UHF Follow-On)": _sat(
        "UFO (UHF Follow-On)", "US Navy", 255_550_000, "NFM", "comms", "open",
        "Successor to FLTSATCOM and still the backbone of US UHF SATCOM. The "
        "narrowband channels carry 25 or 30 kHz NFM and several are heard "
        "carrying open traffic. Channel 20 on UFO6 (105.5W) is the best "
        "documented. Geostationary, so it is audible continuously rather than "
        "only during a pass.",
        "mattblaze.org/misc/uhf-sats", extra=((255_350_000, "NFM"),
                                              (253_650_000, "NFM")),
        constellation=True),
    "Skynet": _sat(
        "Skynet", "UK MoD", 255_550_000, "NFM", "comms", "open",
        "British equivalent of FLTSATCOM, sharing the same 240-270 MHz band. "
        "The documented UK channels sit near 254.075 and 254.150 MHz. Skynet "
        "4 and 5 are the older spacecraft still carrying legacy UHF.",
        "uhf-satcom.com/satellite-reception/uhf", constellation=True),
    "Gonets-M / Strela-3": _sat(
        "Gonets-M / Strela-3", "Roscosmos / Russian military", 260_000_000,
        "FM", "comms", "open",
        "Russian store-and-dump messaging constellation at about 1400 km, "
        "inclined 82.5 degrees. Downlinks in 259.5-265.2 MHz and 387-390 MHz "
        "as short digital bursts, 300-500 ms when no ground station is in "
        "range. Because the orbit is low, passes are frequent and brief, and "
        "the burst timing is a neat way to confirm a pass prediction.",
        "uhf-satcom.com/satellite-reception/uhf",
        extra=((388_000_000, "FM"),), constellation=True),
    "Meridian": _sat(
        "Meridian", "Russian military", 260_000_000, "FM", "comms", "open",
        "Russian military communications satellites in Molniya-type highly "
        "elliptical orbits, the replacement for the Molniya series. Downlinks "
        "at 259.5-265.2 MHz, 387-390 MHz and 1541.0-1541.9 MHz. The "
        "high-latitude apogee means long, high-elevation passes from northern "
        "receivers.",
        "uhf-satcom.com/satellite-reception/uhf",
        extra=((388_000_000, "FM"), (1_541_000_000, "FM")),
        constellation=True),
    "DMSP": _sat(
        "DMSP", "US Space Force", 400_032_000, "FM", "weather", "encrypted",
        "Defense Meteorological Satellite Program, the military weather "
        "constellation flown since the 1960s. The UHF real-time data stream "
        "runs at 400.032 and 400.822 MHz and the S-band links at 2222.5 and "
        "2237.5 MHz. The payload is encrypted, so reception shows the carrier "
        "and its Doppler curve and nothing more. Still a good way to test a "
        "400 MHz receive chain.",
        "eoportal.org/satellite-missions/dmsp-block-5d",
        norad_id=25991, extra=((400_822_000, "FM"), (2_237_500_000, "FM")),
        constellation=True),
    "Molniya": _sat(
        "Molniya", "Russian military", 1_000_450_000, "FM", "comms",
        "encrypted",
        "The original Soviet military communications series, in highly "
        "elliptical 12-hour orbits that give long dwell times at high "
        "latitude. Reported UHF downlinks near 1000.45 and 992.45 MHz, with "
        "direct-sequence spread spectrum at 990.6 MHz. The spread-spectrum "
        "links are effectively unreadable without the spreading code.",
        "uhf-satcom.com/satellite-reception/uhf", constellation=True),
    "DSP / SBIRS": _sat(
        "DSP / SBIRS", "US Space Force", 2_237_500_000, "FM",
        "early-warning", "encrypted",
        "Missile early-warning constellations, geostationary and highly "
        "inclined. They transmit SGLS telemetry and command links in the "
        "2200-2300 MHz range, with carriers reported near 2237.5 and "
        "2252.5 MHz. The links are encrypted and use spread spectrum; a "
        "receiver sees a carrier, not a message.",
        "orbitalfocus.uk/Frequencies/FrequenciesAll.php", constellation=True),
    "NOSS / Intruder": _sat(
        "NOSS / Intruder", "US Navy", 400_000_000, "FM", "reconnaissance",
        "encrypted",
        "Naval ocean surveillance satellites that fly in tight triplets and "
        "locate ships by their radio emissions. Downlinks have been reported "
        "around 400 MHz. There is no open payload, and the formations are "
        "easier to see in the pass timings than in the signal.",
        "uhf-satcom.com/satellite-reception/uhf", constellation=True),
}


#: Satellites that belong on a "military" list but that no public SDR can
#: reach, kept so the reachability report can say so explicitly rather than
#: leaving an obvious gap. EHF and X-band need dishes and hardware that the
#: receivers in this package do not have.
OUT_OF_REACH: Dict[str, str] = {
    "Milstar / AEHF": "20/44 GHz EHF; needs a dish and an EHF terminal.",
    "Lacrosse / Onyx": "X-band radar imaging, around 9.6 GHz; dish required.",
    "KH-11 (Crystal)": "Data relayed through other spacecraft, not broadcast.",
    "GPS (military M-code)": "M-code is encrypted and on a different signal "
                             "from the open L1/L2 C/A that civil receivers use.",
}


@dataclass
class Reachability:
    """Which receivers can hear one satellite."""

    satellite: GovernmentSatellite
    receivers: List[str] = field(default_factory=list)
    #: The first downlink any receiver could reach, if any.
    downlink_hz: Optional[int] = None

    @property
    def count(self) -> int:
        return len(self.receivers)

    @property
    def reachable(self) -> bool:
        return bool(self.receivers)

    def summary(self) -> str:
        if not self.reachable:
            return "%-22s not reachable by any receiver" % self.satellite.name
        return "%-22s %4d receiver(s) can hear %.4f MHz" % (
            self.satellite.name, self.count, self.downlink_hz / 1e6)


def reachable(receivers: Iterable, satellites: Optional[Dict[
        str, GovernmentSatellite]] = None) -> List[Reachability]:
    """Match the table against real receivers, best-covered satellite first.

    A satellite counts as reachable when some receiver's published bands cover
    one of its downlinks. That is a statement about tuning range only: it says
    nothing about whether the spacecraft is still transmitting.
    """
    if satellites is None:
        satellites = GOVERNMENT_SATELLITES
    found = []
    for satellite in satellites.values():
        hits: List[str] = []
        hit_frequency: Optional[int] = None
        for frequency_hz, _mode in satellite.all_downlinks():
            matching = [r.url for r in receivers if r.can_hear(frequency_hz)]
            if matching:
                hit_frequency = frequency_hz
                hits = matching
                break
        found.append(Reachability(satellite=satellite, receivers=hits,
                                  downlink_hz=hit_frequency))
    return sorted(found, key=lambda item: (-item.count, item.satellite.name))


def by_encryption(level: str) -> List[GovernmentSatellite]:
    """Entries with a given encryption level, open ones first."""
    return [satellite for satellite in GOVERNMENT_SATELLITES.values()
            if satellite.encryption == level]


def by_category(category: str) -> List[GovernmentSatellite]:
    """Entries in a category such as ``"comms"`` or ``"weather"``."""
    return [satellite for satellite in GOVERNMENT_SATELLITES.values()
            if satellite.category == category]


def report(receivers: Sequence = ()) -> str:
    """A text report, optionally including what real receivers can reach."""
    lines = ["Government and military downlinks public SDRs can reach",
             "",
             "encryption: open = payload readable, encrypted = carrier and "
             "Doppler only",
             ""]
    for satellite in GOVERNMENT_SATELLITES.values():
        lines.append("  " + satellite.summary())
        lines.append("      %s" % satellite.notes)
        lines.append("      source: %s" % satellite.source)
    if receivers:
        lines.append("")
        lines.append("reachability against %d receiver(s):" % len(receivers))
        for item in reachable(receivers):
            lines.append("  " + item.summary())
    lines.append("")
    lines.append("out of reach for these receivers:")
    for name, why in OUT_OF_REACH.items():
        lines.append("  %-22s %s" % (name, why))
    return "\n".join(lines)
