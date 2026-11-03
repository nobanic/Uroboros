"use client"

import * as React from "react"
import { Avatar as AvatarPrimitive } from "radix-ui"

import { cn } from "@/lib/utils"
import { uiPrimitive } from "@/astra/ui-primitive"

function Avatar({
  className,
  size = "default",
  ...props
}: React.ComponentProps<typeof AvatarPrimitive.Root> & {
  size?: "default" | "sm" | "lg"
}) {
  return (
    <AvatarPrimitive.Root
      data-slot="avatar"
      data-size={size}
      className={cn(
        "group/avatar relative flex size-8 shrink-0 rounded-full select-none after:absolute after:inset-0 after:rounded-full after:border after:border-border after:mix-blend-darken data-[size=lg]:size-10 data-[size=sm]:size-6 dark:after:mix-blend-lighten",
        className
      )}
      {...props}
    />
  )
}

const AvatarImage = uiPrimitive<React.ComponentProps<typeof AvatarPrimitive.Image>>(
  "AvatarImage",
  AvatarPrimitive.Image,
  {
  "data-slot": "avatar-image"
},
  [
  "aspect-square size-full rounded-full object-cover"
],
)

const AvatarFallback = uiPrimitive<React.ComponentProps<typeof AvatarPrimitive.Fallback>>(
  "AvatarFallback",
  AvatarPrimitive.Fallback,
  {
  "data-slot": "avatar-fallback"
},
  [
  "flex size-full items-center justify-center rounded-full bg-muted text-sm text-muted-foreground group-data-[size=sm]/avatar:text-xs"
],
)

const AvatarBadge = uiPrimitive<React.ComponentProps<"span">>(
  "AvatarBadge",
  "span",
  {
  "data-slot": "avatar-badge"
},
  [
  "absolute right-0 bottom-0 z-10 inline-flex items-center justify-center rounded-full bg-primary text-primary-foreground bg-blend-color ring-2 ring-background select-none",
  "group-data-[size=sm]/avatar:size-2 group-data-[size=sm]/avatar:[&>svg]:hidden",
  "group-data-[size=default]/avatar:size-2.5 group-data-[size=default]/avatar:[&>svg]:size-2",
  "group-data-[size=lg]/avatar:size-3 group-data-[size=lg]/avatar:[&>svg]:size-2"
],
)

const AvatarGroup = uiPrimitive<React.ComponentProps<"div">>(
  "AvatarGroup",
  "div",
  {
  "data-slot": "avatar-group"
},
  [
  "group/avatar-group flex -space-x-2 *:data-[slot=avatar]:ring-2 *:data-[slot=avatar]:ring-background"
],
)

const AvatarGroupCount = uiPrimitive<React.ComponentProps<"div">>(
  "AvatarGroupCount",
  "div",
  {
  "data-slot": "avatar-group-count"
},
  [
  "relative flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-sm text-muted-foreground ring-2 ring-background group-has-data-[size=lg]/avatar-group:size-10 group-has-data-[size=sm]/avatar-group:size-6 [&>svg]:size-4 group-has-data-[size=lg]/avatar-group:[&>svg]:size-5 group-has-data-[size=sm]/avatar-group:[&>svg]:size-3"
],
)

export {
  Avatar,
  AvatarImage,
  AvatarFallback,
  AvatarGroup,
  AvatarGroupCount,
  AvatarBadge,
}
