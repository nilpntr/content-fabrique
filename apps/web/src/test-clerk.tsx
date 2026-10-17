/**
 * A stand-in for Clerk in tests (installed by test-setup.ts). The tests have no Clerk keys and no network: the hooks
 * answer from `clerkMock`, and the components are labelled stubs.
 */
import type { ReactNode } from "react";
import type { Session } from "@/lib/auth-guard";

export const SIGNED_IN: Session = { isAuthenticated: true, userId: "user_test", orgId: "org_test" };

/** What the API answers for GET /v1/me. */
export const ME = {
  user: { id: "usr_01test", email: "ada@example.com" },
  workspace: { id: "wks_01test", name: "Fit Studio", role: "owner" },
  entitlements: { plan: "creator", status: "active", active: true, current_period_end: null },
};

export const clerkMock = {
  /** What the server-side session (`fetchSession`) answers, and what `useAuth()` reports. Tests replace it to sign out, drop the org... */
  session: { ...SIGNED_IN } as Session,
  token: "test-token" as string | null,
  /** What the server's `auth()` says, when it differs from the browser's (a stale `__session` cookie). Null: the same. */
  serverSession: null as Session | null,
  /** Whether clerk-js has loaded in the browser (`window.Clerk.loaded`). */
  loaded: true,
  /** How many times the route guard asked the server for the session. */
  serverSessionCalls: 0,
  reset() {
    this.session = { ...SIGNED_IN };
    this.token = "test-token";
    this.serverSession = null;
    this.loaded = true;
    this.serverSessionCalls = 0;
  },
};

/** `window.Clerk`, as the route guard reads it in the browser. */
export function installWindowClerk() {
  Object.defineProperty(window, "Clerk", {
    configurable: true,
    get() {
      const { isAuthenticated, userId, orgId } = clerkMock.session;
      return {
        loaded: clerkMock.loaded,
        user: isAuthenticated ? { id: userId } : null,
        session: isAuthenticated ? { status: "active" } : null,
        organization: orgId ? { id: orgId } : null,
      };
    },
  });
}

export function useAuth() {
  const { isAuthenticated, userId, orgId } = clerkMock.session;
  return { isLoaded: true, isSignedIn: isAuthenticated, userId, orgId, getToken: async () => clerkMock.token };
}

export function useUser() {
  return { isLoaded: true, user: { fullName: "Ada Lovelace", primaryEmailAddress: { emailAddress: "ada@example.com" } } };
}

export function ClerkProvider({ children }: { children: ReactNode }) {
  return <>{children}</>;
}

export function SignIn() {
  return <div data-testid="clerk-sign-in">Sign in</div>;
}

export function SignUp() {
  return <div data-testid="clerk-sign-up">Sign up</div>;
}

export function OrganizationList() {
  return <div data-testid="clerk-organization-list">Choose a workspace</div>;
}

export function OrganizationSwitcher() {
  return <div data-testid="clerk-organization-switcher">Organization switcher</div>;
}

export function UserButton({ children }: { children?: ReactNode }) {
  return (
    <div data-testid="clerk-user-button">
      User button
      {children}
    </div>
  );
}
UserButton.MenuItems = ({ children }: { children?: ReactNode }) => <>{children}</>;
UserButton.Action = ({ label, onClick }: { label: string; onClick?: () => void }) => (
  <button type="button" onClick={onClick}>
    {label}
  </button>
);
