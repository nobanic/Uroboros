"use client";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import type { ChatAttachment } from "@/lib/types";
import type { SideQuestions } from "@/hooks/use-side-questions";
import type { SessionPages } from "./session-journal";
import { sessionBusy, type SessionEntry, workerMessageLength, workerMessageLimit } from "./session-document";

type Arguments = {
  taskId: string;
  entry: SessionEntry;
  pages: SessionPages;
  running: boolean | null;
  side: SideQuestions;
  patch: (id: string, state: string) => void;
  invalidateStatus: () => void;
  updateStatus: (value: { running: boolean }) => void;
};
function requestId() {
  return (
    globalThis.crypto?.randomUUID?.() ||
    `worker-message-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  );
}
export function useSessionComposer(args: Arguments) {
  const [main, writeMain] = useState(""),
    [worker, writeWorker] = useState(""),
    [files, setFiles] = useState<ChatAttachment[]>([]);
  const [sending, setSending] = useState(false),
    [workerSending, setWorkerSending] = useState(false),
    [uploading, setUploading] = useState(false),
    [stopping, setStopping] = useState(false);
  const locks = useRef({ main: false, worker: false, upload: false, stop: false }),
    id = useRef(""),
    scope = useRef(args.entry.key),
    alive = useRef(true);
  scope.current = args.entry.key;
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    writeWorker("");
    id.current = "";
  }, [args.entry.key]);
  const busy = sessionBusy(args.entry, args.pages, sending, args.running);
  const setWorker = (value: string) => {
    writeWorker(value);
    id.current = "";
  };
  async function upload(selected: FileList | File[] | null) {
    if (!selected?.length || locks.current.upload) return;
    locks.current.upload = true;
    setUploading(true);
    try {
      const result = await api.chatUpload("task", args.taskId, Array.from(selected));
      if (alive.current) setFiles((current) => [...current, ...result.attachments]);
    } catch (error) {
      if (alive.current) toast.error(`Upload failed: ${(error as Error).message}`);
    } finally {
      locks.current.upload = false;
      if (alive.current) setUploading(false);
    }
  }
  async function sendMain() {
    const message = main.trim(),
      attachments = files;
    if (args.side.handleCommand(message, () => writeMain(""))) return;
    if ((!message && !attachments.length) || locks.current.main || busy) return;
    locks.current.main = true;
    setSending(true);
    writeMain("");
    setFiles([]);
    args.invalidateStatus();
    try {
      const result = await api.chat(
        message,
        args.taskId,
        attachments.length ? attachments : undefined,
        args.entry.seg ?? 0,
      );
      if (alive.current) args.updateStatus({ running: result.mode === "llm" });
    } catch (error) {
      if (alive.current) {
        writeMain(message);
        setFiles(attachments);
        toast.error(`Send failed: ${(error as Error).message}`);
      }
    } finally {
      locks.current.main = false;
      if (alive.current) setSending(false);
    }
  }
  async function sendWorker() {
    const message = worker.trim(),
      entry = args.entry;
    if (args.side.handleCommand(message, () => setWorker(""))) return;
    if (!entry.intent_id || entry.inherited || entry.status !== "paused" || !message || locks.current.worker) return;
    if (workerMessageLength(message) > workerMessageLimit) {
      toast.error(`Message cannot exceed ${workerMessageLimit} characters`);
      return;
    }
    const key = entry.key,
      token = id.current || requestId();
    id.current = token;
    locks.current.worker = true;
    setWorkerSending(true);
    try {
      const result = await api.sendWorkerMessage(args.taskId, entry.intent_id, message, token);
      if (!alive.current) return;
      args.patch(entry.intent_id, result.state);
      if (scope.current === key) {
        writeWorker("");
        id.current = "";
      }
      toast.success(`Message sent to Worker #${entry.intent_id}; it resumed immediately`);
    } catch (error) {
      if (alive.current) toast.error(`Send failed: ${(error as Error).message}`);
    } finally {
      locks.current.worker = false;
      if (alive.current) setWorkerSending(false);
    }
  }
  async function stop() {
    if (locks.current.stop) return;
    locks.current.stop = true;
    setStopping(true);
    try {
      await api.stopChat(args.taskId);
    } catch (error) {
      if (alive.current) toast.error(`Stop failed: ${(error as Error).message}`);
    } finally {
      locks.current.stop = false;
      if (alive.current) setStopping(false);
    }
  }
  return {
    main,
    setMain: writeMain,
    worker,
    setWorker,
    files,
    removeFile: (path: string) => setFiles((current) => current.filter((file) => file.path !== path)),
    sending,
    workerSending,
    uploading,
    stopping,
    busy,
    sendMain,
    sendWorker,
    upload,
    stop,
  };
}
