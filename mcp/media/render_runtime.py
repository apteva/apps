"""Guarded media runtime, embedded in the sidecar; Python 3 standard library only.

Runs against materialized local sources on either executor. Never uploads bytes.
Shared validation evidence is checked by the caller before uploading.
"""
import hashlib
import json
import math
import os
import re
import struct
import subprocess
import threading
import sys
import tempfile
from fractions import Fraction


class MediaResourceFailure(RuntimeError):
    pass

def check_resources(code, log=""):
    if code in (-9,137) or any(x in log.lower() for x in ("cannot allocate memory","out of memory","media_resource_exhausted")):
        raise MediaResourceFailure("media_resource_exhausted: process was killed or exhausted memory; fallback stopped")

def bounded_args(args):
    result=["-filter_threads","1","-filter_complex_threads","1"]
    for arg in args:
        if arg=="-i":result.extend(["-threads","1"])
        result.append(arg)
    return result

def run(binary, args):
    p = subprocess.run([binary] + (bounded_args(args) if "ffprobe" not in os.path.basename(binary) else args), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    check_resources(p.returncode,p.stderr)
    if p.returncode:
        raise ValueError("media_runtime_failed: " + p.stderr[-3000:])
    return p.stdout, p.stderr


def probe(binary, source, extra=None):
    out, _ = run(binary, ["-v", "error"] + (extra or ["-show_streams", "-show_format"]) + ["-of", "json", source])
    return json.loads(out)


def encode(binary, args):
    # The parent's progress reader (or remote progress.log) stays authoritative.
    p = subprocess.run([binary] + bounded_args(args))
    check_resources(p.returncode)
    if p.returncode:
        raise ValueError("media_runtime_failed: encoding exited " + str(p.returncode))


def seconds(value):
    return "%.9f" % value


def movie_metadata(f, allow_fragmented=False):
    """Read bounded MOV metadata without loading compressed media."""
    size = os.fstat(f.fileno()).st_size
    pos = 0
    moov = None
    while pos < size:
        f.seek(pos)
        header = f.read(8)
        if len(header) != 8:
            raise ValueError("incomplete container header")
        length, kind = struct.unpack(">I4s", header)
        hsize = 8
        if length == 1:
            length = struct.unpack(">Q", f.read(8))[0]
            hsize = 16
        if length == 0:
            length = size - pos
        if length < hsize or pos + length > size or (kind == b"moof" and not allow_fragmented):
            raise ValueError("unsupported fragmented or invalid container")
        if kind == b"moov":
            if moov or length > 32 * 1024 * 1024:
                raise ValueError("unsupported movie metadata")
            moov = (pos, hsize, bytearray(f.read(length - hsize)))
        pos += length
    if not moov:
        raise ValueError("missing movie metadata")
    return moov


def movie_boxes(data, start, end):
    while start < end:
        if start + 8 > end:
            raise ValueError("invalid child box")
        n, t = struct.unpack_from(">I4s", data, start)
        if n < 8 or start + n > end:
            raise ValueError("unsupported child box")
        yield start + 8, start + n, t
        start += n


def presentation_durations(filename):
    """Use exact edit boundaries, not rounded ffprobe stream durations.

    A stream's media duration can end at its last picture's PTS while the
    presentation edit includes that picture. Conversely, guarded copy edits
    intentionally hide reordered dependencies. Preserve either boundary.
    """
    with open(filename, "rb") as f:
        _, _, data = movie_metadata(f, allow_fragmented=True)
    children = list(movie_boxes(data, 0, len(data)))
    clocks = [x for x in children if x[2] == b"mvhd"]
    if len(clocks) != 1:
        raise ValueError("missing movie clock")
    o = clocks[0][0]
    if data[o] not in (0, 1):
        raise ValueError("unsupported movie clock")
    scale = struct.unpack_from(">I", data, o + (12 if data[o] == 0 else 20))[0]
    if scale <= 0:
        raise ValueError("unsupported movie timescale")
    durations = {}
    for a, b, kind in children:
        if kind != b"trak":
            continue
        tk = list(movie_boxes(data, a, b))
        handler = None
        for m, n, t in tk:
            if t == b"mdia":
                for h, _, k in movie_boxes(data, m, n):
                    if k == b"hdlr":
                        handler = bytes(data[h + 8:h + 12]).decode("ascii")
        if handler not in ("vide", "soun"):
            continue
        edits = [x for x in tk if x[2] == b"edts"]
        if len(edits) != 1 or handler in durations:
            continue
        entries = [x for x in movie_boxes(data, edits[0][0], edits[0][1]) if x[2] == b"elst"]
        if len(entries) != 1:
            continue
        o = entries[0][0]
        v = data[o]
        if v not in (0, 1) or struct.unpack_from(">I", data, o + 4)[0] != 1:
            continue
        width = 4 if v == 0 else 8
        media_time = struct.unpack_from(">i" if v == 0 else ">q", data, o + 8 + width)[0]
        rate = struct.unpack_from(">hh", data, o + 8 + 2 * width)
        if media_time < 0 or rate != (1, 0):
            continue
        ticks = struct.unpack_from(">I" if v == 0 else ">Q", data, o + 8)[0]
        durations[handler] = Fraction(ticks, scale)
    return scale, durations


def patch_presentation_end(filename, duration, track_durations=None):
    """Keep decoding dependencies; end the MOV/MP4 presentation at an edit boundary."""
    with open(filename, "r+b") as f:
        offset, header_size, data = movie_metadata(f)
        def boxes(start, end):
            return movie_boxes(data, start, end)

        children = list(boxes(0, len(data)))
        mvhd = [x for x in children if x[2] == b"mvhd"]
        if len(mvhd) != 1:
            raise ValueError("missing movie clock")
        o, _, _ = mvhd[0]
        version = data[o]
        if version not in (0, 1):
            raise ValueError("unsupported movie clock")
        scale_offset = o + (12 if version == 0 else 20)
        scale = struct.unpack_from(">I", data, scale_offset)[0]
        ticks = math.ceil(duration * scale) if track_durations else math.floor((duration - 1e-6) * scale + 1e-7)
        if scale < 1000 or ticks <= 0:
            raise ValueError("unsupported movie timescale")
        struct.pack_into(">I" if version == 0 else ">Q", data, scale_offset + 4, ticks)
        tracks = 0
        for a, b, kind in children:
            if kind != b"trak":
                continue
            tk = list(boxes(a, b))
            track_ticks = ticks
            if track_durations:
                handler = None
                for m, n, t in tk:
                    if t == b"mdia":
                        for h, _, k in boxes(m, n):
                            if k == b"hdlr":
                                handler = bytes(data[h + 8:h + 12]).decode("ascii")
                if handler not in track_durations:
                    continue
                track_ticks = math.ceil(track_durations[handler] * scale)
            headers = [x for x in tk if x[2] == b"tkhd"]
            edits = [x for x in tk if x[2] == b"edts"]
            if len(headers) != 1 or len(edits) != 1:
                raise ValueError("missing track edit")
            o, _, _ = headers[0]
            v = data[o]
            if v not in (0, 1):
                raise ValueError("unsupported track header")
            struct.pack_into(">I" if v == 0 else ">Q", data, o + (20 if v == 0 else 28), track_ticks)
            a, b, _ = edits[0]
            elst = [x for x in boxes(a, b) if x[2] == b"elst"]
            if len(elst) != 1:
                raise ValueError("ambiguous edits")
            o, e, _ = elst[0]
            v = data[o]
            if v not in (0, 1) or struct.unpack_from(">I", data, o + 4)[0] != 1:
                raise ValueError("unsupported edit list")
            fmt = ">I" if v == 0 else ">Q"
            # Require a normal forward edit, not a gap/dwell/rate change.
            width = 4 if v == 0 else 8
            media_time = struct.unpack_from(">i" if v == 0 else ">q", data, o + 8 + width)[0]
            rate = struct.unpack_from(">hh", data, o + 8 + 2 * width)
            if media_time < 0 or rate != (1, 0):
                raise ValueError("unsupported presentation edit")
            struct.pack_into(fmt, data, o + 8, track_ticks)
            tracks += 1
        if not tracks:
            raise ValueError("missing movie tracks")
        f.seek(offset + header_size)
        f.write(data)


def packets(binary, source, start, end):
    return probe(binary, source, ["-select_streams", "v:0", "-read_intervals",
        seconds(max(0, start)) + "%" + seconds(end), "-show_packets",
        "-show_entries", "packet=pts_time,dts_time,duration_time,flags"]).get("packets", [])


def movie_child(data, kind):
    found = [(a, b) for a, b, t in movie_boxes(data, 0, len(data)) if t == kind]
    if len(found) != 1:
        raise ValueError("ambiguous movie child " + repr(kind))
    a, b = found[0]
    return data[a:b]


def movie_replace(data, path, replacements):
    """Rebuild bounded metadata boxes without touching compressed samples."""
    result, seen = bytearray(), set()
    for a, b, kind in movie_boxes(data, 0, len(data)):
        payload = data[a:b]
        if path and kind == path[0]:
            if kind in seen:
                raise ValueError("ambiguous movie path")
            payload = movie_replace(payload, path[1:], replacements)
            seen.add(kind)
        elif not path and kind in replacements:
            payload = replacements[kind]
            seen.add(kind)
        if payload is not None:
            result += struct.pack(">I4s", len(payload) + 8, kind) + payload
    if path and path[0] not in seen:
        raise ValueError("missing movie path")
    if not path:
        for kind, payload in replacements.items():
            if kind not in seen and payload is not None:
                result += struct.pack(">I4s", len(payload) + 8, kind) + payload
    return result


def rebase_movie_chunks(data, start, end, offset, old_end, size, delta):
    for a, b, kind in movie_boxes(data, start, end):
        if kind in (b"trak", b"mdia", b"minf", b"stbl"):
            rebase_movie_chunks(data, a, b, offset, old_end, size, delta)
        if kind not in (b"stco", b"co64"):
            continue
        width, fmt = (4, ">I") if kind == b"stco" else (8, ">Q")
        count = struct.unpack_from(">I", data, a + 4)[0]
        if b - a != 8 + count * width:
            raise ValueError("invalid copied chunk offsets")
        for i in range(count):
            pos = a + 8 + i * width
            value = struct.unpack_from(fmt, data, pos)[0]
            if value < 8 or value >= size or offset <= value < old_end:
                raise ValueError("copied chunk offset outside media")
            if value >= old_end:
                if not 0 <= value + delta < 1 << (8 * width):
                    raise ValueError("copied chunk offset exceeds table width")
                struct.pack_into(fmt, data, pos, value + delta)


def restore_copied_video_presentation(req):
    """Retain the source's edit/timing/dependency tables after stream copy.

    MOV demuxers mark samples outside an edit as discard-only dependencies.
    Remuxing their payloads does not retain that presentation distinction, even
    with copyts. Reuse the original track only after every compressed sample
    matches, replacing just the remuxed chunk layout. Full decoded-picture and
    timestamp validation still follows; this is never a validation shortcut.
    """
    with open(req["source"], "rb") as source:
        _, _, original = movie_metadata(source)
    with open(req["output"], "rb") as output:
        offset, header, current = movie_metadata(output)
        size = os.fstat(output.fileno()).st_size

    def video_track(data):
        tracks = [data[a:b] for a, b, kind in movie_boxes(data, 0, len(data))
                  if kind == b"trak" and movie_child(movie_child(data[a:b], b"mdia"), b"hdlr")[8:12] == b"vide"]
        if len(tracks) != 1:
            raise ValueError("source presentation requires one video track")
        return tracks[0]

    original_video, current_video = video_track(original), video_track(current)
    # Track references, external data and encryption need their own relocation
    # contracts. Refuse them rather than copying potentially stale offsets.
    if any(k == b"tref" for _, _, k in movie_boxes(original_video, 0, len(original_video))):
        raise ValueError("referenced video presentation is unsupported")
    original_minf = movie_child(movie_child(original_video, b"mdia"), b"minf")
    dref = movie_child(movie_child(original_minf, b"dinf"), b"dref")
    entries = list(movie_boxes(dref, 8, len(dref)))
    if struct.unpack_from(">I", dref, 4)[0] != 1 or len(entries) != 1 or entries[0][2] != b"url " or dref[entries[0][0]:entries[0][1]] != b"\0\0\0\1":
        raise ValueError("external video samples are unsupported")
    original_stbl = movie_child(original_minf, b"stbl")
    if struct.unpack_from(">I", movie_child(original_stbl, b"stsd"), 4)[0] != 1 or any(k in (b"saio", b"saiz", b"senc") for _, _, k in movie_boxes(original_stbl, 0, len(original_stbl))):
        raise ValueError("complex video sample descriptions are unsupported")

    def copied_packets(filename):
        data = probe(req["ffprobe"], filename, ["-select_streams", "v:0", "-show_packets", "-show_data_hash", "sha256",
            "-show_entries", "packet=pts_time,dts_time,duration_time,flags,size,data_hash"]).get("packets", [])
        if not data or any(not p.get("data_hash") or int(p.get("size", 0)) <= 0 for p in data):
            raise ValueError("missing copied video sample proof")
        return data

    original_packets, current_packets = copied_packets(req["source"]), copied_packets(req["output"])
    if original_packets == current_packets:
        return False  # No presentation change; avoid rewriting large files.
    if [(p["size"], p["data_hash"]) for p in original_packets] != [(p["size"], p["data_hash"]) for p in current_packets]:
        # Do not apply source tables to different sample data. The existing
        # every-picture validator reports any loss/corruption with its counts.
        return False
    layout = movie_child(movie_child(movie_child(current_video, b"mdia"), b"minf"), b"stbl")
    chunk_offsets = [(kind, layout[a:b]) for a, b, kind in movie_boxes(layout, 0, len(layout)) if kind in (b"stco", b"co64")]
    if len(chunk_offsets) != 1:
        raise ValueError("ambiguous copied video chunk layout")
    replacements = {b"stsc": movie_child(layout, b"stsc"), b"stco": None, b"co64": None}
    replacements[chunk_offsets[0][0]] = chunk_offsets[0][1]
    restored = movie_replace(original_video, [b"mdia", b"minf", b"stbl"], replacements)
    tkhd = bytearray(movie_child(restored, b"tkhd"))
    current_tkhd = movie_child(current_video, b"tkhd")
    if tkhd[0] not in (0, 1) or current_tkhd[0] not in (0, 1):
        raise ValueError("unsupported copied track header")
    track_id_offset, current_id_offset = (12 if tkhd[0] == 0 else 20), (12 if current_tkhd[0] == 0 else 20)
    tkhd[track_id_offset:track_id_offset + 4] = current_tkhd[current_id_offset:current_id_offset + 4]
    restored = movie_replace(restored, [], {b"tkhd": tkhd})
    rebuilt = bytearray()
    for a, b, kind in movie_boxes(current, 0, len(current)):
        payload = restored if kind == b"trak" and current[a:b] == current_video else current[a:b]
        rebuilt += struct.pack(">I4s", len(payload) + 8, kind) + payload

    # Both the copied video edit and the audio edit use the movie clock. Keep
    # the original movie duration as well, including a longer audio tail.
    original_clock = movie_child(original, b"mvhd")
    for a, b, kind in movie_boxes(rebuilt, 0, len(rebuilt)):
        if kind != b"mvhd":
            continue
        v, ov = rebuilt[a], original_clock[0]
        if v not in (0, 1) or ov not in (0, 1):
            raise ValueError("unsupported copied movie clock")
        s, oscale = a + (12 if v == 0 else 20), 12 if ov == 0 else 20
        if struct.unpack_from(">I", rebuilt, s)[0] != struct.unpack_from(">I", original_clock, oscale)[0]:
            raise ValueError("copied movie timescale changed")
        duration = struct.unpack_from(">I" if ov == 0 else ">Q", original_clock, oscale + 4)[0]
        struct.pack_into(">I" if v == 0 else ">Q", rebuilt, s + 4, duration)

    if len(rebuilt) > 32 * 1024 * 1024:
        raise ValueError("copied movie metadata exceeds limit")
    delta = len(rebuilt) + 8 - (len(current) + header)
    # Preserve fast-start placement. Rebase all output tracks' absolute chunk
    # offsets if the replacement moov changes size, without moving sample order.
    rebase_movie_chunks(rebuilt, 0, len(rebuilt), offset, offset + header + len(current), size, delta)
    temporary = None
    try:
        with open(req["output"], "rb") as old, tempfile.NamedTemporaryFile(dir=os.path.dirname(os.path.abspath(req["output"])), prefix=".presentation-", delete=False) as new:
            temporary = new.name
            def copy(count):
                while count:
                    block = old.read(min(count, 1024 * 1024))
                    if not block:
                        raise ValueError("incomplete copied movie")
                    new.write(block)
                    count -= len(block)
            copy(offset)
            new.write(struct.pack(">I4s", len(rebuilt) + 8, b"moov") + rebuilt)
            old.seek(offset + header + len(current))
            copy(size - old.tell())
        os.replace(temporary, req["output"])
        temporary = None
    finally:
        if temporary is not None:
            os.unlink(temporary)
    return True


def runtime_event(req, stage, diagnostics=None):
    if req.get("progress") and req["progress"] != "pipe:1":
        with open(req["progress"], "w"):
            pass
    state = req.setdefault("_live_diagnostics", {})
    state.update(diagnostics or {})
    event = {"stage": stage, "diagnostics": state}
    raw = json.dumps(event, allow_nan=False)
    with open("runtime-status.json.tmp", "w") as f:
        f.write(raw)
    os.replace("runtime-status.json.tmp", "runtime-status.json")
    if req.get("progress") == "pipe:1":
        print("APTEVA_STATUS:" + raw, flush=True)


def packet_signature(binary, source):
    info = probe(binary, source, ["-select_streams", "v:0", "-show_packets", "-show_data_hash", "sha256",
        "-show_entries", "packet=pts_time,dts_time,duration_time,flags,data_hash"])
    data = info.get("packets", [])
    if not data or any(not p.get("data_hash") for p in data):
        raise ValueError("missing video packet signature")
    return hashlib.sha256(json.dumps(data, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def run_with_progress(binary, args, progress):
    p = subprocess.Popen([binary] + bounded_args(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    log = []
    def read_log():
        target = open(progress, "w") if progress != "pipe:1" else None
        try:
            for line in p.stderr:
                if re.match(r"(?:out_time_ms|speed|progress)=", line):
                    if target:
                        target.write(line)
                        target.flush()
                    else:
                        print(line.rstrip(), flush=True)
                else:
                    log.append(line)
        finally:
            if target:
                target.close()
    reader = threading.Thread(target=read_log)
    reader.start()
    out = p.stdout.read()
    p.wait()
    reader.join()
    err = "".join(log)
    check_resources(p.returncode,err)
    if p.returncode:
        raise ValueError("media_runtime_failed: " + err[-3000:])
    return out, err


def scan_video(req, source, duration):
    """Decode output once for picture hashes, black frames and both timelines."""
    runtime_event(req, "validation")
    out, log = run_with_progress(req["ffmpeg"], ["-progress", "pipe:2", "-filter_threads", "1", "-hide_banner", "-nostats", "-v", "info", "-xerror",
        "-threads", "2", "-i", source, "-map", "0:v:0", "-map", "0:a:0?",
        "-vf", "blackdetect=d=0:pix_th=0.10,metadata=mode=print:key=lavfi.black_end,showinfo=checksum=0",
        "-af", "ashowinfo", "-fps_mode", "passthrough", "-enc_time_base", "demux", "-f", "framemd5", "-"], req["progress"])
    frames = parse_frame_hashes(out)
    # Canonical decoded timestamps come from the hash muxer. showinfo supplies
    # picture durations; do not count FFmpeg's possibly interleaved log lines.
    shown = re.findall(r"pts_time:([-0-9.e+]+)\s+duration:\s*[-0-9]+\s+duration_time:([-0-9.e+]+)", log)
    last_duration = float(shown[-1][1]) if shown else 0
    duration_source = None
    if last_duration <= 0 and len(frames)>1:
        streams=probe(req["ffprobe"],source,["-select_streams","v:0","-show_entries","stream=start_time,duration"]).get("streams",[])
        if streams:
            try: endpoint=float(streams[0].get("start_time",0))+float(streams[0]["duration"])
            except (KeyError,TypeError,ValueError): endpoint=0
            delta=endpoint-frames[-1][0]
            bound=min(.1,max(.05,2*(frames[-1][0]-frames[0][0])/(len(frames)-1)))
            if 0 < delta <= bound+1e-6:
                last_duration=delta
                duration_source="video_stream_endpoint"

    audio = re.findall(r"pts_time:([-0-9.e+]+)\s+fmt:\S+\s+channels:.*?rate:(\d+).*?nb_samples:(\d+)", log)
    compact = [line for line in log.splitlines() if "black_start:" in line or "lavfi.black_end=" in line]
    if duration_source:
        compact.append("APTEVA_VIDEO_DURATION_SOURCE "+duration_source)
    if frames:
        compact.append("APTEVA_VIDEO_COUNT_SOURCE decoded_frame_hashes")
        compact.append("APTEVA_VIDEO_SCAN count=%d first=%.9f last=%.9f duration=%.9f" %
                       (len(frames), frames[0][0], frames[-1][0], last_duration))
    if audio:
        last = audio[-1]
        compact.append("APTEVA_AUDIO_SCAN count=%d first=%.9f end=%.9f" %
                       (len(audio), float(audio[0][0]), float(last[0]) + int(last[2]) / int(last[1])))
    if not frames or abs(frames[0][0]) > .002 or (audio and abs(float(audio[0][0])) > .002):
        raise ValueError("trim_validation_failed: missing decoded stream or nonzero opening timestamp")
    if frames[-1][0] + last_duration + max(.05, 2 * last_duration) < duration:
        raise ValueError("trim_validation_failed: video ends before requested interval")
    text = "\n".join(compact) + "\n"
    with open("runtime-validation.log", "w") as f:
        f.write(text)
    return frames, text


def frame_hashes(binary, source, start=None, duration=None):
    args = ["-v", "error", "-xerror", "-threads", "2"]
    if start is not None:
        args += ["-ss", seconds(start)]
    args += ["-i", source]
    if duration is not None:
        args += ["-t", seconds(duration)]
    args += ["-map", "0:v:0", "-fps_mode", "passthrough", "-enc_time_base", "demux", "-f", "framemd5", "-"]
    out, _ = run(binary, args)
    return parse_frame_hashes(out)


def source_window_hashes(binary, source, start, end):
    preroll = max(0, start - 2)
    out, _ = run(binary, ["-v", "error", "-xerror", "-threads", "2", "-copyts", "-ss", seconds(preroll),
        "-t", seconds(end - preroll + 2), "-i", source, "-map", "0:v:0",
        "-vf", "select=gte(t\\,%s)*lt(t\\,%s)" % (seconds(start - 1e-6), seconds(end - 1e-6)),
        "-fps_mode", "passthrough", "-enc_time_base", "demux", "-f", "framemd5", "-"])
    return parse_frame_hashes(out)


def parse_frame_hashes(out):
    tb = None
    frames = []
    for line in out.splitlines():
        if line.startswith("#tb 0:"):
            tb = float(Fraction(line.split(":", 1)[1].strip()))
        elif line and not line.startswith("#"):
            fields = line.split(",")
            if len(fields) == 6 and tb is not None and int(fields[0]) == 0:
                frames.append((int(fields[2]) * tb, fields[5].strip()))
    return frames


def color_args(video):
    args = []
    for key, flag in (("color_range", "-color_range"), ("color_space", "-colorspace"), ("color_transfer", "-color_trc"), ("color_primaries", "-color_primaries")):
        if video.get(key) and video[key] not in ("unknown", "unspecified"):
            args += [flag, video[key]]
    return args


def check_video_color(source, output):
    for key in ("pix_fmt", "color_range", "color_space", "color_transfer", "color_primaries", "width", "height"):
        if source.get(key) and source[key] not in ("unknown", "unspecified") and output.get(key) != source[key]:
            raise ValueError("output_video_properties_changed: " + key)


def copy_trim(req, info, diagnostics):
    p = req["params"]
    source, output = req["source"], req["output"]
    start, end = p["start_ms"] / 1000, p["end_ms"] / 1000
    if os.path.splitext(output)[1].lower() not in (".mp4", ".mov"):
        raise ValueError("copy_requires_mp4_or_mov")
    video = next((s for s in info["streams"] if s.get("codec_type") == "video"), None)
    if not video or video.get("codec_name") not in ("hevc", "h264"):
        raise ValueError("copy_requires_h264_or_hevc_video")
    if video.get("side_data_list") and any("rotation" in d and abs(d["rotation"]) % 360 for d in video["side_data_list"]):
        raise ValueError("rotated_video_requires_accurate_encoding")
    chosen = []
    for label, requested in (("start", start), ("end", end)):
        drift = p.get("max_" + label + "_drift_ms", 250) / 1000
        near = packets(req["ffprobe"], source, max(0, requested - drift - 2), requested + drift + 2)
        keys = [float(x["pts_time"]) for x in near if "K" in x.get("flags", "") and "pts_time" in x]
        candidates = [x for x in keys if abs(x - requested) <= drift + 1e-6]
        if not candidates:
            raise ValueError("no_" + label + "_keyframe_within_drift_limit")
        chosen.append(min(candidates, key=lambda x: (abs(x - requested), x)))
    first, last = chosen
    if last <= first:
        raise ValueError("keyframe_interval_is_empty")
    duration = last - first
    expected = sorted(float(x["pts_time"]) - first for x in packets(req["ffprobe"], source, max(0, first - 2), last + 2)
                      if "pts_time" in x and first - 1e-6 <= float(x["pts_time"]) < last - 1e-6)
    if not expected or abs(expected[0]) > .001:
        raise ValueError("missing_source_opening_packet")
    rate = next((int(s["sample_rate"]) for s in info["streams"] if s.get("codec_type") == "audio"), 48000)
    args = ["-y", "-loglevel", "error", "-progress", req["progress"], "-ss", seconds(first), "-i", source,
            "-t", seconds(duration), "-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn", "-c:v", "copy",
            "-c:a", "aac", "-b:a", "192k", "-af", "atrim=duration=" + seconds(duration) + ",aresample=async=1:first_pts=0",
            "-ar", str(rate), "-avoid_negative_ts", "disabled", "-movie_timescale", "1000000", "-movflags", "+faststart+write_colr"] + color_args(video) + [output]
    diagnostics["trim_diagnostics"].update({"mode": "keyframe_copy", "reencoded": False,
        "actual_start_ms": round(first * 1000, 6), "actual_end_ms": round(last * 1000, 6),
        "effective_video_encoding": {"codec": "copy", "source_codec": video["codec_name"]}})
    runtime_event(req, "video_copy", {"trim_diagnostics": diagnostics["trim_diagnostics"]})
    encode(req["ffmpeg"], args)
    patch_presentation_end(output, duration)
    output_info = probe(req["ffprobe"], output)
    check_video_color(video, next((s for s in output_info["streams"] if s.get("codec_type") == "video"), {}))
    frames, validation_log = scan_video(req, output, duration)
    if len(frames) != len(expected) or any(abs(f[0] - t) > .001 for f, t in zip(frames, expected)):
        raise ValueError("copy_presentation_does_not_match_source_packet_timeline")
    # Decode source windows with preroll and original PTS. A key packet in
    # an open GOP may not be independently decodable: compare every opening
    # and ending picture, rather than just the I pictures at either endpoint.
    for a, b in ((first, min(last, first + 1)), (max(first, last - 2), last)):
        source_frames = source_window_hashes(req["ffmpeg"], source, a, b)
        output_frames = [(t + first, h) for t, h in frames if a - 1e-6 <= t + first < b - 1e-6]
        if len(source_frames) != len(output_frames) or not source_frames or any(abs(x[0] - y[0]) > .001 or x[1] != y[1] for x, y in zip(source_frames, output_frames)):
            raise ValueError("copy_opening_or_ending_pictures_differ_from_source")
    diagnostics["trim_diagnostics"].update({"mode": "keyframe_copy", "reencoded": False,
        "actual_start_ms": round(first * 1000, 6), "actual_end_ms": round(last * 1000, 6),
        "start_drift_ms": round((first - start) * 1000, 6), "end_drift_ms": round((last - end) * 1000, 6),
        "source_frames_expected": len(expected), "copy_frames_checked": len(frames),
        "source_endpoint_hashes_match": True, "effective_video_encoding": {"codec":"copy", "source_codec":video["codec_name"]}, "audio_origin":"selected_start", "presentation_cutoff": "mp4_single_edit_duration"})
    diagnostics["trim_validation_log"] = validation_log
    diagnostics["video_evidence"] = {"algorithm_version": "media-shared-validation-1", "decode_ok": True,
        "frames_checked": len(frames), "packet_signature": packet_signature(req["ffprobe"], output)}
    return duration


def filter_prefix(mode):
    return "highpass=f=80,lowpass=f=8000,acompressor=threshold=-18dB:ratio=3:attack=5:release=100," if mode == "speech_clean" else ""


def measurement(req, source, prefix=""):
    p = req["params"]
    _, err = run(req["ffmpeg"], ["-hide_banner", "-nostats", "-v", "info", "-xerror", "-i", source, "-map", "0:a:0", "-vn",
        "-af", prefix + "loudnorm=I=%s:TP=%s:LRA=11:print_format=json" % (p.get("target_lufs", -16) or -16, p.get("target_peak_dbtp", -1.5)), "-f", "null", "-"])
    matches = re.findall(r'\{\s*"input_i".*?\}', err, re.S)
    if not matches:
        raise ValueError("audio_normalization_failed: no loudness measurement")
    measured = json.loads(matches[-1])
    if not all(math.isfinite(float(measured[k])) for k in ("input_i", "input_tp", "input_lra", "input_thresh", "target_offset")):
        raise ValueError("audio_normalization_failed: source has no measurable programme loudness")
    return measured


def normalized_audio(req, info, diagnostics):
    p = req["params"]
    mode = (p.get("mode", "normalize") or "normalize").strip().lower()
    audio = next((s for s in info["streams"] if s.get("codec_type") == "audio"), {})
    extent = float(audio.get("duration", 0))
    extent_filter = "atrim=duration=" + seconds(extent) + "," if extent > 0 else ""
    prefix = extent_filter + filter_prefix(mode)
    target = p.get("target_lufs", -16) or -16
    peak = p.get("target_peak_dbtp", -1.5)
    rate = next((int(s["sample_rate"]) for s in info["streams"] if s.get("codec_type") == "audio"), None)
    if not rate:
        raise ValueError("audio_normalization_failed: no audio sample rate")
    # Lossy encoders get headroom; the FINAL encoded signal is authoritative.
    codec = next((req["args"][i + 1] for i, a in enumerate(req["args"][:-1]) if a == "-c:a"), "aac")
    if codec == "libopus" and rate not in (8000, 12000, 16000, 24000, 48000):
        raise ValueError("audio_normalization_failed: Opus does not support the source sample rate; choose WAV/FLAC/AAC to preserve it")
    headroom = 2 if codec in ("aac", "libmp3lame", "libopus") else 0
    loudnorm_peak = max(-9, peak - headroom)
    internal_peak = loudnorm_peak
    # Retry from the original source; never normalize an already encoded retry.
    args = list(req["args"])
    if codec == "aac":
        # PNS synthesizes replacement noise at decode time. Its reconstructed
        # peaks can exceed PCM peaks by several dB and change non-monotonically
        # with the normalization envelope. Keep the waveform for peak-limited
        # delivery; the final decoded AAC measurement is still authoritative.
        args[-1:-1] = ["-aac_pns", "0"]
    source_video = next((s for s in info["streams"] if s.get("codec_type") == "video"), {})
    durations = None
    if source_video and "-vn" not in args:
        args[-1:-1] = color_args(source_video)
        if os.path.splitext(req["output"])[1].lower() in (".mov", ".mp4"):
            args[-1:-1] = ["-movflags", "+faststart+write_colr"]
            if "mov" in info.get("format", {}).get("format_name", "").split(","):
                scale, durations = presentation_durations(req["source"])
                # Match the source clock so even an exclusive one-microsecond
                # copy cutoff remains exact through audio-only remuxing.
                args[-1:-1] = ["-movie_timescale", str(scale * 1000 // math.gcd(scale, 1000) if scale < 1000 else scale)]
                # Complex/no-edit sources retain FFmpeg's flattened timeline;
                # the full picture comparison below remains authoritative.
                if "vide" not in durations:
                    durations = None
    # Keep the measured loudnorm curve fixed. Changing its TP/offset on each
    # peak retry can produce a different dynamic envelope, including larger
    # excursions after resampling. Correct gain BEFORE a separate native-rate
    # limiter, and judge both constraints on the encoded result.
    measurement_req = dict(req, params=dict(p, target_lufs=target, target_peak_dbtp=loudnorm_peak))
    runtime_event(req, "audio_measurement")
    measured = measurement(measurement_req, req["source"], prefix)
    loudnorm = ("loudnorm=I=%s:TP=%s:LRA=11:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true" %
                (target, loudnorm_peak, measured["input_i"], measured["input_tp"], measured["input_lra"], measured["input_thresh"], measured["target_offset"]))
    attempts = []
    max_attempts = 5
    correction_gain = 0
    for attempt in range(1, max_attempts + 1):
        limiter = "alimiter=limit=%s:attack=5:release=50:level=false:latency=true" % (10 ** (internal_peak / 20))
        chain = prefix + loudnorm + ",aresample=" + str(rate) + ",volume=%sdB," % correction_gain + limiter
        if extent > 0:
            chain += ",atrim=duration=" + seconds(extent)
        for flag, value in (("-af", chain), ("-ar", str(rate))):
            args[args.index(flag) + 1] = value
        diagnostics["audio_normalization"] = {"algorithm_version": "media-two-pass-loudnorm-7", "passes": 2,
            "target_lufs": target, "target_peak_dbtp": peak, "internal_peak_dbtp": internal_peak,
            "effective_target_lufs": target + correction_gain, "measurement_target_lufs": target,
            "measurement_peak_dbtp": loudnorm_peak, "loudnorm_peak_dbtp": loudnorm_peak,
            "correction_gain_db": correction_gain, "max_correction_gain_db": 3,
            "limiter_peak_dbtp": internal_peak, "limiter_latency_compensated": True,
            "audio_codec": codec, "aac_perceptual_noise_substitution": False if codec == "aac" else None,
            "source_measurements": measured, "attempt": attempt, "max_attempts": max_attempts,
            "attempts": attempts, "loudness_tolerance_lu": .5, "peak_tolerance_db": .1,
            "sample_rate": rate, "effective_filter": chain, "validated": False,
            "presentation_boundary_mode": "exact_source_edits" if durations else "encoded_timeline_verified"}
        runtime_event(req, "audio_normalization", {"audio_normalization": diagnostics["audio_normalization"]})
        encode(req["ffmpeg"], args)
        runtime_event(req, "validation")
        if durations:
            patch_presentation_end(req["output"], max(durations.values()), durations)
        actual = measurement(req, req["output"])
        actual_i, actual_tp = float(actual["input_i"]), float(actual["input_tp"])
        attempts.append({"attempt": attempt, "internal_peak_dbtp": internal_peak,
            "effective_target_lufs": target + correction_gain, "correction_gain_db": correction_gain,
            "limiter_peak_dbtp": internal_peak, "source_measurements": measured,
            "encoded_lufs": actual_i, "encoded_peak_dbtp": actual_tp})
        diagnostics["audio_normalization"].update({"encoded_lufs": actual_i, "encoded_peak_dbtp": actual_tp,
            "retry_count": attempt - 1})
        peak_ok = actual_tp <= peak + .1
        if abs(actual_i - target) <= .5 and peak_ok:
            break
        loudness_ok = abs(actual_i - target) <= .5
        attempts[-1]["retry_reasons"] = (["loudness_miss"] if not loudness_ok else []) + (["peak_overshoot"] if not peak_ok else [])
        # Compensate for encoded loudness error without relaxing the public
        # target/ceiling. Bound cumulative gain and attempts for infeasible audio.
        # Even when loudness is just inside tolerance, aim at its centre while
        # correcting an unsafe peak. A later peak correction gets its own
        # loudness recovery within the same bounded five-attempt budget.
        next_gain = max(-3, min(3, correction_gain + target - actual_i))
        next_peak = internal_peak if peak_ok else max(-9, internal_peak - max(1, actual_tp - peak + .5))
        if attempt == max_attempts or (next_peak == internal_peak and abs(next_gain - correction_gain) < .01):
            raise ValueError("audio_normalization_failed: after %d attempt(s), encoded loudness %.2f LUFS / %.2f dBTP differs from requested %.2f LUFS / ceiling %.2f dBTP (internal ceiling %.2f dBTP)" %
                (attempt, actual_i, actual_tp, target, peak, internal_peak))
        internal_peak = next_peak
        correction_gain = next_gain
    # Validate the accepted output once. Video proof reuse and the final
    # every-picture fallback stay authoritative, independent of audio retries.
    runtime_event(req, "validation", {"audio_normalization": diagnostics["audio_normalization"]})
    if durations:
        try:
            restored = restore_copied_video_presentation(req)
        except (ValueError, OSError, struct.error) as error:
            raise ValueError("audio_normalization_failed: cannot preserve source video presentation: " + str(error)) from error
        diagnostics["audio_normalization"]["video_presentation_restored"] = restored
        runtime_event(req, "validation", {"audio_normalization": diagnostics["audio_normalization"]})
    output_info = probe(req["ffprobe"], req["output"])
    output_audio = next((s for s in output_info["streams"] if s.get("codec_type") == "audio"), {})
    if source_video and "-vn" not in args:
        check_video_color(source_video, next((s for s in output_info["streams"] if s.get("codec_type") == "video"), {}))
    if int(output_audio.get("sample_rate", 0)) != rate:
        raise ValueError("audio_normalization_failed: encoded sample rate changed")
    # Video is copied with the source timestamps. Check each stream's start
    # and duration, so an AAC/filter latency does not introduce a new gap.
    for kind in ("video", "audio"):
        src = next((s for s in info["streams"] if s.get("codec_type") == kind), None)
        dst = next((s for s in output_info["streams"] if s.get("codec_type") == kind), None)
        if src is None or (kind == "video" and "-vn" in args):
            continue
        start_tolerance = .002
        duration_tolerance = .05
        if kind == "audio" and "-vn" in args and codec in ("libmp3lame", "libopus"):
            # Gapless MP3/Opus containers signal encoder priming differently
            # from MOV. Preserve programme extent; report the container delay.
            start_tolerance, duration_tolerance = .03, .08
            diagnostics["audio_normalization"]["audio_container_start_delta_seconds"] = float(dst.get("start_time", 0)) - float(src.get("start_time", 0)) if dst else None
        if dst is None or abs(float(src.get("start_time", 0)) - float(dst.get("start_time", 0))) > start_tolerance:
            raise ValueError("audio_normalization_failed: %s start timeline changed" % kind)
        if src.get("duration") and dst.get("duration") and abs(float(src["duration"]) - float(dst["duration"])) > duration_tolerance:
            raise ValueError("audio_normalization_failed: %s duration changed" % kind)
    if source_video and "-vn" not in args:
        # Container duration tolerances alone cannot detect a missing picture.
        # Check every decoded source/output picture and presentation timestamp,
        # including the last picture and any hidden reorder dependencies.
        evidence = p.get("_validated_video_evidence", {})
        reused = False
        if evidence.get("algorithm_version") == "media-shared-validation-1" and evidence.get("decode_ok") and evidence.get("frames_checked", 0) > 0:
            signature = packet_signature(req["ffprobe"], req["source"])
            if signature == evidence.get("packet_signature") and signature == packet_signature(req["ffprobe"], req["output"]):
                reused = True
                diagnostics["audio_normalization"].update({"video_frames_expected": evidence["frames_checked"],
                    "video_frames_checked": evidence["frames_checked"], "video_validation_reused": True,
                    "video_evidence_source_sha256": evidence.get("sha256"), "video_pictures_match": True})
                diagnostics["video_evidence"] = dict(evidence)
                diagnostics["video_evidence"].pop("sha256", None)
        if not reused:
            source_frames = frame_hashes(req["ffmpeg"], req["source"])
            output_frames = frame_hashes(req["ffmpeg"], req["output"])
            diagnostics["audio_normalization"].update({"video_frames_expected": len(source_frames),
                "video_frames_checked": len(output_frames), "video_validation_reused": False})
            if not source_frames or len(source_frames) != len(output_frames) or any(
                    abs(x[0] - y[0]) > .001 or x[1] != y[1] for x, y in zip(source_frames, output_frames)):
                raise ValueError("audio_normalization_failed: retained video pictures or timestamps changed")
            diagnostics["audio_normalization"]["video_pictures_match"] = True
            diagnostics["video_evidence"] = {"algorithm_version": "media-shared-validation-1", "decode_ok": True,
                "frames_checked": len(output_frames), "packet_signature": packet_signature(req["ffprobe"], req["output"])}
    # Video has already been decoded and checked above; finish audio decoding.
    run(req["ffmpeg"], ["-v", "error", "-xerror", "-i", req["output"], "-map", "0:a:0", "-vn", "-sn", "-dn", "-f", "null", "-"])
    diagnostics["audio_normalization"]["decode_ok"] = True
    diagnostics["audio_normalization"]["validated"] = True
    diagnostics["audio_normalization"]["timeline_validated"] = True


def redacted_audio(req, info, diagnostics):
    p = req["params"]
    plan = p.get("_redaction_plan", {})
    audio = next((s for s in info["streams"] if s.get("codec_type") == "audio"), {})
    rate = int(audio.get("sample_rate", 0))
    if not rate or rate != p.get("_source_sample_rate", 48000):
        raise ValueError("audio_redaction_failed: source sample rate differs from indexed selection plan")
    extent = float(audio.get("duration", info.get("format", {}).get("duration", 0)))
    origin = float(info.get("format", {}).get("start_time", 0))
    audio_start = float(audio.get("start_time", 0)) - origin
    has_video = any(s.get("codec_type") == "video" for s in info["streams"])
    if (has_video and abs(origin) > .002) or abs(audio_start * 1000 - plan.get("audio_start_ms", 0)) > 2:
        raise ValueError("audio_redaction_failed: source timeline differs from indexed selection plan; reindex or normalize timeline")
    if extent <= 0 or any(r["start_ms"] < 0 or r["end_ms"] > (audio_start + extent) * 1000 + 2 for r in plan.get("ranges", [])):
        raise ValueError("audio_redaction_failed: selected passage exceeds the actual audio stream")
    donor = plan.get("room_tone")
    if donor and (donor["start_ms"] < audio_start * 1000 - 2 or donor["end_ms"] > (audio_start + extent) * 1000 + 2):
        raise ValueError("audio_redaction_failed: room-tone donor exceeds the actual audio stream")
    args = list(req["args"])
    codec = args[args.index("-c:a") + 1]
    if codec == "libopus" and rate not in (8000, 12000, 16000, 24000, 48000):
        raise ValueError("audio_redaction_failed: Opus cannot preserve this sample rate; choose WAV, FLAC or AAC")
    source_video = next((s for s in info["streams"] if s.get("codec_type") == "video"), {})
    video_copied = bool(source_video and "-vn" not in args)
    durations = None
    if video_copied:
        args[-1:-1] = color_args(source_video)
        if os.path.splitext(req["output"])[1].lower() in (".mov", ".mp4") and "mov" in info.get("format", {}).get("format_name", "").split(","):
            scale, durations = presentation_durations(req["source"])
            args[-1:-1] = ["-movie_timescale", str(scale * 1000 // math.gcd(scale, 1000) if scale < 1000 else scale)]
            if "vide" not in durations:
                durations = None
    d = {"algorithm_version": "media-audio-redact-1", "method": plan["method"],
        "preview": plan["preview"], "source_start_ms": plan["start_ms"], "source_end_ms": plan["end_ms"],
        "ranges": plan["ranges"], "fade_ms": plan["fade_ms"], "sample_rate": rate,
        "video_copied": video_copied, "leading_audio_silence_ms": round(audio_start * 1000), "validated": False}
    if plan["method"] == "room_tone":
        d["room_tone"] = plan["room_tone"]
        d["limitations"] = ["Donor ambience is caller-selected; speech-free content must be reviewed."]
    if plan["method"] == "muffle":
        d["limitations"] = ["Muffle is an audio effect and does not guarantee unintelligible speech."]
    diagnostics["audio_redaction"] = d
    runtime_event(req, "encode", {"audio_redaction": d})
    encode(req["ffmpeg"], args)
    runtime_event(req, "validation")
    if durations:
        patch_presentation_end(req["output"], max(durations.values()), durations)
    output_info = probe(req["ffprobe"], req["output"])
    dst_audio = next((s for s in output_info["streams"] if s.get("codec_type") == "audio"), {})
    if int(dst_audio.get("sample_rate", 0)) != rate:
        raise ValueError("audio_redaction_failed: output sample rate changed")
    if dst_audio.get("channels") != audio.get("channels"):
        raise ValueError("audio_redaction_failed: output audio channel count changed")
    # Check presentation extent independently of successful encoding.
    expected_start = 0
    expected_duration = (plan["end_ms"] - plan["start_ms"]) / 1000 if plan["preview"] else extent + audio_start
    start_tolerance, duration_tolerance = (.03, .08) if codec in ("libmp3lame", "libopus") else (.002, .05)
    d.update({"source_audio_start_seconds": float(audio.get("start_time", 0)), "output_audio_start_seconds": float(dst_audio.get("start_time", 0))})
    if abs(float(dst_audio.get("start_time", 0)) - expected_start) > start_tolerance:
        raise ValueError("audio_redaction_failed: audio timeline start changed")
    if abs(float(dst_audio.get("duration", output_info.get("format", {}).get("duration", 0))) - expected_duration) > duration_tolerance:
        raise ValueError("audio_redaction_failed: audio duration changed")
    if video_copied:
        dst_video = next((s for s in output_info["streams"] if s.get("codec_type") == "video"), {})
        check_video_color(source_video, dst_video)
        if abs(float(source_video.get("start_time", 0)) - float(dst_video.get("start_time", 0))) > .002:
            raise ValueError("audio_redaction_failed: video timeline start changed")
        if source_video.get("duration") and abs(float(source_video["duration"]) - float(dst_video.get("duration", 0))) > .002:
            raise ValueError("audio_redaction_failed: video presentation duration changed")
        signature = packet_signature(req["ffprobe"], req["source"])
        if signature != packet_signature(req["ffprobe"], req["output"]):
            raise ValueError("audio_redaction_failed: compressed video pictures or timestamps changed")
        evidence = p.get("_validated_video_evidence", {})
        reused = evidence.get("algorithm_version") == "media-shared-validation-1" and evidence.get("decode_ok") and evidence.get("frames_checked", 0) > 0 and evidence.get("packet_signature") == signature
        frames_checked = evidence["frames_checked"] if reused else len(frame_hashes(req["ffmpeg"], req["output"]))
        if not frames_checked:
            raise ValueError("audio_redaction_failed: no decoded video pictures")
        d.update({"video_pictures_match": True, "video_frames_checked": frames_checked, "video_validation_reused": bool(reused)})
        diagnostics["video_evidence"] = {"algorithm_version": "media-shared-validation-1", "decode_ok": True,
            "frames_checked": frames_checked, "packet_signature": signature}
    # Decode the complete encoded audio and reject invalid/non-finite samples.
    _, log = run(req["ffmpeg"], ["-hide_banner", "-nostats", "-v", "info", "-xerror", "-i", req["output"], "-map", "0:a:0", "-vn", "-af", "astats=metadata=0:reset=0", "-f", "null", "-"])
    counts = re.findall(r"Number of samples:\s*([0-9.e+]+)", log)
    if not counts or not any(float(n) > 0 for n in counts):
        raise ValueError("audio_redaction_failed: no decoded audio samples")
    for name in ("Number of NaNs", "Number of Infs"):
        if any(float(n) > 0 for n in re.findall(name + r":\s*([0-9.e+]+)", log)):
            raise ValueError("audio_redaction_failed: non-finite encoded audio")
    d.update({"decode_ok": True, "timeline_validated": True, "validated": True})


def main(req, diagnostics):
    info = probe(req["ffprobe"], req["source"])
    if req["operation"] == "trim":
        p = req["params"]
        diagnostics["trim_diagnostics"] = dict(p.get("trim_diagnostics", {}))
        video = next((s for s in info["streams"] if s.get("codec_type") == "video"), {})
        if p.get("require_dolby_vision"):
            raise ValueError("unsupported_color_preservation: Dolby Vision preservation is not supported by this trim workflow")
        if any(d.get("side_data_type") == "DOVI configuration record" for d in video.get("side_data_list", [])):
            diagnostics["trim_diagnostics"]["limitations"] = ["Output carries the source base-layer color; Dolby Vision dynamic metadata and container signaling are not guaranteed by this workflow."]
        if video.get("color_transfer") == "arib-std-b67":
            diagnostics["trim_diagnostics"]["output_color"] = "HLG / BT.2020" if video.get("color_primaries") == "bt2020" else "HLG"
        planned = dict(diagnostics["trim_diagnostics"])
        try:
            copy_trim(req, info, diagnostics)
        except (ValueError, OSError) as error:
            diagnostics["trim_diagnostics"] = planned
            diagnostics["trim_diagnostics"]["fallback_reason"] = str(error)
            diagnostics["trim_diagnostics"]["mode"] = "accurate"
            diagnostics["trim_diagnostics"]["reencoded"] = True
            budget = p.get("render_budget", {})
            if budget.get("estimated_seconds", 0) > req.get("remaining_seconds", 1e10):
                raise ValueError("render_budget_exceeded: accurate fallback estimate exceeds remaining timeout")
            runtime_event(req, "video_encoding", {"trim_diagnostics": diagnostics["trim_diagnostics"]})
            encode(req["ffmpeg"], req["args"])
            duration = (p["end_ms"] - p["start_ms"]) / 1000
            if video:
                frames, validation_log = scan_video(req, req["output"], duration)
                diagnostics["trim_validation_log"] = validation_log
                diagnostics["video_evidence"] = {"algorithm_version": "media-shared-validation-1", "decode_ok": True,
                    "frames_checked": len(frames), "packet_signature": packet_signature(req["ffprobe"], req["output"])}
    elif req["operation"] == "redact_audio":
        redacted_audio(req, info, diagnostics)
    else:
        normalized_audio(req, info, diagnostics)


if __name__ == "__main__":
    diagnostics = {}
    try:
        with open(sys.argv[1]) as f:
            request = json.load(f)
        main(request, diagnostics)
    except Exception as error:
        diagnostics["runtime_error"] = str(error)
        print(str(error), file=sys.stderr)
        sys.exitcode = 1
    finally:
        with open("runtime-result.json", "w") as f:
            json.dump(diagnostics, f, allow_nan=False)
        print("APTEVA_RUNTIME:" + json.dumps(diagnostics, allow_nan=False), flush=True)
    sys.exit(getattr(sys, "exitcode", 0))
