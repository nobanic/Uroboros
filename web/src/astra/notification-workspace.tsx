"use client";
import { useCallback, useEffect, useRef, useState } from "react";

import Link from "next/link";

import { ArrowRight, Bell, CircleCheck, Clock3, Filter, Plus, RefreshCw, Send, Settings2, Trash2 } from "lucide-react";

import { statusMeta } from "@/lib/status";

import {
  NotificationEditor,
  NotificationError,
  NotificationSwitch,
  type NotificationView,
} from "./notification-controls";
import {
  notificationBacklog,
  notificationCanRetry,
  notificationConstraints,
  notificationKind,
  notificationStates,
} from "./notification-document";
import { useNotifications } from "./use-notifications";
import "./notification-workspace.css";

function NotificationLedger({ journal, state }: NotificationView) {
  const { ledger } = state,
    busy = Boolean(state.writing),
    loading = state.reads.ledger.loading,
    pages = Math.max(1, Math.ceil(ledger.total / ledger.query.pageSize));
  const time = (value: string) =>
    Number.isNaN(new Date(value).getTime()) ? value : new Date(value).toLocaleString("en-US", { hour12: false });
  return (
    <section data-notification-ledger className="astra-notify-ledger">
      <div className="astra-notify-toolbar">
        <select
          aria-label="Delivery channel"
          value={ledger.query.channelId ?? "all"}
          onChange={(event) =>
            journal.queryLedger({ channelId: event.target.value === "all" ? undefined : Number(event.target.value) })
          }
        >
          <option value="all">All channels</option>
          {state.channels.map((channel) => (
            <option key={channel.id} value={channel.id}>
              {channel.name}
            </option>
          ))}
        </select>
        <select
          aria-label="Delivery status"
          value={ledger.query.state ?? "all"}
          onChange={(event) =>
            journal.queryLedger({ state: event.target.value === "all" ? undefined : event.target.value })
          }
        >
          <option value="all">All statuses</option>
          {notificationStates.map((value) => (
            <option key={value} value={value}>
              {statusMeta("delivery", value).label}
            </option>
          ))}
        </select>
        <button type="button" disabled={loading} onClick={() => void journal.refreshLedger()}>
          <RefreshCw size={14} />
          Refresh
        </button>
        <span>{ledger.total} total</span>
      </div>
      <NotificationError text={state.reads.ledger.error} retry={() => void journal.refreshLedger()} />
      <div className="astra-notify-tablewrap" aria-busy={loading}>
        <table>
          <thead>
            <tr>
              <th>Time</th>
              <th>Vulnerability</th>
              <th>Channel</th>
              <th>Status</th>
              <th>Attempts</th>
              <th>Error</th>
              <th>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {ledger.rows.map((row) => (
              <tr key={row.id} data-delivery={row.id}>
                <td>
                  <time dateTime={row.created_at}>{time(row.created_at)}</time>
                </td>
                <td>
                  <span data-notify-severity={row.severity}>{statusMeta("severity", row.severity).label}</span>
                  <strong>{row.title || "(untitled)"}</strong>
                  {row.event_kind === "finding_status_changed" && <small>Status change</small>}
                </td>
                <td>{row.channel_name}</td>
                <td>
                  <span data-notify-state={row.state}>{statusMeta("delivery", row.state).label}</span>
                </td>
                <td>{row.attempts}</td>
                <td>
                  <pre>{row.last_error}</pre>
                </td>
                <td>
                  {notificationCanRetry(row.state) && (
                    <button type="button" disabled={busy} onClick={() => void journal.retry(row.id)}>
                      Resend
                    </button>
                  )}
                </td>
              </tr>
            ))}
            {!ledger.rows.length && (
              <tr>
                <td colSpan={7}>{loading ? "Loading…" : "No delivery records"}</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <footer className="astra-notify-pagination">
        <span>
          {loading ? "Refreshing saved records…" : "Queued deliveries only. Direct channel tests are separate."}
        </span>
        {pages > 1 && (
          <>
            <button
              type="button"
              disabled={ledger.query.page <= 1 || loading}
              onClick={() => journal.queryLedger({ page: ledger.query.page - 1 })}
            >
              Previous
            </button>
            <output>
              {ledger.query.page} / {pages}
            </output>
            <button
              type="button"
              disabled={ledger.query.page >= pages || loading}
              onClick={() => journal.queryLedger({ page: ledger.query.page + 1 })}
            >
              Next
            </button>
          </>
        )}
      </footer>
    </section>
  );
}
function RouterDefaults({ journal, state }: NotificationView) {
  const busy = Boolean(state.writing),
    ready = Boolean(state.meta);
  return (
    <aside className="astra-notify-defaults">
      <header>
        <Settings2 size={17} />
        <h2>Router defaults</h2>
      </header>
      <p>Links and cadence shared by every route.</p>
      <fieldset disabled={!ready || busy}>
        <label htmlFor="n-base">Console link-back</label>
        <input
          id="n-base"
          value={state.defaults.baseURL}
          onChange={(event) => journal.patchDefaults({ baseURL: event.target.value })}
          placeholder="https://uroboros.example.com"
        />
        <small>The evidence deep link included in messages.</small>
        <label htmlFor="n-digest">Digest cadence</label>
        <div className="astra-notify-cadence">
          <input
            id="n-digest"
            type="number"
            min={1}
            max={1440}
            value={state.defaults.interval}
            placeholder={String(state.meta?.defaults.digest_interval_min ?? 30)}
            onChange={(event) => journal.patchDefaults({ interval: event.target.value })}
          />
          <span>minutes</span>
        </div>
        <small>Only routes using digest delivery are affected.</small>
        <button type="button" onClick={() => void journal.saveDefaults()}>
          Save router defaults
        </button>
        {state.defaults.dirty && <small className="astra-notify-unsaved">Unsaved router defaults</small>}
      </fieldset>
      <div className="astra-notify-routeguide">
        <Send size={18} />
        <strong>Test the saved destination</strong>
        <p>
          A direct test verifies the saved channel response. It bypasses the queue and does not create a ledger entry.
        </p>
        <CircleCheck size={18} />
        <strong>Review queued delivery</strong>
        <p>
          The ledger records normal event delivery, retry attempts and provider errors. A test response does not
          establish that this queue processed an event.
        </p>
      </div>
    </aside>
  );
}
export function NotificationWorkspace() {
  const { journal, state } = useNotifications(),
    [query, setQuery] = useState(""),
    [mode, setMode] = useState("all"),
    busy = Boolean(state.writing);
  const search = useRef<HTMLInputElement>(null),
    [contextOpen, setContextOpen] = useState(false);
  const toggleContext = useCallback(
    () =>
      setContextOpen((open) => {
        localStorage.setItem("uroboros-module-rail", open ? "closed" : "open");
        return !open;
      }),
    [],
  );
  const focusRoutes = useCallback(() => {
    journal.selectTab("channels");
    requestAnimationFrame(() => search.current?.focus());
  }, [journal]);
  useEffect(() => {
    setContextOpen(localStorage.getItem("uroboros-module-rail") === "open");
  }, []);
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if (
        state.editor ||
        event.ctrlKey ||
        event.altKey ||
        event.metaKey ||
        (event.target instanceof HTMLElement &&
          (event.target.matches("input,textarea,select") || event.target.isContentEditable))
      )
        return;
      if (event.key === "/") {
        event.preventDefault();
        focusRoutes();
      }
      if (event.key === "]") {
        event.preventDefault();
        toggleContext();
      }
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, [state.editor, focusRoutes, toggleContext]);
  const channels = state.channels.filter(
    (channel) =>
      (mode === "all" || channel.mode === mode) &&
      [channel.name, notificationKind(channel.kind), ...notificationConstraints(channel.filter)]
        .join(" ")
        .toLocaleLowerCase()
        .includes(query.trim().toLocaleLowerCase()),
  );
  const stats = state.meta?.stats;
  return (
    <div data-notification-workspace className="astra-notifications">
      <header className="astra-notify-heading">
        <div>
          <span className="astra-notify-eyebrow">
            <Bell size={15} />
            Event delivery
          </span>
          <h1>Notification routes</h1>
          <p>From finding to destination, with delivery history in view.</p>
        </div>
        <div className="astra-notify-master">
          <span data-live={Boolean(state.meta?.enabled)}>
            {state.meta ? (state.meta.enabled ? "Router live" : "Router paused") : "Loading router…"}
          </span>
          <NotificationSwitch
            aria-label="Notifications master switch"
            checked={state.meta?.enabled ?? false}
            disabled={!state.meta || busy}
            onCheckedChange={(enabled) => void journal.toggleMaster(enabled)}
          />
        </div>
      </header>
      <div className="astra-notify-contextbar">
        <button type="button" onClick={focusRoutes}>
          Focus routes <kbd>/</kbd>
        </button>
        <button type="button" aria-controls="notification-context" aria-expanded={contextOpen} onClick={toggleContext}>
          Routing context <kbd>]</kbd>
        </button>
        <span>Route → test → audit</span>
      </div>
      {contextOpen && (
        <section id="notification-context" className="astra-notify-context" aria-label="Notification routing context">
          <p>Configure a route, test its saved destination, then inspect queued event delivery separately.</p>
          <ol>
            <li>Route · choose the destination and event policy.</li>
            <li>Test · send directly with saved credentials.</li>
            <li>Audit · inspect queued deliveries and provider errors.</li>
          </ol>
          <nav aria-label="Related notification surfaces">
            <Link href="/function/findings">
              Finding registry <ArrowRight size={13} />
            </Link>
            <Link href="/system/logs">
              System log <ArrowRight size={13} />
            </Link>
            <Link href="/system/settings">
              Configuration <ArrowRight size={13} />
            </Link>
          </nav>
        </section>
      )}
      <dl className="astra-notify-metrics">
        {[
          ["Routes enabled", stats ? `${stats.channels_on} / ${stats.channels}` : "—"],
          ["Sent today", stats?.sent_today ?? "—"],
          ["Pending", stats?.pending ?? "—"],
          ["Failed", stats?.failed ?? "—"],
          ["Oldest queued event", stats ? notificationBacklog(stats.backlog_age_ms) : "—"],
        ].map(([label, value]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
      <NotificationError text={state.reads.meta.error} retry={() => void journal.refreshMeta()} />
      <NotificationError text={state.error} />
      {state.notice && (
        <p className="astra-notify-success" role="status">
          {state.notice}
        </p>
      )}
      <section className="astra-notify-surface">
        <nav className="astra-notify-tabs" aria-label="Notification views">
          <button type="button" aria-pressed={state.tab === "channels"} onClick={() => journal.selectTab("channels")}>
            <Filter size={15} />
            Routing map
          </button>
          <button
            type="button"
            aria-pressed={state.tab === "deliveries"}
            onClick={() => journal.selectTab("deliveries")}
          >
            <Clock3 size={15} />
            Delivery ledger
          </button>
          <button
            type="button"
            className="astra-notify-primary"
            disabled={busy || !state.meta}
            onClick={() => journal.openEditor()}
          >
            <Plus size={15} />
            Add route
          </button>
        </nav>
        {state.tab === "deliveries" ? (
          <NotificationLedger journal={journal} state={state} />
        ) : (
          <div className="astra-notify-layout">
            <section className="astra-notify-routes">
              <div className="astra-notify-toolbar">
                <input
                  ref={search}
                  aria-label="Search notification routes"
                  placeholder="Search routes or event policy…"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
                <select aria-label="Route delivery mode" value={mode} onChange={(event) => setMode(event.target.value)}>
                  <option value="all">All timing</option>
                  <option value="realtime">Realtime</option>
                  <option value="digest">Digest</option>
                </select>
                <button
                  type="button"
                  disabled={busy || state.reads.channels.loading || state.reads.meta.loading}
                  onClick={() => void journal.refresh()}
                  aria-label="Refresh notification routes"
                >
                  <RefreshCw size={15} />
                </button>
              </div>
              <NotificationError text={state.reads.channels.error} retry={() => void journal.refreshChannels()} />
              {channels.map((channel) => (
                <article
                  key={channel.id}
                  data-notification-route={channel.id}
                  className="astra-notify-route"
                  data-enabled={channel.enabled}
                >
                  <header>
                    <span className="astra-notify-route-icon">
                      <Bell size={18} />
                    </span>
                    <button
                      type="button"
                      className="astra-notify-destination"
                      disabled={busy}
                      onClick={() => journal.openEditor(channel)}
                    >
                      <small>
                        {notificationKind(channel.kind)} / #{channel.id}
                      </small>
                      <strong>{channel.name}</strong>
                    </button>
                    <NotificationSwitch
                      checked={channel.enabled}
                      disabled={busy}
                      onCheckedChange={() => void journal.changeChannel(channel)}
                      aria-label={`Enable ${channel.name}`}
                    />
                    <button
                      type="button"
                      disabled={busy}
                      aria-label={`Configure ${channel.name}`}
                      onClick={() => journal.openEditor(channel)}
                    >
                      <Settings2 size={15} />
                    </button>
                    <button
                      type="button"
                      disabled={busy}
                      aria-label={`Delete ${channel.name}`}
                      onClick={() => void journal.changeChannel(channel, true)}
                    >
                      <Trash2 size={15} />
                    </button>
                  </header>
                  <div className="astra-notify-flow">
                    <span>Finding lifecycle</span>
                    <ArrowRight size={13} />
                    <span>
                      {channel.filter.min_severity ? `${channel.filter.min_severity}+ severity` : "All severities"}
                    </span>
                    <ArrowRight size={13} />
                    <span>{channel.mode === "digest" ? "Digest window" : "Realtime"}</span>
                    <ArrowRight size={13} />
                    <span>{channel.enabled ? "Route enabled" : "Route paused"}</span>
                  </div>
                  <footer>
                    <Filter size={13} />
                    {notificationConstraints(channel.filter).map((constraint, i) => (
                      <span key={`${i}:${constraint}`}>{constraint}</span>
                    ))}
                    <small>{channel.rate_per_min ? `${channel.rate_per_min}/min` : "No rate limit"}</small>
                  </footer>
                </article>
              ))}
              {!channels.length && (
                <div className="astra-notify-empty">
                  <Bell size={30} />
                  <strong>
                    {state.reads.channels.loading
                      ? "Loading saved routes…"
                      : state.reads.channels.error
                        ? "Saved routes could not be loaded"
                        : state.channels.length
                          ? "No routes match these filters"
                          : "No notification routes yet"}
                  </strong>
                  <p>
                    {state.channels.length
                      ? "Change the search or timing filter to see more routes."
                      : "Connect a destination and choose its event policy."}
                  </p>
                </div>
              )}
              <p className="astra-notify-footnote">
                {channels.length} of {state.channels.length} saved routes · Global pause and individual route enablement
                are separate controls.
              </p>
            </section>
            <RouterDefaults journal={journal} state={state} />
          </div>
        )}
      </section>
      <NotificationEditor journal={journal} state={state} />
    </div>
  );
}
