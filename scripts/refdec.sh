# Source me after env.sh: make the external reference decoders (WSJT-X
# jt9/wsprd for FT8/FT4/WSPR, minimodem for RTTY) available and export
# SDRSTREAM_REFDEC_DIR pointing at them.
#
# No root: the Ubuntu .debs are downloaded with apt-get download and
# extracted with dpkg-deb -x into $SDR_REFDEC (default
# $SDR_TOOLCHAIN/refdec). The Go wrapper (decode.refBin/refEnv) finds
# binaries under <dir>/usr/bin and prepends <dir>/usr/lib/<triplet> to
# LD_LIBRARY_PATH for the child process only.
#
# Never fails the caller: on any error it warns and returns, leaving the
# Go decoders as the fallback (decode.FTXDecodeAuto/WSPRDecodeAuto and the
# Go RTTY decoder when minimodem is absent - decode.rttyRef finds it under
# <dir>/usr/bin via SDRSTREAM_REFDEC_DIR, like the WSJT-X tools).
#
# Env:
#   SDR_REFDEC   install prefix (default $SDR_TOOLCHAIN/refdec)
#   APT_LISTS    apt package-lists dir (default $SDR_REFDEC/.aptlists)

SDR_REFDEC="${SDR_REFDEC:-${SDR_TOOLCHAIN:-$HOME/toolchain}/refdec}"
APT_LISTS="${APT_LISTS:-$SDR_REFDEC/.aptlists}"

if [ -x "$SDR_REFDEC/usr/bin/jt9" ] && [ -x "$SDR_REFDEC/usr/bin/wsprd" ] && [ -x "$SDR_REFDEC/usr/bin/minimodem" ]; then
  export SDRSTREAM_REFDEC_DIR="$SDR_REFDEC"
  return 0 2>/dev/null || exit 0
fi

# Map a "not found" shared library to the Ubuntu jammy package that ships it.
_refdec_pkg_for_lib() {
  case "$1" in
    libgfortran.so.5) echo libgfortran5 ;;
    libquadmath.so.0) echo libquadmath0 ;;
    libfftw3f.so.3|libfftw3f_threads.so.3) echo libfftw3-single3 ;;
    libQt5Core.so.5) echo libqt5core5a ;;
    libdouble-conversion.so.3) echo libdouble-conversion3 ;;
    libicui18n.so.70|libicuuc.so.70|libicudata.so.70) echo libicu70 ;;
    libpcre2-16.so.0) echo libpcre2-16-0 ;;
    libzstd.so.1) echo libzstd1 ;;
    libglib-2.0.so.0) echo libglib2.0-0 ;;
    libpcre.so.3) echo libpcre3 ;;
    libz.so.1) echo zlib1g ;;
    libgomp.so.1) echo libgomp1 ;;
    libstdc++.so.6) echo libstdc++6 ;;
    libgcc_s.so.1) echo libgcc-s1 ;;
    libsndfile.so.1) echo libsndfile1 ;;
    libfftw3.so.3) echo libfftw3-double3 ;;
    libasound.so.2) echo libasound2 ;;
    libpulse.so.0) echo libpulse0 ;;
    libopus.so.0) echo libopus0 ;;
    libFLAC.so.8) echo libflac8 ;;
    libvorbisenc.so.2) echo libvorbisenc2 ;;
    libvorbis.so.0) echo libvorbis0a ;;
    libogg.so.0) echo libogg0 ;;
    libmpg123.so.0) echo libmpg123-0 ;;
    libmp3lame.so.0) echo libmp3lame0 ;;
    libasyncns.so.0) echo libasyncns0 ;;
    libdbus-1.so.3) echo libdbus-1-3 ;;
    libxcb.so.1) echo libxcb1 ;;
    libX11.so.6) echo libx11-6 ;;
    libXau.so.6) echo libxau6 ;;
    libXdmcp.so.6) echo libxdmcp6 ;;
    libbsd.so.0) echo libbsd0 ;;
    libmd.so.0) echo libmd0 ;;
    liblzma.so.5) echo liblzma5 ;;
    liblz4.so.1) echo liblz4-1 ;;
    libgcrypt.so.20) echo libgcrypt20 ;;
    libgpg-error.so.0) echo libgpg-error0 ;;
    libcap.so.2) echo libcap2 ;;
  esac
}

_refdec_apt() {
  apt-get -o Dir::State::Lists="$APT_LISTS" \
    -o Dir::Cache="$SDR_REFDEC/.aptcache" \
    -o Dir::Cache::Archives="$SDR_REFDEC/.debs" "$@"
}

_refdec_warn() { echo "refdec: $* (reference decoders unavailable; Go decoders will be used)" >&2; }

mkdir -p "$APT_LISTS/partial" "$SDR_REFDEC/.debs" "$SDR_REFDEC/.aptcache/archives/partial" 2>/dev/null || { _refdec_warn "cannot create $SDR_REFDEC"; return 0 2>/dev/null || exit 0; }

if ! ls "$APT_LISTS"/*Packages* >/dev/null 2>&1 && ! ls "$APT_LISTS"/*lz4* >/dev/null 2>&1; then
  _refdec_apt update -qq || { _refdec_warn "apt-get update failed"; return 0 2>/dev/null || exit 0; }
fi

pkgs="wsjtx minimodem"
for round in 1 2 3; do
  ( cd "$SDR_REFDEC/.debs" && _refdec_apt download $pkgs ) || { _refdec_warn "apt-get download $pkgs failed"; return 0 2>/dev/null || exit 0; }
  for d in "$SDR_REFDEC/.debs"/*.deb; do
    [ -e "$d" ] && dpkg-deb -x "$d" "$SDR_REFDEC" && rm -f "$d"
  done
  missing="$( { ldd "$SDR_REFDEC/usr/bin/jt9" "$SDR_REFDEC/usr/bin/wsprd" "$SDR_REFDEC/usr/bin/minimodem"; } 2>/dev/null | awk '/not found/ {print $1}' | sort -u )"
  [ -z "$missing" ] && break
  pkgs=""
  for lib in $missing; do
    p="$(_refdec_pkg_for_lib "$lib")"
    if [ -z "$p" ]; then
      _refdec_warn "no package mapping for $lib"
      continue
    fi
    case " $pkgs " in *" $p "*) ;; *) pkgs="$pkgs $p" ;; esac
  done
  [ -z "$pkgs" ] && break
done

if [ -x "$SDR_REFDEC/usr/bin/jt9" ] && [ -x "$SDR_REFDEC/usr/bin/wsprd" ] && [ -x "$SDR_REFDEC/usr/bin/minimodem" ]; then
  export SDRSTREAM_REFDEC_DIR="$SDR_REFDEC"
  echo "refdec: jt9/wsprd/minimodem ready under $SDR_REFDEC"
else
  _refdec_warn "install incomplete"
fi
