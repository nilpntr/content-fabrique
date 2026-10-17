import { CloudUpload } from "lucide-react";
import * as React from "react";
import { filesFromDrop } from "@/lib/upload/files";
import { useUploadCommands } from "@/lib/upload/provider";
import { cn } from "@/lib/utils";
import { UploadButton } from "./upload-button";

function hasFiles(event: React.DragEvent): boolean {
  return [...event.dataTransfer.types].includes("Files");
}

/**
 * Drop clips here. Dragging is a pointer feature, so the Upload button inside is the way in for the keyboard and screen
 * readers; the zone only adds a bigger target and a highlight while files are over it.
 */
export function UploadDropzone({ className }: { className?: string }) {
  const { addFiles, available } = useUploadCommands();
  const [over, setOver] = React.useState(false);
  // dragenter/dragleave also fire for the children, so the zone counts them.
  const depth = React.useRef(0);

  return (
    <section
      aria-label="Upload clips"
      data-dragging={over}
      onDragEnter={(event) => {
        if (!hasFiles(event)) return;
        event.preventDefault();
        depth.current++;
        setOver(true);
      }}
      onDragOver={(event) => {
        if (!hasFiles(event)) return;
        event.preventDefault(); // allows the drop
        event.dataTransfer.dropEffect = "copy";
      }}
      onDragLeave={(event) => {
        if (!hasFiles(event)) return;
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) setOver(false);
      }}
      onDrop={(event) => {
        if (!hasFiles(event)) return;
        event.preventDefault();
        depth.current = 0;
        setOver(false);
        if (available) void filesFromDrop(event.dataTransfer).then((files) => (files.length > 0 ? addFiles(files) : undefined));
      }}
      className={cn(
        "flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-dashed border-border bg-well px-4 py-3 transition-[background-color,border-color] duration-(--dur-fast) ease-out",
        "data-[dragging=true]:border-brand data-[dragging=true]:bg-brand-wash",
        className,
      )}
    >
      <div className="flex min-w-0 items-center gap-3">
        <span aria-hidden="true" className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-surface-raised text-muted-foreground shadow-raised">
          <CloudUpload className="size-4.5" />
        </span>
        <p className="min-w-0 text-[13.5px] text-muted-foreground text-pretty">
          <span className="font-medium text-foreground">{over ? "Drop to upload" : "Drag clips here"}</span>
          <span className="hidden sm:inline"> to add them to your library. Uploads continue if you leave this page, and resume if the connection drops.</span>
        </p>
      </div>
      <UploadButton variant="secondary" />
    </section>
  );
}
