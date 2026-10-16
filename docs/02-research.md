# 02 — Research Report

> Status: **v0.1** · researched 2026-10-16 · Scope: Content Fabrique v1 (multi-tenant SaaS, fitness creators, TikTok only).

**How this was researched.** A deep-research workflow ran first. It split the question into five angles, found 30 sources and extracted falsifiable claims from 14 of them. **Its 3-vote adversarial verification step did not run** (see §6). Targeted follow-up searches and primary sources filled the gaps: official docs, GitHub repositories and licence files, and Cloudflare's docs repository. This environment's network policy blocked direct page fetching outside GitHub, so most web sources were read through search-engine extracts. **Prices and limits change often; re-check them on the vendor's page before committing money.**

**Confidence markers.**
- **✔** means the claim survived three-vote adversarial verification (none in this version; see §6).
- **◐** means it is backed by a primary source, or by two or more consistent sources, but was not adversarially verified.
- **△** means a single secondary source, or vendor marketing.
- **⚠** means sources conflict.

## Executive summary: what the research changes in the plan

1. **Publishing = inbox drafts, not auto-posting.**
   - TikTok's Content Posting API **cannot attach a TikTok sound**.
   - Direct Post stays private-only until TikTok audits the app, and agents must never post on their own.
   - The *upload-to-inbox* draft flow keeps the creator in the loop and lets them add the sound in TikTok. → §1.1
2. **Never reproduce trending songs.**
   - A platform that supplies music is directly liable.
   - Personal-account sounds are licensed for non-commercial personal use only, and business accounts are limited to TikTok's Commercial Music Library.
   - So we **cut to the beat, preview with a beat-guide click track, export with a sound cue**, and bake in only licensed or creator-owned music. → §1.3, [D-03](05-decisions.md#d-03-music-cue-only-by-default)
3. **Trend data has no official commercial API.**
   - The Research API is non-commercial only.
   - Creative Center has no API, and its song chart was unavailable in Sept–Oct 2026.
   - Use licensed music analytics (Chartmetric from ~$350/month), curated formats, web search and creator-supplied links. Unofficial scrapers only after a legal review. → §1.2, [D-11](05-decisions.md#d-11-trend-data-sources)
4. **Video models give "what", not "exactly when".**
   - Gemini samples 1 fps at ~100–300 tokens/s with **second-level timestamps**; Claude takes frames only.
   - So: deterministic tools for frame-accurate boundaries, a small model for tags (≈ $0.003 per footage-minute), and a video-native pass only if evals justify it. → §2, [D-10](05-decisions.md#d-10-video-understanding)
5. **Open-source licences matter for a SaaS.**
   - Avoid Ultralytics YOLO (AGPL), Essentia (AGPL) and madmom's models (non-commercial).
   - Use MediaPipe (Apache-2.0), Beat This! (MIT) and librosa (ISC). → §2.2, [D-16](05-decisions.md#d-16-licensing-policy-for-ml-and-media-components)
6. **The market converges on our design.**
   - Agent-native editors (video-use, HyperFrames, FrameJam) all use a *declarative edit spec + fixed renderer + visual review loop*.
   - None does beat-synced cutting, and none is creator-facing. That is our gap.
   - HyperFrames (Apache-2.0) is a candidate for motion graphics; Remotion needs a paid licence above 3 employees. → §3
7. **Agents and sandboxes: two workload classes.**
   - Agents run in a Go-native Claude loop (strict tools, tool runner), with Claude Managed Agents as an optional hosted loop (hard $ budgets, $0.08 per session-hour, a self-hosted Go worker possible).
   - Our media jobs run in hardened containers on our hosts.
   - Free-form agent code runs in a hosted sandbox. Cloudflare's Sandbox SDK mounts R2 without putting credentials in the sandbox and can disable internet.
   - Self-hosted microVMs pay off only at thousands of sandbox-hours a month. → §4, [D-07](05-decisions.md#d-07-agent-runtime-go-native-loop-on-the-claude-messages-api), [D-08](05-decisions.md#d-08-sandbox-pluggable-interface-default-provider)
8. **The platform fits, with three corrections.**
   - TanStack Start on Workers and R2 are a good fit as planned.
   - **Use Postgres from day one**: D1 over REST shares Cloudflare's 1,200-requests-per-5-minutes API budget, and PlanetScale Postgres starts at $5/month with pgvector.
   - **Don't use Cloudflare Stream**: it would bill for every minute twice next to R2 originals.
   - **Don't use Clerk Billing for an EU business yet**: no VAT and not a merchant of record. Keep Clerk for auth and bill through a merchant of record.
   - **K2 is not needed in v1.** → §5, [D-01](05-decisions.md#d-01-database-postgres-from-day-one-skip-d1-for-the-go-backend), [D-05](05-decisions.md#d-05-realtime-updates-sse-from-go-k2-deferred), [D-06](05-decisions.md#d-06-video-delivery-mp4-proxies-on-r2-no-cloudflare-stream-in-v1), [D-13](05-decisions.md#d-13-billing)

---

## 1. TikTok trend data and music licensing

### 1.1 What TikTok's official developer APIs allow

| API | What it does | Fit for Content Fabrique |
|---|---|---|
| **Content Posting API, Upload (draft)**, scope `video.upload` | Sends a video to the creator's TikTok **inbox**. The creator taps the notification, continues editing in TikTok and posts. Limits: 6 requests/min per user token, at most 5 pending shares per 24 h. ◐ [TikTok docs](https://developers.tiktok.com/docs/en/content-posting-api-get-started-upload-content) | ✅ **The v1 publishing path.** A human stays in the loop, and the creator can add a TikTok sound in-app. |
| **Content Posting API, Direct Post**, scope `video.publish` | Posts directly. **Until the app passes TikTok's audit:** posts are `SELF_ONLY`, accounts must be private, max 5 posting users per 24 h. ◐ [Content Sharing Guidelines](https://developers.tiktok.com/docs/en/content-sharing-guidelines) | Later, after the audit. |
| Direct Post UX obligations | A preview before posting; the **Music Usage Confirmation** consent text; **no promotional watermarks or logos** on creator content; read `creator_info` and respect `max_video_post_duration_sec`; a privacy picker with **no default**; a commercial-content disclosure toggle (Promotional content / Paid partnership). ◐ [Content Sharing Guidelines](https://developers.tiktok.com/docs/en/content-sharing-guidelines) | Shapes the export UI. Agents may never post on their own. |
| Attaching a TikTok **sound** via API | **Not possible.** The API uploads a file and a caption; it can't add sounds or use TikTok's editing features. △ [TokPortal](https://www.tokportal.com/learn/tiktok-sounds-api) (Sprinklr and Sprout Social help docs list the same limitation). The one vendor offering "programmatic sounds" does it with human operators on real phones, which we won't depend on. | Drives the cue-only music design. |
| **Research API** | Public-content data for **independent, non-commercial research only**. Creators, advertisers and commercial users are excluded, and the terms forbid commercial use. Quota ≈ 1,000 requests/day. ◐ [KeyAPI summary of TikTok's FAQ/terms](https://www.keyapi.ai/blog/tiktok-research-api-commercial-trend-scanning/), [TikTok Research API](https://developers.tiktok.com/products/research-api/) | ❌ Not usable. |
| **Embed / oEmbed** | `GET https://www.tiktok.com/oembed?url=…` returns the title, author and embed HTML for a public video or profile. ◐ [TikTok docs](https://developers.tiktok.com/docs/en/embed-videos) | ✅ The official way to show a trend example in our UI, and to take "this is the trend I want" links from creators. |
| Display API + Login Kit | The creator's own profile and videos, with consent | Later: learn from the creator's own performance. |

### 1.2 Trend data sources

| Source | What you get | Cost / access | Legal & operational risk | Verdict |
|---|---|---|---|---|
| **TikTok Creative Center** (web) | **Trending hashtags, hashtag analytics, trending videos, top ads.** ⚠ The **song (sound) and creator rankings were unavailable in Sept–Oct 2026**: an Apify scraper lists them as discontinued ([Apify](https://apify.com/doliz/tiktok-creative-center-scraper)), and Buffer says "no trending audio is available for now" while the lists are "undergoing some changes" ([Buffer](https://buffer.com/resources/trending-songs-tiktok/)). One guide still describes a working Songs tab, so this may be temporary. | Free web UI; **no official public API**. Scrapers call its internal endpoints, some with logged-in cookies (from ~$1.40 per 1k fetches on Apify) | Automated scraping is unofficial access (ToS and account risk); manual review is fine | Manual curation of formats and hashtags by our team. **Not a dependable source for trending sounds.** |
| **Licensed music analytics** (Chartmetric, Soundcharts) | Track-level metrics across platforms **including TikTok** | Chartmetric API **from ~$350/month** (1–10 req/s) ◐ [Chartmetric](https://chartmetric.com/plans/api); Soundcharts custom pricing △ | Licensed data, lowest risk; coverage of *TikTok-native* sounds (non-label audio) may be thin | ✅ Primary source for *trending songs* |
| **Unofficial TikTok data APIs** (EnsembleData, TikAPI, ScrapeCreators, Apify actors) | Hashtags, search, videos, music metadata | EnsembleData: free 50 units/day, paid plans from about $100/month ⚠ ([SocialCrawl](https://www.socialcrawl.dev/blog/best-tiktok-data-apis-2026), [Social Fetch](https://www.socialfetch.dev/blog/best-tiktok-data-apis-2026)); TikAPI about $29–189/month △ | Built on unofficial access: ToS and legal exposure, breakage, and GDPR questions about processing third-party creators' data | ⚠ Only after a legal review, behind the provider interface, never sole source |
| **Web search** (model provider's search tool, about $10 per 1k searches) | Trend reports, newsletters, monthly curated sound roundups (e.g. [Buffer](https://buffer.com/resources/trending-songs-tiktok/), [Dash Social](https://www.dashsocial.com/blog/tiktok-sounds)) and platform blog posts about what is working | Pay per use | Low (we use facts such as song names, not copy) | ✅ Evidence and context for the Research agent; a stop-gap for sounds |
| **Creator input** | "Make something like this" TikTok links via oEmbed, plus descriptions | Free | Low | ✅ Always available |
| Creator's own analytics (Display API, later) | What works for *this* creator | OAuth consent | Low | Later |

### 1.3 Music licensing

- **Two in-app libraries, by account type.** ◐
  - Personal accounts use the **General Sound Library**: the major-label and indie tracks that trending sounds come from, licensed for **non-commercial personal use only**.
  - Business accounts get only the **Commercial Music Library (CML)**: 1M+ pre-cleared tracks per Soundstripe ⚠ (TikTok's older ads blog said 600k+). It is cleared for organic brand posts and TikTok ads, **on TikTok only**.
  - Sources: [Soundstripe, 2026-08](https://www.soundstripe.com/blogs/tiktok-music-library-explained).
- **Business accounts may upload music they licensed themselves**, already mixed into the file, after TikTok's Music Usage Confirmation. TikTok's fingerprinting can still mute unlicensed audio. △ Same source.
- **A platform that supplies the music is directly liable.** If our software picks a copyrighted trending song and bakes it into an export, *we* copy and distribute it. The DMCA §512(c) safe harbour covers only material stored *at a user's direction*, and only under conditions: a registered designated agent, notice-and-takedown, and a repeat-infringer policy. △ [Soundstripe](https://www.soundstripe.com/blogs/whos-liable-for-music-on-your-platform)
- ***Cox v. Sony* (US Supreme Court, 25 March 2026)** narrowed contributory liability to services that induce infringement or are tailored to it. **Vicarious liability was not reviewed**, and marketing still matters: promoting "use popular songs for free" can read as inducement. △ Same source, with law-firm write-ups agreeing.
- The **Music Usage Confirmation** is a self-attestation by the posting user. The posting API grants no music licence. ◐ [Content Sharing Guidelines](https://developers.tiktok.com/docs/en/content-sharing-guidelines)

### 1.4 Implications for Content Fabrique

1. **Never reproduce trending copyrighted audio**, in exports *or* previews. Cut to the beat grid, preview with a beat-guide track, export with a sound cue, and let the creator attach the sound in TikTok via the inbox draft. → [D-03](05-decisions.md#d-03-music-cue-only-by-default)
2. **Publishing in v1 = draft upload** (`video.upload`). Direct Post comes after the audit. Exports carry **no watermark**. Exports respect the creator's maximum post duration.
3. **Business-account creators** need CML sounds (chosen in-app) or a licensed stock library we integrate. We ask the account type at onboarding.
4. **Marketing copy** never promises free use of popular songs.
5. **Trend data**: licensed analytics + manual Creative Center curation + web search + creator links. Unofficial scrapers only after legal sign-off. → [D-11](05-decisions.md#d-11-trend-data-sources)
6. **Beat grid without copying the song** is an open question. Options: tempo + first-beat offset from provider metadata; analysis of audio we may lawfully process (needs legal review, e.g. the EU text-and-data-mining exception); a tap-tempo fallback in the UI.
7. As a host of user uploads we need notice-and-takedown processes: a DMCA designated agent for the US, and EU Digital Services Act notice-and-action for hosting services (legal check).

---

## 2. Video understanding: clip selection, pose and beat sync

### 2.1 Native video models vs sampled frames

- **Gemini (static mode, the default)** samples **1 frame per second**, with a custom fps and clip start/end offsets available. Google warns it can miss fast motion. ◐ [Gemini video docs](https://ai.google.dev/gemini-api/docs/video-understanding)
- **Gemini token cost**: 66 tokens per frame at `low` media resolution or 258 otherwise, plus 32 tokens per second of audio. That is **≈ 100 tokens per second of video (low) or ≈ 300 (default)**, so roughly 6k–18k tokens per footage-minute. ⚠ Google's media-resolution page lists 70/280, so the exact figure depends on the model. ◐ Same source.
- **Gemini timestamps are whole seconds.** The File API stores video at 1 fps with a timestamp every second, and prompts reference `MM:SS`. ◐ Same source.
  - A developer building editing tools reported (mid-2025) that Gemini 2.5 Pro timestamps were reliable only to the second. Google's reply promised no fix. △ [Gemini forum](https://discuss.ai.google.dev/t/improve-timestamp-accuracy-on-video-understanding/95356)
  - **⇒ Video LLMs tell us *what* and *roughly where*. Frame-accurate cut points must come from deterministic tools.**
- **Gemini "agentic" processing** (opt-in; listed for Gemini 4 Argon, 3.8 Flash, 3.6 Flash and 3.5 Flash Lite) navigates the timeline and loads only the frames, transcript or audio it needs. Google claims up to 88 % fewer tokens on long-form content, but it can *increase* tokens and latency on clips under 5 minutes. Our clips are short, so static mode at low resolution is the better default. ◐ Same source.
- **Claude models take images, not video**, so we sample frames: keyframes per shot, or contact sheets. Prices per million tokens (Oct 2026):
  - Haiku 5.5: **$0.10 / $0.50** for prompts up to 100k tokens.
  - Sonnet 5.5: **$2 / $10**.
  - Opus 5.5: **$4 / $20**.
  - ◐ [Anthropic models](https://platform.claude.com/docs/en/about-claude/models/overview), [pricing](https://platform.claude.com/docs/en/about-claude/pricing)
- **Practitioner pattern: "The LLM never watches the video. It reads it."** browser-use's *video-use* packs a word-level transcript (ElevenLabs Scribe: timestamps, speakers, audio events) into a ~12 KB file. It looks at visuals only on demand, through filmstrip and waveform PNGs of a time range. The README contrasts this with a naive frame-by-frame approach costing ~45M tokens. ◐ [video-use README](https://github.com/browser-use/video-use)
- **Twelve Labs** (Pegasus 1.5 time-based metadata, Marengo embeddings) is a strong candidate for semantic search, **but it is not verified here**: the source fetch failed and the search budget ran out. Benchmark it in the P2 spike.

### 2.2 Deterministic signals and their licences

| Signal | Recommended | Licence | Notes |
|---|---|---|---|
| Shot boundaries | PySceneDetect or ffmpeg `scdet` | Permissive | Most phone clips are one continuous shot, so split further by motion and pose |
| Pose / "best pose" / reps | **MediaPipe Pose Landmarker** | Apache-2.0 source model ◐ ([Google](https://ai.google.dev/mediapipe/solutions/vision/pose_landmarker); check the bundle's model card) | Score poses (symmetry, joint angles, stability) and segment reps from keypoints |
| — avoid | Ultralytics YOLO pose | **AGPL-3.0** unless an Enterprise licence is bought ◐ ([Ultralytics](https://pypi.org/project/ultralytics/8.3.80/)) | Copyleft over the network; would force open-sourcing or a paid licence |
| Beat / downbeat / tempo | **Beat This!** (ISMIR 2024) + librosa | Code **and published weights MIT** ◐ ([CPJKU/beat_this](https://github.com/CPJKU/beat_this)); librosa ISC △ | The README notes some *training* audio is copyrighted; acceptable for us, but log it in the licence inventory |
| — avoid | madmom pretrained models; Essentia | madmom models **CC BY-NC-SA 4.0 (non-commercial)** ◐ ([paper](https://arxiv.org/pdf/1605.07008)); Essentia **AGPL-3.0** △ | |
| Transcription | ElevenLabs Scribe, Whisper family | Commercial API / MIT | Only for clips with speech |

### 2.3 Implications

1. **A cheap-first hybrid pipeline** ([architecture §5](03-architecture.md#5-media-pipeline)):
   - Deterministic signals → keyframes → small-model tagging (Claude Haiku 5.5) with a strict JSON schema and fitness taxonomy → transcript → `moments`.
   - **Estimate:** 30 keyframes per minute × ~700 tokens ≈ 21k tokens ≈ **$0.002–0.003 per footage-minute** on Haiku 5.5.
2. **Boundaries come from deterministic tools.** Model timestamps are hints only.
3. **An optional video-native pass** (Gemini Flash-class, low resolution, ~100 tokens/s) for whole-clip semantics such as exercise sequence and rep counting. Keep it only if eval set #1 shows it beats frames-only. → [D-10](05-decisions.md#d-10-video-understanding)
4. **A beat-synced cut is a differentiator**: the agent-native editors surveyed don't do it (§3). It needs a beat grid without copying the song (§1.4).

---

## 3. Editing & rendering stack and competitors

### 3.1 Agent-native video editing (Oct 2026)

| Project | How it works | Licence / status | Relevance |
|---|---|---|---|
| **[FrameJam](https://github.com/moritzkremb/framejam)** (linked in our README) | A review loop for coding agents. Pick one of 30 *styles* and 15 *playbooks*; the agent renders with HyperFrames, Remotion, Motion Canvas or ffmpeg. The user reviews the MP4 and comments by typing, **pinning a spot** or **dragging a range**. The agent receives JSON (`comments[]` with `at`, `time`, `endTime`, `position`) plus **JPEG frames with the pin drawn on them** via MCP tools (`open_review`, `wait_for_feedback`, `get_feedback`, `add_version`, `list_reviews`). | MIT; one local Node process (Hono + SSE + React); updated 2026-10-11 ◐ | The review UX we build into the product ([architecture §7.2](03-architecture.md#72-editor-agent)); its styles and playbooks ≈ our concepts and brand kit |
| **[browser-use/video-use](https://github.com/browser-use/video-use)** | A skill for any shell-capable coding agent. Transcript-first; the LLM writes **`edl.json`** and a fixed `render.py` executes it with ffmpeg (segments → concat → overlays → subtitles last). It self-evaluates every cut, with at most 3 fix/re-render passes. 30 ms audio fades at cuts, auto colour grade, 2-word uppercase subtitles by default. **No beat detection**; music only ducked under speech. | MIT; cloud version announced (waitlist) ◐ | Independent confirmation of our EDL + fixed renderer design ([D-02](05-decisions.md#d-02-edit-representation-declarative-timeline-json--deterministic-renderer)) |
| **[HeyGen HyperFrames](https://github.com/heygen-com/hyperframes)** | Video as **HTML/CSS** with timing data attributes (`data-start`, `data-duration`, `data-track-index`). A deterministic render (headless Chrome seeks each frame, FFmpeg encodes) with GSAP, Lottie, Three.js and other adapters. Renders locally, in Docker, on HeyGen cloud or self-deployed AWS Lambda. Ships skills for Claude Code and Codex. | **Apache-2.0, no per-render fees**; pre-1.0 (v0.8.163 on 2026-10-16, about ten releases in 4 days → fast churn) ◐ | Candidate for *motion-graphics* overlays (titles, kinetic captions) and in-browser live preview later |
| **[Remotion](https://github.com/remotion-dev/remotion/blob/main/LICENSE.md)** | React components rendered via headless Chrome + FFmpeg | **Source-available.** Free for individuals, for-profits with **≤ 3 employees**, non-profits and evaluation; otherwise a **Company License** (pricing at remotion.pro). Licence changes "slightly" in v5. ◐ | Usable early, but a cost and lock-in once we grow |
| Editframe, cutagent, HyperFrames Studio | HTML/CSS → MP4 for coding agents; agent-first JSON edits over FFmpeg; a desktop agent editor (launched Oct 2026) | △ (launch pages, PyPI) | Same direction: agents edit structured specs |

### 3.2 What this means for the edit representation

Two independent agent-native projects converge on the same pattern: **the LLM writes a declarative edit description, and a fixed, deterministic renderer executes it**. video-use does it with `edl.json` + `render.py`, HyperFrames with HTML + data attributes. Neither has the LLM free-write ffmpeg. Our timeline JSON + Go compiler follows the same pattern and adds validation, versioning and an exact comment-to-clip mapping.

### 3.3 Rendering

- **The core path is an ffmpeg filter graph** (cuts, reframe, speed, looks, transitions, ASS/libass captions): the fastest and cheapest option, and fully under our control.
- **Motion graphics:** a later layer using HyperFrames (Apache-2.0) for richer titles and kinetic captions, composited onto the ffmpeg output. It is slower (headless Chrome), so use it only for overlay segments. Remotion is possible while we are ≤ 3 people, but needs a company licence after that.
- **Compute reference:** Cloudflare Containers bills $0.000020 per vCPU-second (≈ $0.072 per vCPU-hour) ◐ ([Cloudflare](https://developers.cloudflare.com/containers/platform/pricing/)). A short that takes 4 vCPUs for 60 s costs ≈ $0.005 in CPU. **Rendering is not the cost driver; model tokens are.**

### 3.4 Captions, safe zones, beat-snapping

- **TikTok UI-safe zones.** The best-sourced numbers come from TikTok's In-Feed Ads template scaled to 1080×1920: keep text, faces and logos out of the **top 240 px, bottom 660 px and 120 px on each side**. The right-hand action column (≈ 180 px wide, from y ≈ 840) is measured, not official. Blogs disagree, so the renderer uses configurable, conservative defaults. △ [Socialync](https://www.socialync.io/tools/safe-zones/tiktok)
- **Caption style:** short uppercase word chunks are the norm (video-use defaults to 2-word uppercase chunks) ◐.
- **Beat-snapping** is missing from the agent-native tools surveyed. It is our differentiator for music-driven fitness edits.

### 3.5 Competitor landscape

| Segment | Examples | What they do | Gap for us |
|---|---|---|---|
| Long → short clipping | OpusClip, Vizard, Klap, Quso | Transcribe long recordings, find highlights, reframe, caption △ ([roundup](https://motionbox.io/blog/agent-that-edits-video-2026)) | They don't assemble *several raw clips* into a *concept* |
| Captions / talking-head polish | Submagic, Captions, CapCut auto-captions | Captions, zooms, b-roll, effects | No trend research, no multi-clip assembly from a brief |
| Chat-driven editors | Descript, VEED, CapCut | Edit by instruction or transcript | General-purpose; no niche or trend awareness |
| Agent-native, developer-facing | FrameJam, video-use, HyperFrames Studio, Editframe | Coding agents make videos | Need a coding agent and terminal; not creator-facing |
| Generative editing models | e.g. "Gemini Omni Flash" (GA reported late Aug 2026) | Re-render a clip from an instruction △ ([Fora Soft](https://www.forasoft.com/blog/article/ai-powered-video-editing-solutions)) | Generative, not footage-faithful assembly |

**Positioning:** no product surveyed packages **niche-aware (fitness), trend-informed, beat-synced assembly of a creator's own raw clips, with a point-at-the-frame review loop**, for creators rather than developers. **Competitor prices were not verified in this pass** because the search budget ran out (see §6). Benchmark them before setting final prices.

---

## 4. Agent runtime and sandboxes

### 4.1 Claude from a Go backend (Anthropic docs, Oct 2026) ◐

| Option | What it gives | Fit |
|---|---|---|
| **Messages API + tool use** (`anthropic-sdk-go`) | Typed client tools with `strict` JSON schemas, structured outputs (`output_config.format`), streaming, prompt caching, and a beta **tool runner** in Go (`toolrunner` package) ([docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-runner)) | ✅ The core editor and research loops in our Go worker |
| Claude Agent SDK | The Claude Code harness (bash, file tools, subagents) | TypeScript/Python only: a sidecar if ever needed |
| **[Claude Managed Agents](https://platform.claude.com/docs/en/managed-agents/overview)** (beta) | Anthropic runs the loop **and** a per-session container (`cloud`), or tools run on **our** hosts through a Go `EnvironmentWorker` that polls outbound-only (`self_hosted`). Hard **$ session budgets**; list cost covers tokens, web search at $10/1k and **session runtime at $0.08/hour**. Also outcomes (rubric-graded iteration), multi-agent, memory stores, webhooks, scheduled runs. Self-hosted environments accept no file resources (pass pointers) and no vault env credentials (keep secrets host-side). | ✅ Optional runtime for free-form sandbox sessions; scheduled research runs |

Model notes for the agent loop:
- Claude Opus 5.5 and Sonnet 5.5 **reject forced `tool_choice`** (`any`/`tool`). Use `auto` + `strict` tools + prompt instructions, or structured outputs.
- Thinking is adaptive, with an `effort` control. Opus 5.5 defaults to `medium`, so set it explicitly.
- Prompt-cache reads are $0.20 per MTok on Opus 5.5 and Sonnet 5.5.

### 4.2 Sandbox options

| Option | Isolation | Price signal | Notes |
|---|---|---|---|
| **Cloudflare Sandbox SDK** (on Containers) | Container per sandbox; **internet can be disabled** | Containers: $0.000020 per vCPU-s, $0.0000025 per GiB-s of memory, $0.00000007 per GB-s of disk. Workers Paid includes 375 vCPU-min, 25 GiB-h and 200 GB-h per month. ◐ ([pricing](https://developers.cloudflare.com/containers/platform/pricing/)) | **Mounts an R2 prefix via s3fs while the Worker keeps the credentials and signs each request** (`S3Mount`/`S3Gateway`); any process can touch everything under the mounted prefix, so mount narrowly ◐ ([docs](https://developers.cloudflare.com/sandbox/)). Max 4 vCPU / 12 GiB / 20 GB disk per instance; 1–3 s cold starts; no built-in autoscaling yet; SIGTERM + 15 min grace on host events ◐ ([limits](https://developers.cloudflare.com/containers/platform/limits/), [FAQ](https://developers.cloudflare.com/containers/faq/)) |
| E2B | Firecracker microVMs (nested inside GCP VMs) | ≈ $0.166/h for 2 vCPU / 4 GiB, metered △ ([bex.co](https://bex.co/blog/2026/09/24/e2b-l2-guests-firecracker-nested-virtualization-vs-hetzner-microvms)) | Mature agent sandbox; Python/JS SDKs + REST API |
| Daytona | — | — | ⚠ Its open-source repo is reported unmaintained or archived since June 2026 △ ([Spheron](https://www.spheron.network/blog/ai-agent-code-execution-sandbox-e2b-daytona-firecracker/)) |
| Claude Managed Agents | Anthropic cloud container, or our host via a self-hosted worker | $0.08 per session-hour + tokens | The loop comes included |
| Self-hosted Firecracker (E2B OSS, AgentENV) | microVM; **needs KVM, so bare metal** | Hetzner AX102 ≈ $115/month. Crossover vs managed somewhere between **~2,100 and ~11,000 sandbox-hours/month** depending on how ops time is valued △ ([bex.co](https://bex.co/blog/2026/08/07/self-hosting-firecracker-sandboxes-ai-agents-cost-crossover)) | Production means a 2–4 host cluster (Nomad) plus snapshots and garbage collection △ |
| Self-hosted hardened containers (gVisor `runsc`) | User-space kernel; no KVM needed | Host cost only | A fit for **our own deterministic media jobs** on untrusted files |

### 4.3 Implications

- **There are two workload classes:**
  1. *Our* deterministic media jobs (ingest, analysis, render) on untrusted files run in hardened containers on our worker hosts.
  2. *Free-form agent code* (the escape hatch) runs in a per-job hosted sandbox.
- At MVP volume (well under 1,000 sandbox-hours a month), **managed sandboxes beat self-hosted microVMs on total cost**. Revisit past the crossover. → [D-08](05-decisions.md#d-08-sandbox-pluggable-interface-default-provider)

---

## 5. Platform: Cloudflare, Clerk, Go, costs

### 5.1 Web app on Workers

TanStack Start deploys to Workers with `@cloudflare/vite-plugin` ◐ ([Cloudflare guide](https://developers.cloudflare.com/workers/framework-guides/web-apps/tanstack-start/)):
- In the Vite config: `cloudflare({ viteEnvironment: { name: "ssr" } })` next to `tanstackStart()`.
- In `wrangler.jsonc`: `main: "@tanstack/react-start/server-entry"` with `nodejs_compat`.
- Server functions read bindings via `import { env } from "cloudflare:workers"`. A custom server entry adds Queues, Cron, Durable Objects or Workflows.
- Rsbuild projects are not supported.

### 5.2 Backend hosting: Cloudflare Containers vs a VM

| | Cloudflare Containers | EU VM / dedicated server (e.g. Hetzner) |
|---|---|---|
| Cost | ≈ $0.072 per vCPU-hour + memory (provisioned) ◐ | ≈ $115/month for a 16-core / 32-thread AX102 △: about 7× cheaper per core-hour, or 15× per thread-hour, at full use |
| Limits | ≤ 4 vCPU, 12 GiB, **20 GB ephemeral disk** per instance; no autoscaling yet; addressed through Workers/Durable Objects ◐ | Whatever the box has; KVM available on dedicated servers |
| Ops | Near-zero; deploy with `wrangler` | Patching, monitoring, scaling by hand |
| Fit | Burst render/analysis workers; stateless API later | MVP API + workers + hardened job containers in one place, EU-located, cheapest |

### 5.3 R2 storage

Sources ◐: [limits](https://developers.cloudflare.com/r2/platform/limits/), [uploads](https://developers.cloudflare.com/r2/objects/upload-objects/), [pricing](https://developers.cloudflare.com/r2/pricing/), [data location](https://developers.cloudflare.com/r2/reference/data-location/).

- **Uploads:** objects up to 5 TiB; single PUT up to 5 GiB; multipart up to 10,000 parts of 5 MiB–5 GiB (all but the last part ≥ 5 MiB). Incomplete multipart uploads **auto-abort after 7 days** by default.
- **Writes:** at most 1 concurrent write per second to the same key.
- **Pricing:**
  - Standard **$0.015/GB-month**; Class A $4.50 per million; Class B $0.36 per million.
  - Infrequent Access $0.01/GB-month, with $0.01/GB retrieval and a 30-day minimum.
  - **Egress is free.** The free tier is 10 GB-month, 1M Class A and 10M Class B per month.
- **EU jurisdiction buckets** must be used through `https://<ACCOUNT_ID>.eu.r2.cloudflarestorage.com`, and the jurisdiction can't be changed after creation.

### 5.4 Database: D1 vs PlanetScale
- **The Cloudflare API rate limit is 1,200 requests per 5 minutes per user**, cumulative across dashboard, keys and tokens. Exceeding it blocks *all* API calls for 5 minutes. ◐ [Cloudflare](https://developers.cloudflare.com/fundamentals/api/reference/limits/) A Go service reaching D1 over the REST API shares that budget. (A third-party page claims D1 has its own quota; unconfirmed.)
- **D1 limits:** 10 GB per database (paid), 100 bound parameters per query, 30 s for API requests ◐ [D1 limits](https://developers.cloudflare.com/d1/platform/limits/). It is built for Worker bindings.
- **PlanetScale Postgres:**
  - From **$5/month** single-node (PS-5, 512 MiB) and **$15/month** HA (3 nodes) ◐ [plans](https://planetscale.com/docs/planetscale-plans).
  - PostgreSQL 17/18, with **pgvector + pgvectorscale** ◐ [docs](https://planetscale.com/docs/postgres/extensions/pgvector).
  - Since June 2026 it can be created and billed through the Cloudflare dashboard ◐ [Cloudflare changelog](https://developers.cloudflare.com/changelog/post/2026-06-18-planetscale-databases-cloudflare-billing/).
  - Check that an EU region is available for the chosen plan.

### 5.5 Auth and billing
- **Clerk Go SDK v2** (`github.com/clerk/clerk-sdk-go/v2`, Go ≥ 1.24) provides `clerkhttp.WithHeaderAuthorization` / `RequireHeaderAuthorization` middleware, and `clerk.SessionClaimsFromContext` downstream. ◐ [README](https://github.com/clerk/clerk-sdk-go)
- **Clerk Billing:**
  - It costs 0.7 % per transaction on top of Stripe fees and supports B2B per-seat plans. It has **no usage metering**, **no tax or VAT support yet**, and is **not a merchant of record**. ◐ [Clerk pricing](https://clerk.com/pricing), [Clerk billing docs](https://clerk.com/docs/billing)
  - A third-party guide adds "USD-only, no 3-D Secure" △ [Dodo Payments](https://dodopayments.com/blogs/clerk-billing). If true, that is a blocker for EU cards that need strong customer authentication.
- **Merchant-of-record alternatives** handle EU VAT for us: Paddle at **5 % + $0.50** △, and Stripe's own MoR product (Managed Payments; Lemon Squeezy users are being migrated to it) △. Stripe Tax is 0.5 % per transaction, but we would stay the seller of record. [Overview](https://alexcloudstar.com/blog/merchant-of-record-indie-saas-2026/)

### 5.6 Jobs and realtime
- **River** is a transactional job queue backed by Postgres (or SQLite):
  - Jobs enqueued in a transaction become visible only on commit.
  - It has periodic/cron jobs, unique jobs (by args, period, queue, state) and automatic retries, plus River UI.
  - The Go driver is pgx v5. ◐ [README](https://github.com/riverqueue/river)
- **Cloudflare K2** (announced 2026-10-01) is a serverless, Kafka-like event-stream service on R2, in public beta on Workers Paid. Cloudflare recommends Pipelines when data lands in R2/Iceberg, and K2 for custom processing. Its roadmap includes keys, push-based Worker consumers and Kafka client compatibility. ◐ [Cloudflare blog](https://blog.cloudflare.com/cloudflare-k2-streams/) **We don't need it for v1** ([D-05](05-decisions.md#d-05-realtime-updates-sse-from-go-k2-deferred)).

### 5.7 Video playback
- **Cloudflare Stream** costs **$5/month per 1,000 minutes stored** (prepaid) plus **$1 per 1,000 minutes delivered**; ingest and encoding are free, and preloading counts as delivery. ◐ [Stream pricing](https://developers.cloudflare.com/stream/pricing/)
- **We must keep originals in R2 for editing anyway**, so MP4 proxies on R2 (free egress) avoid paying twice. → [D-06](05-decisions.md#d-06-video-delivery-mp4-proxies-on-r2-no-cloudflare-stream-in-v1)

### 5.8 Cost-model inputs (Oct 2026 list prices)

| Item | Unit price |
|---|---|
| Claude Opus 5.5 / Sonnet 5.5 / Haiku 5.5 | $4 / $20 · $2 / $10 · $0.10 / $0.50 per MTok (in / out) |
| Web search (model tool) | $10 per 1,000 searches |
| Managed Agents session runtime | $0.08 per hour |
| R2 Standard storage | $0.015 per GB-month, egress free |
| Cloudflare Containers CPU | $0.000020 per vCPU-s (≈ $0.072 per vCPU-hour) |
| PlanetScale Postgres | $5 (single node) / $15 (HA) per month entry |
| Clerk Billing | 0.7 % + Stripe fees; MoR alternatives ≈ 5 % + $0.50 |
| Chartmetric API | from ~$350 per month |

---

## 6. Limits of this research and open questions

**How the research ran.**
- The deep-research workflow completed scoping (5 angles), search (30 sources) and claim extraction for 14 sources. 10 yielded claims; the rest failed or were skipped.
- It was **stopped before its 3-vote adversarial verification**: this session's web-search budget (200 searches per turn, shared by all agents) ran out, and verifiers without search default to "refuted".
- Targeted follow-up searches and primary sources on GitHub (licences, READMEs, Cloudflare's docs repository) filled most gaps.
- As a result, **no claim in this report carries ✔**. The workflow can be resumed from its cached results to run verification.

**Not covered or unverified:**
- Competitor pricing (OpusClip, Submagic, Captions, CapCut).
- Twelve Labs pricing and quality.
- Current Gemini prices.
- PlanetScale EU regions.
- Whether Creative Center's song chart returns.
- Legal questions:
  - deriving beat grids from copyrighted audio (EU TDM exception?);
  - GDPR for third-party creator data from unofficial APIs;
  - DSA hosting obligations;
  - TikTok developer terms for draft uploads by a third-party SaaS.

**Open product questions** are listed in the [implementation plan](04-implementation-plan.md#open-questions-for-the-founder).
