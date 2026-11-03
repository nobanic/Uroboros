"use client";

import { useEffect, useMemo, useState } from "react";
import { UroIcon } from "@/components/uroboros-icons";
import { packetDocument, packetSegments } from "./packet-document";
import "./http-packet.css";

export function HttpPacket({ raw }: { raw: string }) {
  const [wrap, setWrap] = useState(true),
    [copied, setCopied] = useState(false),
    [notice, notify] = useState("");
  const packet = useMemo(() => packetDocument(raw), [raw]);
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(timer);
  }, [copied]);
  async function copy() {
    const value = raw || "(empty)";
    let success = false;
    try {
      await navigator.clipboard.writeText(value);
      success = true;
    } catch {
      const focused = document.activeElement as HTMLElement | null,
        field = document.createElement("textarea");
      field.value = value;
      field.style.position = "fixed";
      field.style.opacity = "0";
      document.body.appendChild(field);
      try {
        field.select();
        success = document.execCommand("copy");
      } catch {
        success = false;
      } finally {
        field.remove();
        if (focused?.isConnected) focused.focus();
      }
    }
    setCopied(success);
    notify(success ? "" : "Copy failed; use Ctrl/Cmd+A inside the packet, then copy.");
  }
  return (
    <div className="astra-packet" data-wrap={wrap}>
      <div className="astra-packet-tools">
        <span>{packet.lines.length} lines</span>
        <button
          type="button"
          aria-label={wrap ? "Turn off word wrap" : "Turn on word wrap"}
          aria-pressed={wrap}
          onClick={() => setWrap((value) => !value)}
        >
          <UroIcon name="arrange" aria-hidden="true" />
          Wrap
        </button>
        <button type="button" aria-label={copied ? "Packet copied" : "Copy packet"} onClick={() => void copy()}>
          <UroIcon name="records" aria-hidden="true" />
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <div
        className="astra-packet-code"
        role="textbox"
        aria-label="HTTP packet code"
        aria-readonly="true"
        aria-multiline="true"
        tabIndex={0}
        onKeyDown={(event) => {
          if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "a") return;
          event.preventDefault();
          const selection = window.getSelection(),
            range = document.createRange();
          range.selectNodeContents(event.currentTarget);
          selection?.removeAllRanges();
          selection?.addRange(range);
        }}
      >
        {packet.lines.map((line, index) => (
          <div className="astra-packet-line" key={index} data-line={index + 1}>
            <code>
              {packetSegments(line, index, packet.separator, packet.format).map((segment, token) => (
                <span key={token} data-tone={segment.tone}>
                  {segment.text}
                </span>
              ))}
            </code>
          </div>
        ))}
      </div>
      {notice && <p role="status">{notice}</p>}
    </div>
  );
}
