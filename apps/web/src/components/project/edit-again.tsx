import { Wand2 } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { useStartEdit } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";
import { startEditMessage } from "./errors";

/** The longest instruction the API takes. */
export const INSTRUCTIONS_MAX_LENGTH = 2000;

/** What one edit may cost at most, unless the server says otherwise (the API's default spend cap, in dollars). The run panel shows the real cap once the run exists. */
export const DEFAULT_BUDGET_USD = 5;

/**
 * Starts another editor run on the project with new instructions. The instructions are optional: with none, the editor looks
 * at the cut again by itself. The server's default model profile is used, and the spend cap is shown, not changeable here.
 */
export function EditAgain({
  projectId,
  busy = false,
  onStarted,
  firstCut = false,
  className,
}: {
  projectId: string;
  /** An editor run is going on: the server would refuse another one. */
  busy?: boolean;
  onStarted: (runId: string) => void;
  /** The project has no cut yet: the button says "Start the editor". */
  firstCut?: boolean;
  className?: string;
}) {
  const [open, setOpen] = React.useState(false);
  const [text, setText] = React.useState("");
  const start = useStartEdit();
  const id = React.useId();
  const hintId = React.useId();
  const openRef = React.useRef<HTMLButtonElement>(null);
  const areaRef = React.useRef<HTMLTextAreaElement>(null);

  React.useEffect(() => {
    if (open) areaRef.current?.focus();
  }, [open]);

  function submit(event: React.FormEvent) {
    event.preventDefault();
    if (start.isPending || busy) return;
    const instructions = text.trim();
    start.mutate({ projectId, body: instructions === "" ? {} : { instructions } }, {
      onSuccess: (run) => {
        setOpen(false);
        setText("");
        onStarted(run.id);
      },
    });
  }

  function close() {
    setOpen(false);
    start.reset();
    openRef.current?.focus();
  }

  const label = firstCut ? "Start the editor" : "Edit again";

  if (!open) {
    return (
      <div className={cn("flex flex-wrap items-center gap-3", className)}>
        <Button ref={openRef} variant="secondary" onClick={() => setOpen(true)} disabled={busy}>
          <Wand2 aria-hidden />
          {label}
        </Button>
        {busy ? <p className="text-[13px] text-muted-foreground">The editor is still on this project. You can edit again when it has finished or you stop it.</p> : null}
      </div>
    );
  }

  const remaining = INSTRUCTIONS_MAX_LENGTH - text.length;
  return (
    <form onSubmit={submit} aria-label={label} className={cn("space-y-3 rounded-xl bg-surface-raised p-4 shadow-soft ring-1 ring-border", className)}>
      <div className="space-y-1.5">
        <div className="flex items-baseline justify-between gap-3">
          <label htmlFor={id} className="font-medium">
            What should change? <span className="font-normal text-subtle-foreground">(optional)</span>
          </label>
          <span className={cn("text-[12.5px] tabular-nums", remaining <= 200 ? "text-foreground" : "text-subtle-foreground")}>
            {text.length} / {INSTRUCTIONS_MAX_LENGTH}
            <span className="sr-only"> characters</span>
          </span>
        </div>
        <Textarea
          ref={areaRef}
          id={id}
          aria-describedby={hintId}
          rows={4}
          maxLength={INSTRUCTIONS_MAX_LENGTH}
          value={text}
          disabled={start.isPending}
          onChange={(event) => setText(event.target.value)}
          placeholder="For example: make it shorter, open on the lockout, and add a caption to the last clip."
        />
        <p id={hintId} className="text-[13px] text-muted-foreground">
          The editor starts from the latest version and makes a new one. Earlier versions stay.
        </p>
      </div>
      {start.isError ? (
        <p role="alert" className="rounded-xl bg-del-wash px-3 py-2 text-destructive">
          {startEditMessage(start.error)}
        </p>
      ) : null}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[13px] text-muted-foreground">
          Spending cap per edit: <span className="font-medium text-foreground tabular-nums">${DEFAULT_BUDGET_USD.toFixed(2)}</span>
        </p>
        <div className="flex gap-2">
          <Button type="button" variant="ghost" onClick={close} disabled={start.isPending}>
            Cancel
          </Button>
          <Button type="submit" disabled={start.isPending || busy} focusableWhenDisabled>
            {start.isPending ? "Starting…" : label}
          </Button>
        </div>
      </div>
    </form>
  );
}
