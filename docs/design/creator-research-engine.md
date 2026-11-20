# Design note: the creator research engine

> Status: **proposed** · 2026-11-20 · Extends [D-11](../05-decisions.md#d-11-trend-data-sources) (trend sources), [architecture §7.3](../03-architecture.md#73-research-agent-and-ideation) (research agent), [formats v1](formats-v1.md) and [observability v1 §8](observability-v1.md#8-the-improvement-loop) (the improve loop). **R1** (tables, allow-list, retention, `forget`) and **R2** (oEmbed resolver and the watch sheet, section 10) are built; R3 onwards is not. Nothing here asks for a credential or a paid API without the founder's approval (section 8).

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

## 10. R2 as built: the watch sheet and the oEmbed resolver

**The watch sheet.** `cfctl research template > watch-sheet.csv` prints the empty sheet ([`apps/server/internal/research/watch-sheet.template.csv`](../../apps/server/internal/research/watch-sheet.template.csv)) with every column explained. A person watches a public post as a normal viewer and fills one row: `url` (required), `handle` (Instagram and YouTube links only; a TikTok link carries it), `observer`, `watched_at`, `posted_at` (the date the post shows), `length` (42, 42s or 0:42), `format` (an id of `config/formats`), `hook_kind`, `hook_text` (the title FORM in capitals and slots, `<TOPIC> 101`, never the words), `caption_pattern` (single_title, story_captions, dialogue or none), `cta`, `topic` (a `config/tags` value), `cuts`, `first_cut_s`, the counts as shown (`views`, `likes`, `comments`, `saves`, `shares`: 1234, 1,234, 1.2K or 1,2K, 3.4M) and `notes` (our own words, 280 characters, one line, no links). A JSON sheet is an array of objects with the same keys (or `{"rows": [...]}`). A row may stop early (the cells left out are empty) and empty unnamed columns of a spreadsheet export are ignored. One row is `cfctl research add-post <link> --saves 1.2K --format one_take ...`.

**Import.** `cfctl research import week-47.csv [--observer sam]` checks every row first with the R1 rules (a post page only; the watch sheet's source, method and metrics; format ids and tag values that exist; a title form; the note; `research.CheckFixture` over the whole sheet, so a media column such as `video_url`, a link to a media file or a video host, or a data URI anywhere is refused) and stores nothing when one row is wrong. `--dry-run` stops there (no database, no network) and also checks the shown dates against the TikTok ids. Then per row: the creator is checked without writing (a forgotten creator is refused before any call, a dropped one is not sampled), the TikTok link is resolved, and then in one transaction: the creator (a new one is a candidate), the post (named by its platform and id, so the case of a handle in a link does not make another post), one `watch_sheet` observation (method manual, `raw_ref` = the hash of the row as written, so importing the same sheet twice stores nothing twice), the person's annotation with the note (cells the row leaves empty keep what the same person noted before), and for TikTok one `oembed` observation (no metrics, `raw_ref` = the hash of the answer's stable metadata: post id, author and caption, not the thumbnail link, which TikTok signs with an expiry) and an `oembed` annotation with the caption's length and hashtag count. The caption itself is not stored in the database. Retention is R1's (12 months). The note is a new column (migration 00028) with the same limits as a CHECK.

**Post time from the id.** A TikTok post id's top 32 bits are the Unix time of the post (`research.TikTokPostTime`), so `posted_at` comes from the id (`posted_at_source = id`). When the sheet shows a date, the import checks that the id's time falls on that calendar day in some time zone from UTC-12 to UTC+14 (a shown time with its zone: within an hour) and prints a warning, keeping the id's time, when it does not.

**The oEmbed client** (`internal/research/oembed`). It calls only `https://www.tiktok.com/oembed?url=<post link>` with a GET, follows no redirect off that host, waits at least one second between requests (`RESEARCH_OEMBED_INTERVAL_MS`, 1000 to 60000: a setting can slow it, never speed it up), gives up after `RESEARCH_OEMBED_TIMEOUT_S` (default 10), reads at most 256 KB, sends the User-Agent `ContentFabrique-Research/1.0 (oEmbed metadata client; one request a second)` (no person, no address) and caches answers by link. A 429 stops the import; a 400 or 404 (removed, private) fails that row. It keeps only the metadata fields (title, author name and handle, author link, thumbnail link and size, provider); the embed HTML and its script are dropped, and the thumbnail is never fetched. The answer must name the creator in the link. `--record <dir>` writes each answer as a fixture (`tiktok_<id>.json`, checked by `CheckFixture` before it is written) and uses the fixtures already there instead of a call; `--replay <dir>` reads fixtures only and makes no network call (a missing fixture fails the row, a fixture with media is refused when read); `--no-resolve` makes no call at all.

**Fixtures and tests.** `apps/server/internal/research/testdata/r2` holds a synthetic sheet of 30 posts by 5 made-up creators (no real account) and their 30 oEmbed fixtures, generated, not recorded from TikTok: no test and no part of this phase called TikTok. The tests import that sheet in replay (in memory and into Postgres): 30 posts and 5 creators stored and resolved, the 10 shown dates agree with the ids (one sits across midnight in Amsterdam), a second import stores nothing, a forgotten creator is refused before any call, a 429 stops the run. The id decoding is also checked on the example post of TikTok's embed documentation (2019-07-27).

**Still to do for R2's acceptance on real data.** Fill a real sheet of 30 posts from 5 approved creators, import it once with `--record research/fixtures/oembed` (30 calls, about 30 seconds) and compare the stored post times with the dates the posts show on 10 of them. That needs a person watching real posts (question 3) and is the founder's call.
