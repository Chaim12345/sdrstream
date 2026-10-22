# Local receive-only decoder tools

These binaries were unpacked from Ubuntu Jammy packages solely for this local SDRStream run. They are not part of SDRStream's source code and must remain local/untracked. The Dire Wolf shared libraries are included for runtime compatibility; their notices are in `../decoder-notices/`. Do not redistribute without checking all dependency terms.

- multimon-ng 1.1.9+dfsg-2, https://github.com/EliasOenal/multimon-ng, GPL-2.0+; notice at `../decoder-notices/multimon-ng.copyright`.
- Dire Wolf 1.6+dfsg-2, https://github.com/wb2osz/direwolf, GPL-2+; notice at `../decoder-notices/direwolf.copyright`.
- libgps (BSD-3-Clause) and libhamlib (LGPL-2.1+/GPL-2+) notices are also preserved.

Pass explicit binary paths with `--monitor-multimon` and `--monitor-direwolf`. Dire Wolf uses a temporary `ADEVICE stdin null` config and has no network or transmit configuration.
