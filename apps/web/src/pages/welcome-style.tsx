import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight, Loader2 } from "lucide-react";
import { cardClassName } from "@/components/page";
import { ReferenceVideos } from "@/components/style/reference-videos";
import { VibePicker } from "@/components/style/vibe-picker";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { notify } from "@/components/ui/toast";
import type { EditVibe } from "@/lib/api/client";
import { useMe } from "@/lib/api/hooks";
import { useStyle, useUpdateStyleSettings } from "@/lib/api/style-hooks";
import { learningLine, withSetting } from "@/lib/style-words";

/**
 * The optional onboarding step "Show me your style" (P6-05b): upload 3 to 10 of your own videos, or skip that and pick Calm, Lively or
 * Hype. Both can be changed later in Settings → Your style.
 */
export function WelcomeStylePage() {
  const navigate = useNavigate();
  const style = useStyle();
  const me = useMe();
  const update = useUpdateStyleSettings();
  const canEdit = me.data ? me.data.workspace.role !== "viewer" : true;
  const data = style.data;
  const learning = data ? learningLine(data) : null;
  const done = () => void navigate({ to: "/" });

  function pick(vibe: EditVibe) {
    if (!data) return;
    update.mutate(withSetting(data.settings, "vibe", vibe), {
      onError: () => notify("Your vibe wasn't saved", { description: "Try again in a moment.", type: "error" }),
    });
  }

  const chosenVibe = update.variables?.vibe ?? data?.settings.vibe;

  return (
    <div className="mx-auto max-w-2xl space-y-5 pb-4">
      <div className="animate-rise space-y-1.5">
        <p className="text-[12.5px] font-medium tracking-wide text-brand uppercase">Optional · about a minute</p>
        <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">Show me your style</h1>
        <p className="max-w-prose text-muted-foreground text-pretty">
          Upload 3 to 10 videos you already posted, and I'll edit your new ones the way you do. Or skip that and just pick a vibe.
        </p>
      </div>

      {style.isPending ? (
        <div className="space-y-4" aria-busy="true">
          <Skeleton className="h-40 w-full rounded-xl" />
          <Skeleton className="h-32 w-full rounded-xl" />
        </div>
      ) : style.isError || !data ? (
        <div role="alert" className={`${cardClassName} space-y-2`}>
          <p className="font-medium">This step could not be loaded</p>
          <p className="text-[13.5px] text-muted-foreground">You can do it later in Settings.</p>
          <Button variant="secondary" size="sm" onClick={done}>
            Go to Home
          </Button>
        </div>
      ) : (
        <>
          <section aria-labelledby="welcome-videos-title" className={`${cardClassName} space-y-3`}>
            <div>
              <h2 id="welcome-videos-title" className="text-[15px] font-semibold tracking-[-0.01em]">
                Upload your videos
              </h2>
              <p className="text-[13px] text-muted-foreground">Finished videos from your TikTok or camera roll. They only teach your style.</p>
            </div>
            <ReferenceVideos style={data} compact />
            {learning ? (
              <p role="status" className="flex items-center gap-2 rounded-lg bg-wash px-3 py-2 text-[13.5px]">
                <Loader2 aria-hidden className="size-4 animate-spin text-brand motion-reduce:animate-none" />
                {learning} You can carry on; it finishes by itself.
              </p>
            ) : data.summary ? (
              <p className="rounded-lg bg-brand-wash px-3 py-2 text-[14px] text-pretty">{data.summary.text}</p>
            ) : null}
          </section>

          <div className="flex items-center gap-3 text-[12.5px] font-medium tracking-wide text-subtle-foreground uppercase">
            <span className="h-px flex-1 bg-hairline" />
            or
            <span className="h-px flex-1 bg-hairline" />
          </div>

          <section aria-labelledby="welcome-vibe-title" className={`${cardClassName} space-y-3 [animation-delay:60ms]`}>
            <div>
              <h2 id="welcome-vibe-title" className="text-[15px] font-semibold tracking-[-0.01em]">
                Pick a vibe
              </h2>
              <p className="text-[13px] text-muted-foreground">How much energy your videos should have. You can change it for any video.</p>
            </div>
            <VibePicker value={chosenVibe ?? data.effective.vibe} legend="Vibe" onChange={pick} disabled={!canEdit || update.isPending} />
          </section>

          <div className="sticky bottom-0 z-20 -mx-2 flex items-center justify-between gap-3 border-t border-hairline bg-background/90 px-2 py-3 backdrop-blur-md">
            <Link to="/" className={buttonVariants({ variant: "ghost" })}>
              Skip for now
            </Link>
            <Button size="lg" onClick={done} disabled={!data.references.length && !chosenVibe}>
              Continue
              <ArrowRight aria-hidden />
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
