"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";
import type { UpdateCheck } from "@/lib/types";

/**
 * The top bar's "new version available" badge: checks once on full page load, and if
 * there's an update, lights up next to the version number; clicking jumps straight to
 * the "Version & Updates" card on the system settings page.
 *
 * The backend caches its GitHub query result for 30 minutes, so checking once on every
 * mount here is safe — the unauthenticated GitHub API allows only 60 requests/hour/IP,
 * and without that cache layer, opening a few more tabs would exhaust the quota, leaving
 * you unable to check when you actually want to update.
 *
 * A failed query is always silent: the top bar isn't the place for errors; the user will
 * see the reason by going to the settings page and clicking "Check for updates".
 */
export function UpdateBadge() {
  const [latest, setLatest] = React.useState("");
  const [sourceUpdate, setSourceUpdate] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    const show = (r: UpdateCheck) => {
      if (!alive) return;
      setLatest(r.has_update && r.latest ? r.latest.replace(/^v(?=\d)/, "") : "");
      setSourceUpdate(r.mode === "docker-source");
    };
    const changed = (event: Event) => show((event as CustomEvent<UpdateCheck>).detail);
    window.addEventListener("uroboros:updates-changed", changed);
    api
      .checkUpdate()
      .then(show)
      .catch(() => {
        // Silent: neither being offline nor GitHub rate-limiting should pop an error in the top bar.
      });
    return () => {
      alive = false;
      window.removeEventListener("uroboros:updates-changed", changed);
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      onClick={() => { sessionStorage.setItem("uroboros.update.section", "lifecycle"); window.dispatchEvent(new Event("uroboros:show-updates")); }}
      title={sourceUpdate ? `New commit ${latest} on main, click to update` : `New version ${latest} available, click to update`}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* Pulsing dot: the top bar is crowded, so plain text is easy to miss; the animation makes it instantly visible. */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">{sourceUpdate ? "Source update" : "New version"} {latest}</span>
      <span className="sm:hidden">New version</span>
    </Link>
  );
}
