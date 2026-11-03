"use client";

import { ExecutionTranscript, type TranscriptProps } from "@/astra/execution-transcript";

export function Transcript(props: TranscriptProps) {
  return <ExecutionTranscript {...props} />;
}
