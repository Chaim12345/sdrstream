"""A small persistent scheduler that records satellite passes automatically.

The pass finder in :mod:`sdrstream.satellite` plans one pass at a time and
:func:`~sdrstream.satellite.chase` waits for the pass to start, which makes
unattended operation a matter of calling it at the right moment. This module
is that missing piece: a JSON job store of satellites to follow, a loop that
checks for upcoming passes, and automatic recording of each pass exactly
once — keyed by the pass start, so restarting the daemon never re-records a
pass it already captured.

The clock and the sleep are injectable, so tests drive the scheduler
without waiting on wall time.
"""

from __future__ import annotations

import json
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Dict, List, Optional

DEFAULT_STORE = Path(
    __import__("os").environ.get(
        "SDRSTREAM_SCHEDULE", str(Path.home() / ".cache" / "sdrstream" / "schedule.json")
    )
)


@dataclass
class Job:
    """One satellite to record automatically."""

    satellite: str
    output_dir: str = "captures"
    min_elevation: float = 10.0
    lookahead_hours: float = 12.0
    #: Pass starts already recorded, as ISO timestamps; pruned on save.
    done: List[str] = field(default_factory=list)

    def to_dict(self) -> Dict:
        return {
            "satellite": self.satellite,
            "output_dir": self.output_dir,
            "min_elevation": self.min_elevation,
            "lookahead_hours": self.lookahead_hours,
            "done": self.done[-100:],  # keep the store bounded
        }

    @classmethod
    def from_dict(cls, raw: Dict) -> "Job":
        return cls(
            satellite=raw["satellite"],
            output_dir=raw.get("output_dir", "captures"),
            min_elevation=float(raw.get("min_elevation", 10.0)),
            lookahead_hours=float(raw.get("lookahead_hours", 12.0)),
            done=list(raw.get("done", [])),
        )


def _pass_key(satellite: str, start: datetime) -> str:
    return "%s@%s" % (satellite.lower(),
                      start.astimezone(timezone.utc).isoformat())


class Scheduler:
    """Records the next pass of every job whose time has come.

    *planner* and *recorder* are injectable for tests; the defaults use the
    real pass planner and :func:`sdrstream.satellite.chase`. *clock* and
    *sleep* default to wall-clock functions.
    """

    def __init__(
        self,
        store: Path = DEFAULT_STORE,
        *,
        planner: Optional[Callable] = None,
        recorder: Optional[Callable] = None,
        clock: Callable[[], float] = time.time,
    ):
        self.store = Path(store)
        self._planner = planner
        self._recorder = recorder
        self._clock = clock
        self._jobs: Dict[str, Job] = {}
        self.load()

    # -- store ---------------------------------------------------------------

    def load(self) -> None:
        self._jobs = {}
        if not self.store.exists():
            return
        try:
            raw = json.loads(self.store.read_text())
        except ValueError:
            return  # a corrupt store starts fresh rather than refusing to run
        for entry in raw.get("jobs", []):
            try:
                job = Job.from_dict(entry)
            except (KeyError, TypeError, ValueError):
                continue
            self._jobs[job.satellite.lower()] = job

    def save(self) -> None:
        self.store.parent.mkdir(parents=True, exist_ok=True)
        payload = {"jobs": [job.to_dict() for job in self._jobs.values()]}
        self.store.write_text(json.dumps(payload, indent=1))

    # -- job management ------------------------------------------------------

    def add(self, satellite: str, output_dir: str = "captures",
            min_elevation: float = 10.0,
            lookahead_hours: float = 12.0) -> Job:
        job = Job(satellite=satellite, output_dir=output_dir,
                  min_elevation=min_elevation,
                  lookahead_hours=lookahead_hours)
        self._jobs[satellite.lower()] = job
        self.save()
        return job

    def remove(self, satellite: str) -> bool:
        existed = self._jobs.pop(satellite.lower(), None) is not None
        if existed:
            self.save()
        return existed

    def jobs(self) -> List[Job]:
        return sorted(self._jobs.values(), key=lambda job: job.satellite)

    # -- scheduling ----------------------------------------------------------

    def _plan(self, job: Job):
        """The first upcoming pass for *job*, or None (no pass / no TLE)."""
        from .satellites import by_name

        planner = self._planner
        if planner is None:
            from .satellite import plan_pass as planner  # type: ignore[no-redef]
        satellite = by_name(job.satellite)
        try:
            return planner(
                satellite,
                hours=job.lookahead_hours,
                min_elevation=job.min_elevation,
            )
        except (ValueError, KeyError, RuntimeError, FileNotFoundError):
            return None  # no pass in the window, or elements unavailable

    def due(self, within_seconds: float = 120.0) -> List[tuple]:
        """Jobs whose next pass starts within *within_seconds*, unrecorded."""
        now = datetime.fromtimestamp(self._clock(), tz=timezone.utc)
        due_jobs = []
        for job in self.jobs():
            plan = self._plan(job)
            if plan is None:
                continue
            key = _pass_key(job.satellite, plan.start)
            if key in job.done:
                continue
            wait = (plan.start - now).total_seconds()
            if wait <= within_seconds:
                due_jobs.append((job, plan, key))
        return due_jobs

    def run_once(self, within_seconds: float = 120.0,
                 on_event: Optional[Callable[[str], None]] = None) -> int:
        """Record every due pass once. Returns how many passes were run.

        The recorder waits for the pass itself, so a due pass is handed over
        immediately and marked done; a later restart will not repeat it.
        """
        say = on_event or (lambda message: None)
        recorded = 0
        for job, plan, key in self.due(within_seconds):
            say("recording %s pass starting %s"
                % (job.satellite, plan.start.strftime("%H:%M:%S")))
            recorder = self._recorder
            if recorder is None:
                from .satellite import chase as recorder  # type: ignore[no-redef]
            out_path = str(Path(job.output_dir) / (
                "%s_%s.wav"
                % (job.satellite.replace(" ", "_"),
                   plan.start.strftime("%Y%m%d_%H%M%S"))))
            Path(job.output_dir).mkdir(parents=True, exist_ok=True)
            try:
                import asyncio

                asyncio.run(recorder(plan, out_path, on_progress=say))
            except Exception as error:  # noqa: BLE001 - one job never kills all
                say("  pass failed: %s" % error)
                continue
            job.done.append(key)
            self.save()
            recorded += 1
        return recorded

    def run(self, poll_seconds: float = 60.0,
            on_event: Optional[Callable[[str], None]] = None) -> None:
        """Poll forever, recording passes as they come due."""
        say = on_event or (lambda message: None)
        while True:
            try:
                self.run_once(within_seconds=poll_seconds + 60.0,
                              on_event=say)
            except Exception as error:  # noqa: BLE001 - the daemon outlives hiccups
                say("scheduler cycle failed: %s" % error)
            time.sleep(poll_seconds)
