import { cn } from "@/lib/utils"
import { Loader2Icon } from "lucide-react"
import { uiPrimitive } from "@/astra/ui-primitive"

const Spinner = uiPrimitive<React.ComponentProps<"svg">>(
  "Spinner",
  Loader2Icon,
  {
  "data-slot": "spinner",
  "role": "status",
  "aria-label": "Loading"
},
  [
  "size-4 animate-spin"
],
)

export { Spinner }
