"use client";

import { useEffect, useRef, useState } from "react";
import { type DetailPart, type DetailPort, readTranscriptParts } from "./transcript-document";

/** Each mounted entry owns its cache. A changed fragment set invalidates the whole document. */
export function useTranscriptDetails(parts: DetailPart[], read: DetailPort, enabled: boolean) {
  const identity = JSON.stringify(parts);
  const [attempt, setAttempt] = useState(0);
  const [document, setDocument] = useState<{ identity: string; text: string; failed: boolean } | null>(null);
  const [loading, setLoading] = useState(false);
  const cached = useRef<{ identity: string; read: DetailPort; attempt: number } | null>(null);
  useEffect(() => {
    if (
      !enabled ||
      (cached.current?.identity === identity && cached.current.read === read && cached.current.attempt === attempt)
    )
      return;
    let accepted = true;
    setLoading(true);
    const requested: DetailPart[] = JSON.parse(identity);
    void readTranscriptParts(requested, read).then((result) => {
      if (accepted) {
        cached.current = { identity, read, attempt };
        setDocument({ ...result, identity });
        setLoading(false);
      }
    });
    return () => {
      accepted = false;
    };
  }, [identity, read, enabled, attempt]);
  return {
    text: document?.identity === identity ? document.text : null,
    failed: document?.identity === identity && document.failed,
    loading,
    retry: () => setAttempt((value) => value + 1),
  };
}
