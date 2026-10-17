import { useRouterState } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Paywall, PaywallPending } from "@/components/paywall";
import { Button } from "@/components/ui/button";
import { ApiError } from "@/lib/api";
import { decidePlanGate } from "@/lib/paywall";
import { useMe } from "@/lib/use-me";

/**
 * Wraps the page area of the shell: the page for a workspace with an active plan, the Paywall for one without, on every
 * route but Settings (src/lib/paywall.ts). The sidebar and the header stay outside it, so the user can still switch
 * workspace or account. The plan comes from GET /v1/me, keyed by user and organization, so switching workspace asks again.
 */
export function PlanGate({ children }: { children: ReactNode }) {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const me = useMe();
  const gate = decidePlanGate(pathname, me);

  switch (gate) {
    case "app":
      return children;
    case "loading":
      return <PaywallPending />;
    case "error":
      return (
        <div role="alert" className="mx-auto max-w-md space-y-3 py-24 text-center">
          <h1 className="text-[1.375rem] font-semibold tracking-[-0.02em]">We could not check your plan</h1>
          <p className="text-muted-foreground">
            {me.error instanceof ApiError && me.error.code === "network_error"
              ? "The server could not be reached."
              : "Something went wrong on our side."}
          </p>
          <Button variant="outline" onClick={() => void me.refetch()}>
            Try again
          </Button>
        </div>
      );
    case "paywall":
      // decidePlanGate only answers "paywall" when there is data.
      return me.data ? <Paywall workspace={me.data.workspace} plan={me.data.entitlements} /> : null;
  }
}
