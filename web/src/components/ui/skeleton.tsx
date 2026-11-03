import { cn } from "@/lib/utils"
import { uiPrimitive } from "@/astra/ui-primitive"

const Skeleton = uiPrimitive<React.ComponentProps<"div">>(
  "Skeleton",
  "div",
  {
  "data-slot": "skeleton"
},
  [
  "animate-pulse rounded-md bg-muted"
],
)

export { Skeleton }
