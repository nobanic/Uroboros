"use client";

import { createElement, type ComponentProps, type ElementType } from "react";
import { cn } from "@/lib/utils";

// Authored parts retain the primitive's complete public props, including ref,
// controlled values and event handlers. Caller slots and utility classes still
// override defaults; state and accessibility remain owned by Radix/cmdk.
export function surfacePart<T extends ElementType>(primitive: T, slot: string, appearance?: string) {
  function SurfacePart(properties: ComponentProps<T>) {
    const attributes = properties as ComponentProps<T> & { className?: string };
    const resolved = appearance ? { ...attributes, className: cn(appearance, attributes.className) } : attributes;
    return createElement(primitive, { "data-slot": slot, ...resolved });
  }
  SurfacePart.displayName = slot;
  return SurfacePart;
}
