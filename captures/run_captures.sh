#!/bin/bash
# Sequential overnight pass captures via the Go sdrstream binary.
# satrecord --hours 6 finds the NEXT pass with peak elevation >= min-el and
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
        --hours 6 --min-elevation "$minel" \
        --out "/tmp/${sat}_pass.wav"
    if [ -f "/tmp/${sat}_pass.wav" ]; then
        mv "/tmp/${sat}_pass.wav" "$OUT/${sat}_$(date -u +%Y-%m-%d_%H%M)_via_sdrge.wav"
        echo "=== $(date -u +%H:%M:%S) saved $sat ==="
    else
        echo "=== $(date -u +%H:%M:%S) $sat produced no file ==="
    fi
}

# RS-44 CW beacon 435.620 MHz over sdr.ge (41.7292, 44.7917): pass at 00:55 UTC, 28.6 deg
capture RS-44 41.7292 44.7917 http://sdr.ge:8901 10
# FO-29 CW beacon 435.800 MHz over sdr.ge: near-overhead pass at 02:43 UTC, 87.7 deg
capture FO-29 41.7292 44.7917 http://sdr.ge:8901 10
echo "=== $(date -u +%H:%M:%S) all captures done ==="
