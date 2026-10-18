"""Stream and decode audio from public WebSDR and OpenWebRX servers.

The package is split by protocol:

* :mod:`sdrstream.openwebrx` -- OpenWebRX / OpenWebRX+ clients (JSON control
  channel, IMA ADPCM or PCM audio).
* :mod:`sdrstream.websdr` -- WebSDR clients (raw stream, mixed table/LPC
  vocoder).
* :mod:`sdrstream.adpcm` and :mod:`sdrstream.vocoder` -- the two audio
  decoders, usable on their own for offline captures.
* :mod:`sdrstream.common` -- URL handling, tuning helpers and WAV output.

Only :mod:`websockets` is required for network use; WAV output relies on the
standard library. ``numpy`` and ``scipy`` are not needed.
"""

from .adpcm import AdpcmDecoder
from .common import (
    OPENWEBRX_MODES,
    WEBSDR_MODE_CODES,
    pcm_bytes,
    wav_bytes,
    websdr_tuning_params,
    websocket_url,
    write_wav,
)
from .openwebrx import (
    AudioChunk,
    OpenWebRXClient,
    OpenWebRXConfig,
)
from .vocoder import WebSdrVocoder
from .websdr import (
    WebSDRClient,
    WebSdrChunk,
    WebSdrStatus,
)

__version__ = "0.1.0"

__all__ = [
    "AdpcmDecoder",
    "AudioChunk",
    "OPENWEBRX_MODES",
    "OpenWebRXClient",
    "OpenWebRXConfig",
    "WEBSDR_MODE_CODES",
    "WebSDRClient",
    "WebSdrChunk",
    "WebSdrStatus",
    "WebSdrVocoder",
    "pcm_bytes",
    "wav_bytes",
    "websdr_tuning_params",
    "websocket_url",
    "write_wav",
]
