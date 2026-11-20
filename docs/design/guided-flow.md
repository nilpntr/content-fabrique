# Guided flow: "Make a video"

One page. The screens, the words on them, and what each one calls. For a creator who has never edited a video and never wants to.

## Rules

- **Plain words.** Clips, song, video, changes. Never timeline, ops, asset, render, run, version numbers, credits maths or error codes.
- **One thing per screen.** One question, one main button. Tapping an answer moves on; "Back" is always there.
- **Mobile first.** One column, buttons at least 56 px tall and full width on a phone. Nothing depends on hover or drag.
- **Colour-blind safe.** A state is always an icon plus words ("Done", "Now", "Needs a look"). One accent colour; red and green are never used to tell two things apart.
- **Calm.** No fades, bounces or confetti. A spinner turns only while something is really happening, and not at all with reduced motion.
- **Every problem** says what happened in one sentence and offers one next step.
- **No new API.** Everything uses the calls the app already has (section "What it calls").

## Screens

Routes: `/make` (screens 1 to 3) and `/make/$projectId` (screens 4 and 5). Sidebar item: **Make a video**.

### 1. Clips (`/make`)

| | |
|---|---|
| Title | **Add your clips** |
| Line | Drop in everything you filmed. I'll pick the best parts. |
| Drop zone (the whole card is the button) | **Tap to choose clips** / or drag them here. While dragging: **Drop them here** |
| Song | **Add a song** (optional). After one is added: *Song: name* with **Remove** |
| Each clip | name, then one of: **Uploading 40%** · **Getting it ready** · **Ready** · **Needs a look** (with **Try again** or **Remove**) |
| Main button | **Next** (works as soon as one clip is added; uploads carry on in the background) |
| Other way in | Already uploaded your clips? **Pick from your library** (the existing new-video page) |

Empty: only the drop zone and its line. Problems: *"Photos can't be used yet. Add video clips."*, *"That file can't be used. Add a video (MP4 or MOV) or a song (MP3, M4A, WAV)."*, *"A video can use up to 49 clips. I've left the rest out."*, *"This one didn't upload. Try again."*, *"I couldn't read this one. Remove it and add another."*, *"This one is no longer uploading. Remove it and add it again."*

A video takes 49 clips and one song (the API takes 50 files a project). A new song replaces the old one. A clip this browser has already uploaded is used as it is, not uploaded twice.

### 2. Three questions (`/make`), one at a time

Progress line above each: **Question 1 of 3**.

1. **What kind of video is it?** Workout · Talking · Product · Vlog
2. **How long should it be?** Short (about 15 seconds) · Medium (about 30 seconds) · Long (about a minute) · You decide
3. **Which style?** My usual style (*Like the videos you showed me*, or *I'll pick what fits your clips* when no style is learnt yet) · Calm (*Slow and steady*) · Lively (*Moves with the music*) · Hype (*Fast and punchy*)

### 3. Ready (`/make`)

| | |
|---|---|
| Title | **Ready to make your video?** |
| Summary | 3 clips and a song · Workout · About 30 seconds · My usual style (each with **Change**) |
| Cost line | Uses about one video from your plan, with room for two rounds of changes. |
| Main button | **Make my video** |
| Still uploading | **Getting your clips in… 2 of 3 are ready.** Keep this page open. I'll start as soon as they're in. With **Not yet, let me change something** to take it back |
| Problems | The existing plain sentences for a refused start (no plan, no videos left, clip gone…), each with **Try again** or **Back to your clips** |

### 4. Making it (`/make/$projectId`)

| | |
|---|---|
| Title | **I'm making your video** |
| Steps (icon + word state) | Watching your clips · Finding your best moments · Cutting to the beat · Adding captions · Checking it over |
| Line under | This takes a few minutes. You can leave this page. Your video will be waiting under Projects. |
| Small button | **Stop** |
| A question from the editor | **I have a question for you**, the question, a text box, **Send answer** |
| Loading | Grey blocks in the shape of the screen |

Driven by the edit's own events (the same feed the project page reads): each step lights up when the editor starts that kind of work. A step the editor skips is simply ticked when a later one starts.

Problems: **I couldn't finish your video** + why in one sentence + *It didn't cost you anything.* + **Try again** (or **Start over** when trying again cannot help). Stopped by the creator: **You stopped this one** + **Make my video** / **Start over**.

### 5. Your video (`/make/$projectId`)

| | |
|---|---|
| Title | **Here's your video** |
| Video | Phone-shaped player. Until it can play: **Putting your video together… 40%** |
| Shot list (when known) | **What's in it**: 1. Opens on your back pose with the title BACK DAY 101 · 2. Your seated row, cut on the music · … |
| Action 1 | **Looks great** → **All done** screen: **Download video**, **Make another video**, **See all my videos** |
| Action 2 | **Change something** → chips **Shorter · Longer · More energy · Different music**, a box *"Or tell me in your own words"*, **Make the changes** (back to screen 4; the current video is kept) |
| Action 3 | **Start over** (*This video stays saved under Projects.*) → screen 1 |

"The video" is the newest one a finished edit made. What a failed or stopped edit left half-made is not shown here (it cannot be downloaded); a change that failed shows the video from before it, with *"I couldn't make that change. … your video is as it was."* above it.

Problems: *"We couldn't find this video"* → **Make a new video**; *"This video could not be loaded. Check your connection."* → **Try again**; the video cannot be shown → why + **Try again**. No video yet and nothing running: **No video yet** + **Make my video**.

## What it calls (all existing)

| Step | Call |
|---|---|
| Clips and song | the upload queue (`useUploadCommands`), `GET /v1/assets` to see when each is ready |
| Make my video | `POST /v1/projects` (9:16, the clips, then the song), `POST /v1/projects/{id}/edit` |
| Making it | `GET /v1/runs/{id}` (events), `POST /v1/runs/{id}/cancel`, `POST /v1/runs/{id}/reply` |
| Your video | `GET/POST /v1/projects/{id}/renders`, `GET /v1/renders/{id}`, the run's plan for the shot list |
| Change something | `POST /v1/projects/{id}/edit` with the words; "More energy" also asks for the hype vibe |

The kind of video, the length and the song are sent as sentences in the instructions, because the API has no fields for them yet.

## Not in this version (needs API or a decision)

- Fields for kind of video, target length and "this file is the song" (today: sentences in the instructions).
- More than one saved style to choose from (today: one style per workspace, plus calm / lively / hype).
- "Looks great" marking the video as approved on the server (today it only leads to the download).
- Remembering the answers across a page reload. (The picked style is kept in the browser per video, so "Try again" on a first video keeps it; the API stores the words on the project but not the style.)
- The upload queue telling which uploads it made for the files it was given (today a clip is matched to its upload by name and size, so removing a clip that another page is also uploading stops that upload).
- Making this the default: Home's "New video" button and the old "New video" sidebar item still open the detailed page.
