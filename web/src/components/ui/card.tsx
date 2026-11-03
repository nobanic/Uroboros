import * as React from "react"

import { cn } from "@/lib/utils"
import { uiPrimitive } from "@/astra/ui-primitive"

function Card({
  className,
  size = "default",
  ...props
}: React.ComponentProps<"div"> & { size?: "default" | "sm" }) {
  return (
    <div
      data-slot="card"
      data-size={size}
      className={cn(
        "group/card flex flex-col gap-(--card-spacing) overflow-hidden rounded-xl bg-card py-(--card-spacing) text-sm text-card-foreground ring-1 ring-foreground/10 [--card-spacing:--spacing(4)] has-data-[slot=card-footer]:pb-0 has-[>img:first-child]:pt-0 data-[size=sm]:[--card-spacing:--spacing(3)] data-[size=sm]:has-data-[slot=card-footer]:pb-0 *:[img:first-child]:rounded-t-xl *:[img:last-child]:rounded-b-xl",
        className
      )}
      {...props}
    />
  )
}

const CardHeader = uiPrimitive<React.ComponentProps<"div">>(
  "CardHeader",
  "div",
  {
  "data-slot": "card-header"
},
  [
  "group/card-header @container/card-header grid auto-rows-min items-start gap-1 rounded-t-xl px-(--card-spacing) has-data-[slot=card-action]:grid-cols-[1fr_auto] has-data-[slot=card-description]:grid-rows-[auto_auto] [.border-b]:pb-(--card-spacing)"
],
)

const CardTitle = uiPrimitive<React.ComponentProps<"div">>(
  "CardTitle",
  "div",
  {
  "data-slot": "card-title"
},
  [
  "font-heading text-base leading-snug font-medium group-data-[size=sm]/card:text-sm"
],
)

const CardDescription = uiPrimitive<React.ComponentProps<"div">>(
  "CardDescription",
  "div",
  {
  "data-slot": "card-description"
},
  [
  "text-sm text-muted-foreground"
],
)

const CardAction = uiPrimitive<React.ComponentProps<"div">>(
  "CardAction",
  "div",
  {
  "data-slot": "card-action"
},
  [
  "col-start-2 row-span-2 row-start-1 self-start justify-self-end"
],
)

const CardContent = uiPrimitive<React.ComponentProps<"div">>(
  "CardContent",
  "div",
  {
  "data-slot": "card-content"
},
  [
  "px-(--card-spacing)"
],
)

const CardFooter = uiPrimitive<React.ComponentProps<"div">>(
  "CardFooter",
  "div",
  {
  "data-slot": "card-footer"
},
  [
  "flex items-center rounded-b-xl border-t bg-muted/50 p-(--card-spacing)"
],
)

export {
  Card,
  CardHeader,
  CardFooter,
  CardTitle,
  CardAction,
  CardDescription,
  CardContent,
}
