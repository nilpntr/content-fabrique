import { SignIn } from "@clerk/tanstack-react-start";
import { createFileRoute } from "@tanstack/react-router";
import { SIGN_IN_PATH, SIGN_UP_PATH } from "@/lib/auth-guard";

// A catch-all, because Clerk's sign-in has several steps (verification codes, a pending "choose an organization" task)
// that live at /sign-in/<step>.
export const Route = createFileRoute("/sign-in/$")({
  component: () => <SignIn routing="path" path={SIGN_IN_PATH} signUpUrl={SIGN_UP_PATH} />,
});
