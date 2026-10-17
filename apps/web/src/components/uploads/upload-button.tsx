import { Upload } from "lucide-react";
import * as React from "react";
import { Button, type ButtonProps } from "@/components/ui/button";
import { ACCEPT, pickWithHandles } from "@/lib/upload/files";
import { useUploads } from "@/lib/upload/provider";

/**
 * Opens the file picker and adds the chosen files to the upload queue. Where the browser has the File System Access API
 * the picker gives file handles, which let an interrupted upload continue after a reload without picking the files again;
 * elsewhere a plain file input does the job.
 */
export function UploadButton({ children = "Upload clips", ...props }: Omit<ButtonProps, "onClick" | "type">) {
  const { addFiles, available } = useUploads();
  const input = React.useRef<HTMLInputElement>(null);

  async function open() {
    const picked = await pickWithHandles();
    if (picked === null) return input.current?.click();
    if (picked.length > 0) await addFiles(picked);
  }

  return (
    <>
      <Button type="button" disabled={!available} onClick={() => void open()} {...props}>
        <Upload aria-hidden="true" />
        {children}
      </Button>
      <input
        ref={input}
        type="file"
        multiple
        accept={ACCEPT}
        tabIndex={-1}
        aria-hidden="true"
        data-testid="upload-input"
        className="hidden"
        onChange={(event) => {
          const files = [...(event.target.files ?? [])];
          event.target.value = ""; // the same file can be picked again
          if (files.length > 0) void addFiles(files);
        }}
      />
    </>
  );
}
