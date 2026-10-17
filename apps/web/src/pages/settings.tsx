import { CreditsCard } from "@/components/credits";
import { cardClassName } from "@/components/page-placeholder";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError } from "@/lib/api";
import { useMe } from "@/lib/api/hooks";
import { creditsOf } from "@/lib/credits";

/** Settings. For now it shows what the API knows about the signed-in user and the active workspace (GET /v1/me). */
export function SettingsPage() {
  const me = useMe();

  return (
    <div className="space-y-6">
      <div className="animate-rise">
        <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">Settings</h1>
        <p className="mt-1.5 max-w-prose text-muted-foreground">Your account and the workspace you are working in.</p>
      </div>

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
        </div>
      ) : (
        <dl className={`${cardClassName} grid max-w-xl grid-cols-[8rem_1fr] gap-x-4 gap-y-3 text-[13.5px] [animation-delay:95ms]`}>
          <Row label="Email" value={me.data.user.email} />
          <Row label="User id" value={me.data.user.id} mono />
          <Row label="Workspace" value={me.data.workspace.name} />
          <Row label="Workspace id" value={me.data.workspace.id} mono />
          <Row label="Your role" value={me.data.workspace.role} />
        </dl>
      )}

      {/* The balance, once the API reports one. */}
      {me.data && creditsOf(me.data) ? (
        <div className={`${cardClassName} max-w-xl space-y-3 [animation-delay:140ms]`}>
          <h2 className="text-[15px] font-semibold tracking-[-0.01em]">Credits</h2>
          <CreditsCard credits={creditsOf(me.data)} />
        </div>
      ) : null}
    </div>
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
    if (error.code === "network_error") return "The server could not be reached. Is the API running?";
    return `${error.message} (${error.code})`;
  }
  return "Something went wrong.";
}
