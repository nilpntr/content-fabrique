import { ArrowRight, Check, ListChecks, Minus, Waves, X } from "lucide-react";
import * as React from "react";
import { cn } from "@/lib/utils";
import { Pill } from "./badges";
import { applicable, type CheckItem, type CheckRound, type CheckStatus, type Fix } from "./run-artifacts";

/** The checks of `check_edit` in the creator's words (edit-style-v1 section 7 and audio-v1). An id this build does not know reads as its name. */
const CHECK_NAMES: Readonly<Record<string, string>> = {
  hook_length: "Hook length",
  hook_title: "Hook title",
  shot_pacing: "Shot pacing",
  shot_variety: "Shot variety",
  pace_builds: "Pace builds",
  rep_boundaries: "Cuts on reps",
  dead_time: "No dead time",
  effect_density: "Effect density",
  text_safe_zone: "Text in safe zones",
  duration: "Length",
  brightness_jump: "Even brightness",
  crushed: "Shadows kept",
  beat_sync: "Cuts on the beat",
  audio_level: "Loudness",
  audio_bed: "Sound bed",
  audio_effects: "Sound effects",
};

export const checkName = (id: string): string => CHECK_NAMES[id] ?? id.replaceAll("_", " ").replace(/^./, (c) => c.toUpperCase());

const FIX_NAMES: Readonly<Record<Fix, string>> = { match_exposure: "match_exposure", edit: "re-edit" };

const STATUS: Readonly<Record<CheckStatus, { label: string; chip: string; icon: React.ReactNode }>> = {
  pass: { label: "Pass", chip: "bg-add-wash text-foreground", icon: <Check aria-hidden strokeWidth={3} className="size-3 text-good" /> },
  borderline: { label: "Borderline", chip: "bg-warning/15 text-foreground", icon: <Waves aria-hidden strokeWidth={2.5} className="size-3 text-warning" /> },
  fail: { label: "Fail", chip: "bg-del-wash text-foreground", icon: <X aria-hidden strokeWidth={3} className="size-3 text-critical" /> },
  skip: { label: "Not applicable", chip: "bg-wash text-subtle-foreground", icon: <Minus aria-hidden strokeWidth={3} className="size-3" /> },
};

/** "12/13", the round at a glance. */
const ratio = (round: CheckRound): string => `${round.passed}/${applicable(round)}`;

/** The summary line: "13 of 13 pass". */
export const summaryText = (round: CheckRound): string => `${round.passed} of ${applicable(round)} pass`;

function CheckChip({ check, open, onToggle }: { check: CheckItem; open: boolean; onToggle: () => void }) {
  const status = STATUS[check.status];
  return (
    <li>
      <button
        type="button"
        aria-expanded={check.detail ? open : undefined}
        aria-label={`${checkName(check.id)}: ${status.label}`}
        title={check.detail}
        disabled={!check.detail}
        data-check={check.id}
        data-status={check.status}
        onClick={onToggle}
        className={cn(
          "inline-flex h-6 items-center gap-1.5 rounded-full px-2.5 text-[12px] font-medium whitespace-nowrap outline-none transition-[box-shadow,background-color] focus-visible:ring-2 focus-visible:ring-brand enabled:cursor-pointer enabled:hover:shadow-[inset_0_0_0_1px_var(--input)]",
          status.chip,
          open && "shadow-[inset_0_0_0_1px_var(--input)]",
        )}
      >
        {status.icon}
        {checkName(check.id)}
      </button>
    </li>
  );
}

/** The share of a round that passes, as a bar: the length of each part is its checks. */
function RoundBar({ round }: { round: CheckRound }) {
  const total = Math.max(applicable(round), 1);
  return (
    <div aria-hidden className="flex h-1.5 w-full max-w-40 gap-px overflow-hidden rounded-full bg-wash">
      <div className="bg-good transition-[width] duration-(--dur-slow)" style={{ width: `${(round.passed / total) * 100}%` }} />
      <div className="bg-critical transition-[width] duration-(--dur-slow)" style={{ width: `${(round.failed / total) * 100}%` }} />
    </div>
  );
}

/**
 * The editor's self-review: how many checks passed, round by round ("check 1: 12/13, then match_exposure, then check 2: 13/13"),
 * and for the round on show a chip for each check. A chip with a reason opens it in a line below (and shows it on hover).
 */
export function EditChecksCard({ rounds, className }: { rounds: CheckRound[]; className?: string }) {
  const latest = rounds.length;
  const [picked, setPicked] = React.useState<number | null>(null);
  const [openId, setOpenId] = React.useState<string | null>(null);
  // Follows the newest round until the creator picks one, and goes back to following when the picked one is no longer there.
  const index = picked !== null && picked <= latest ? picked : latest;
  const round = rounds[index - 1];
  if (!round) return null;

  const missing = applicable(round) + round.skipped - round.checks.length;
  const open = round.checks.find((check) => check.id === openId && check.detail);
  const allPass = round.failed === 0;

  return (
    <section aria-label="Self-review" data-testid="edit-checks" className={cn("space-y-3 rounded-xl bg-well p-3.5 shadow-well", className)}>
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <ListChecks aria-hidden className="size-4 text-brand" />
        <h3 className="text-[13.5px] font-semibold tracking-[-0.01em]">Self-review</h3>
        <Pill tone={allPass ? "good" : "bad"} className="tabular-nums" data-slot="summary">
          {summaryText(round)}
        </Pill>
        <RoundBar round={round} />
        {round.failed > 0 ? <span className="text-[12px] text-muted-foreground">{round.failed === 1 ? "1 check needs work" : `${round.failed} checks need work`}</span> : null}
      </div>

      {rounds.length > 1 ? (
        <ol aria-label="Rounds" className="flex flex-wrap items-center gap-1.5 text-[12px]">
          {rounds.map((each, i) => (
            <React.Fragment key={each.nth}>
              {i > 0 ? (
                <>
                  <li aria-hidden className="text-subtle-foreground">
                    <ArrowRight className="size-3" />
                  </li>
                  {each.fixes.length > 0 ? (
                    <>
                      <li className="font-mono text-[11.5px] text-muted-foreground" data-slot="fixes">
                        {each.fixes.map((fix) => FIX_NAMES[fix]).join(" + ")}
                      </li>
                      <li aria-hidden className="text-subtle-foreground">
                        <ArrowRight className="size-3" />
                      </li>
                    </>
                  ) : null}
                </>
              ) : null}
              <li>
                <button
                  type="button"
                  aria-pressed={each.nth === index}
                  aria-label={`Check ${each.nth}: ${summaryText(each)}`}
                  onClick={() => {
                    setPicked(each.nth);
                    setOpenId(null);
                  }}
                  className={cn(
                    "inline-flex h-6 cursor-pointer items-center gap-1.5 rounded-full px-2.5 font-medium tabular-nums outline-none transition-colors focus-visible:ring-2 focus-visible:ring-brand",
                    each.nth === index ? "bg-foreground text-background" : "bg-wash text-muted-foreground hover:text-foreground",
                  )}
                >
                  check {each.nth}
                  <span className={cn("font-mono", each.nth === index ? "opacity-80" : "")}>{ratio(each)}</span>
                </button>
              </li>
            </React.Fragment>
          ))}
        </ol>
      ) : null}

      {round.checks.length > 0 ? (
        <ul aria-label={`Checks of round ${round.nth}`} className="flex flex-wrap gap-1.5">
          {round.checks.map((check) => (
            <CheckChip key={check.id} check={check} open={openId === check.id} onToggle={() => setOpenId((current) => (current === check.id ? null : check.id))} />
          ))}
          {missing > 0 ? (
            <li>
              <Pill className="tabular-nums">+ {missing} more</Pill>
            </li>
          ) : null}
        </ul>
      ) : null}

      {/* Reserved while a reason can be shown, so opening one does not move what is below. */}
      {round.checks.some((check) => check.detail) ? (
        <p aria-live="polite" className="min-h-10 max-w-prose text-[12.5px] leading-5 text-muted-foreground" data-testid="check-detail">
          {open ? (
            <>
              <span className="font-medium text-foreground">{checkName(open.id)}.</span> {open.detail}
            </>
          ) : (
            <span className="text-subtle-foreground">Select a check to see why it passed or what to change.</span>
          )}
        </p>
      ) : null}
    </section>
  );
}
