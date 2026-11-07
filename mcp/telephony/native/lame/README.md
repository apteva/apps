# Recording MP3 encoder

LAME 3.100, LGPL-2.0-or-later (see COPYING). Unmodified upstream source is
included in lame-3.100.tar.gz; the small Telephony ABI is in encoder.c.
Source: https://downloads.sourceforge.net/project/lame/lame/3.100/lame-3.100.tar.gz
SHA-256: ddfe36cab873794038ae2c1210557ad34857a4b6bdc515785d1da9e175b1da1e

Build with `WASI_SDK_PATH=/absolute/path/to/wasi-sdk-34 ./build.sh`, then rebuild
Telephony normally. This supports replacement/relinking with a modified LAME.
The distributed reactor was built with wasi-sdk 34.0. No runtime installation,
subprocess, host codec library, filesystem grant or network grant is required.
Wazero runs an isolated 32 MiB-limited module per conversion with cancellation.
The runtime caches compiled code, not recording data. Normal background import
and playback concurrency permits apply. PCM is supplied in bounded blocks.

MP3 is 128 kbps CBR, separate stereo channels (not forced mono/joint stereo),
quality 5, with a LAME duration/seek/encoder-delay tag. MP3 is lossy and has codec
padding; keep WAV storage if exact source samples are required. Mono remains mono.
