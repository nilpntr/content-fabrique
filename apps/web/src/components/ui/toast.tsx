import { Toast } from "@base-ui/react/toast";
import { CircleAlert, CircleCheck, XIcon } from "lucide-react";
import type * as React from "react";

export const toastManager = Toast.createToastManager();

export function notify(title: string, options: { description?: string; type?: "success" | "error" } = {}) {
  toastManager.add({ title, description: options.description, type: options.type ?? "success" });
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  return (
    <Toast.Provider toastManager={toastManager} timeout={4500} limit={3}>
      {children}
      <Toast.Portal>
        <Toast.Viewport className="fixed right-4 bottom-4 z-[60] mx-auto flex w-[min(360px,calc(100vw-2rem))] sm:right-6 sm:bottom-6">
          <ToastList />
        </Toast.Viewport>
      </Toast.Portal>
    </Toast.Provider>
  );
}

function ToastList() {
  const { toasts } = Toast.useToastManager();
  return toasts.map((toast) => (
    <Toast.Root key={toast.id} toast={toast} className="toast-root">
      <Toast.Content className="toast-content flex items-start gap-3">
        {toast.type === "error" ? (
          <CircleAlert className="mt-0.5 size-4 shrink-0 text-critical" aria-hidden />
        ) : (
          <CircleCheck className="mt-0.5 size-4 shrink-0 text-good" aria-hidden />
        )}
        <div className="min-w-0 flex-1 pr-5">
          <Toast.Title className="text-[13.5px] leading-5 font-medium" />
          <Toast.Description className="mt-0.5 text-[13px] leading-5 text-muted-foreground" />
        </div>
        <Toast.Close
          aria-label="Dismiss"
          className="absolute top-2.5 right-2.5 flex size-6 cursor-pointer items-center justify-center rounded-[7px] text-subtle-foreground transition-colors hover:bg-wash hover:text-foreground"
        >
          <XIcon className="size-3.5" />
        </Toast.Close>
      </Toast.Content>
    </Toast.Root>
  ));
}
