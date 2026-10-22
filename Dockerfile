# sdrstream all-in-one image: Go server + pure-Go decoders, with
# multimon-ng / direwolf / minimodem available as external decoders.
#
# Reference decoders jt9/wsprd (WSJT-X, GPL) are NOT baked in: install them
# on the host with scripts/refdec.sh and mount the result, e.g.
#   volumes: ["~/toolchain/refdec:/opt/refdec:ro"]
#   environment: ["SDRSTREAM_REFDEC_DIR=/opt/refdec"]
# They run as separate processes, so their GPL terms do not extend to this
# image (mere aggregation), and sdrstream's AGPL does not extend to them.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go/ ./go/
WORKDIR /src/go
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/sdrstream ./cmd/sdrstream

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata multimon-ng minimodem \
 && adduser -D -u 10001 sdrstream \
 && mkdir -p /data && chown sdrstream:sdrstream /data
COPY --from=build /out/sdrstream /usr/local/bin/sdrstream
USER sdrstream
ENV HOME=/data \
    SDRSTREAM_STATE_DIR=/data/state
# SDRSTREAM_UI_PASSWORD must be supplied at runtime (env or --env-file);
# never bake it into the image.
EXPOSE 8000
VOLUME ["/data"]
ENTRYPOINT ["sdrstream", "web"]
CMD ["--host", "0.0.0.0", "--port", "8000", \
     "--scan-open-directory", "--scan-kiwi-directory", "--scan-record-all"]
