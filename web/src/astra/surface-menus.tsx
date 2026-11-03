"use client";

import type { ComponentProps } from "react";
import { DropdownMenu as Menu, Popover as Pop, Select as Choice, Tooltip as Hint } from "radix-ui";
import { CheckIcon, ChevronDownIcon, ChevronRightIcon, ChevronUpIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { surfacePart as part } from "./surface-part";

export const Popover = part(Pop.Root, "popover");
export const PopoverTrigger = part(Pop.Trigger, "popover-trigger");
export const PopoverAnchor = part(Pop.Anchor, "popover-anchor");
export const PopoverHeader = part("div", "popover-header", "as-popover-heading");
export const PopoverTitle = part("div", "popover-title", "as-popover-title");
export const PopoverDescription = part("p", "popover-description", "as-surface-description");
export function PopoverContent({
  align = "center",
  sideOffset = 4,
  className,
  ...properties
}: ComponentProps<typeof Pop.Content>) {
  return (
    <Pop.Portal>
      <Pop.Content
        data-slot="popover-content"
        align={align}
        sideOffset={sideOffset}
        className={cn("as-surface as-popup as-popover", className)}
        {...properties}
      />
    </Pop.Portal>
  );
}

export const Tooltip = part(Hint.Root, "tooltip");
export const TooltipTrigger = part(Hint.Trigger, "tooltip-trigger");
export function TooltipProvider({ delayDuration = 0, ...properties }: ComponentProps<typeof Hint.Provider>) {
  return <Hint.Provider data-slot="tooltip-provider" delayDuration={delayDuration} {...properties} />;
}
export function TooltipContent({
  children,
  sideOffset = 0,
  className,
  ...properties
}: ComponentProps<typeof Hint.Content>) {
  return (
    <Hint.Portal>
      <Hint.Content
        data-slot="tooltip-content"
        sideOffset={sideOffset}
        className={cn("as-surface as-popup as-tooltip", className)}
        {...properties}
      >
        {children}
        <Hint.Arrow className="as-tooltip-arrow" />
      </Hint.Content>
    </Hint.Portal>
  );
}

export const Select = part(Choice.Root, "select");
export const SelectValue = part(Choice.Value, "select-value");
export const SelectGroup = part(Choice.Group, "select-group", "as-choice-group");
export const SelectLabel = part(Choice.Label, "select-label", "as-menu-label");
export const SelectSeparator = part(Choice.Separator, "select-separator", "as-menu-separator");
export function SelectTrigger({
  size = "default",
  className,
  children,
  ...properties
}: ComponentProps<typeof Choice.Trigger> & { size?: "sm" | "default" }) {
  return (
    <Choice.Trigger
      data-slot="select-trigger"
      data-size={size}
      className={cn("as-choice-trigger", className)}
      {...properties}
    >
      {children}
      <Choice.Icon asChild>
        <ChevronDownIcon className="as-choice-chevron" />
      </Choice.Icon>
    </Choice.Trigger>
  );
}
export function SelectContent({
  position = "item-aligned",
  align = "center",
  className,
  children,
  ...properties
}: ComponentProps<typeof Choice.Content>) {
  return (
    <Choice.Portal>
      <Choice.Content
        data-slot="select-content"
        data-align-trigger={position === "item-aligned"}
        position={position}
        align={align}
        className={cn("as-surface as-popup as-choice-content", className)}
        {...properties}
      >
        <SelectScrollUpButton />
        <Choice.Viewport data-position={position} className="as-choice-viewport">
          {children}
        </Choice.Viewport>
        <SelectScrollDownButton />
      </Choice.Content>
    </Choice.Portal>
  );
}
export function SelectItem({ children, className, ...properties }: ComponentProps<typeof Choice.Item>) {
  return (
    <Choice.Item data-slot="select-item" className={cn("as-menu-row as-choice-item", className)} {...properties}>
      <span className="as-menu-indicator">
        <Choice.ItemIndicator>
          <CheckIcon />
        </Choice.ItemIndicator>
      </span>
      <Choice.ItemText>{children}</Choice.ItemText>
    </Choice.Item>
  );
}
export function SelectScrollUpButton({ className, ...properties }: ComponentProps<typeof Choice.ScrollUpButton>) {
  return (
    <Choice.ScrollUpButton
      data-slot="select-scroll-up-button"
      className={cn("as-choice-scroll", className)}
      {...properties}
    >
      <ChevronUpIcon />
    </Choice.ScrollUpButton>
  );
}
export function SelectScrollDownButton({ className, ...properties }: ComponentProps<typeof Choice.ScrollDownButton>) {
  return (
    <Choice.ScrollDownButton
      data-slot="select-scroll-down-button"
      className={cn("as-choice-scroll", className)}
      {...properties}
    >
      <ChevronDownIcon />
    </Choice.ScrollDownButton>
  );
}

export const DropdownMenu = part(Menu.Root, "dropdown-menu");
export const DropdownMenuPortal = part(Menu.Portal, "dropdown-menu-portal");
export const DropdownMenuTrigger = part(Menu.Trigger, "dropdown-menu-trigger");
export const DropdownMenuGroup = part(Menu.Group, "dropdown-menu-group");
export const DropdownMenuRadioGroup = part(Menu.RadioGroup, "dropdown-menu-radio-group");
export const DropdownMenuSub = part(Menu.Sub, "dropdown-menu-sub");
export const DropdownMenuSeparator = part(Menu.Separator, "dropdown-menu-separator", "as-menu-separator");
export const DropdownMenuShortcut = part("span", "dropdown-menu-shortcut", "as-menu-shortcut");
export function DropdownMenuContent({
  align = "start",
  sideOffset = 4,
  className,
  ...properties
}: ComponentProps<typeof Menu.Content>) {
  return (
    <Menu.Portal>
      <Menu.Content
        data-slot="dropdown-menu-content"
        align={align}
        sideOffset={sideOffset}
        className={cn("as-surface as-popup as-menu", className)}
        {...properties}
      />
    </Menu.Portal>
  );
}
export function DropdownMenuItem({
  inset,
  variant = "default",
  className,
  ...properties
}: ComponentProps<typeof Menu.Item> & { inset?: boolean; variant?: "default" | "destructive" }) {
  return (
    <Menu.Item
      data-slot="dropdown-menu-item"
      data-inset={inset}
      data-variant={variant}
      className={cn("as-menu-row", className)}
      {...properties}
    />
  );
}
export function DropdownMenuLabel({
  inset,
  className,
  ...properties
}: ComponentProps<typeof Menu.Label> & { inset?: boolean }) {
  return (
    <Menu.Label
      data-slot="dropdown-menu-label"
      data-inset={inset}
      className={cn("as-menu-label", className)}
      {...properties}
    />
  );
}
export function DropdownMenuCheckboxItem({
  inset,
  checked,
  children,
  className,
  ...properties
}: ComponentProps<typeof Menu.CheckboxItem> & { inset?: boolean }) {
  return (
    <Menu.CheckboxItem
      data-slot="dropdown-menu-checkbox-item"
      data-inset={inset}
      checked={checked}
      className={cn("as-menu-row as-menu-choice", className)}
      {...properties}
    >
      <span className="as-menu-indicator" data-slot="dropdown-menu-checkbox-item-indicator">
        <Menu.ItemIndicator>
          <CheckIcon />
        </Menu.ItemIndicator>
      </span>
      {children}
    </Menu.CheckboxItem>
  );
}
export function DropdownMenuRadioItem({
  inset,
  children,
  className,
  ...properties
}: ComponentProps<typeof Menu.RadioItem> & { inset?: boolean }) {
  return (
    <Menu.RadioItem
      data-slot="dropdown-menu-radio-item"
      data-inset={inset}
      className={cn("as-menu-row as-menu-choice", className)}
      {...properties}
    >
      <span className="as-menu-indicator" data-slot="dropdown-menu-radio-item-indicator">
        <Menu.ItemIndicator>
          <CheckIcon />
        </Menu.ItemIndicator>
      </span>
      {children}
    </Menu.RadioItem>
  );
}
export function DropdownMenuSubTrigger({
  inset,
  children,
  className,
  ...properties
}: ComponentProps<typeof Menu.SubTrigger> & { inset?: boolean }) {
  return (
    <Menu.SubTrigger
      data-slot="dropdown-menu-sub-trigger"
      data-inset={inset}
      className={cn("as-menu-row as-menu-branch", className)}
      {...properties}
    >
      {children}
      <ChevronRightIcon className="as-menu-branch-arrow" />
    </Menu.SubTrigger>
  );
}
export function DropdownMenuSubContent({ className, ...properties }: ComponentProps<typeof Menu.SubContent>) {
  return (
    <Menu.SubContent
      data-slot="dropdown-menu-sub-content"
      className={cn("as-surface as-popup as-menu as-menu-sub", className)}
      {...properties}
    />
  );
}
