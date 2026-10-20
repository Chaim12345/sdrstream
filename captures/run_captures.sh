#!/bin/bash
# Sequential pass captures via the Go sdrstream binary.
# satrecord --hours 30 finds the NEXT pass with peak elevation >= min-el and
# records it fully; a 3 h timeout covers the longest wait+pass windows.
set -u
cd /workspace/project/sdrstream/go
OUT=/workspace/project/sdrstream/captures
mkdir -p "$OUT"

capture() {
    local sat=$1 lat=$2 lon=$3 server=$4 minel=$5
    echo "=== $(date -u +%H:%M:%S) queueing $sat via $server (min el $minel) ==="
    timeout 10800 ./bin/sdrstream satrecord \
        --satellite "$sat" --lat "$lat" --lon "$lon" \
        --server "$server" --kind websdr \
        --hours 30 --min-elevation "$minel" \
        --out "/tmp/${sat}_pass.wav"
    if [ -f "/tmp/${sat}_pass.wav" ]; then
        mv "/tmp/${sat}_pass.wav" "$OUT/${sat}_$(date -u +%Y-%m-%d_%H%M)_via_sdrge.wav"
        echo "=== $(date -u +%H:%M:%S) saved $sat ==="
    else
        echo "=== $(date -u +%H:%M:%S) $sat produced no file ==="
    fi
}

# RS-44 CW beacon 435.610 MHz over sdr.ge (41.7292, 44.7917): pass at 11:38 UTC, 49.4 deg
capture RS-44 41.7292 44.7917 http://sdr.ge:8901 10
# FO-29 CW beacon 435.795 MHz over sdr.ge: pass at 15:38 UTC, 73.5 deg
capture FO-29 41.7292 44.7917 http://sdr.ge:8901 10
# RS-44 again: high pass at 23:35 UTC, 84.8 deg
capture RS-44 41.7292 44.7917 http://sdr.ge:8901 10
echo "=== $(date -u +%H:%M:%S) all captures done ==="
