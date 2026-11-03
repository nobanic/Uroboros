"use client";

import type { ComponentProps } from "react";
import { Command as Palette } from "cmdk";
import { CheckIcon, SearchIcon } from "lucide-react";
import { InputGroup, InputGroupAddon } from "@/components/ui/input-group";
import { cn } from "@/lib/utils";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./surface-dialogs";
import { surfacePart as part } from "./surface-part";

export const Command = part(Palette, "command", "as-command");
export const CommandList = part(Palette.List, "command-list", "as-command-list");
export const CommandEmpty = part(Palette.Empty, "command-empty", "as-command-empty");
export const CommandGroup = part(Palette.Group, "command-group", "as-command-group");
export const CommandSeparator = part(Palette.Separator, "command-separator", "as-menu-separator");
export const CommandShortcut = part("span", "command-shortcut", "as-menu-shortcut");

export function CommandDialog({
  title = "Command Palette",
  description = "Search for a command to run...",
  showCloseButton = false,
  children,
  className,
  ...properties
}: ComponentProps<typeof Dialog> & {
  title?: string;
  description?: string;
  className?: string;
  showCloseButton?: boolean;
}) {
  return (
    <Dialog {...properties}>
      <DialogContent className={cn("as-command-modal", className)} showCloseButton={showCloseButton}>
        <DialogHeader className="sr-only">
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {children}
      </DialogContent>
    </Dialog>
  );
}
export function CommandInput({ className, ...properties }: ComponentProps<typeof Palette.Input>) {
  return (
    <div data-slot="command-input-wrapper" className="as-command-search">
      <InputGroup className="as-command-input-group">
        <Palette.Input data-slot="command-input" className={cn("as-command-input", className)} {...properties} />
        <InputGroupAddon>
          <SearchIcon />
        </InputGroupAddon>
      </InputGroup>
    </div>
  );
}
export function CommandItem({ children, className, ...properties }: ComponentProps<typeof Palette.Item>) {
  return (
    <Palette.Item data-slot="command-item" className={cn("as-menu-row as-command-item", className)} {...properties}>
      {children}
      <CheckIcon className="as-command-check" />
    </Palette.Item>
  );
}
