import { cardClassName, PagePlaceholder } from "@/components/page-placeholder";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { notify } from "@/components/ui/toast";

export function SettingsPage() {
  return (
    <PagePlaceholder title="Settings" description="An example settings page." cards={1}>
      <form
        className={`${cardClassName} max-w-xl space-y-4 [animation-delay:95ms]`}
        onSubmit={(event) => {
          event.preventDefault();
          notify("Settings saved", { description: "The workspace name is updated." });
        }}
      >
        <div>
          <label htmlFor="workspace-name" className="text-[13.5px] font-medium">
            Workspace name
          </label>
          <p className="mt-0.5 text-[13px] text-muted-foreground">Shown in the sidebar and in page titles.</p>
        </div>
        <Input id="workspace-name" name="workspace" defaultValue="Dashboard" autoComplete="off" />
        <div className="flex items-center gap-2">
          <Button type="submit">Save changes</Button>
          <Button type="reset" variant="ghost">
            Reset
          </Button>
        </div>
      </form>
    </PagePlaceholder>
  );
}
