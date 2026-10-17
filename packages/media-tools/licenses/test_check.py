"""Tests for check.py. Run: python3 -I -m unittest discover -s packages/media-tools/licenses -v"""
import copy
import hashlib
import json
import tempfile
import unittest
from pathlib import Path

import check

DEP5 = "Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/\n\nFiles: *\nCopyright: x\nLicense: {lic}\n"
CONF = "--prefix=/opt/ffmpeg --enable-gpl --enable-libx264 --enable-libass"


def sha(text):
    return hashlib.sha256(text.encode()).hexdigest()


class Image:
    """A fake extracted image."""

    def __init__(self, root: Path, pkgs: dict[str, str], conf: str = CONF):
        (root / "media-tools").mkdir()
        rows = ["kind\tname\tversion\tsource\tsha256"]
        for name, text in pkgs.items():
            (root / "doc" / name).mkdir(parents=True)
            (root / "doc" / name / "copyright").write_text(text)
            rows.append(f"deb\t{name}\t1.0\t{name}\t")
        (root / "media-tools" / "inventory.tsv").write_text("\n".join(rows) + "\n")
        (root / "media-tools" / "ffmpeg-configuration.txt").write_text(conf + "\n")
        self.root = root


def policy_for(pkgs: dict[str, str], **overrides):
    comps = {n: {"category": "permissive", "binaryLicence": "MIT", "copyrightSha256": sha(t)} for n, t in pkgs.items()}
    p = {"ffmpegConfigure": {"requiredEnable": ["--enable-gpl"], "allowedEnable": ["--enable-gpl", "--enable-libx264", "--enable-libass"]},
         "components": comps}
    for k, v in overrides.items():
        p["components"][k].update(v)
    return p


class CheckTest(unittest.TestCase):
    def run_check(self, pkgs, policy, conf=CONF):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / "doc").mkdir()
            Image(root, pkgs, conf)
            comps, got_conf = check.load_components(root)
            return check.check(policy, comps, got_conf)

    def test_clean(self):
        pkgs = {"a": DEP5.format(lic="MIT"), "b": "prose only, no license field"}
        fails, warns = self.run_check(pkgs, policy_for(pkgs))
        self.assertEqual(fails, [])
        self.assertEqual(warns, [])

    def test_unlisted_component_fails(self):
        pkgs = {"a": DEP5.format(lic="MIT"), "new": DEP5.format(lic="MIT")}
        pol = policy_for({"a": pkgs["a"]})
        fails, _ = self.run_check(pkgs, pol)
        self.assertTrue(any("new" in f and "not in the allowlist" in f for f in fails), fails)

    def test_changed_copyright_fails(self):
        pkgs = {"a": DEP5.format(lic="MIT")}
        pol = policy_for(pkgs)
        pkgs["a"] = DEP5.format(lic="MIT") + "\nFiles: x\nLicense: GPL-3+\n"
        fails, _ = self.run_check(pkgs, pol)
        self.assertTrue(any("changed since the last review" in f for f in fails), fails)

    def test_banned_tokens_fail_even_when_reviewed(self):
        for lic in ["AGPL-3+", "AGPL-3.0-only", "SSPL-1.0", "CC-BY-NC-SA-4.0", "non-free", "Proprietary", "PolyForm-Noncommercial-1.0.0", "Commons-Clause"]:
            with self.subTest(lic=lic):
                pkgs = {"a": DEP5.format(lic=lic)}
                fails, _ = self.run_check(pkgs, policy_for(pkgs))  # the SHA matches: still banned
                self.assertTrue(any("banned licence" in f for f in fails), fails)

    def test_banned_inside_expression(self):
        pkgs = {"a": DEP5.format(lic="MIT or AGPL-3+")}
        fails, _ = self.run_check(pkgs, policy_for(pkgs))
        self.assertTrue(any("banned" in f for f in fails), fails)

    def test_unrelated_words_are_not_banned(self):
        pkgs = {"a": DEP5.format(lic="MIT") + "\nLicense: Zlib\n\nLicense: BSD-3-clause\n"}
        fails, _ = self.run_check(pkgs, policy_for(pkgs))
        self.assertEqual(fails, [])

    def test_permissive_must_be_on_the_list(self):
        pkgs = {"a": DEP5.format(lic="MIT")}
        fails, _ = self.run_check(pkgs, policy_for(pkgs, a={"binaryLicence": "GPL-2.0-or-later"}))
        self.assertTrue(any("not on the permissive list" in f for f in fails), fails)

    def test_exception_needs_reason_and_may_not_be_banned(self):
        pkgs = {"a": DEP5.format(lic="GPL-2+")}
        fails, _ = self.run_check(pkgs, policy_for(pkgs, a={"category": "copyleft-exception", "binaryLicence": "GPL-2.0-or-later"}))
        self.assertTrue(any("needs a reason" in f for f in fails), fails)
        fails, _ = self.run_check(pkgs, policy_for(pkgs, a={"category": "copyleft-exception", "binaryLicence": "AGPL-3.0", "reason": "no"}))
        self.assertTrue(any("is banned" in f for f in fails), fails)
        fails, _ = self.run_check(pkgs, policy_for(pkgs, a={"category": "copyleft-exception", "binaryLicence": "GPL-2.0-or-later", "reason": "x264"}))
        self.assertEqual(fails, [])

    def test_bad_category_and_sha(self):
        pkgs = {"a": DEP5.format(lic="MIT")}
        fails, _ = self.run_check(pkgs, policy_for(pkgs, a={"category": "whatever", "copyrightSha256": "zz"}))
        self.assertTrue(any("category must be" in f for f in fails) and any("malformed" in f for f in fails), fails)

    def test_ffmpeg_flags(self):
        pkgs = {"a": DEP5.format(lic="MIT")}
        pol = policy_for(pkgs)
        for flag in ["--enable-nonfree", "--enable-libfdk-aac", "--enable-libx265", "--enable-version3"]:
            with self.subTest(flag=flag):
                fails, _ = self.run_check(pkgs, pol, CONF + " " + flag)
                self.assertTrue(any(flag in f for f in fails), fails)
        fails, _ = self.run_check(pkgs, pol, "--prefix=/opt/ffmpeg --enable-libx264")
        self.assertTrue(any("missing the required --enable-gpl" in f for f in fails), fails)
        fails, _ = self.run_check(pkgs, pol, "")
        self.assertTrue(any("ffmpeg-configuration.txt is missing" in f for f in fails), fails)

    def test_stale_entry_warns(self):
        pkgs = {"a": DEP5.format(lic="MIT")}
        pol = policy_for(pkgs)
        pol["components"]["gone"] = copy.deepcopy(pol["components"]["a"])
        fails, warns = self.run_check(pkgs, pol)
        self.assertEqual(fails, [])
        self.assertTrue(any("gone" in w for w in warns), warns)

    def test_source_checksum(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / "doc" / "ffmpeg").mkdir(parents=True)
            text = "GPL text"
            (root / "doc" / "ffmpeg" / "copyright").write_text(text)
            (root / "media-tools").mkdir()
            (root / "media-tools" / "inventory.tsv").write_text(
                "kind\tname\tversion\tsource\tsha256\nsource\tffmpeg\t8\turl\tdeadbeef\n")
            (root / "media-tools" / "ffmpeg-configuration.txt").write_text(CONF)
            comps, conf = check.load_components(root)
            pol = policy_for({"ffmpeg": text}, ffmpeg={"category": "copyleft-exception", "binaryLicence": "GPL-2.0-or-later",
                                                      "reason": "x264", "sourceSha256": "cafebabe"})
            fails, _ = check.check(pol, comps, conf)
            self.assertTrue(any("source checksum" in f for f in fails), fails)

    def test_tokens(self):
        t = "License: GPL-2+ with GCC exception or MIT\n\nLicense: BSD-3-clause\n Free text\n and more\n"
        self.assertEqual(check.tokens_of(t), ["BSD-3-clause", "GCC exception", "GPL-2+", "MIT"])
        self.assertEqual(check.tokens_of("no fields here"), [])

    def test_markdown_block_round_trip(self):
        pkgs = {"a": DEP5.format(lic="MIT")}
        pol = policy_for(pkgs)
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / "doc").mkdir()
            Image(root, pkgs)
            comps, conf = check.load_components(root)
        block = check.generated_block(pol, comps, conf)
        self.assertEqual(block, check.generated_block(pol, comps, conf), "must be deterministic")
        md = f"# Title\n\nhand written\n\n{check.BEGIN}\nold\n{check.END}\n\nmore hand written\n"
        out = check.splice(md, block)
        self.assertIn("hand written", out)
        self.assertIn("| a | 1.0 | MIT | permissive | MIT |", out)
        self.assertEqual(check.splice(out, block), out)
        with self.assertRaises(check.Problem):
            check.splice("no markers", block)

    def test_the_committed_policy_is_coherent(self):
        pol = json.loads((Path(check.HERE) / "policy.json").read_text())
        for name, e in pol["components"].items():
            self.assertRegex(e["copyrightSha256"], r"^[0-9a-f]{64}$", name)
            if e["category"] == "permissive":
                self.assertIn(e["binaryLicence"], check.PERMISSIVE, name)
            else:
                self.assertEqual(e["category"], "copyleft-exception", name)
                self.assertTrue(e["reason"].strip(), name)
            self.assertIsNone(check.BANNED.search(e["binaryLicence"]), name)
        self.assertTrue(set(pol["ffmpegConfigure"]["requiredEnable"]) <= set(pol["ffmpegConfigure"]["allowedEnable"]))
        self.assertNotIn("--enable-nonfree", pol["ffmpegConfigure"]["allowedEnable"])


if __name__ == "__main__":
    unittest.main()
