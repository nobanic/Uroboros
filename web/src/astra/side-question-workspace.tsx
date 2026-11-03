"use client";

import type { ReactNode } from "react";

import { MessageCircleQuestion } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Drawer, DrawerContent, DrawerDescription, DrawerHeader, DrawerTitle } from "@/components/ui/drawer";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { useIsMobile } from "@/hooks/use-mobile";
import type { SideQuestions } from "@/hooks/use-side-questions";

import { type SideComposerLayout, SideQuestionPanel } from "./side-question-panel";
import "./side-question-workspace.css";

export function SideQuestionButton({ side }: { side: SideQuestions }) {
  return side.enabled ? (
    <Button variant="outline" size="sm" onClick={() => side.setOpen(true)} title="/btw side question">
      <MessageCircleQuestion data-icon="inline-start" />
      Side question
    </Button>
  ) : null;
}

export function SideQuestionWorkspace({
  side,
  label,
  children,
  composerLayout = "stacked",
}: {
  side: SideQuestions;
  label: string;
  children: ReactNode;
  composerLayout?: SideComposerLayout;
}) {
  const mobile = useIsMobile();
  return (
    <>
      <ResizablePanelGroup orientation="horizontal" className="min-h-0 min-w-0 flex-1">
        <ResizablePanel id="main-conversation" minSize="35%" className="flex min-h-0 min-w-0 flex-col">
          {children}
        </ResizablePanel>
        {side.open && side.enabled && !mobile && (
          <>
            <ResizableHandle withHandle />
            <ResizablePanel id="side-question" defaultSize="38%" minSize="280px" maxSize="65%">
              <SideQuestionPanel side={side} label={label} composerLayout={composerLayout} />
            </ResizablePanel>
          </>
        )}
      </ResizablePanelGroup>
      <Drawer open={mobile && side.open && side.enabled} onOpenChange={side.setOpen}>
        <DrawerContent className="h-[85svh]">
          <DrawerHeader className="sr-only">
            <DrawerTitle>Side questions</DrawerTitle>
            <DrawerDescription>Standalone Q&amp;A for {label}</DrawerDescription>
          </DrawerHeader>
          <SideQuestionPanel side={side} label={label} composerLayout={composerLayout} />
        </DrawerContent>
      </Drawer>
    </>
  );
}
