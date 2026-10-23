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
    #: The TLE block this plan was built from, so a later re-lookup by the
    #: satellite's table name (which may differ from the catalogue name, as
    #: PO-101 does from DIWATA-2B) cannot lose the orbit.
    tle_text: Optional[str] = None
    #: The element record the plan was built from, when GP/OMM data was used.
    #: Preferred over ``tle_text`` by :func:`_record_pass` because it survives
    #: the six-digit catalog numbers the TLE layout cannot hold.
    orbit_record: Optional[object] = None
    #: The dated event that overrode the catalogue downlink, when one did.
    event: Optional[object] = None

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
    orbit_records: Optional[list] = None,
    offline_tle: bool = False,
) -> ChasePlan:
    """Work out when and where to record *satellite*.

    The observer position comes from *receiver* when given, otherwise from
    *latitude* and *longitude*. With neither, the nearest known receiver that
    covers the downlink is used.

    Element data comes from *orbit_records* when given, else from GP JSON
    (``orbit_records`` omitted), else from *tle_text*, else from the amateur
    TLE set. The GP path is preferred because it is the only one that carries
    six-digit NORAD catalog numbers.
    """
    from . import events as events_module
    from . import tle as tle_module

    # A dated transmission event (e.g. an ARISS SSTV series) temporarily
    # overrides the satellite's catalogue downlink: tune the event
    # frequency and vet receiver coverage against it, not the legacy one.
    event = events_module.active_event(satellite.name, at=start)
    downlink_hz = event.downlink_hz if event is not None else satellite.downlink_hz

    if receiver is None and (latitude is None or longitude is None):
        candidates = catalogue.for_frequency(downlink_hz)
        if not candidates:
            raise ValueError(
                "no known receiver covers %.4f MHz; pass a receiver or "
                "observer coordinates" % (downlink_hz / 1e6)
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
    if receiver.bands and not receiver.can_hear(downlink_hz):
        raise ValueError(
            "%s cannot hear %.4f MHz (bands: %s)"
            % (receiver.url, downlink_hz / 1e6,
               ", ".join(b.name for b in receiver.bands) or "none")
        )

    text = tle_text
    record = None
    if text is not None and orbit_records is None:
        # Caller-supplied elements win: no network fetch, no chance of a
        # stale or unreachable feed overriding fresh data.
        tracker = Tracker.from_tle_text(text, satellite.name)
    elif orbit_records is not None:
        tracker = Tracker.from_records(orbit_records, satellite.name)
        for candidate in orbit_records:
            if satellite.name.strip().upper() in candidate.name.upper():
                record = candidate
                break
    else:
        group = satellite.group or "amateur"
        try:
            records = tle_module.load_records(group, offline=offline_tle)
            tracker = Tracker.from_records(records, satellite.name)
            for candidate in records:
                if satellite.name.strip().upper() in candidate.name.upper():
                    record = candidate
                    break
        except Exception:
            # Decommissioned birds (NOAA 15/18/19 since 2025) drop out of
            # the group feeds; CelesTrak still tracks them by catalog number.
            if text is None:
                text = tle_module.load_catnr(satellite.norad_id,
                                             offline=offline_tle)
            if text is None:
                text = tle_module.load(group, offline=offline_tle)
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
            .doppler_shift(downlink_hz)
        )
        t += timedelta(seconds=10)
    return ChasePlan(
        satellite=satellite,
        receiver=receiver,
        passes=passes,
        start=begin,
        end=end,
        start_frequency_hz=downlink_hz + (shifts[0] if shifts else 0.0),
        doppler_range_hz=(int(min(shifts)), int(max(shifts))) if shifts else (0, 0),
        tle_text=text,
        orbit_record=record,
        event=event,
    )


#: Demodulator bandwidth NOAA APT needs, in Hz. The downlink is wide FM
#: (+/-17 kHz deviation), so the default narrow-FM filter (~12.5 kHz)
#: destroys the waveform. Weather-table birds only; amateur FM voice
#: satellites keep the narrow default.
APT_BANDWIDTH_HZ = 40000


def _bandwidth_for(kind: str, sat) -> "Optional[int]":
    """Explicit demodulator bandwidth for receivers that accept one.

    Only OpenWebRX takes a bandwidth parameter, and only weather-table
    satellites need the wide APT filter.
    """
    if kind == "openwebrx" and getattr(sat, "group", "amateur") == "weather":
        return APT_BANDWIDTH_HZ
    return None


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


class _ChaseSession:
    """One receiver connection held for the whole pass, retuned in-stream.

    WebSDR retunes over the live connection with its ``~~param`` frame and
    OpenWebRX with its own JSON command, so Doppler tracking no longer costs
    a reconnect per segment: the audio stream stays continuous, and the
    connection is only rebuilt when it actually fails.
    """

    def __init__(self, plan: ChasePlan, say: Optional[Callable[[str], None]] = None):
        self._plan = plan
        self._mode = _mode_for(plan.receiver.kind, plan.satellite.mode)
        self._bandwidth = _bandwidth_for(plan.receiver.kind, plan.satellite)
        self._say = say or (lambda message: None)
        self._client = None
        self._is_openwebrx = plan.receiver.kind == "openwebrx"

    async def _open(self):
        if self._is_openwebrx:
            self._client = OpenWebRXClient(self._plan.receiver.url,
                                           reconnect=False)
            await self._client.__aenter__()
            # Tuning only reaches a profile whose span covers the target
            # frequency; servers often default to an HF profile. Match the
            # receiver band's recorded profile display name and select it.
            band = self._plan.receiver.band_for(self._plan.satellite.downlink_hz)
            if band is not None and band.name:
                want = band.name.lower()
                matched = False
                for profile in self._client.profiles:
                    name = str(profile.get("name", "")).lower()
                    key = profile.get("id", "")
                    if key and want in name:
                        await self._client.select_profile(key)
                        self._say("  selected profile %s (%s)"
                                  % (key, profile.get("name", "")))
                        matched = True
                        break
                if not matched:
                    self._say("  no profile matching band %r; tuning the "
                              "server's default profile" % band.name)
            if self._bandwidth is not None:
                self._say("  wide-FM demod for APT: %s bandwidth %d Hz"
                          % (self._mode, self._bandwidth))
        else:
            self._client = WebSDRClient(self._plan.receiver.url,
                                        reconnect=False)
            await self._client.__aenter__()
        return self._client

    async def close(self) -> None:
        client, self._client = self._client, None
        if client is not None:
            try:
                await client.__aexit__(None, None, None)
            except Exception:  # noqa: BLE001 - best-effort teardown
                pass

    async def record(self, frequency_hz: float, seconds: float):
        """Record *seconds* at *frequency_hz*, retuning without a reconnect.

        A failed call closes the connection so the next call rebuilds it;
        the caller decides how hard to retry.
        """
        client = self._client
        if client is None:
            client = await self._open()
        try:
            if self._is_openwebrx:
                await client.tune(frequency=int(frequency_hz),
                                  modulation=self._mode,
                                  bandwidth=self._bandwidth)
                chunks = await client.record(seconds)
                rate = chunks[0].samplerate if chunks else 12000
                return OpenWebRXClient.concat(chunks), rate
            await client.tune(frequency_khz=frequency_hz / 1000.0,
                              mode=self._mode)
            chunks = await client.record(seconds)
            rate = int(chunks[0].samplerate) if chunks else 8000
            return WebSDRClient.concat(chunks), rate
        except Exception:
            await self.close()  # force a fresh connection on the next call
            raise


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

    Returns the number of samples written. One connection is held for the
    whole pass and retuned in-stream while tracking Doppler, so there are no
    segment-boundary gaps; a dropped connection costs the rest of the current
    window, not the pass.
    """
    from . import tle as tle_module

    say = on_progress or (lambda message: None)
    if getattr(plan, "orbit_record", None) is not None:
        tracker = Tracker.from_record(plan.orbit_record)
    else:
        text = plan.tle_text or tle_module.load("amateur", offline=offline_tle)
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

    session = _ChaseSession(plan, say)
    try:
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
                samples, rate = await session.record(current, window)
                failures = 0
            except Exception as exc:
                failures += 1
                say("  segment failed (%s); retrying in %.0f s"
                    % (exc, min(failures, 10)))
                # A receiver that refuses instantly would otherwise spin.
                await asyncio.sleep(min(failures, 10))
                continue
            if samples:
                collected.extend(samples)
                say("  +%d samples (%.1f s of audio, total %.1f s)"
                    % (len(samples), len(samples) / rate, len(collected) / rate))
    finally:
        await session.close()

    if not collected:
        raise RuntimeError("no audio captured during the pass")
    write_wav(out_path, collected, rate)
    say("wrote %s: %d samples at %d Hz (%.1f s)"
        % (out_path, len(collected), rate, len(collected) / rate))
    return len(collected)
