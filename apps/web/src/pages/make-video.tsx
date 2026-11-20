import * as React from "react";
import { ClipsStep } from "@/components/guided/clips-step";
import { QuestionStep, type ReadyStatus, ReadyStep } from "@/components/guided/questions";
import { useGuidedClips } from "@/components/guided/use-guided-clips";
import { rememberVibe } from "@/components/guided/vibe-memory";
import { type Answers, buildBrief, KINDS, LENGTHS, STYLES, usualStyleHint, vibeOf, videoTitle } from "@/components/guided/words";
import { createProjectMessage, startEditMessage } from "@/components/project/errors";
import { notify } from "@/components/ui/toast";
import type { CreateProjectRequest } from "@/lib/api/client";
import { useCreateProject, useStartEdit } from "@/lib/api/project-hooks";
import { useStyle } from "@/lib/api/style-hooks";

type Step = "clips" | "kind" | "length" | "style" | "ready";

const QUESTIONS = 3;

/** What the first video of a project uses, in the plan's own unit (a video), never in credits or money. */
const COST_NOTE = "Uses about one video from your plan, with room for two rounds of changes.";

/**
 * The guided way to make a video (docs/design/guided-flow.md): hand over the clips, answer three questions with one tap each, and
 * press "Make my video". The clips upload while the questions are answered; if they are not all in when the button is pressed, the
 * page waits for them and then starts by itself. Starting makes the project, starts the editor on it and opens the screen that
 * follows it (`/make/$projectId`).
 */
export function MakeVideoPage({ onOpenVideo }: { onOpenVideo: (projectId: string, runId?: string) => void }) {
  const source = useGuidedClips();
  const style = useStyle();
  const createProject = useCreateProject();
  const startEdit = useStartEdit();
  const [step, setStep] = React.useState<Step>("clips");
  const [answers, setAnswers] = React.useState<Partial<Answers>>({});
  /** "Make my video" was pressed and has not been answered yet. */
  const [wanted, setWanted] = React.useState(false);
  const [error, setError] = React.useState<string | undefined>(undefined);
  const sending = React.useRef(false);

  const { clips, song } = source;
  const used = song ? [...clips, song] : clips;
  const ready = used.filter((clip) => clip.state.kind === "ready").length;
  const problem = used.some((clip) => clip.state.kind === "problem");
  const allReady = clips.length > 0 && ready === used.length;
  const learnt = style.data?.status === "ready";
  const complete = answers.kind !== undefined && answers.length !== undefined && answers.style !== undefined;

  // Where the first unanswered thing is: a "Change" from the last screen comes back to it after one answer.
  const next = (after: Partial<Answers>): Step => (after.kind === undefined ? "kind" : after.length === undefined ? "length" : after.style === undefined ? "style" : "ready");
  const answer = (patch: Partial<Answers>) => {
    const after = { ...answers, ...patch };
    setAnswers(after);
    setStep(next(after));
  };

  const begin = React.useEffectEvent(async () => {
    if (sending.current || !complete) return;
    sending.current = true;
    const chosen = answers as Answers;
    const brief = buildBrief(chosen, song?.name);
    const vibe = vibeOf(chosen.style);
    try {
      const project = await createProject.mutateAsync({
        format: "9:16",
        // The clips in the order they were added, then the song; each file once.
        asset_ids: [...new Set(used.flatMap((clip) => (clip.assetId ? [clip.assetId] : [])))] as CreateProjectRequest["asset_ids"],
        title: videoTitle(chosen.kind),
        brief,
      });
      // The project keeps the words but not the style: it is kept here for an edit that has to be started again.
      rememberVibe(project.id, vibe);
      try {
        const run = await startEdit.mutateAsync({ projectId: project.id, body: { instructions: brief, ...(vibe ? { vibe } : {}) } });
        onOpenVideo(project.id, run.id);
      } catch (failure) {
        // The video is saved; only the editor did not start. Its screen offers "Make my video" again.
        notify("Your clips are saved, but I couldn't start", { description: startEditMessage(failure), type: "error" });
        onOpenVideo(project.id);
      }
    } catch (failure) {
      setError(createProjectMessage(failure));
      setWanted(false);
      sending.current = false;
    }
  });

  // The start waits for the clips: it goes as soon as the last one is ready, and gives up when one of them cannot be used.
  React.useEffect(() => {
    if (!wanted) return;
    if (problem) setWanted(false);
    else if (allReady) void begin();
  }, [wanted, problem, allReady]);

  if (step === "kind") {
    return <QuestionStep number={1} of={QUESTIONS} title="What kind of video is it?" options={KINDS} picked={answers.kind} onPick={(kind) => answer({ kind })} onBack={() => setStep("clips")} />;
  }
  if (step === "length") {
    return <QuestionStep number={2} of={QUESTIONS} title="How long should it be?" options={LENGTHS} picked={answers.length} onPick={(length) => answer({ length })} onBack={() => setStep("kind")} />;
  }
  if (step === "style") {
    const options = STYLES.map((option) => (option.id === "mine" ? { ...option, hint: usualStyleHint(learnt) } : option));
    return <QuestionStep number={3} of={QUESTIONS} title="Which style?" options={options} picked={answers.style} onPick={(picked) => answer({ style: picked })} onBack={() => setStep("length")} />;
  }
  if (step === "ready" && complete && clips.length > 0) {
    let status: ReadyStatus;
    if (problem) status = { kind: "clip_problem" };
    else if (wanted && allReady) status = { kind: "starting" };
    else if (wanted) status = { kind: "waiting", ready, total: used.length };
    else if (error) status = { kind: "error", message: error };
    else status = { kind: "idle" };
    return (
      <ReadyStep
        answers={answers as Answers}
        clips={clips.length}
        song={Boolean(song)}
        learntStyle={learnt}
        status={status}
        costNote={COST_NOTE}
        onStart={() => {
          setError(undefined);
          setWanted(true);
        }}
        onStopWaiting={() => setWanted(false)}
        onEdit={setStep}
        onBack={() => setStep("style")}
      />
    );
  }
  return <ClipsStep source={source} onNext={() => setStep(next(answers))} />;
}
