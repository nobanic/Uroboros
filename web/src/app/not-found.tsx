"use client";

import Link from "next/link";

import { Button } from "@/components/ui/button";
import { UroIcon } from "@/components/uroboros-icons";

export default function NotFound() {
  return (
    <main className="uro-not-found">
      <div className="uro-not-found__grid" aria-hidden="true" />
      <section className="uro-not-found__signal" aria-hidden="true">
        <i />
        <i />
        <i />
        <span>
          <UroIcon name="route" />
        </span>
        <strong>404</strong>
      </section>
      <section className="uro-not-found__copy">
        <span>Navigation fault / route unresolved</span>
        <h1>Signal lost beyond the mapped surface.</h1>
        <p>
          The requested control-plane coordinate does not exist. Return to a known operational surface and continue from
          verified state.
        </p>
        <nav>
          <Button asChild>
            <Link prefetch={false} replace href="/dashboard">
              <UroIcon name="mission" />
              Mission Control
            </Link>
          </Button>
          <Button asChild variant="outline">
            <Link prefetch={false} replace href="/function/tasks">
              <UroIcon name="tasks" />
              Operations
            </Link>
          </Button>
        </nav>
      </section>
      <footer>
        <span>STATUS</span>
        <strong>ROUTE_UNRESOLVED</strong>
        <i />
        <small>No application state was changed.</small>
      </footer>
    </main>
  );
}
