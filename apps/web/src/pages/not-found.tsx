import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";

export function NotFound() {
  return (
    <div className="mx-auto max-w-md py-24 text-center">
      <h1 className="text-[1.375rem] font-semibold tracking-[-0.02em]">Nothing at this address</h1>
      <p className="mt-2 text-muted-foreground">This page doesn't exist, or it has moved.</p>
      <Button className="mt-6" variant="outline" nativeButton={false} render={<Link to="/" />}>
        Back to Home
      </Button>
    </div>
  );
}
