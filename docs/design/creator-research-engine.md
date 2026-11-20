# Design note: the creator research engine

> Status: **proposed** · 2026-11-20 · Extends [D-11](../05-decisions.md#d-11-trend-data-sources) (trend sources), [architecture §7.3](../03-architecture.md#73-research-agent-and-ideation) (research agent), [formats v1](formats-v1.md) and [observability v1 §8](observability-v1.md#8-the-improvement-loop) (the improve loop). Nothing here is built. Nothing here asks for a credential or a paid API without the founder's approval (section 8).

## 1. Goal and rules

Find fitness creators whose videos look like our creators' (long takes, one title or story captions, cuts on the drop), on TikTok and Instagram. Learn what works for them: hooks, length, cadence, formats, caption patterns, posting times, and what gets saves and shares. Write the result as a **playbook** in which every claim links to its evidence. The playbook feeds the format library and the improve loop.

**Hard rules.** Public data only. No logins to anyone else's account, no scraping, no automation against accounts. We never download or republish other people's videos. We store links, metadata, short notes in our own words and our own derived numbers. Every source has a rate limit in code. The playbook is creator-agnostic, like the format cards: no other creator's titles or lines.

## 2. Data sources that are usable (checked 2026-11-20)

| Source | What it gives | Limits and terms | Use |
|---|---|---|---|
| **TikTok oEmbed** (`tiktok.com/oembed?url=`) | caption (`title`), author name and URL, thumbnail URL, embed HTML ([docs](https://developers.tiktok.com/docs/en/embed-videos)) | free, no key; no counts, no duration. It is a TikTok-provided tool, so it is allowed under ToS 4.5 | ✅ resolve every TikTok link. The post time comes from the video id (its top 32 bits are the Unix time; to verify on samples) |
| **Viewing public posts as a person** | everything a viewer sees: views, likes, comments, saves, shares, length, the hook | manual only. TikTok's ToS 4.5 bans extracting data "using any automated system ... not provided by TikTok" ([terms](https://www.tiktok.com/legal/page/eea/terms-of-service/en)), and Instagram's terms ban automated collection without permission | ✅ the **watch sheet** (section 4). It is our only source of saves and shares for other people's posts |
| **TikTok Creative Center** (web) | trending hashtags and videos by industry, top ads | no API; the songs and creators tabs were flaky in 2026 (D-11) | ✅ manual review only, as D-11 already says |
| **Web search** (model provider's tool, about $10 per 1,000) | creator roundups, public ranking pages (Virlo, Modash, Heepsy free tiers), platform blogs | we keep facts and links, never copy | ✅ discovery and context |
| **Instagram Business Discovery** (Graph API) | for a known professional account: followers, media count, and per post `like_count`, `comments_count`, `view_count`, plus the public media fields ([docs](https://developers.facebook.com/docs/instagram-platform/instagram-api-with-facebook-login/business-discovery)) | needs our own IG professional account linked to a Facebook Page, a Meta app and `instagram_basic`, `instagram_manage_insights`, `pages_read_engagement`. It skips age-gated accounts and hides likes the owner hid. No saves or shares. The username must be known (there is no search) | ⏳ after approval (Q2) |
| **YouTube Data API v3** | Shorts that fitness creators cross-post: `publishedAt`, duration, title, description, views, likes, comments | free with an API key, 10,000 units a day: `videos.list` costs 1 unit, `search.list` 100 ([quota](https://developers.google.com/youtube/v3/determine_quota_cost)). Data must be **refreshed or deleted within 30 days** ([policies](https://developers.google.com/youtube/terms/developer-policies-guide)) | ⏳ after approval (Q1) |
| **Our creators' own analytics** (TikTok Display API, Instagram Insights, with OAuth consent) | their own posts' views, likes, comments, shares | consent per creator; the TikTok app review is already planned | ⏳ later: the best evidence of what works *for us* |
| **Not used** | TikTok Research API and Meta Content Library (academic and non-profit only); unofficial APIs and scrapers such as EnsembleData, TikAPI and Apify (D-11: only after a legal review) | | ❌ |

## 3. Data model (one migration; shared across workspaces like `trends`)

- `research_creators`: `platform`, `handle`, `profile_url`, `why_similar` (our words), `similarity` (0–1), `status` (candidate, approved, dropped), `approved_by`.
- `research_posts`: `platform`, `url`, `external_id`, `creator_id`, `posted_at` (with `posted_at_source`: id, api or manual), `duration_s`. There are no media columns. A URL that does not point to a post page is refused.
- `research_evidence`: one row per observation. `post_id` or `creator_id`, `source` (oembed, watch_sheet, web, ig_business_discovery, youtube_api, creator_oauth), `method` (api or manual), `observed_at`, `observer` (a person or a run), `metrics` jsonb (views, likes, comments, saves, shares: only what the source gives), `raw_ref` (a hash of the response, never the response), `retain_until` (30 days for YouTube, 12 months otherwise), `run_id`.
- `research_annotations`: `post_id`, `format_id` (an id from `config/formats`), `hook_kind`, `hook_text_pattern` (a title form such as `<TOPIC> 101`, never the words), `text_pattern` (the four of D-38), `cuts_estimate`, `first_cut_s`, `caption_len`, `hashtags_n`, `cta` (save, follow, comment, none), `topic` (a `config/tags` value), `annotator`, `confidence`.
- `research_findings`: `playbook_version`, `claim`, `dimension` (hook, length, cadence, format, caption, time, saves_shares), `effect` (number and unit), `n_posts`, `n_creators`, `confidence`, `evidence_ids[]`.

The research output is shared across workspaces like `trends` (§7.3). The playbook is regenerated from the evidence, so deleting expired evidence regenerates it.

## 4. The agent loop

`cfctl research run --max-usd 2 [--replay <dir>]` runs as a weekly River job once it is stable. Its routing comes from the `research` profile: an Opus 5.5 lead with Haiku 5.5 workers.

1. **Discover.** Workers search the web for fitness creators and read public ranking pages. The lead scores each candidate against our creators' style profiles: the share of each format, the text pattern, the cut rate and the length (`styleprofile` measures). It keeps 20 candidates or fewer, each with a reason and a link. **A person approves** a candidate before it is sampled. Cap: 40 searches a run.
2. **Sample.** For each approved creator, take the 10 most recent posts and their 3 top posts. TikTok posts come from links a person pastes (the watch sheet), resolved by oEmbed. Instagram and YouTube posts come from the API once it is approved. Each source has a token bucket (oEmbed at 1 request a second; YouTube under 2,000 units a day; Graph under its app limit) and a cache keyed by URL.
3. **Annotate.** The **watch sheet** (a `cfctl research sheet` CSV now, a small internal web form later) asks a person who watches the post for the counts and the hook (format id, hook kind, text pattern, about how many cuts, the first cut). That takes about a minute a post. A Haiku worker then annotates the caption from oEmbed or the API: its length, hashtags, call to action, language and the title form, using the same Go matchers as `formats.MatchesTitleForm`. It costs about $0.001 a post, and captions are untrusted data. No model ever sees a frame of someone else's video.
4. **Aggregate** (Go, no model). Compare each post's views and its saves and shares rate with **its own creator's median**, so a large account does not outweigh a small one. Then compare dimensions: format, hook kind, length band, posting hour and weekday, cadence and caption call to action, each in a paired, creator-level comparison with a standard error, in the spirit of `evals/stats.go`.
5. **Playbook.** The lead writes the claims in our words from the aggregate table only, with the evidence ids attached. A claim whose numbers are not in the table is refused. The output is `research/playbook/<yyyy-ww>.md` plus `findings.yaml`.

**Caps.** A run costs at most $2: about 40 searches ($0.40), 200 caption reads ($0.20) and the lead (under $1). That is about $8 a month, inside the spend control of D-31 (platform-billed, its own ledger stage). Every model call is metered and traced (D-39).

**Replay.** Every source response is stored as a fixture of metadata only: the oEmbed fields, the API JSON with media URLs stripped, and the sheet rows. `--replay` serves those fixtures and `LLM_MODE=replay` serves the model calls. A replayed run makes no network call and costs nothing, and it is the test fixture for eval set #3.

## 5. Playbook format

Each finding has a claim, its effect, its sample and its evidence. For example: *"Hooks that show the set before the title: 1.8× the creator's median views (n = 34 posts, 9 creators, ± 0.4) → medium."* It links to every post and observation it rests on. **Confidence:** high means 5 creators or more, 30 posts or more, and an effect over twice its standard error that is seen in at least 2 weekly runs. Medium means 3 creators and 15 posts at the same effect rule. Anything else is low and is shown as a hint. Each finding also states what it does *not* show: correlation, not cause; and the sample, approved creators only. Findings older than 8 weeks without new evidence fall to low.

## 6. How it feeds the product

- **Formats.** A finding about a format becomes a `config/formats/evidence.yaml` entry (format id, finding ids, confidence, trend direction). `list_formats` adds one line per card ("working now: medium evidence") and a small ranking prior that never outweighs fit or the creator's own use. New cards and changes to cards stay **human-written PRs**, as formats v1 §8 says. The engine only drafts a card skeleton with evidence for the person who writes it. The library's hash covers the new file, so the baseline cache follows it.
- **Improve loop.** The improver is shown the current playbook's medium and high findings as a read-only data block, next to the recorded feedback. It is a hint about what to try, never a rule. The acceptance rule is unchanged: a change is kept only when the suite says it helped. The playbook may also *propose* suite rubric items such as "hook carries the text by 1 s", which a person adds to the suite.
- **Creators' own style wins** (D-36). The playbook informs the editor's choice of format and the improver's ideas. It never overrides a creator's measured style or their settings.

## 7. Compliance guardrails (in code, tested)

- Sources are an allowlist in code: a source not in the enum cannot write evidence.
- No column or fixture holds media. A test fails on a fixture with a `media_url` or a video file.
- Rate limits and quotas are enforced per source, and a 429 stops the run.
- A retention sweeper deletes YouTube rows after 30 days and everything else after 12 months, and the playbook is regenerated.
- Takedown: `cfctl research forget <handle>` deletes a creator and all their evidence.
- Captions and web pages are untrusted and delimited in prompts.
- The playbook passes a check that it quotes no other creator's text verbatim: an 8-word overlap with any stored caption fails.
- GDPR: handles and public numbers are personal data. We keep the minimum, keep a legitimate-interest note (Q4) and never sell it or show it to customers by name.

**Flags against settled decisions.** D-11 lists TikTok sources only, and the README scopes v1 to TikTok. Instagram here is **learning, not publishing**, so D-25 still holds, but adding Instagram as a research source needs a line in D-11. Architecture §7.3 describes a *daily* trend scan with `trends`, `trend_snapshots` and `concepts`. This engine is weekly and creator-centred: it writes `research_*` and can fill `trends` (kind format) from its findings. No conflict, but §7.3 should link here.

## 8. Build plan

| # | Task | Acceptance |
|---|---|---|
| R1 | Migration for the five tables, the source allowlist, the retention sweeper and `cfctl research forget` | The tests refuse a media field and an unknown source; the sweeper deletes a 31-day YouTube row; no network call |
| R2 | oEmbed resolver (cache, 1 request a second, time from the id) and the watch sheet import (CSV → evidence and annotations, validated against format ids and the taxonomy) | 30 posts from 5 creators entered and resolved; the post times match the shown dates on 10 samples; replay fixtures recorded |
| R3 | Discovery run (web search, similarity to the style profiles, a person approves) with `--max-usd` and `--replay` | One live run under $1 gives 15 candidates or more, each with a working link; a replay is free and the same |
| R4 | Aggregation and playbook (Go stats, confidence rules, a lead write-up refused when it is not backed by the table, the verbatim check) | Deterministic from fixtures; every claim has an evidence link; eval set #3's link checker passes |
| R5 | Feeding the product: `evidence.yaml`, the `list_formats` line and prior, the improver's data block, and the library hash | The suite shows no regression in a stored run; the improver's request contains the findings; the tests cover the prior's cap |
| R6 | (gated on Q1 and Q2) YouTube and Instagram Business Discovery adapters, with quota guards | Quota stays under the cap in a test; fixtures are stripped of media URLs; 30-day refresh for YouTube |

## 9. Open questions for the founder

1. **YouTube Data API key:** a Google Cloud project with the key in `.env`. It is free at 10,000 units a day. Approve?
2. **Meta:** an Instagram professional account for Content Fabrique, a Facebook Page, a Meta app with App Review for `instagram_basic`, `instagram_manage_insights` and `pages_read_engagement`, and probably business verification of the BV (D-29). It is free but takes weeks. Approve?
3. **Who fills the watch sheet?** About 1 minute a post, about 50 posts a week. You, a helper or the beta creators?
4. **Legal (P0-12):** may we keep public handles and counts of third-party creators under a legitimate-interest note, for 12 months?
5. **Budget:** $2 a run, weekly (about $8 a month) on the dev key. Does it count toward the $75 test cap?
6. **Paid data:** I recommend **no** unofficial TikTok APIs (EnsembleData from about $100 a month, TikAPI about $29–189; D-11 needs a legal review first). Chartmetric (from about $350 a month) stays deferred for songs. Agree?
7. **Seeds:** may we use our two creators' public accounts and their own analytics (with OAuth consent) as the reference for "similar"?
