# Human speech fixtures

These are two unmodified utterances from LibriSpeech `dev-clean`, speaker 1272,
chapter 128104, decoded from FLAC to mono 16 kHz PCM16 WAV. They contain actual
human audiobook speech, not tones, TTS, customer recordings or production data.

- `1272-128104-0000`: “Mister Quilter is the apostle of the middle classes and we
  are glad to welcome his gospel.”
- `1272-128104-0001`: “Nor is Mister Quilter's manner less interesting than his
  matter.”

Attribution: Vassil Panayotov, Guoguo Chen, Daniel Povey and Sanjeev Khudanpur,
*LibriSpeech: an ASR corpus based on public domain audio books*, 2015. The corpus
is distributed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
Original source and license: [OpenSLR resource 12](https://www.openslr.org/12/).
The recordings originate from the LibriVox public-domain audiobook project.

Download mirror used:
<https://huggingface.co/datasets/hf-internal-testing/librispeech_asr_dummy/blob/main/clean/validation-00000-of-00001.parquet>

Conversion: `ffmpeg -i <utterance>.flac -map_metadata -1 -c:a pcm_s16le <utterance>.wav`.
The test verifies each WAV's SHA256. Playback tests trim only the quiet lead-in,
keeping 40 ms before detected speech onset, and resample for G.711. The stored
fixtures remain unchanged. No external downloads or speech tools are needed to
run tests on any supported platform.
