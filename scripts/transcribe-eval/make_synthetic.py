#!/usr/bin/env python3
"""Builds the synthetic half of the transcription timing test set (P2-05).

Speech with exactly known word timings, without a forced aligner: every word is recorded on its own with macOS `say`, its
acoustic onset and offset are found in the recording (the first and last 10 ms window louder than -42 dB below its own peak),
and the sentence is assembled by concatenating the trimmed words with silence gaps of known length. A word's start in the
assembled clip is therefore known to the sample, whatever the voice did.

    make_synthetic.py OUTDIR [--voices Samantha,Daniel,...] [--seed N] [--noise-db -45]

writes OUTDIR/<name>.wav (16 kHz, mono, signed 16 bit: the format of the audio_16k.wav derivative) and OUTDIR/truth.json:

    {"clips": [{"file": "synth_samantha_01.wav", "language": "en", "words": [{"text": "...", "start_ms": 520, "end_ms": 800}, ...]}]}

Needs macOS (`say`) and ffmpeg on PATH. Python standard library only. The set is not committed (it is generated in seconds, and
a different voice list gives a different set); the figures it produces are reproducible with the same voices and seed.
"""
import argparse
import array
import json
import math
import os
import random
import shutil
import subprocess
import sys
import tempfile
import wave

RATE = 16000

# Sentences of the kind a gym creator says. Plain words only: the transcript is compared word by word.
SENTENCES = [
    "drive your elbows back and squeeze at the top",
    "this is the best exercise for building a wide back",
    "keep your chest up and your core tight on every rep",
    "three sets of ten with a slow controlled tempo",
    "never lock out your knees at the bottom of the squat",
    "the secret is to lift heavy but keep perfect form",
    "rest for ninety seconds and then go again",
    "if you want bigger arms you need to train them twice a week",
    "breathe in on the way down and push through your heels",
    "today we are going to hit chest and shoulders",
    "do not rush the last rep because that is where growth happens",
    "add five pounds to the bar every single week",
]


def say_word(voice, word, tmp):
    aiff = os.path.join(tmp, "w.aiff")
    wav = os.path.join(tmp, "w.wav")
    subprocess.run(["say", "-v", voice, "-o", aiff, word], check=True)
    subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-y", "-i", aiff, "-ar", str(RATE), "-ac", "1", "-sample_fmt", "s16", wav], check=True)
    with wave.open(wav, "rb") as w:
        if w.getframerate() != RATE or w.getnchannels() != 1 or w.getsampwidth() != 2:
            raise SystemExit("unexpected format from ffmpeg")
        data = array.array("h")
        data.frombytes(w.readframes(w.getnframes()))
    return data


def trim(samples, floor_db=-42.0, win_ms=10):
    """Returns the samples from the first to the last window whose RMS is within floor_db of the loudest window."""
    win = RATE * win_ms // 1000
    rms = []
    for i in range(0, len(samples) - win + 1, win):
        chunk = samples[i : i + win]
        rms.append(math.sqrt(sum(s * s for s in chunk) / win))
    peak = max(rms) if rms else 0
    if peak == 0:
        raise SystemExit("silent recording")
    limit = peak * 10 ** (floor_db / 20)
    idx = [i for i, r in enumerate(rms) if r >= limit]
    return samples[idx[0] * win : (idx[-1] + 1) * win]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("outdir")
    ap.add_argument("--voices", default="Samantha,Daniel,Karen,Moira")
    ap.add_argument("--seed", type=int, default=205)
    ap.add_argument("--noise-db", type=float, default=-48.0, help="level of the white noise mixed under every clip, dBFS (0 = none)")
    args = ap.parse_args()
    if not shutil.which("say") or not shutil.which("ffmpeg"):
        sys.exit("needs macOS `say` and ffmpeg")
    os.makedirs(args.outdir, exist_ok=True)
    rnd = random.Random(args.seed)
    clips = []
    with tempfile.TemporaryDirectory() as tmp:
        for voice in args.voices.split(","):
            cache = {}
            for n, sentence in enumerate(SENTENCES, 1):
                words = sentence.split()
                out = array.array("h", [0] * (RATE * rnd.randint(400, 700) // 1000))
                truth = []
                for wi, word in enumerate(words):
                    if word not in cache:
                        cache[word] = trim(say_word(voice, word, tmp))
                    if wi:
                        gap = rnd.randint(120, 420)  # ms of silence between two words
                        out.extend([0] * (RATE * gap // 1000))
                    start = len(out)
                    out.extend(cache[word])
                    truth.append({"text": word, "start_ms": round(start * 1000 / RATE), "end_ms": round(len(out) * 1000 / RATE)})
                out.extend([0] * (RATE * rnd.randint(400, 700) // 1000))
                if args.noise_db < 0:
                    amp = 32767 * 10 ** (args.noise_db / 20)
                    for i in range(len(out)):
                        out[i] = max(-32768, min(32767, out[i] + int(rnd.gauss(0, amp))))
                name = f"synth_{voice.lower()}_{n:02d}.wav"
                with wave.open(os.path.join(args.outdir, name), "wb") as w:
                    w.setnchannels(1)
                    w.setsampwidth(2)
                    w.setframerate(RATE)
                    w.writeframes(out.tobytes())
                clips.append({"file": name, "language": "en", "source": f"macOS say, voice {voice}, words joined with known gaps", "words": truth})
                print(name, len(out) / RATE, "s", len(words), "words")
    with open(os.path.join(args.outdir, "truth.json"), "w") as f:
        json.dump({"clips": clips}, f, indent=1)


if __name__ == "__main__":
    main()
