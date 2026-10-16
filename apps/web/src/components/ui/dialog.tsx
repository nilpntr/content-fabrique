import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { XIcon } from "lucide-react";
import type * as React from "react";
import { cn } from "@/lib/utils";

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;

export function DialogContent({
  className,
  children,
  hideClose = false,
  ...props
}: Omit<DialogPrimitive.Popup.Props, "className"> & { className?: string; hideClose?: boolean }) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Backdrop className="fixed inset-0 z-50 bg-[oklch(0.25_0.01_80/0.18)] backdrop-blur-[2px] transition-opacity duration-(--dur-base) data-ending-style:opacity-0 data-starting-style:opacity-0 dark:bg-black/55" />
      <DialogPrimitive.Popup
        className={cn(
          "fixed top-[16vh] left-1/2 z-50 w-[calc(100%-2rem)] max-w-md -translate-x-1/2 rounded-[18px] bg-popover p-6 shadow-pop outline-none",
          "transition-[opacity,scale,translate] duration-(--dur-slow) ease-out data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-ending-style:duration-(--dur-fast) data-starting-style:-translate-y-2 data-starting-style:scale-[0.97] data-starting-style:opacity-0",
          className,
        )}
        {...props}
      >
        {children}
        {!hideClose && (
          <DialogPrimitive.Close
            aria-label="Close"
            className="absolute top-4 right-4 cursor-pointer rounded-[8px] p-1 text-subtle-foreground transition-colors hover:bg-wash hover:text-foreground"
          >
            <XIcon className="size-4" />
          </DialogPrimitive.Close>
        )}
      </DialogPrimitive.Popup>
    </DialogPrimitive.Portal>
  );
}

export function DialogTitle({ className, ...props }: Omit<DialogPrimitive.Title.Props, "className"> & { className?: string }) {
  return <DialogPrimitive.Title className={cn("text-[15px] font-semibold tracking-tight", className)} {...props} />;
}

export function DialogDescription({
  className,
  ...props
}: Omit<DialogPrimitive.Description.Props, "className"> & { className?: string }) {
  return <DialogPrimitive.Description className={cn("mt-1.5 text-[13.5px] text-muted-foreground", className)} {...props} />;
}

export function DialogFooter({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("mt-6 flex items-center justify-end gap-2", className)} {...props} />;
}
