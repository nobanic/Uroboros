"use client"

import * as React from "react"
import { Label as LabelPrimitive } from "radix-ui"

import { cn } from "@/lib/utils"
import { uiPrimitive } from "@/astra/ui-primitive"

const Label = uiPrimitive<React.ComponentProps<typeof LabelPrimitive.Root>>(
  "Label",
  LabelPrimitive.Root,
  {
  "data-slot": "label"
},
  [
  "flex items-center gap-2 text-sm leading-none font-medium select-none group-data-[disabled=true]:pointer-events-none group-data-[disabled=true]:opacity-50 peer-disabled:cursor-not-allowed peer-disabled:opacity-50"
],
)

export { Label }
