#!/usr/bin/env python3
"""Builds the real-speech half of the transcription timing test set (P2-05): LibriSpeech test-clean utterances with word
alignments.

    make_librispeech.py OUTDIR --audio test-clean.tar.gz --alignments librispeech_alignments.zip [--count 30] [--seed 205]

Sources (both permissive):
  * LibriSpeech test-clean, https://www.openslr.org/resources/12/test-clean.tar.gz (CC BY 4.0, V. Panayotov et al.)
  * LibriSpeech Alignments, https://zenodo.org/records/2619474 (CC BY 4.0, Montreal Forced Aligner word and phone boundaries
    by Lugosch). md5 2bab567d0ace651a4ba254e813629f46.

It picks `count` utterances of 5 to 20 seconds, spread over different speakers, converts them to 16 kHz mono signed 16 bit WAV
(the format of the audio_16k.wav derivative) and writes OUTDIR/truth.json in the format make_synthetic.py writes. The alignment
is a forced alignment (a model's estimate, accurate to about 20-30 ms on clean read speech), not a hand label; the figure it
gives is "agreement with an aligner", which is the best open reference there is. Needs ffmpeg on PATH. Standard library only.
The archives are treated as untrusted data: only the members it needs are read, as bytes, never extracted by path.
"""
import argparse
import io
import json
import random
import re
import subprocess
import sys
import tarfile
import zipfile
import os

INTERVAL = re.compile(r'xmin = ([0-9.]+)\s*\n\s*xmax = ([0-9.]+)\s*\n\s*text = "([^"]*)"')


def words_of(textgrid):
    """The 'words' tier of a Praat long-form TextGrid as [(start_s, end_s, WORD)], silences left out."""
    i = textgrid.index('name = "words"')
    j = textgrid.find('item [', i)
    tier = textgrid[i : j if j > 0 else len(textgrid)]
    return [(float(a), float(b), t) for a, b, t in INTERVAL.findall(tier) if t.strip()]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("outdir")
    ap.add_argument("--audio", required=True)
    ap.add_argument("--alignments", required=True)
    ap.add_argument("--count", type=int, default=30)
    ap.add_argument("--seed", type=int, default=205)
    args = ap.parse_args()
    os.makedirs(args.outdir, exist_ok=True)
    rnd = random.Random(args.seed)

    z = zipfile.ZipFile(args.alignments)
    grids = sorted(n for n in z.namelist() if n.startswith("test-clean/") and n.endswith(".TextGrid"))
    by_speaker = {}
    for n in grids:
        by_speaker.setdefault(n.split("/")[1], []).append(n)
    speakers = sorted(by_speaker)
    rnd.shuffle(speakers)

    picked = []  # (utt id, words)
    for spk in speakers * 3:
        if len(picked) >= args.count:
            break
        cands = by_speaker[spk][:]
        rnd.shuffle(cands)
        for n in cands:
            utt = os.path.basename(n)[: -len(".TextGrid")]
            if any(utt == p[0] for p in picked):
                continue
            ws = words_of(z.read(n).decode("utf-8"))
            if ws and 5 <= ws[-1][1] - ws[0][0] <= 20:
                picked.append((utt, ws))
                break
    wanted = {u for u, _ in picked}

    t = tarfile.open(args.audio, "r:gz")
    flac = {}
    for m in t:
        if m.isfile() and m.name.endswith(".flac"):
            utt = os.path.basename(m.name)[: -len(".flac")]
            if utt in wanted:
                flac[utt] = t.extractfile(m).read()
                if len(flac) == len(wanted):
                    break

    clips = []
    for utt, ws in picked:
        name = f"libri_{utt}.wav"
        subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "flac", "-i", "pipe:0", "-ar", "16000", "-ac", "1",
                        "-sample_fmt", "s16", os.path.join(args.outdir, name)], input=flac[utt], check=True)
        clips.append({
            "file": name, "language": "en", "source": "LibriSpeech test-clean (CC BY 4.0) with MFA alignments (CC BY 4.0)",
            "words": [{"text": w.lower(), "start_ms": round(a * 1000), "end_ms": round(b * 1000)} for a, b, w in ws],
        })
        print(name, round(ws[-1][1], 1), "s", len(ws), "words")
    with open(os.path.join(args.outdir, "truth.json"), "w") as f:
        json.dump({"clips": clips}, f, indent=1)


if __name__ == "__main__":
    sys.exit(main())
