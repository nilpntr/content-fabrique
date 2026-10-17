import { SignUp } from "@clerk/tanstack-react-start";
import { createFileRoute } from "@tanstack/react-router";
import { SIGN_IN_PATH, SIGN_UP_PATH } from "@/lib/auth-guard";

export const Route = createFileRoute("/sign-up/$")({
  component: () => <SignUp routing="path" path={SIGN_UP_PATH} signInUrl={SIGN_IN_PATH} />,
});
