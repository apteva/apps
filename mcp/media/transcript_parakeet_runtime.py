"""Transcribe one 16 kHz mono PCM WAV with Parakeet TDT (sherpa-onnx, CPU).

Usage: python runtime.py request.json MODEL_DIR
Prints one line: APTEVA_PARAKEET:{json}. Words are [start_ms, end_ms, text,
confidence, estimated]. Failures print APTEVA_PARAKEET_ERROR:{"code":...}.
"""
import json
import math
import os
import sys
import time
import wave

RUNTIME = "sherpa-onnx-2.0.2"
MODEL = "parakeet-tdt-0.6b-v3-int8"
MIN_WORD_S = 0.04


def fail(code, status=1):
    print("APTEVA_PARAKEET_ERROR:" + json.dumps({"code": code}), flush=True)
    sys.exit(status)


def read_wav(path):
    import numpy as np
    with wave.open(path, "rb") as w:
        if w.getnchannels() != 1 or w.getsampwidth() != 2 or w.getframerate() != 16000:
            fail("parakeet_audio_format_invalid")
        frames = w.readframes(w.getnframes())
    return np.frombuffer(frames, dtype="<i2").astype(np.float32) / 32768.0, 16000


def chunk_bounds(samples, sr, chunk_s):
    """Cut every ~chunk_s seconds at the quietest 0.5 s within +-3 s, so a
    word is rarely split between two decodes."""
    import numpy as np
    n = len(samples)
    chunk = int(chunk_s * sr)
    if chunk <= 0 or n <= chunk:
        return [(0, n)]
    bounds, start, win = [], 0, int(0.5 * sr)
    while start < n:
        target = start + chunk
        if target >= n:
            bounds.append((start, n))
            break
        lo, hi = max(start + sr, target - 3 * sr), min(n, target + 3 * sr)
        seg = samples[lo:hi]
        if len(seg) <= win:
            cut = target
        else:
            energy = np.convolve(seg * seg, np.ones(win, dtype=np.float32), "valid")
            cut = lo + int(np.argmin(energy)) + win // 2
        bounds.append((start, cut))
        start = cut
    return bounds


def chunk_words(res, offset):
    """Merge SentencePiece tokens into words. A token starting with a space
    (or U+2581) starts a word. Punctuation-only tokens join the previous word's
    text but never move its end: TDT gives them the following pause as duration."""
    toks = list(res.tokens)
    starts = list(res.timestamps)
    durs = list(getattr(res, "durations", []) or [])
    logp = list(getattr(res, "ys_log_probs", []) or [])
    words = []
    for i, tok in enumerate(toks):
        t0 = starts[i]
        t1 = t0 + durs[i] if durs else (starts[i + 1] if i + 1 < len(starts) else t0 + 0.08)
        piece = tok.replace("▁", " ")
        lp = [logp[i]] if i < len(logp) else []
        punct = not any(ch.isalnum() for ch in piece)
        if (piece.startswith(" ") and not punct) or not words:
            words.append({"text": piece.strip(), "start": t0, "end": t1, "lp": lp})
        else:
            words[-1]["text"] += piece.strip() if punct else piece
            if not punct:
                words[-1]["end"] = t1
            words[-1]["lp"] += lp
    for w in words:
        w["start"] += offset
        w["end"] += offset
    return [w for w in words if w["text"].strip()]


def spread_collapsed(words):
    """The decoder occasionally emits several words on one frame. Spread such a
    run evenly up to the next word's start and flag the words as estimated."""
    i = 0
    while i < len(words):
        j = i
        while (j + 1 < len(words) and words[j]["end"] - words[j]["start"] <= 0.081
               and words[j + 1]["start"] - words[j]["start"] <= 0.081):
            j += 1
        collapsed = any(words[k + 1]["start"] - words[k]["start"] < 0.01 for k in range(i, j))
        if j > i and collapsed:
            n = j - i + 1
            a = words[i]["start"]
            b = words[j + 1]["start"] if j + 1 < len(words) else max(words[j]["end"], a + 0.08 * n)
            step = max(b - a, 0.08 * n) / n
            for k in range(i, j + 1):
                words[k]["start"] = a + step * (k - i)
                words[k]["end"] = a + step * (k - i + 1)
                words[k]["estimated"] = True
        i = j + 1


def finalize(words, duration_s):
    """Every word gets a positive interval inside the audio, with starts in order."""
    out, prev = [], 0.0
    for w in words:
        start = min(max(w["start"], prev, 0.0), max(duration_s - MIN_WORD_S, 0.0))
        end = min(max(w["end"], start + MIN_WORD_S), duration_s)
        if end <= start:
            continue
        prev = start
        conf = None
        if w["lp"]:
            conf = min(1.0, max(0.0, math.exp(sum(w["lp"]) / len(w["lp"]))))
        out.append([int(round(start * 1000)), int(round(end * 1000)), w["text"].strip(),
                    None if conf is None else round(conf, 4), bool(w.get("estimated"))])
    return out


def main():
    if len(sys.argv) != 3:
        fail("parakeet_usage")
    with open(sys.argv[1]) as fh:
        req = json.load(fh)
    model_dir = sys.argv[2]
    base = os.path.dirname(os.path.abspath(sys.argv[1]))
    audio = req["audio"] if os.path.isabs(req["audio"]) else os.path.join(base, req["audio"])
    threads = max(1, min(int(req.get("threads", 4)), os.cpu_count() or 1, 16))
    chunk_s = float(req.get("chunk_s", 30))
    try:
        import sherpa_onnx
    except Exception:
        fail("parakeet_runtime_unavailable")
    samples, sr = read_wav(audio)
    duration_s = len(samples) / sr
    t = time.monotonic()
    try:
        rec = sherpa_onnx.OfflineRecognizer.from_transducer(
            encoder=os.path.join(model_dir, "encoder.int8.onnx"),
            decoder=os.path.join(model_dir, "decoder.int8.onnx"),
            joiner=os.path.join(model_dir, "joiner.int8.onnx"),
            tokens=os.path.join(model_dir, "tokens.txt"),
            model_type="nemo_transducer",
            num_threads=threads,
            provider="cpu",
        )
    except Exception:
        fail("parakeet_model_unavailable")
    load_ms = int((time.monotonic() - t) * 1000)
    t = time.monotonic()
    bounds = chunk_bounds(samples, sr, chunk_s)
    texts, words = [], []
    for a, b in bounds:
        st = rec.create_stream()
        st.accept_waveform(sr, samples[a:b])
        rec.decode_stream(st)
        text = st.result.text.strip()
        if text:
            texts.append(text)
        words.extend(chunk_words(st.result, a / sr))
    spread_collapsed(words)
    timed = finalize(words, duration_s)
    result = {
        "runtime": RUNTIME,
        "model": MODEL,
        "duration_ms": int(round(duration_s * 1000)),
        "threads": threads,
        "chunks": len(bounds),
        "load_ms": load_ms,
        "decode_ms": int((time.monotonic() - t) * 1000),
        "text": " ".join(texts),
        "words": timed,
    }
    print("APTEVA_PARAKEET:" + json.dumps(result, ensure_ascii=False, separators=(",", ":")), flush=True)


if __name__ == "__main__":
    main()
