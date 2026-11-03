"use client";

import { useState } from "react";
import { UroIcon } from "@/components/uroboros-icons";
import type { TrafficDetail, TrafficExchange } from "@/lib/types";
import { HttpPacket } from "./http-packet";
import { displayTrafficRequest, trafficBytes } from "./traffic-document";
import { useConsoleResource } from "./use-console-resource";

export function TrafficReader({ exchange }: { exchange: TrafficExchange }) {
  const detail = useConsoleResource<TrafficDetail>(`/traffic/exchange?id=${encodeURIComponent(exchange.id)}`);
  const [side, show] = useState<"req" | "resp">("req"),
    [original, showOriginal] = useState(false);
  const source = detail.data?.req || "",
    displayed = displayTrafficRequest(source, exchange),
    restored = source !== displayed;
  return (
    <section className="astra-traffic-reader" aria-label="Captured exchange inspector">
      <header>
        <div>
          <span className="astra-traffic-method">{exchange.method}</span>
          <span className="astra-traffic-code" data-family={Math.floor(exchange.status / 100)}>
            {exchange.status}
          </span>
          <time>{new Date(exchange.ts).toLocaleString()}</time>
        </div>
        <h3>{exchange.host}</h3>
        <code>{exchange.url}</code>
      </header>
      <dl className="astra-traffic-reader-facts">
        <div>
          <dt>Response</dt>
          <dd>{trafficBytes(exchange.resp_len)}</dd>
        </div>
        <div>
          <dt>Content type</dt>
          <dd>{exchange.content_type || "Not reported"}</dd>
        </div>
        <div>
          <dt>Capture ID</dt>
          <dd>{exchange.id}</dd>
        </div>
      </dl>
      <nav className="astra-traffic-reader-tabs" aria-label="Captured message side">
        <button aria-pressed={side === "req"} onClick={() => show("req")}>
          <UroIcon name="arrow-up-right" aria-hidden="true" />
          Request
        </button>
        <button aria-pressed={side === "resp"} onClick={() => show("resp")}>
          <UroIcon name="traffic" aria-hidden="true" />
          Response
        </button>
      </nav>
      {detail.error && (
        <div className="astra-form-error" role="alert">
          <p>{detail.error.message}</p>
          <button className="astra-button" onClick={() => void detail.reload()}>
            Retry captured exchange
          </button>
        </div>
      )}
      {detail.busy && !detail.data ? (
        <p className="astra-empty" role="status">
          Loading captured message…
        </p>
      ) : (
        detail.data && (
          <>
            {side === "req" && restored && (
              <div className="astra-traffic-host-note">
                <span>Host header reconstructed for display from the recorded URL.</span>
                <button aria-pressed={original} onClick={() => showOriginal((value) => !value)}>
                  {original ? "Show restored Host" : "Show original capture"}
                </button>
              </div>
            )}
            <HttpPacket key={side} raw={side === "resp" ? detail.data.resp : original ? source : displayed} />
          </>
        )
      )}
    </section>
  );
}
