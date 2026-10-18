import { CheckIcon } from "lucide-react";
import { cardClassName } from "@/components/page";
import { Skeleton } from "@/components/ui/skeleton";
import type { Me, PlanName } from "@/lib/api";
import { isLapsed } from "@/lib/paywall";
import { cn } from "@/lib/utils";

interface PlanCard {
  /** The API's name for the plan. */
  id: Exclude<PlanName, "none">;
  name: string;
  price: string;
  /** Who pays for the AI (product plan section 10). */
  billing: string;
  includes: readonly string[];
}

/**
 * The plans and prices of the product plan (docs/01 section 10). They are hypotheses to be validated in the beta, so the
 * prices say "about". Billing arrives with P7-01; until then the cards only inform.
 */
export const PLAN_CARDS: readonly PlanCard[] = [
  {
    id: "trial",
    name: "Trial",
    price: "On request",
    billing: "AI included, billed by us",
    includes: ["2 exported videos", "2 GB storage", "Previews are watermarked; exports never are"],
  },
  {
    id: "studio",
    name: "Studio (BYOK)",
    price: "About €25 / month",
    billing: "AI billed to your own Anthropic key",
    includes: ["60 exports a month", "100 GB storage", "Trend board", "Ideas"],
  },
  {
    id: "creator",
    name: "Creator (managed)",
    price: "About €59 / month",
    billing: "AI included, billed by us from credits",
    includes: ["15 videos a month", "50 GB storage", "Trend board", "Ideas"],
  },
  {
    id: "pro",
    name: "Pro (managed)",
    price: "About €129 / month",
    billing: "AI included, billed by us from credits",
    includes: ["40 videos a month", "200 GB storage", "Brand kit", "Priority renders", "TikTok drafts"],
  },
];

/**
 * Shown instead of every app page to a signed-in user whose workspace has no active plan. There is no checkout yet
 * (P7-01): plans are activated by hand, so the page says so and names the workspace to activate. No address is invented.
 */
export function Paywall({ workspace, plan }: { workspace: Me["workspace"]; plan: Me["entitlements"] | undefined }) {
  // plan is undefined only when the API sent no entitlements: treated as no plan.
  const lapsed = isLapsed(plan);
  const planName = plan && plan.plan !== "none" ? `The ${plan.plan} plan` : "The plan";
  return (
    <section aria-labelledby="paywall-title" data-testid="paywall" className="mx-auto max-w-5xl space-y-6">
      <div className="animate-rise">
        <h1 id="paywall-title" className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">
          {lapsed ? "Your plan has ended" : "Choose a plan to start"}
        </h1>
        <p className="mt-1.5 max-w-prose text-muted-foreground">
          {lapsed
            ? `${planName} of ${workspace.name} is no longer active. Pick a plan below to carry on.`
            : `${workspace.name} has no plan yet. Pick one below to unlock the app.`}
        </p>
      </div>

      <ul className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {PLAN_CARDS.map((card, i) => (
          <li
            key={card.id}
            data-plan={card.id}
            className={cn(cardClassName, "flex flex-col gap-3")}
            style={{ animationDelay: `${50 + i * 45}ms` }}
          >
            <div>
              <h2 className="text-[15px] font-semibold tracking-[-0.01em]">{card.name}</h2>
              <p className="mt-1 text-[1.25rem] leading-tight font-semibold tracking-[-0.02em]">{card.price}</p>
              <p className="mt-1 text-[12.5px] text-subtle-foreground">{card.billing}</p>
            </div>
            <ul className="space-y-1.5 text-[13.5px]">
              {card.includes.map((item) => (
                <li key={item} className="flex items-start gap-2">
                  <CheckIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-brand" />
                  <span>{item}</span>
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>

      <div className={cn(cardClassName, "max-w-xl space-y-2")} style={{ animationDelay: "260ms" }}>
        <h2 className="text-[15px] font-semibold tracking-[-0.01em]">Ask us to activate your plan</h2>
        <p className="text-[13.5px] text-muted-foreground">
          Checkout is not open yet, so plans are switched on by hand. Tell us which plan you want and give us the workspace
          below. The app opens the next time you reload the page after we have switched it on.
        </p>
        <p className="text-[13.5px]">
          <span className="text-muted-foreground">Workspace </span>
          <span className="font-medium">{workspace.name}</span>{" "}
          <code className="font-mono text-[12.5px] break-all text-muted-foreground">{workspace.id}</code>
        </p>
      </div>
    </section>
  );
}

/** The plan is still being fetched: the shape of the page, no content, so a user with a plan never sees the paywall flash. */
export function PaywallPending() {
  return (
    <div aria-busy="true" data-testid="plan-pending" className="mx-auto max-w-5xl space-y-6">
      <Skeleton className="h-8 w-1/3" />
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }, (_, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: static skeleton cards, the list never reorders.
          <Skeleton key={i} className="h-44" />
        ))}
      </div>
    </div>
  );
}
