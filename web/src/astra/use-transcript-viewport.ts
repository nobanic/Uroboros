"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";

const viewportFor = (node: Element | null) => node?.closest<HTMLElement>('[data-slot="scroll-area-viewport"]');

export function useTranscriptProximity() {
  const element = useRef<HTMLDivElement>(null),
    [visible, setVisible] = useState(false);
  useEffect(() => {
    if (visible || !element.current) return;
    if (!globalThis.IntersectionObserver) {
      setVisible(true);
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) setVisible(true);
      },
      { root: viewportFor(element.current), rootMargin: "400px" },
    );
    observer.observe(element.current);
    return () => observer.disconnect();
  }, [visible]);
  return { element, visible };
}

export function useTranscriptFocusSpace(focusedSeq?: number) {
  const element = useRef<HTMLDivElement>(null),
    [padding, setPadding] = useState(0);
  useLayoutEffect(() => {
    if (focusedSeq == null) {
      setPadding(0);
      return;
    }
    const viewport = viewportFor(element.current);
    if (!viewport) return;
    const measure = () => setPadding(viewport.clientHeight / 2);
    measure();
    if (!globalThis.ResizeObserver) return;
    const observer = new ResizeObserver(measure);
    observer.observe(viewport);
    return () => observer.disconnect();
  }, [focusedSeq]);
  return { element, padding };
}

/** Focus anchoring is temporary and yields immediately to deliberate operator scrolling. */
export function useTranscriptCallFocus(focused: boolean, ready: boolean) {
  const element = useRef<HTMLElement>(null),
    anchored = useRef(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: A newly selected focus restarts its short anchoring lifetime.
  useEffect(() => {
    anchored.current = false;
  }, [focused]);
  useEffect(() => {
    const node = element.current,
      viewport = viewportFor(node);
    if (!focused || !ready || anchored.current || !node || !viewport) return;
    anchored.current = true;
    const center = () => {
      const item = node.getBoundingClientRect(),
        frame = viewport.getBoundingClientRect();
      viewport.scrollTop += item.top + item.height / 2 - frame.top - frame.height / 2;
    };
    const observer = globalThis.ResizeObserver ? new ResizeObserver(center) : null;
    observer?.observe(node.parentElement ?? node);
    observer?.observe(viewport);
    const release = () => observer?.disconnect();
    const gestures = ["wheel", "touchstart", "pointerdown"];
    for (const gesture of gestures) viewport.addEventListener(gesture, release, { once: true, passive: true });
    const frame = requestAnimationFrame(center),
      timer = setTimeout(release, 2000);
    return () => {
      cancelAnimationFrame(frame);
      clearTimeout(timer);
      release();
      for (const gesture of gestures) viewport.removeEventListener(gesture, release);
    };
  }, [focused, ready]);
  return element;
}
