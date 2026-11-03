"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { uiPrimitive } from "@/astra/ui-primitive"

function Table({ className, ...props }: React.ComponentProps<"table">) {
  return (
    <div
      data-slot="table-container"
      className="relative w-full overflow-x-auto"
    >
      <table
        data-slot="table"
        className={cn("w-full caption-bottom text-sm", className)}
        {...props}
      />
    </div>
  )
}

const TableHeader = uiPrimitive<React.ComponentProps<"thead">>(
  "TableHeader",
  "thead",
  {
  "data-slot": "table-header"
},
  [
  "[&_tr]:border-b"
],
)

const TableBody = uiPrimitive<React.ComponentProps<"tbody">>(
  "TableBody",
  "tbody",
  {
  "data-slot": "table-body"
},
  [
  "[&_tr:last-child]:border-0"
],
)

const TableFooter = uiPrimitive<React.ComponentProps<"tfoot">>(
  "TableFooter",
  "tfoot",
  {
  "data-slot": "table-footer"
},
  [
  "border-t bg-muted/50 font-medium [&>tr]:last:border-b-0"
],
)

const TableRow = uiPrimitive<React.ComponentProps<"tr">>(
  "TableRow",
  "tr",
  {
  "data-slot": "table-row"
},
  [
  "border-b transition-colors hover:bg-muted/50 has-aria-expanded:bg-muted/50 data-[state=selected]:bg-muted"
],
)

const TableHead = uiPrimitive<React.ComponentProps<"th">>(
  "TableHead",
  "th",
  {
  "data-slot": "table-head"
},
  [
  "h-10 px-2 text-left align-middle font-medium whitespace-nowrap text-foreground [&:has([role=checkbox])]:pr-0"
],
)

const TableCell = uiPrimitive<React.ComponentProps<"td">>(
  "TableCell",
  "td",
  {
  "data-slot": "table-cell"
},
  [
  "p-2 align-middle whitespace-nowrap [&:has([role=checkbox])]:pr-0"
],
)

const TableCaption = uiPrimitive<React.ComponentProps<"caption">>(
  "TableCaption",
  "caption",
  {
  "data-slot": "table-caption"
},
  [
  "mt-4 text-sm text-muted-foreground"
],
)

export {
  Table,
  TableHeader,
  TableBody,
  TableFooter,
  TableHead,
  TableRow,
  TableCell,
  TableCaption,
}
