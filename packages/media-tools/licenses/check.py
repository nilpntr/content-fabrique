#!/usr/bin/env python3
"""Licence inventory check for the media-tools image (D-16 as amended, P1-03).

What it does
  1. Takes the inventory the image build wrote into the image (/usr/share/media-tools/inventory.tsv: every Debian package
     whose files are in the image, plus ffmpeg built from source) and each package's copyright file.
  2. Compares it with the committed allowlist, licenses/policy.json. FAIL when
       - a component is in the image but not in the allowlist;
       - a component's copyright file changed since a human reviewed it (SHA-256 differs), which is how a new or changed
         licence is noticed, including in packages whose copyright file is prose and has no machine-readable tokens;
       - a licence token in a machine-readable copyright file is banned (AGPL, SSPL, non-commercial, non-free, ...);
       - an allowlist entry is inconsistent: a "permissive" component whose licence is not on the permissive list, or a
         "copyleft-exception" one without a reason;
       - ffmpeg was configured with an --enable-* option the allowlist does not name (for example --enable-nonfree,
         --enable-libfdk-aac or --enable-libx265).
  3. Optionally writes or checks the generated table in LICENSES.md.

Only the standard library is used. The tests are in test_check.py.

    check.py --image media-tools:dev [--check-md ../LICENSES.md | --write-md ../LICENSES.md]
    check.py --extracted DIR ...        # DIR holds media-tools/ and doc/ copied out of the image
    check.py --image media-tools:dev --print-shas   # prints each component's current copyright SHA-256, for a review
"""
from __future__ import annotations

import argparse
import csv
import hashlib
import json
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
BEGIN = "<!-- BEGIN GENERATED INVENTORY -->"
END = "<!-- END GENERATED INVENTORY -->"

# Licence tokens that are never acceptable in the product, whatever else the allowlist says. Matched against the tokens of
# machine-readable (DEP-5) copyright files, case-insensitively.
BANNED = re.compile(
    r"agpl|affero|sspl|server[- ]side[- ]public|non-?commercial|(^|[-_ ])nc([-_ ]|$)|non-?free|proprietary|"
    r"commons-?clause|busl|business[- ]source|elastic-2|polyform",
    re.IGNORECASE,
)

# Licences accepted without a named exception (D-16: permissive). Tokens are the names used in policy.json's
# "binaryLicence", not Debian's copyright tokens.
PERMISSIVE = {
    "MIT", "Expat", "BSD-2-clause", "BSD-3-clause", "BSD-variant", "Apache-2.0", "ISC", "Zlib", "libpng", "FTL",
    "HPND-sell-variant", "WTFPL", "public-domain", "0BSD",
}

TOKEN_SPLIT = re.compile(r"\s+(?:or|and|with)\s+|[,()]", re.IGNORECASE)


class Problem(Exception):
    pass


def tokens_of(copyright_text: str) -> list[str]:
    """Licence names in the 'License:' fields of a DEP-5 copyright file, sorted and unique."""
    found: set[str] = set()
    for line in copyright_text.splitlines():
        m = re.match(r"^License:\s*(\S.*)$", line)
        if not m:
            continue
        for tok in TOKEN_SPLIT.split(m.group(1)):
            tok = tok.strip()
            if tok:
                found.add(tok)
    return sorted(found, key=str.lower)


class Component:
    def __init__(self, kind: str, name: str, version: str, source: str, sha256: str, copyright_path: Path):
        self.kind, self.name, self.version, self.source, self.declared_sha256 = kind, name, version, source, sha256
        self.copyright_path = copyright_path
        self.text = copyright_path.read_text(encoding="utf-8", errors="replace") if copyright_path.exists() else ""
        self.copyright_sha256 = hashlib.sha256(self.text.encode("utf-8")).hexdigest() if self.text else ""
        self.tokens = tokens_of(self.text)


def load_components(extracted: Path) -> tuple[list[Component], str]:
    inv = extracted / "media-tools" / "inventory.tsv"
    if not inv.exists():
        raise Problem(f"{inv} is missing: not a media-tools image, or built without the inventory")
    comps = []
    with inv.open(newline="", encoding="utf-8") as f:
        for row in csv.DictReader(f, delimiter="\t"):
            comps.append(
                Component(row["kind"], row["name"], row["version"], row["source"], row["sha256"],
                          extracted / "doc" / row["name"] / "copyright")
            )
    conf = extracted / "media-tools" / "ffmpeg-configuration.txt"
    return sorted(comps, key=lambda c: c.name), conf.read_text(encoding="utf-8").strip() if conf.exists() else ""


def check(policy: dict, comps: list[Component], ffmpeg_conf: str) -> tuple[list[str], list[str]]:
    """Returns (failures, warnings)."""
    fails: list[str] = []
    warns: list[str] = []
    entries: dict = policy.get("components", {})

    # The allowlist itself must be coherent before it is trusted.
    for name, e in entries.items():
        if e.get("category") == "permissive":
            if e.get("binaryLicence") not in PERMISSIVE:
                fails.append(f"policy: {name}: 'permissive' but binaryLicence {e.get('binaryLicence')!r} is not on the permissive list")
        elif e.get("category") == "copyleft-exception":
            if not e.get("reason", "").strip():
                fails.append(f"policy: {name}: a copyleft exception needs a reason")
            if BANNED.search(e.get("binaryLicence", "")):
                fails.append(f"policy: {name}: binaryLicence {e['binaryLicence']!r} is banned")
        else:
            fails.append(f"policy: {name}: category must be 'permissive' or 'copyleft-exception'")
        if not re.fullmatch(r"[0-9a-f]{64}", e.get("copyrightSha256", "")):
            fails.append(f"policy: {name}: copyrightSha256 is missing or malformed")

    names = set()
    for c in comps:
        names.add(c.name)
        e = entries.get(c.name)
        if e is None:
            fails.append(f"{c.name} {c.version}: not in the allowlist. Review its licence ({c.copyright_path.name} in the image), "
                         f"then add it to licenses/policy.json with category and binaryLicence")
            continue
        if not c.text:
            fails.append(f"{c.name}: the image has no copyright file for it")
            continue
        for tok in c.tokens:
            if BANNED.search(tok):
                fails.append(f"{c.name}: banned licence {tok!r}")
        if c.copyright_sha256 != e.get("copyrightSha256"):
            fails.append(f"{c.name} {c.version}: its copyright file changed since the last review (now {c.copyright_sha256}). "
                         f"Read the diff of the licences, then update copyrightSha256 in licenses/policy.json")
        if c.kind == "source" and c.declared_sha256 != e.get("sourceSha256"):
            fails.append(f"{c.name}: source checksum {c.declared_sha256} is not the reviewed {e.get('sourceSha256')}")
    for name in sorted(set(entries) - names):
        warns.append(f"allowlist entry {name} matches nothing in the image (stale?)")

    fails += check_ffmpeg_flags(policy, ffmpeg_conf)
    return fails, warns


def check_ffmpeg_flags(policy: dict, conf: str) -> list[str]:
    if not conf:
        return ["ffmpeg-configuration.txt is missing from the image"]
    allowed = set(policy.get("ffmpegConfigure", {}).get("allowedEnable", []))
    required = set(policy.get("ffmpegConfigure", {}).get("requiredEnable", []))
    flags = set(re.findall(r"--enable-[A-Za-z0-9_-]+", conf))
    out = [f"ffmpeg is configured with {f}, which the allowlist does not name" for f in sorted(flags - allowed)]
    out += [f"ffmpeg is missing the required {f}" for f in sorted(required - flags)]
    return out


def render_table(policy: dict, comps: list[Component]) -> str:
    entries = policy.get("components", {})
    lines = [
        "| Component | Version | Licence of what ships | Status | Licence tokens in its copyright file |",
        "|---|---|---|---|---|",
    ]
    for c in comps:
        e = entries.get(c.name, {})
        status = "named exception" if e.get("category") == "copyleft-exception" else "permissive"
        toks = ", ".join(c.tokens) if c.tokens else "prose only"
        lines.append(f"| {c.name} | {c.version} | {e.get('binaryLicence', '?')} | {status} | {toks} |")
    return "\n".join(lines)


def render_exceptions(policy: dict) -> str:
    out = []
    for name, e in sorted(policy.get("components", {}).items()):
        if e.get("category") == "copyleft-exception":
            out.append(f"- **{name}** ({e['binaryLicence']}): {e['reason']}")
    return "\n".join(out)


def generated_block(policy: dict, comps: list[Component], conf: str) -> str:
    return "\n".join([
        BEGIN,
        "<!-- Generated by licenses/check.py from the built image. Do not edit by hand. -->",
        "",
        "### Components in the image",
        "",
        render_table(policy, comps),
        "",
        "### Named copyleft exceptions",
        "",
        render_exceptions(policy),
        "",
        "### ffmpeg configuration",
        "",
        "```",
        conf,
        "```",
        END,
    ])


def splice(md: str, block: str) -> str:
    if BEGIN not in md or END not in md:
        raise Problem(f"LICENSES.md needs the markers {BEGIN} and {END}")
    head, rest = md.split(BEGIN, 1)
    _, tail = rest.split(END, 1)
    return head + block + tail


def extract_image(image: str, docker: str, dest: Path) -> None:
    """Copies /usr/share/media-tools and /usr/share/doc out of the image without running it."""
    # The image has no shell and no default command, so `create` needs one; it is never started.
    cid = subprocess.run([docker, "create", image, "ffmpeg"], check=True, capture_output=True, text=True).stdout.strip()
    try:
        for src, dst in (("/usr/share/media-tools", "media-tools"), ("/usr/share/doc", "doc")):
            subprocess.run([docker, "cp", f"{cid}:{src}", str(dest / dst)], check=True, capture_output=True)
    finally:
        subprocess.run([docker, "rm", "-f", cid], check=False, capture_output=True)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    src = ap.add_mutually_exclusive_group(required=True)
    src.add_argument("--image", help="image reference to inspect (docker create + cp; the image is never run)")
    src.add_argument("--extracted", type=Path, help="directory holding media-tools/ and doc/ from the image")
    ap.add_argument("--docker", default="docker")
    ap.add_argument("--policy", type=Path, default=HERE / "policy.json")
    md = ap.add_mutually_exclusive_group()
    md.add_argument("--write-md", type=Path, help="rewrite the generated block of this LICENSES.md")
    md.add_argument("--check-md", type=Path, help="fail if the generated block of this LICENSES.md is out of date")
    ap.add_argument("--print-shas", action="store_true", help="print each component's copyright SHA-256 and exit")
    ap.add_argument("--save-md-to", type=Path, help="write the freshly generated block here (for a CI artefact)")
    args = ap.parse_args(argv)

    policy = json.loads(args.policy.read_text(encoding="utf-8"))
    tmp = None
    try:
        if args.image:
            tmp = Path(tempfile.mkdtemp(prefix="media-tools-licences-"))
            extract_image(args.image, args.docker, tmp)
            extracted = tmp
        else:
            extracted = args.extracted
        comps, conf = load_components(extracted)
    except (Problem, subprocess.CalledProcessError) as e:
        print(f"licence check: {e}", file=sys.stderr)
        return 1
    finally:
        if tmp is not None:
            shutil.rmtree(tmp, ignore_errors=True)

    if args.print_shas:
        for c in comps:
            print(f"{c.name}\t{c.version}\t{c.copyright_sha256}\t{', '.join(c.tokens)}")
        return 0

    fails, warns = check(policy, comps, conf)
    for w in warns:
        print(f"warning: {w}", file=sys.stderr)
    block = generated_block(policy, comps, conf)
    if args.save_md_to:
        args.save_md_to.write_text(block + "\n", encoding="utf-8")
    if args.write_md and not fails:
        args.write_md.write_text(splice(args.write_md.read_text(encoding="utf-8"), block), encoding="utf-8")
    if args.check_md:
        current = args.check_md.read_text(encoding="utf-8")
        if splice(current, block) != current:
            fails.append(f"{args.check_md} is out of date with the image. Run: python3 packages/media-tools/licenses/check.py "
                         f"--image <image> --write-md packages/media-tools/LICENSES.md (or take the 'licence-inventory' CI artefact)")
    if fails:
        for f in fails:
            print(f"FAIL: {f}", file=sys.stderr)
        print(f"licence check: {len(fails)} problem(s)", file=sys.stderr)
        return 1
    print(f"licence check: {len(comps)} components, all on the allowlist ({sum(1 for c in comps if policy['components'][c.name]['category'] == 'copyleft-exception')} named copyleft exceptions)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
