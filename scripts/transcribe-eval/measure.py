#!/usr/bin/env python3
"""Measures the word timing error of the transcription pipeline (P2-05 acceptance: median start error under 100 ms).

    measure.py CLIPDIR --cfctl PATH [--model large-v3-turbo-q5_0] [--models-dir DIR] [--image media-tools:dev] [--jobs 2] [--limit N]
               [--results results.json] [--save-raw DIR]

CLIPDIR holds 16 kHz mono WAV clips and the truth.json that make_synthetic.py or make_librispeech.py wrote. Every clip goes through
`cfctl transcribe file` (the pipeline's own steps in the media-tools sandbox: speech check, whisper.cpp with word alignment,
Refine). The words it returns are matched to the truth words by a sequence alignment on the lower-cased letters (a word the model
got wrong or dropped is not measured, and the match rate is reported), and for every matched word

    start error = |start_ms (pipeline) - start_ms (truth)|          end error likewise.

The median of the start errors is the acceptance figure; it is printed for the pipeline output ("refined") and for the raw model
output before Refine ("raw") so the effect of Refine is visible. Python standard library only.
"""
import argparse
import concurrent.futures
import difflib
import json
import os
import re
import statistics
import subprocess
import sys


def norm(s):
    return re.sub(r"[^a-z0-9']", "", s.lower())


def run_clip(args, clip):
    cmd = [args.cfctl, "transcribe", "file", os.path.join(args.clipdir, clip["file"]), "--model", args.model, "--image", args.image]
    if args.models_dir:
        cmd += ["--models-dir", args.models_dir]
    if args.language:
        cmd += ["--language", args.language]
    p = subprocess.run(cmd, capture_output=True, text=True)
    if p.returncode != 0:
        raise SystemExit(f"{clip['file']}: cfctl failed: {p.stderr.strip()[:500]}")
    out = json.loads(p.stdout)
    if args.save_raw:
        os.makedirs(args.save_raw, exist_ok=True)
        with open(os.path.join(args.save_raw, clip["file"] + ".json"), "w") as f:
            json.dump(out, f)
    return out


def match(truth_words, got_words):
    a = [norm(w["text"]) for w in got_words]
    b = [norm(w["text"]) for w in truth_words]
    sm = difflib.SequenceMatcher(None, a, b, autojunk=False)
    pairs = []
    for blk in sm.get_matching_blocks():
        for k in range(blk.size):
            pairs.append((got_words[blk.a + k], truth_words[blk.b + k]))
    return pairs


def stats(errs):
    if not errs:
        return {"n": 0}
    s = sorted(errs)
    return {"n": len(s), "median_ms": statistics.median(s), "mean_ms": round(statistics.mean(s), 1),
            "p90_ms": s[min(len(s) - 1, int(len(s) * 0.9))], "within_100ms": round(sum(1 for e in s if e <= 100) / len(s), 3)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("clipdir")
    ap.add_argument("--cfctl", required=True)
    ap.add_argument("--model", default="large-v3-turbo-q5_0")
    ap.add_argument("--models-dir", default="")
    ap.add_argument("--image", default="media-tools:dev")
    ap.add_argument("--language", default="")
    ap.add_argument("--jobs", type=int, default=2)
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--results", default="")
    ap.add_argument("--save-raw", default="")
    args = ap.parse_args()

    truth = json.load(open(os.path.join(args.clipdir, "truth.json")))["clips"]
    if args.limit:
        truth = truth[: args.limit]
    with concurrent.futures.ThreadPoolExecutor(args.jobs) as ex:
        outs = list(ex.map(lambda c: run_clip(args, c), truth))

    refined, raw, rend, total_truth, total_matched = [], [], [], 0, 0
    per_clip = []
    for clip, out in zip(truth, outs):
        pairs = match(clip["words"], out["words"])
        total_truth += len(clip["words"])
        total_matched += len(pairs)
        r = [abs(g["start_ms"] - t["start_ms"]) for g, t in pairs]
        refined += r
        rend += [abs(g["end_ms"] - t["end_ms"]) for g, t in pairs]
        rawpairs = match(clip["words"], out["raw_words"])
        raw += [abs(g["start_ms"] - t["start_ms"]) for g, t in rawpairs]
        per_clip.append({"file": clip["file"], "truth_words": len(clip["words"]), "matched": len(pairs),
                         "median_start_ms": statistics.median(r) if r else None, "audio_ms": out["audio_ms"],
                         "transcribe_ms": out["transcribe_ms"], "vad_ms": out["vad_ms"]})
    res = {"model": args.model, "clips": len(truth), "truth_words": total_truth, "matched_words": total_matched,
           "match_rate": round(total_matched / max(total_truth, 1), 3),
           "start_error_refined": stats(refined), "start_error_raw": stats(raw), "end_error_refined": stats(rend), "per_clip": per_clip}
    print(f"set {os.path.basename(os.path.normpath(args.clipdir))}: {len(truth)} clips, {total_truth} words, {total_matched} matched ({res['match_rate']:.1%})")
    for k in ("start_error_raw", "start_error_refined", "end_error_refined"):
        v = res[k]
        print(f"  {k:20s} median {v.get('median_ms')} ms  mean {v.get('mean_ms')} ms  p90 {v.get('p90_ms')} ms  within 100 ms {v.get('within_100ms')}")
    if args.results:
        with open(args.results, "w") as f:
            json.dump(res, f, indent=1)
    ok = res["start_error_refined"].get("median_ms", 1e9) < 100
    print("ACCEPTANCE (median start error < 100 ms):", "PASS" if ok else "FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
