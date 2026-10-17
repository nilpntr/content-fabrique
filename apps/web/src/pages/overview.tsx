import { Link } from "@tanstack/react-router";
import { PlusIcon } from "lucide-react";
import { PagePlaceholder } from "@/components/page-placeholder";
import { buttonVariants } from "@/components/ui/button";

export function OverviewPage() {
  return (
    <PagePlaceholder
      title="Overview"
      description="A summary of what matters most goes here."
      actions={
        <Link to="/projects/new" className={buttonVariants({ size: "md" })}>
          <PlusIcon aria-hidden="true" />
          New project
        </Link>
      }
    />
  );
}
