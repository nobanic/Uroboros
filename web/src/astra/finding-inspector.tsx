"use client";

import type { MouseEventHandler, ReactNode } from "react";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { statusMeta } from "@/lib/status";
import "./finding-inspector.css";

type SectionProps = {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  after?: ReactNode;
  className?: string;
  headerClassName?: string;
  contentClassName?: string;
};

export function FindingSection({
  title,
  description,
  actions,
  children,
  after,
  className = "",
  headerClassName = "",
  contentClassName = "",
}: SectionProps) {
  return (
    <section className={`finding-section ${className}`}>
      {title != null && (
        <header className={`finding-section-head ${headerClassName}`}>
          <div className="finding-section-intro">
            <h2 className="finding-section-title">
              {title}
            </h2>
            {description != null && (
              <p className="finding-section-description">
                {description}
              </p>
            )}
          </div>
          {actions != null && <div className="finding-section-actions">{actions}</div>}
        </header>
      )}
      <div className={`finding-section-body ${contentClassName}`}>
        {children}
      </div>
      {after}
    </section>
  );
}

export function FindingDialogHeading({
  title,
  description,
  className = "",
  descriptionClassName,
}: {
  title: ReactNode;
  description?: ReactNode;
  className?: string;
  descriptionClassName?: string;
}) {
  return (
    <div data-slot="dialog-header" className={`finding-dialog-heading ${className}`}>
      <span className="finding-dialog-mark" aria-hidden="true" />
      <DialogTitle>{title}</DialogTitle>
      {description != null && <DialogDescription className={descriptionClassName}>{description}</DialogDescription>}
    </div>
  );
}

export function FindingProperty({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="finding-property" role="group" aria-label={label}>
      <span>{label}</span>
      <div>{children}</div>
    </div>
  );
}

export function FindingDisposition({
  domain,
  value,
  options,
  onValueChange,
  triggerClassName,
}: {
  domain: "finding" | "severity";
  value: string;
  options: readonly string[];
  onValueChange: (value: string) => void;
  triggerClassName?: string;
}) {
  return (
    <Select value={value} onValueChange={onValueChange}>
      <SelectTrigger size="sm" className={triggerClassName}>
        <StatusBadge domain={domain} value={value} dot />
      </SelectTrigger>
      <SelectContent position="popper" align="end">
        <SelectGroup>
          {options.map((option) => (
            <SelectItem key={option} value={option}>
              {statusMeta(domain, option).label}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  );
}

export function FindingEmpty({ title, description }: { title: ReactNode; description?: ReactNode }) {
  return (
    <div className="finding-empty">
      <span aria-hidden="true" className="finding-empty-mark" />
      <h3>{title}</h3>
      {description != null && <p>{description}</p>}
    </div>
  );
}

export function FindingRow({ expanded, children }: { expanded: boolean; children: ReactNode }) {
  return (
    <article className="finding-inspector-row" data-expanded={expanded}>
      {children}
    </article>
  );
}

export function FindingSectionFooter({ children, className = "" }: { children: ReactNode; className?: string }) {
  return (
    <footer className={`finding-section-footer ${className}`}>
      {children}
    </footer>
  );
}

export function FindingPacket({
  lineCount,
  tools,
  children,
}: {
  lineCount: number;
  tools: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="finding-packet">
      <div className="finding-packet-bar">
        <span>
          HTTP message <small>{lineCount} lines</small>
        </span>
        <div>{tools}</div>
      </div>
      {children}
    </div>
  );
}

export function FindingPacketAction({
  label,
  hint,
  pressed,
  onClick,
  children,
}: {
  label: string;
  hint: ReactNode;
  pressed?: boolean;
  onClick: MouseEventHandler<HTMLButtonElement>;
  children: ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={label}
          aria-pressed={pressed}
          onClick={onClick}
        >
          {children}
        </Button>
      </TooltipTrigger>
      <TooltipContent side="bottom">{hint}</TooltipContent>
    </Tooltip>
  );
}
