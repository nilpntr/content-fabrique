import { Link } from "@tanstack/react-router";
import { ChevronRight, Palette } from "lucide-react";
import { CreditsCard } from "@/components/credits";
import { cardClassName, PageHeader } from "@/components/page";
import { Disclosure } from "@/components/ui/disclosure";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError } from "@/lib/api";
import { useMe } from "@/lib/api/hooks";
import { useStyle } from "@/lib/api/style-hooks";
import { creditsOf } from "@/lib/credits";
import { vibeLabel } from "@/lib/style-words";

const capitalise = (word: string): string => word.replace(/^./, (c) => c.toUpperCase());

/** Settings: the plan and what is left of it, the account, and the workspace. Ids are behind "Details". */
export function SettingsPage() {
  const me = useMe();
  const credits = me.data ? creditsOf(me.data) : undefined;

  return (
    <div className="space-y-6">
      <PageHeader title="Settings" description="Your plan, your style, your account and your team's workspace." />

      <StyleCard />

      {/* The balance, once the API reports one. */}
      {credits ? (
        <section aria-labelledby="plan-title" className={`${cardClassName} max-w-xl space-y-3 [animation-delay:60ms]`}>
          <h2 id="plan-title" className="text-[15px] font-semibold tracking-[-0.01em]">
            {me.data?.entitlements.plan && me.data.entitlements.plan !== "none" ? `${capitalise(me.data.entitlements.plan)} plan` : "Your plan"}
          </h2>
          <CreditsCard credits={credits} />
        </section>
      ) : null}

      {me.isPending ? (
        <div className={`${cardClassName} max-w-xl space-y-3 [animation-delay:95ms]`} aria-busy="true">
          <Skeleton className="h-4 w-1/3" />
          <Skeleton className="h-5 w-2/3" />
          <Skeleton className="h-5 w-1/2" />
        </div>
      ) : me.isError ? (
        <div role="alert" className={`${cardClassName} max-w-xl space-y-1 [animation-delay:95ms]`}>
          <p className="text-[13.5px] font-medium">Your account could not be loaded.</p>
          <p className="text-[13px] text-muted-foreground">{describeError(me.error)}</p>
          {me.error instanceof ApiError && me.error.code ? (
            <Disclosure summary="Details" className="pt-1">
              <p className="font-mono text-[12px] text-subtle-foreground">{me.error.code}</p>
            </Disclosure>
          ) : null}
        </div>
      ) : (
        <section aria-labelledby="account-title" className={`${cardClassName} max-w-xl space-y-3 [animation-delay:95ms]`}>
          <h2 id="account-title" className="text-[15px] font-semibold tracking-[-0.01em]">
            Account
          </h2>
          <dl className="grid grid-cols-[minmax(6rem,8rem)_1fr] gap-x-4 gap-y-3 text-[13.5px]">
            <Row label="Email" value={me.data.user.email} />
            <Row label="Workspace" value={me.data.workspace.name} />
            <Row label="Your role" value={capitalise(me.data.workspace.role)} />
          </dl>
          <Disclosure summary="Details" hint="for support">
            <dl className="grid grid-cols-[minmax(6rem,8rem)_1fr] gap-x-4 gap-y-2 text-[13px]">
              <Row label="User id" value={me.data.user.id} mono />
              <Row label="Workspace id" value={me.data.workspace.id} mono />
            </dl>
          </Disclosure>
        </section>
      )}
    </div>
  );
}

/** "Your style": the AI's sentence (or what to do to get one) and a way in. */
function StyleCard() {
  const style = useStyle();
  const data = style.data;
  const line = !data
    ? "How your videos look, and the settings the editor follows."
    : data.summary
      ? data.summary.text
      : data.status === "learning"
        ? "Learning your style from your videos…"
        : data.references.length === 0
          ? `Vibe: ${vibeLabel(data.effective.vibe)}. Add a few of your own videos so the editor matches how you edit.`
          : "Your style is learnt.";
  return (
    <Link
      to="/settings/style"
      aria-labelledby="style-card-title"
      className={`${cardClassName} group/style flex max-w-xl items-center gap-3 transition-shadow hover:shadow-pop [animation-delay:30ms]`}
    >
      <span aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-brand-wash text-brand">
        <Palette className="size-4.5" />
      </span>
      <span className="min-w-0 flex-1">
        <span id="style-card-title" className="block text-[15px] font-semibold tracking-[-0.01em]">
          Your style
        </span>
        <span className="line-clamp-2 block text-[13px] text-muted-foreground">{line}</span>
      </span>
      <ChevronRight aria-hidden className="size-4 shrink-0 text-subtle-foreground transition-transform group-hover/style:translate-x-0.5" />
    </Link>
  );
}

function Row({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? "min-w-0 truncate font-mono text-[12.5px]" : "min-w-0 truncate font-medium"}>{value}</dd>
    </>
  );
}

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === "no_active_workspace") return "Choose or create a workspace first.";
    if (error.code === "network_error") return "We couldn't reach the server. Check your connection and try again.";
    return error.message;
  }
  return "Something went wrong. Try again in a moment.";
}
