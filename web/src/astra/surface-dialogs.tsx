"use client";

import type { ComponentProps } from "react";
import { AlertDialog as Confirm, Dialog as Modal } from "radix-ui";
import { XIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn, radixOverlayWasOpenAtPointerDown } from "@/lib/utils";
import { surfacePart as part } from "./surface-part";

export const Dialog = part(Modal.Root, "dialog");
export const DialogTrigger = part(Modal.Trigger, "dialog-trigger");
export const DialogPortal = part(Modal.Portal, "dialog-portal");
export const DialogClose = part(Modal.Close, "dialog-close");
export const DialogOverlay = part(Modal.Overlay, "dialog-overlay", "as-backdrop");
export const DialogHeader = part("div", "dialog-header", "as-modal-heading");
export const DialogTitle = part(Modal.Title, "dialog-title", "as-surface-title");
export const DialogDescription = part(Modal.Description, "dialog-description", "as-surface-description");

function CornerClose({ sheet = false }: { sheet?: boolean }) {
  return (
    <Modal.Close data-slot={sheet ? "sheet-close" : "dialog-close"} asChild>
      <Button variant="ghost" size="icon-sm" className="as-surface-close">
        <XIcon />
        <span className="sr-only">Close</span>
      </Button>
    </Modal.Close>
  );
}

export function DialogContent({
  children,
  className,
  showCloseButton = true,
  onInteractOutside,
  ...properties
}: ComponentProps<typeof Modal.Content> & { showCloseButton?: boolean }) {
  return (
    <DialogPortal>
      <DialogOverlay />
      <Modal.Content
        data-slot="dialog-content"
        className={cn("as-surface as-modal", className)}
        onInteractOutside={(event) => {
          const target = event.detail.originalEvent.target as Element | null;
          const backdrop = Boolean(target?.closest?.("[data-slot='dialog-overlay']"));
          if (!backdrop || radixOverlayWasOpenAtPointerDown()) event.preventDefault();
          onInteractOutside?.(event);
        }}
        {...properties}
      >
        {children}
        {showCloseButton && <CornerClose />}
      </Modal.Content>
    </DialogPortal>
  );
}

export function DialogFooter({
  children,
  className,
  showCloseButton = false,
  ...properties
}: ComponentProps<"div"> & { showCloseButton?: boolean }) {
  return (
    <div data-slot="dialog-footer" className={cn("as-surface-footer", className)} {...properties}>
      {children}
      {showCloseButton && (
        <DialogClose asChild>
          <Button variant="outline">Close</Button>
        </DialogClose>
      )}
    </div>
  );
}

export const Sheet = part(Modal.Root, "sheet");
export const SheetTrigger = part(Modal.Trigger, "sheet-trigger");
export const SheetClose = part(Modal.Close, "sheet-close");
export const SheetHeader = part("div", "sheet-header", "as-sheet-heading");
export const SheetFooter = part("div", "sheet-footer", "as-sheet-footer");
export const SheetTitle = part(Modal.Title, "sheet-title", "as-surface-title");
export const SheetDescription = part(Modal.Description, "sheet-description", "as-surface-description");
const SheetPortal = part(Modal.Portal, "sheet-portal");
const SheetOverlay = part(Modal.Overlay, "sheet-overlay", "as-backdrop");

export function SheetContent({
  children,
  className,
  side = "right",
  showCloseButton = true,
  ...properties
}: ComponentProps<typeof Modal.Content> & { side?: "top" | "right" | "bottom" | "left"; showCloseButton?: boolean }) {
  return (
    <SheetPortal>
      <SheetOverlay />
      <Modal.Content
        data-slot="sheet-content"
        data-side={side}
        className={cn("as-surface as-sheet", className)}
        {...properties}
      >
        {children}
        {showCloseButton && <CornerClose sheet />}
      </Modal.Content>
    </SheetPortal>
  );
}

export const AlertDialog = part(Confirm.Root, "alert-dialog");
export const AlertDialogTrigger = part(Confirm.Trigger, "alert-dialog-trigger");
export const AlertDialogPortal = part(Confirm.Portal, "alert-dialog-portal");
export const AlertDialogOverlay = part(Confirm.Overlay, "alert-dialog-overlay", "as-backdrop");
export const AlertDialogHeader = part("div", "alert-dialog-header", "as-confirm-heading");
export const AlertDialogFooter = part("div", "alert-dialog-footer", "as-surface-footer as-confirm-footer");
export const AlertDialogMedia = part("div", "alert-dialog-media", "as-confirm-media");
export const AlertDialogTitle = part(Confirm.Title, "alert-dialog-title", "as-surface-title");
export const AlertDialogDescription = part(Confirm.Description, "alert-dialog-description", "as-surface-description");

export function AlertDialogContent({
  size = "default",
  className,
  ...properties
}: ComponentProps<typeof Confirm.Content> & { size?: "default" | "sm" }) {
  return (
    <AlertDialogPortal>
      <AlertDialogOverlay />
      <Confirm.Content
        data-slot="alert-dialog-content"
        data-size={size}
        className={cn("as-surface as-modal as-confirm", className)}
        {...properties}
      />
    </AlertDialogPortal>
  );
}

type ActionAppearance = Pick<ComponentProps<typeof Button>, "variant" | "size">;
export function AlertDialogAction({
  variant = "default",
  size = "default",
  className,
  ...properties
}: ComponentProps<typeof Confirm.Action> & ActionAppearance) {
  return (
    <Button asChild variant={variant} size={size}>
      <Confirm.Action data-slot="alert-dialog-action" className={cn(className)} {...properties} />
    </Button>
  );
}
export function AlertDialogCancel({
  variant = "outline",
  size = "default",
  className,
  ...properties
}: ComponentProps<typeof Confirm.Cancel> & ActionAppearance) {
  return (
    <Button asChild variant={variant} size={size}>
      <Confirm.Cancel data-slot="alert-dialog-cancel" className={cn(className)} {...properties} />
    </Button>
  );
}
