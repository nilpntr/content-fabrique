import { Button as ButtonPrimitive } from "@base-ui/react/button";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "btn relative isolate inline-flex shrink-0 cursor-pointer items-center justify-center gap-1.5 rounded-[10px] font-medium whitespace-nowrap select-none disabled:pointer-events-none disabled:opacity-45 data-disabled:pointer-events-none data-disabled:opacity-45 [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-(image:--ink-fill) text-primary-foreground shadow-ink after:bg-white dark:after:bg-black",
        secondary: "bg-wash text-foreground shadow-[inset_0_0_0_1px_var(--border)] after:bg-foreground",
        ghost: "text-muted-foreground after:bg-foreground hover:text-foreground",
        outline: "bg-surface-raised text-foreground shadow-raised after:bg-foreground",
        link: "px-0 text-brand underline-offset-4 after:hidden hover:underline",
      },
      size: {
        sm: "h-7 rounded-[8px] px-2.5 text-[13px]",
        md: "h-8.5 px-3.5 text-[13.5px]",
        lg: "h-10 px-4.5 text-sm",
        icon: "size-8 [--press:0.92]",
        "icon-sm": "size-7 rounded-[8px] [--press:0.9]",
      },
    },
    defaultVariants: { variant: "default", size: "md" },
  },
);

export interface ButtonProps extends Omit<ButtonPrimitive.Props, "className">, VariantProps<typeof buttonVariants> {
  className?: string;
}

/** Set `nativeButton={false}` when `render` produces a non-button element. */
export function Button({ className, variant, size, ...props }: ButtonProps) {
  return <ButtonPrimitive data-slot="button" className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}

export { buttonVariants };
