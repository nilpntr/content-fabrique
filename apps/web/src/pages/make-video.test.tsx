import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { UploadItem, UploadSnapshot } from "@/lib/upload/manager";
import { type UploadCommands, UploadCommandsContext, UploadStateContext } from "@/lib/upload/provider";
import { apiError, type FakeApi, installApi, json, renderApp } from "@/test-api";
import { apiAsset, assetId, learntStyle, project, projectId, run, runId } from "@/test-fixtures";
import { MakeVideoPage } from "./make-video";

vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Link: ({ to, children, className }: { to: string; children: ReactNode; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));

afterEach(cleanup);

const video = (name: string, bytes = 4) => new File(["x".repeat(bytes)], name, { type: "video/mp4" });
const song = (name: string) => new File(["xx"], name, { type: "audio/mpeg" });

function upload(name: string, size: number, over: Partial<UploadItem> = {}): UploadItem {
  return { id: `up-${name}`, assetId: null, name, size, status: "uploading", progress: 0.4, bytesDone: 0, speed: 0, etaSeconds: null, partsDone: 0, partCount: 1, error: null, note: null, ...over };
}

/** The app's upload queue, faked: the test says what is in it, and the page hears about every change. */
function fakeUploads(initial: UploadItem[] = [], over: Partial<UploadCommands> = {}) {
  const listeners = new Set<() => void>();
  let snapshot = { items: initial, ready: true } as unknown as UploadSnapshot;
  const commands: UploadCommands = {
    available: true,
    addFiles: vi.fn(async () => ({ added: 1, resumed: 0, duplicates: 0, rejected: [] })),
    cancel: vi.fn(),
    retry: vi.fn(),
    retryAllFailed: vi.fn(),
    clearDone: vi.fn(),
    ...over,
  };
  return {
    commands,
    store: {
      subscribe: (listener: () => void) => {
        listeners.add(listener);
        return () => listeners.delete(listener);
      },
      getSnapshot: () => snapshot,
    },
    set(items: UploadItem[]) {
      snapshot = { items, ready: true } as unknown as UploadSnapshot;
      act(() => {
        for (const listener of listeners) listener();
      });
    },
  };
}

function page(uploads: ReturnType<typeof fakeUploads>, onOpenVideo = vi.fn()) {
  renderApp(
    <UploadCommandsContext.Provider value={uploads.commands}>
      <UploadStateContext.Provider value={uploads.store}>
        <MakeVideoPage onOpenVideo={onOpenVideo} />
      </UploadStateContext.Provider>
    </UploadCommandsContext.Provider>,
  );
  return onOpenVideo;
}

const choose = (files: File[], input = "clips-input") => fireEvent.change(screen.getByTestId(input), { target: { files } });
const row = (name: string) => screen.getByText(name).closest("li") as HTMLElement;
const assets = (...items: object[]) => ({ items, next_cursor: null });

/** Two clips that are uploaded and prepared, and the API of a start that goes well. */
function readyApi(extra: Record<string, object> = {}): FakeApi {
  return installApi({
    "GET /v1/assets": assets(apiAsset(1), apiAsset(2)),
    "POST /v1/projects": json(201, project(1)),
    "POST /v1/projects/{id}/edit": json(202, run({ id: runId(7) })),
    ...extra,
  });
}
const twoReady = () => [upload("squat.mp4", 4, { status: "done", progress: 1, assetId: assetId(1) }), upload("row.mp4", 5, { status: "done", progress: 1, assetId: assetId(2) })];

/** Adds the two clips and answers the three questions with a tap each. */
async function answerAll({ kind = "Workout", length = "Medium", style = "My usual style" } = {}) {
  choose([video("squat.mp4", 4), video("row.mp4", 5)]);
  fireEvent.click(screen.getByRole("button", { name: /^Next/ }));
  fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${kind}`) }));
  fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${length}`) }));
  fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${style}`) }));
  await screen.findByRole("heading", { name: "Ready to make your video?" });
}

test("it opens on one big place to add clips, and Next waits for the first clip", () => {
  installApi({ "GET /v1/assets": assets() });
  page(fakeUploads());
  expect(screen.getByRole("heading", { name: "Add your clips" })).toBeTruthy();
  expect(screen.getByRole("button", { name: /Tap to choose clips/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /Add a song/ })).toBeTruthy();
  expect(screen.queryByRole("list", { name: "Your clips" })).toBeNull();
  expect((screen.getByRole("button", { name: /^Next/ }) as HTMLButtonElement).disabled).toBe(true);
});

test("chosen clips start uploading at once and say where they are in words", async () => {
  installApi({ "GET /v1/assets": assets(apiAsset(2, { status: "processing" }), apiAsset(3)) });
  const uploads = fakeUploads();
  page(uploads);
  const files = [video("squat.mp4", 4), video("row.mp4", 5), video("pose.mp4", 6)];
  choose(files);
  expect(uploads.commands.addFiles).toHaveBeenCalledWith(files);
  uploads.set([upload("squat.mp4", 4, { progress: 0.4 }), upload("row.mp4", 5, { status: "done", assetId: assetId(2) }), upload("pose.mp4", 6, { status: "done", assetId: assetId(3) })]);
  expect(within(row("squat.mp4")).getByText(/Uploading/).textContent).toBe("Uploading 40%");
  expect(within(row("row.mp4")).getByText("Getting it ready")).toBeTruthy();
  await waitFor(() => expect(within(row("pose.mp4")).getByText("Ready")).toBeTruthy());
  // Uploading goes on behind the questions: Next does not wait for it.
  expect((screen.getByRole("button", { name: /^Next/ }) as HTMLButtonElement).disabled).toBe(false);
});

test("a photo is left out with a sentence, and a song is listed as the song", () => {
  installApi({ "GET /v1/assets": assets() });
  const uploads = fakeUploads();
  page(uploads);
  choose([new File(["x"], "me.png", { type: "image/png" })]);
  expect(screen.getByRole("alert").textContent).toBe("Photos can't be used yet. Add video clips.");
  expect(uploads.commands.addFiles).not.toHaveBeenCalled();

  choose([song("beat.mp3")], "song-input");
  expect(row("beat.mp3").textContent).toContain("Song: beat.mp3");
  // One song a video: the button to add one is gone, and a song alone is not enough to go on.
  expect(screen.queryByRole("button", { name: /Add a song/ })).toBeNull();
  expect((screen.getByRole("button", { name: /^Next/ }) as HTMLButtonElement).disabled).toBe(true);
});

test("a clip that did not upload says so, and can be tried again or removed", () => {
  installApi({ "GET /v1/assets": assets() });
  const uploads = fakeUploads();
  page(uploads);
  choose([video("squat.mp4", 4)]);
  uploads.set([upload("squat.mp4", 4, { status: "failed" })]);
  expect(within(row("squat.mp4")).getByText("This one didn't upload. Try again.")).toBeTruthy();
  fireEvent.click(within(row("squat.mp4")).getByRole("button", { name: "Try again" }));
  expect(uploads.commands.retry).toHaveBeenCalledWith("up-squat.mp4");
  fireEvent.click(screen.getByRole("button", { name: "Remove squat.mp4" }));
  expect(uploads.commands.cancel).toHaveBeenCalledWith("up-squat.mp4");
  expect(screen.queryByText("squat.mp4")).toBeNull();
});

test("the questions come one at a time, and a tap answers and moves on", async () => {
  readyApi();
  page(fakeUploads(twoReady()));
  choose([video("squat.mp4", 4), video("row.mp4", 5)]);
  fireEvent.click(screen.getByRole("button", { name: /^Next/ }));

  expect(screen.getByText("Question 1 of 3")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "What kind of video is it?" })).toBeTruthy();
  expect(within(screen.getByRole("group", { name: "What kind of video is it?" })).getAllByRole("button")).toHaveLength(4);
  fireEvent.click(screen.getByRole("button", { name: /^Talking/ }));

  expect(screen.getByText("Question 2 of 3")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "How long should it be?" })).toBeTruthy();
  // Back keeps the answer, shown as picked in words.
  fireEvent.click(screen.getByRole("button", { name: "Back" }));
  expect(screen.getByRole("button", { name: /^Talking/ }).getAttribute("aria-pressed")).toBe("true");
  expect(screen.getByRole("button", { name: /^Talking/ }).textContent).toContain("Picked");
  fireEvent.click(screen.getByRole("button", { name: /^Talking/ }));
  fireEvent.click(screen.getByRole("button", { name: /^Short/ }));

  expect(screen.getByText("Question 3 of 3")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Which style?" })).toBeTruthy();
  expect(screen.getByRole("button", { name: /^My usual style/ }).textContent).toContain("I'll pick what fits your clips");
});

test("with a learnt style, 'My usual style' says it follows the creator's own videos", async () => {
  readyApi({ "GET /v1/style": learntStyle() });
  page(fakeUploads(twoReady()));
  choose([video("squat.mp4", 4)]);
  fireEvent.click(screen.getByRole("button", { name: /^Next/ }));
  fireEvent.click(screen.getByRole("button", { name: /^Vlog/ }));
  fireEvent.click(screen.getByRole("button", { name: /^You decide/ }));
  await waitFor(() => expect(screen.getByRole("button", { name: /^My usual style/ }).textContent).toContain("Like the videos you showed me"));
});

test("'Make my video' makes the project from the clips and the answers, starts the editor and opens the video", async () => {
  const api = readyApi();
  const onOpen = page(fakeUploads(twoReady()));
  await answerAll({ kind: "Workout", length: "Medium", style: "Hype" });

  const choices = within(screen.getByRole("list", { name: "Your choices" }));
  expect(choices.getByText("2 clips")).toBeTruthy();
  expect(choices.getByText("Workout")).toBeTruthy();
  expect(choices.getByText("Medium · About 30 seconds")).toBeTruthy();
  expect(choices.getByText("Hype")).toBeTruthy();
  // What it uses is said in videos, never in credits or money.
  expect(screen.getByText(/about one video from your plan/)).toBeTruthy();
  expect(screen.queryByText(/\$\d|credit/i)).toBeNull();

  await waitFor(() => expect(within(screen.getByRole("list", { name: "Your choices" })).getByText("2 clips")).toBeTruthy());
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(onOpen).toHaveBeenCalledWith(projectId(1), runId(7)));

  const brief = "Make a workout video. Keep it about 30 seconds long.";
  const created = api.callsTo("POST /v1/projects")[0]?.body as { title: string };
  expect(created).toEqual({ format: "9:16", asset_ids: [assetId(1), assetId(2)], title: created.title, brief });
  expect(created.title).toMatch(/^Workout video · /);
  expect(api.callsTo("POST /v1/projects/{id}/edit").map((call) => call.body)).toEqual([{ instructions: brief, vibe: "hype" }]);
});

test("a song goes last in the project and is named to the editor; the usual style sends no vibe", async () => {
  const api = readyApi({ "GET /v1/assets": assets(apiAsset(1), apiAsset(2), apiAsset(3, { kind: "audio" })) });
  const onOpen = page(fakeUploads([...twoReady(), upload("beat.mp3", 2, { status: "done", assetId: assetId(3) })]));
  choose([song("beat.mp3")], "song-input");
  await answerAll({ length: "You decide" });
  expect(screen.getByText("2 clips and a song")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(onOpen).toHaveBeenCalled());
  const brief = "Make a workout video. Use the song I added (beat.mp3) as the music.";
  expect(api.callsTo("POST /v1/projects")[0]?.body).toMatchObject({ asset_ids: [assetId(1), assetId(2), assetId(3)], brief });
  expect(api.callsTo("POST /v1/projects/{id}/edit")[0]?.body).toEqual({ instructions: brief });
});

test("with clips still on their way it waits in words, then starts by itself when they are in", async () => {
  const api = readyApi();
  const uploads = fakeUploads([upload("squat.mp4", 4, { status: "done", assetId: assetId(1) }), upload("row.mp4", 5, { progress: 0.5 })]);
  const onOpen = page(uploads);
  await answerAll();
  await waitFor(() => expect(api.callsTo("GET /v1/assets").length).toBeGreaterThan(0));
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));

  expect((await screen.findByRole("status")).textContent).toContain("Getting your clips in… 1 of 2 is ready.");
  expect(screen.getByRole("status").textContent).toContain("Keep this page open");
  expect((screen.getByRole("button", { name: "Waiting for your clips…" }) as HTMLButtonElement).disabled).toBe(true);
  expect(api.callsTo("POST /v1/projects")).toHaveLength(0);

  uploads.set(twoReady());
  await waitFor(() => expect(onOpen).toHaveBeenCalledWith(projectId(1), runId(7)));
  expect(api.callsTo("POST /v1/projects")).toHaveLength(1);
});

test("while it waits for the clips, the creator can still take the start back", async () => {
  const api = readyApi();
  const uploads = fakeUploads([upload("squat.mp4", 4, { status: "done", assetId: assetId(1) }), upload("row.mp4", 5, { progress: 0.1 })]);
  const onOpen = page(uploads);
  await answerAll();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  fireEvent.click(await screen.findByRole("button", { name: "Not yet, let me change something" }));
  expect(screen.getByRole("button", { name: "Make my video" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Change clips" })).toBeTruthy();

  // The clips coming in later start nothing: the button was taken back.
  uploads.set(twoReady());
  await waitFor(() => expect(within(screen.getByRole("list", { name: "Your choices" })).getByText("2 clips")).toBeTruthy());
  await new Promise((resolve) => setTimeout(resolve, 50));
  expect(api.callsTo("POST /v1/projects")).toHaveLength(0);
  expect(onOpen).not.toHaveBeenCalled();
});

test("the style that was picked is remembered for the video, in case its edit has to be started again", async () => {
  readyApi({ "POST /v1/projects/{id}/edit": apiError(429, "rate_limited") });
  const onOpen = page(fakeUploads(twoReady()));
  await answerAll({ style: "Calm" });
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(onOpen).toHaveBeenCalledWith(projectId(1)));
  expect(window.localStorage.getItem(`cf:guided-vibe:${projectId(1)}`)).toBe("calm");
});

test("the same file handed over twice is listed once", () => {
  installApi({ "GET /v1/assets": assets() });
  const uploads = fakeUploads();
  page(uploads);
  choose([video("squat.mp4", 4), video("squat.mp4", 4)]);
  choose([video("squat.mp4", 4)]);
  expect(screen.getAllByText("squat.mp4")).toHaveLength(1);
  expect(uploads.commands.addFiles).toHaveBeenCalledTimes(1);
  expect(vi.mocked(uploads.commands.addFiles).mock.calls[0]?.[0]).toHaveLength(1);
});

test("more clips than a video can use are left out, in a sentence", () => {
  installApi({ "GET /v1/assets": assets() });
  const uploads = fakeUploads();
  page(uploads);
  choose(Array.from({ length: 52 }, (_, n) => video(`clip-${n}.mp4`, n + 1)));
  expect(screen.getByRole("alert").textContent).toBe("A video can use up to 49 clips. I've left the rest out.");
  expect(within(screen.getByRole("list", { name: "Your clips" })).getAllByRole("listitem")).toHaveLength(49);
  expect(vi.mocked(uploads.commands.addFiles).mock.calls[0]?.[0]).toHaveLength(49);
});

test("a new song takes the place of the old one, whose upload is stopped", () => {
  installApi({ "GET /v1/assets": assets() });
  const uploads = fakeUploads();
  page(uploads);
  choose([song("beat.mp3")], "song-input");
  uploads.set([upload("beat.mp3", 2, { progress: 0.3 })]);
  // Two songs in one go: only the last one is uploaded.
  choose([new File(["xxx"], "other.mp3", { type: "audio/mpeg" }), new File(["xxxx"], "final.mp3", { type: "audio/mpeg" })]);
  expect(uploads.commands.cancel).toHaveBeenCalledWith("up-beat.mp3");
  expect(screen.queryByText("beat.mp3")).toBeNull();
  expect(screen.queryByText("other.mp3")).toBeNull();
  expect(row("final.mp3").textContent).toContain("Song: final.mp3");
  expect(vi.mocked(uploads.commands.addFiles).mock.calls[1]?.[0].map((file) => (file as File).name)).toEqual(["final.mp3"]);
});

test("a clip this browser already uploaded is used as it is, not uploaded again", async () => {
  const api = readyApi();
  const uploads = fakeUploads(twoReady());
  page(uploads);
  await waitFor(() => expect(api.callsTo("GET /v1/assets").length).toBeGreaterThan(0));
  await new Promise((resolve) => setTimeout(resolve, 20));
  choose([video("squat.mp4", 4)]);
  await waitFor(() => expect(within(row("squat.mp4")).getByText("Ready")).toBeTruthy());
  expect(uploads.commands.addFiles).not.toHaveBeenCalled();
});

test("an uploaded clip that is no longer in the library says so, after the library was read once more", async () => {
  const api = installApi({ "GET /v1/assets": assets() });
  const uploads = fakeUploads();
  page(uploads);
  choose([video("squat.mp4", 4)]);
  await waitFor(() => expect(api.callsTo("GET /v1/assets").length).toBeGreaterThan(0));
  const before = api.callsTo("GET /v1/assets").length;
  uploads.set([upload("squat.mp4", 4, { status: "done", assetId: assetId(1) })]);
  await waitFor(() => expect(within(row("squat.mp4")).getByText("This one is no longer uploading. Remove it and add it again.")).toBeTruthy());
  expect(api.callsTo("GET /v1/assets").length).toBe(before + 1);
});

test("an upload that was removed from the uploads list says so", async () => {
  installApi({ "GET /v1/assets": assets() });
  const uploads = fakeUploads();
  page(uploads);
  choose([video("squat.mp4", 4)]);
  uploads.set([upload("squat.mp4", 4, { progress: 0.2 })]);
  expect(within(row("squat.mp4")).getByText(/Uploading/)).toBeTruthy();
  uploads.set([]);
  await waitFor(() => expect(within(row("squat.mp4")).getByText("This one is no longer uploading. Remove it and add it again.")).toBeTruthy());
});

test("Next waits while a clip has a problem, and says why in a sentence", () => {
  readyApi();
  const uploads = fakeUploads([upload("squat.mp4", 4, { status: "done", assetId: assetId(1) }), upload("row.mp4", 5, { status: "failed" })]);
  page(uploads);
  choose([video("squat.mp4", 4), video("row.mp4", 5)]);
  const next = screen.getByRole("button", { name: /^Next/ }) as HTMLButtonElement;
  expect(next.disabled).toBe(true);
  expect(next.getAttribute("aria-describedby")).toBeTruthy();
  expect(document.getElementById(next.getAttribute("aria-describedby") as string)?.textContent).toBe("One of your clips didn't work. Try it again or remove it to go on.");
  fireEvent.click(screen.getByRole("button", { name: "Remove row.mp4" }));
  expect((screen.getByRole("button", { name: /^Next/ }) as HTMLButtonElement).disabled).toBe(false);
});

test("a clip that fails behind the questions stops the start and sends the creator back to the clips", async () => {
  const api = readyApi();
  const uploads = fakeUploads([upload("squat.mp4", 4, { status: "done", assetId: assetId(1) }), upload("row.mp4", 5, { progress: 0.2 })]);
  page(uploads);
  await answerAll();
  uploads.set([upload("squat.mp4", 4, { status: "done", assetId: assetId(1) }), upload("row.mp4", 5, { status: "failed" })]);
  await screen.findByRole("alert");
  expect(screen.getByRole("alert").textContent).toContain("One of your clips needs a look");
  expect(screen.queryByRole("button", { name: "Make my video" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Back to your clips" }));
  expect(screen.getByRole("heading", { name: "Add your clips" })).toBeTruthy();
  expect(api.callsTo("POST /v1/projects")).toHaveLength(0);
});

test("a start the API refuses is said in a sentence, with the button to try again", async () => {
  const api = readyApi({ "POST /v1/projects": apiError(402, "plan_required") });
  const onOpen = page(fakeUploads(twoReady()));
  await answerAll();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("I couldn't start your video");
  expect(alert.textContent).toContain("You need an active plan to make videos. Choose one in Settings.");
  expect(alert.textContent).not.toContain("plan_required");
  expect(onOpen).not.toHaveBeenCalled();

  api.on("POST /v1/projects", json(201, project(1)));
  fireEvent.click(screen.getByRole("button", { name: "Try again" }));
  await waitFor(() => expect(onOpen).toHaveBeenCalledWith(projectId(1), runId(7)));
});

test("when only the editor does not start, the video is kept and its screen is opened without an edit", async () => {
  readyApi({ "POST /v1/projects/{id}/edit": apiError(429, "rate_limited") });
  const onOpen = page(fakeUploads(twoReady()));
  await answerAll();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(onOpen).toHaveBeenCalledWith(projectId(1)));
});

test("'Change' on the last screen goes to that question and comes straight back", async () => {
  readyApi();
  page(fakeUploads(twoReady()));
  await answerAll();
  fireEvent.click(screen.getByRole("button", { name: "Change length" }));
  expect(screen.getByRole("heading", { name: "How long should it be?" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: /^Long/ }));
  expect(screen.getByRole("heading", { name: "Ready to make your video?" })).toBeTruthy();
  expect(screen.getByText("Long · About a minute")).toBeTruthy();
});
