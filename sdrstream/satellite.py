"""Record a satellite pass through a public SDR receiver.

This ties the pieces together: predict when a satellite is over a receiver,
then stream from that receiver and retune as Doppler shifts the signal. Two
practical points shape the design:

* Tuning a remote receiver is a request, not a guarantee. It takes a moment to
  take effect and a busy server may be slow, so the loop only retunes once the
  correction has moved past a threshold and never more often than a fixed
  interval.
* A public receiver is a shared, best-effort resource. It drops connections and
  refuses tuning, so each connection is made independently and a failure loses
  only the audio from that window.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Callable, List, Optional

from . import catalogue
from .common import write_wav
from .openwebrx import OpenWebRXClient
from .satellites import Satellite
from .tracking import Tracker
from .websdr import WebSDRClient

#: How long one connection stays open before we reconnect and retune. Short
#: enough that a missed retune costs little audio, long enough to be polite.
SEGMENT_SECONDS = 20.0


@dataclass
class ChasePlan:
    """What a pass-chasing recording is going to do."""

    satellite: Satellite
    receiver: catalogue.Receiver
    passes: list
    start: datetime
    end: datetime
    start_frequency_hz: float
    doppler_range_hz: tuple

    def summary(self) -> str:
        lines = [
            "%s via %s (%.2f, %.2f)"
            % (self.satellite.name, self.receiver.url,
               self.receiver.latitude, self.receiver.longitude),
            "  nominal downlink %.4f MHz, mode %s"
            % (self.satellite.downlink_hz / 1e6, self.satellite.mode),
            "  recording %s to %s UTC (%.1f min)"
            % (self.start.strftime("%H:%M:%S"), self.end.strftime("%H:%M:%S"),
               (self.end - self.start).total_seconds() / 60.0),
            "  Doppler range %+d to %+d Hz"
            % (self.doppler_range_hz[0], self.doppler_range_hz[1]),
        ]
        lines.extend("  " + p.summary() for p in self.passes)
        return "\n".join(lines)


def plan_pass(
    satellite: Satellite,
    receiver: Optional[catalogue.Receiver] = None,
    *,
    latitude: Optional[float] = None,
    longitude: Optional[float] = None,
    start: Optional[datetime] = None,
    hours: float = 12.0,
    min_elevation: float = 10.0,
    lead_seconds: float = 90.0,
    tail_seconds: float = 60.0,
    tle_text: Optional[str] = None,
) -> ChasePlan:
    """Work out when and where to record *satellite*.

    The observer position comes from *receiver* when given, otherwise from
    *latitude* and *longitude*. With neither, the nearest known receiver that
    covers the downlink is used.
    """
    from . import tle as tle_module

    if receiver is None and (latitude is None or longitude is None):
        candidates = catalogue.for_frequency(satellite.downlink_hz)
        if not candidates:
            raise ValueError(
                "no known receiver covers %.4f MHz; pass a receiver or "
                "observer coordinates" % (satellite.downlink_hz / 1e6)
            )
        receiver = candidates[0]

    if receiver is None:
        receiver = catalogue.Receiver(
            url="(observer)", kind="observer",
            latitude=latitude, longitude=longitude,
        )
    elif latitude is None or longitude is None:
        latitude, longitude = receiver.location

    # A bare observer has no bands to check; only real receivers are vetted.
    if receiver.bands and not receiver.can_hear(satellite.downlink_hz):
        raise ValueError(
            "%s cannot hear %.4f MHz (bands: %s)"
            % (receiver.url, satellite.downlink_hz / 1e6,
               ", ".join(b.name for b in receiver.bands) or "none")
        )

    text = tle_text if tle_text is not None else tle_module.load("amateur")
    tracker = Tracker.from_tle_text(text, satellite.name)
    passes = tracker.find_passes(
        latitude, longitude, start=start, hours=hours,
        min_elevation=min_elevation,
    )
    if not passes:
        raise ValueError(
            "no %s passes above %.0f deg in the next %.0f h from %.3f,%.3f"
            % (satellite.name, min_elevation, hours, latitude, longitude)
        )
    chosen = passes[0]
    begin = chosen.rise - timedelta(seconds=lead_seconds)
    end = chosen.set_time + timedelta(seconds=tail_seconds)

    shifts: List[float] = []
    t = begin
    while t <= end:
        shifts.append(
            tracker.look_angle(t, latitude, longitude)
            .doppler_shift(satellite.downlink_hz)
        )
        t += timedelta(seconds=10)
    return ChasePlan(
        satellite=satellite,
        receiver=receiver,
        passes=passes,
        start=begin,
        end=end,
        start_frequency_hz=satellite.downlink_hz + (shifts[0] if shifts else 0.0),
        doppler_range_hz=(int(min(shifts)), int(max(shifts))) if shifts else (0, 0),
    )


def _mode_for(kind: str, mode: str) -> str:
    """Translate a satellite mode into the receiver's own vocabulary."""
    if kind == "openwebrx":
        # OpenWebRX names narrowband FM "nfm"; wide FM is "wfm".
        return {"FM": "nfm", "USB": "usb", "LSB": "lsb", "CW": "cw"}.get(
            mode.upper(), mode.lower())
    return mode.upper()


async def _segment(plan: ChasePlan, frequency_hz: float, seconds: float,
                   offline_tle: bool = False):
    """Open a fresh connection, tune, record one segment, close."""
    kind = plan.receiver.kind
    mode = _mode_for(kind, plan.satellite.mode)
    if kind == "openwebrx":
        async with OpenWebRXClient(plan.receiver.url, reconnect=False) as client:
            await client.tune(frequency=int(frequency_hz), modulation=mode)
            chunks = await client.record(seconds)
        rate = chunks[0].samplerate if chunks else 12000
        return OpenWebRXClient.concat(chunks), rate

    async with WebSDRClient(plan.receiver.url, reconnect=False) as client:
        await client.tune(frequency_khz=frequency_hz / 1000.0, mode=mode)
        chunks = await client.record(seconds)
    rate = int(chunks[0].samplerate) if chunks else 8000
    return WebSDRClient.concat(chunks), rate


async def chase(
    plan: ChasePlan,
    out_path: str,
    *,
    retune_threshold_hz: float = 300.0,
    min_retune_interval: float = 2.0,
    segment_seconds: float = SEGMENT_SECONDS,
    on_progress: Optional[Callable[[str], None]] = None,
    offline_tle: bool = False,
) -> int:
    """Run *plan* to completion, writing audio to *out_path*.

    Returns the number of samples written. The receiver is reconnected for each
    segment so a dropped connection costs one segment, not the whole pass.
    """
    from . import tle as tle_module

    say = on_progress or (lambda message: None)
    text = tle_module.load("amateur", offline=offline_tle)
    tracker = Tracker.from_tle_text(text, plan.satellite.name)
    latitude, longitude = plan.receiver.location
    nominal = plan.satellite.downlink_hz

    wait = (plan.start - datetime.now(timezone.utc)).total_seconds()
    if wait > 0:
        say("waiting %.0f s for the pass to begin" % wait)
        await asyncio.sleep(wait)

    collected: List[int] = []
    rate = 12000
    current = plan.start_frequency_hz
    last_retune = 0.0
    failures = 0

    while datetime.now(timezone.utc) < plan.end:
        now = datetime.now(timezone.utc)
        look = tracker.look_angle(now, latitude, longitude)
        wanted = nominal + look.doppler_shift(nominal)
        if (abs(wanted - current) >= retune_threshold_hz
                and time.monotonic() - last_retune >= min_retune_interval):
            say("  el %5.1f deg  Doppler %+6d Hz  tune %.4f MHz"
                % (look.elevation, int(wanted - nominal), wanted / 1e6))
            current = wanted
            last_retune = time.monotonic()

        remaining = (plan.end - datetime.now(timezone.utc)).total_seconds()
        if remaining <= 0:
            break
        window = min(segment_seconds, remaining)
        try:
            samples, rate = await _segment(plan, current, window)
            failures = 0
        except Exception as exc:
            failures += 1
            say("  segment failed (%s); retrying in %.0f s" % (exc, min(failures, 10)))
            # A receiver that refuses instantly would otherwise spin.
            await asyncio.sleep(min(failures, 10))
            continue
        if samples:
            collected.extend(samples)
            say("  +%d samples (%.1f s of audio, total %.1f s)"
                % (len(samples), len(samples) / rate, len(collected) / rate))

    if not collected:
        raise RuntimeError("no audio captured during the pass")
    write_wav(out_path, collected, rate)
    say("wrote %s: %d samples at %d Hz (%.1f s)"
        % (out_path, len(collected), rate, len(collected) / rate))
    return len(collected)
