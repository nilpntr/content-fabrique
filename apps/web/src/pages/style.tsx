import { useAuth } from "@clerk/tanstack-react-start";
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronLeft, Loader2, RefreshCw, Sparkles } from "lucide-react";
import * as React from "react";
import { cardClassName, PageHeader } from "@/components/page";
import { ReferenceVideos } from "@/components/style/reference-videos";
import { StyleSettingsForm } from "@/components/style/style-settings";
import { VibePicker } from "@/components/style/vibe-picker";
import { Button } from "@/components/ui/button";
import { Disclosure } from "@/components/ui/disclosure";
import { Skeleton } from "@/components/ui/skeleton";
import { notify } from "@/components/ui/toast";
import { ApiError, type CreatorStyle, type StyleSettings } from "@/lib/api/client";
import { useMe } from "@/lib/api/hooks";
import { useRelearnStyle, useRemoveStyleReference, useResetStyle, useStyle, useUpdateStyleSettings } from "@/lib/api/style-hooks";
import { STYLE_QUERY_KEY } from "@/lib/query-client";
import { chosenCount, learningLine, sourceNote, withMySettings, withSetting } from "@/lib/style-words";

/** Something went wrong saving: a sentence, never a code. */
function failure(error: unknown): string {
  if (error instanceof ApiError && error.code === "insufficient_role") return "Only owners and editors can change the style.";
  if (error instanceof ApiError && error.code === "network_error") return "We couldn't reach the server. Check your connection and try again.";
  if (error instanceof ApiError && error.code === "style_learning_in_progress") return "Your videos are still being learnt. Try again when that's done.";
  if (error instanceof ApiError && error.code === "too_many_relearns") return "You've learnt your style again as often as you can today. Try again tomorrow.";
  return "That didn't save. Try again in a moment.";
}

/**
 * Settings → Your style: what the AI says the creator's style is, the videos it learnt it from, the vibe and the settings the creator
 * controls. The measured numbers are behind a closed "Details".
 */
export function StylePage() {
  const style = useStyle();
  const me = useMe();
  const canEdit = me.data ? me.data.workspace.role !== "viewer" : true;

  return (
    <div className="space-y-6">
      <Link to="/settings" className="inline-flex items-center gap-1 text-[13.5px] text-muted-foreground transition-colors hover:text-foreground">
        <ChevronLeft aria-hidden className="size-4" />
        Settings
      </Link>
      <PageHeader title="Your style" description="How your videos look. The editor follows it, and your own choices always win." />
      {style.isPending ? (
        <div className="max-w-2xl space-y-4" aria-busy="true">
          <Skeleton className="h-28 w-full rounded-xl" />
          <Skeleton className="h-48 w-full rounded-xl" />
          <p role="status" className="sr-only">
            Loading your style
          </p>
        </div>
      ) : style.isError ? (
        <div role="alert" className={`${cardClassName} max-w-2xl space-y-2`}>
          <p className="font-medium">Your style could not be loaded</p>
          <p className="text-[13.5px] text-muted-foreground">Check your connection and try again.</p>
          <Button variant="secondary" size="sm" onClick={() => void style.refetch()}>
            Try again
          </Button>
        </div>
      ) : (
        <StyleEditor style={style.data} canEdit={canEdit} />
      )}
    </div>
  );
}

export function StyleEditor({ style, canEdit }: { style: CreatorStyle; canEdit: boolean }) {
  const { userId, orgId } = useAuth();
  const queryClient = useQueryClient();
  const update = useUpdateStyleSettings();
  const relearn = useRelearnStyle();
  const remove = useRemoveStyleReference();
  const reset = useResetStyle();
  const [confirmReset, setConfirmReset] = React.useState(false);
  const learning = learningLine(style);
  const ready = style.references.some((r) => r.status === "ready");

  function save(settings: StyleSettings) {
    const key = [STYLE_QUERY_KEY, userId, orgId];
    const before = queryClient.getQueryData<CreatorStyle>(key);
    if (before) queryClient.setQueryData(key, withMySettings(before, settings));
    update.mutate(settings, {
      onError: (error) => {
        if (before) queryClient.setQueryData(key, before);
        notify("Your change wasn't saved", { description: failure(error), type: "error" });
      },
    });
  }

  return (
    <div className="max-w-2xl space-y-4">
      {/* What the AI says, in a sentence or two. */}
      <section aria-labelledby="style-summary-title" className={`${cardClassName} space-y-3`}>
        <div className="flex items-center gap-2">
          <span aria-hidden className="flex size-8 items-center justify-center rounded-lg bg-brand-wash text-brand">
            <Sparkles className="size-4" />
          </span>
          <h2 id="style-summary-title" className="text-[15px] font-semibold tracking-[-0.01em]">
            In a sentence
          </h2>
        </div>
        {style.summary ? (
          <>
            <p className="text-[17px] leading-relaxed tracking-[-0.01em] text-pretty">{style.summary.text}</p>
            <p className="text-[12.5px] text-subtle-foreground">
              Written by AI from {style.summary.references} of your {style.summary.references === 1 ? "video" : "videos"}.
            </p>
          </>
        ) : style.measures ? (
          <p className="text-muted-foreground">Your style is learnt. The description will appear here soon.</p>
        ) : (
          <p className="text-muted-foreground text-pretty">Add 3 to 10 of your own finished videos below, and I'll describe your style here and set the editor up to match it.</p>
        )}
        {learning ? (
          <p role="status" className="flex items-center gap-2 rounded-lg bg-wash px-3 py-2 text-[13.5px]">
            <Loader2 aria-hidden className="size-4 animate-spin text-brand motion-reduce:animate-none" />
            {learning}
          </p>
        ) : null}
      </section>

      {/* The videos it is learnt from. */}
      <section aria-labelledby="style-videos-title" className={`${cardClassName} space-y-3 [animation-delay:40ms]`}>
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <div>
            <h2 id="style-videos-title" className="text-[15px] font-semibold tracking-[-0.01em]">
              Your videos
            </h2>
            <p className="text-[13px] text-muted-foreground">Videos you already posted. They only teach your style; they never end up in a new video.</p>
          </div>
          {ready && canEdit ? (
            <Button
              variant="ghost"
              size="sm"
              disabled={relearn.isPending || style.status === "learning"}
              onClick={() =>
                relearn.mutate(undefined, {
                  onSuccess: () => notify("Learning your style again"),
                  onError: (error) => notify("That didn't start", { description: failure(error), type: "error" }),
                })
              }
            >
              <RefreshCw aria-hidden />
              Learn again
            </Button>
          ) : null}
        </div>
        <ReferenceVideos
          style={style}
          removing={remove.isPending ? (remove.variables ?? null) : null}
          onRemove={
            canEdit
              ? (assetId) =>
                  remove.mutate(assetId, {
                    onError: (error) => notify("That video wasn't removed", { description: failure(error), type: "error" }),
                  })
              : undefined
          }
        />
      </section>

      {/* The vibe. */}
      <section aria-labelledby="style-vibe-title" className={`${cardClassName} space-y-3 [animation-delay:80ms]`}>
        <div>
          <h2 id="style-vibe-title" className="text-[15px] font-semibold tracking-[-0.01em]">
            Vibe
          </h2>
          <p className="text-[13px] text-muted-foreground">The energy of your videos. You can still pick another one for a single video.</p>
        </div>
        <VibePicker
          value={style.effective.vibe}
          legend="Vibe"
          disabled={!canEdit}
          onChange={(vibe) => save(withSetting(style.settings, "vibe", vibe))}
          note={style.settings.vibe ? null : sourceNote(style.effective.sources.vibe)}
        />
      </section>

      {/* The settings. */}
      <section aria-labelledby="style-settings-title" className={`${cardClassName} space-y-4 [animation-delay:120ms]`}>
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <div>
            <h2 id="style-settings-title" className="text-[15px] font-semibold tracking-[-0.01em]">
              Your settings
            </h2>
            <p className="text-[13px] text-muted-foreground">Change anything. Learning again from your videos never undoes your choices.</p>
          </div>
          {chosenCount(style.settings) > 0 && canEdit ? (
            <Button variant="ghost" size="sm" onClick={() => save({})}>
              Use all suggestions
            </Button>
          ) : null}
        </div>
        {canEdit ? null : <p className="rounded-lg bg-wash px-3 py-2 text-[13px] text-muted-foreground">Only owners and editors can change the style.</p>}
        <StyleSettingsForm style={style} onChange={save} disabled={!canEdit} />
      </section>

      {/* The numbers, out of the way. */}
      <Disclosure summary="Details" hint="what was measured" className={`${cardClassName} [animation-delay:160ms]`}>
        {style.measures ? (
          <dl className="grid grid-cols-[minmax(6rem,9rem)_1fr] gap-x-4 gap-y-2 text-[13px]">
            {style.measures.lines.map((line) => (
              <React.Fragment key={line.id}>
                <dt className="text-muted-foreground">{line.label}</dt>
                <dd className="min-w-0">{line.value}</dd>
              </React.Fragment>
            ))}
          </dl>
        ) : (
          <p className="text-[13px] text-muted-foreground">Nothing measured yet.</p>
        )}
        {style.measures && !style.measures.firm ? (
          <p className="mt-3 text-[13px] text-muted-foreground">With fewer than 3 videos this is a rough guide: add more for a closer match.</p>
        ) : null}
        {canEdit && (style.references.length > 0 || chosenCount(style.settings) > 0) ? (
          <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-hairline pt-3">
            {confirmReset ? (
              <>
                <p className="text-[13px]">Remove your videos, what was learnt and your settings?</p>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={reset.isPending}
                  onClick={() =>
                    reset.mutate(undefined, {
                      onSuccess: () => {
                        setConfirmReset(false);
                        notify("Your style was cleared");
                      },
                      onError: (error) => notify("That didn't work", { description: failure(error), type: "error" }),
                    })
                  }
                >
                  Yes, start over
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setConfirmReset(false)}>
                  Keep it
                </Button>
              </>
            ) : (
              <Button variant="ghost" size="sm" onClick={() => setConfirmReset(true)}>
                Start over
              </Button>
            )}
          </div>
        ) : null}
      </Disclosure>
    </div>
  );
}
