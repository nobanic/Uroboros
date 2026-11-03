import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"
import { uiPrimitive } from "@/astra/ui-primitive"

const Empty = uiPrimitive<React.ComponentProps<"div">>(
  "Empty",
  "div",
  {
  "data-slot": "empty"
},
  [
  "flex w-full min-w-0 flex-1 flex-col items-center justify-center gap-4 rounded-xl border-dashed p-6 text-center text-balance"
],
)

const EmptyHeader = uiPrimitive<React.ComponentProps<"div">>(
  "EmptyHeader",
  "div",
  {
  "data-slot": "empty-header"
},
  [
  "flex max-w-sm flex-col items-center gap-2"
],
)

const emptyMediaVariants = cva(
  "mb-2 flex shrink-0 items-center justify-center [&_svg]:pointer-events-none [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-transparent",
        icon: "flex size-8 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground [&_svg:not([class*='size-'])]:size-4",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

function EmptyMedia({
  className,
  variant = "default",
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof emptyMediaVariants>) {
  return (
    <div
      data-slot="empty-icon"
      data-variant={variant}
      className={cn(emptyMediaVariants({ variant, className }))}
      {...props}
    />
  )
}

const EmptyTitle = uiPrimitive<React.ComponentProps<"div">>(
  "EmptyTitle",
  "div",
  {
  "data-slot": "empty-title"
},
  [
  "font-heading text-sm font-medium tracking-tight"
],
)

const EmptyDescription = uiPrimitive<React.ComponentProps<"p">>(
  "EmptyDescription",
  "div",
  {
  "data-slot": "empty-description"
},
  [
  "text-sm/relaxed text-muted-foreground [&>a]:underline [&>a]:underline-offset-4 [&>a:hover]:text-primary"
],
)

const EmptyContent = uiPrimitive<React.ComponentProps<"div">>(
  "EmptyContent",
  "div",
  {
  "data-slot": "empty-content"
},
  [
  "flex w-full max-w-sm min-w-0 flex-col items-center gap-2.5 text-sm text-balance"
],
)

export {
  Empty,
  EmptyHeader,
  EmptyTitle,
  EmptyDescription,
  EmptyContent,
  EmptyMedia,
}
