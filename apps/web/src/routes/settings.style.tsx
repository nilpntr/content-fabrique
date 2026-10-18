import { createFileRoute } from "@tanstack/react-router";
import { StylePage } from "@/pages/style";

// Settings → Your style (P6-05b). Protected like every route.
export const Route = createFileRoute("/settings/style")({ component: StylePage });
