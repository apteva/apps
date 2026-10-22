# Test-only mature jitter-buffer reference

Unmodified Xiph SpeexDSP 1.2.1 jitter-buffer source, pinned to commit
`1b28a0f61bc31162979e1f26f3981fc3637095c8` (tag `SpeexDSP-1.2.1`).
Upstream: https://github.com/xiph/speexdsp/tree/1b28a0f61bc31162979e1f26f3981fc3637095c8

See `COPYING` and source notices for the BSD license. The small generated integer
type header and `replay.c` are test adapters. No production Go binary links this
code, downloads it or requires a C compiler. Tests compile locally when `cc` is
available; there is no Docker or network dependency.

The adapter feeds the same immutable RTP header trace and pulls 20 ms output
using SpeexDSP's adaptive jitter buffer with a 60 ms margin. Go injects known
speech and measures decoded output using the same codecs and analysis as the
Telephony receiver. This compares packet selection/playout; the decoder and
concealment are shared test components. It is not a NetEq implementation or a
full independent SIP stack. SpeexDSP's own capacity/delay policy is retained and
must be reported separately from Telephony's limits.
