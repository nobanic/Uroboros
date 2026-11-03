"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";

import { UroIcon } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import { AUTH_DISABLED, auth } from "@/lib/auth";

export default function SetupPage() {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [unavailable, setUnavailable] = useState(false);

  useEffect(() => {
    if (AUTH_DISABLED) {
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (initialized) router.replace("/login");
      })
      .catch((cause) => {
        setError(cause instanceof Error ? cause.message : "Unable to connect to the control plane.");
        setUnavailable(true);
      })
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    if (password !== confirm) {
      setError("The two passphrases do not match.");
      return;
    }
    if (password.length < 8) {
      setError("The passphrase must contain at least 8 characters.");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.initPassword(password);
      auth.setToken(token);
      router.replace("/dashboard");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Initialization failed.");
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div className="uro-auth-boot" role="status">
        <span className="uro-auth-spinner" />
        <span>Inspecting control-plane state</span>
      </div>
    );
  }

  return (
    <div className="uro-auth-shell uro-setup-shell">
      <div className="uro-auth-grid" aria-hidden="true" />
      <section className="uro-auth-branding" aria-label="Uroboros initialization">
        <div className="uro-auth-brand">
          {/* biome-ignore lint/performance/noImgElement: Static export brand asset. */}
          <img src="/uroboros/dark-blue/logo-stacked.png" alt="Uroboros" width={1254} height={1254} />
        </div>
        <div className="uro-auth-statement">
          <span className="uro-kicker">Secure the operator boundary</span>
          <h1>One assignment.<br />Autonomous<br />execution.</h1>
          <p>The first operator credential seals access to this Uroboros instance.</p>
        </div>
      </section>

      <main className="uro-auth-access">
        <form onSubmit={handleSubmit} className="uro-auth-card" aria-busy={loading}>
          <header>
            <span className="uro-auth-card-icon"><UroIcon name={unavailable ? "blocked" : "intercept"} /></span>
            <span>
              <span className="uro-kicker">Initialization / 01</span>
              <h2>{unavailable ? "Control plane unavailable" : "Create operator passphrase"}</h2>
              <p>
                {unavailable
                  ? "Setup is fail-closed until backend state can be verified."
                  : "Use at least 8 characters. This credential protects the local operator boundary."}
              </p>
            </span>
          </header>

          {unavailable ? (
            <>
              {error && <p className="uro-auth-error" role="alert"><UroIcon name="info" />{error}</p>}
              <button className="uro-auth-submit" type="button" onClick={() => window.location.reload()}>
                <UroIcon name="replay" /><span>Retry state check</span><UroIcon name="chevron" />
              </button>
            </>
          ) : (
            <>
              <label className="uro-auth-field" htmlFor="password">
                <span>New passphrase</span>
                <span className="uro-auth-input">
                  <UroIcon name="intercept" />
                  <input
                    id="password"
                    type="password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    placeholder="Minimum 8 characters"
                    autoComplete="new-password"
                    // biome-ignore lint/a11y/noAutofocus: Setup is a single keyboard-first form.
                    autoFocus
                  />
                </span>
              </label>
              <label className="uro-auth-field" htmlFor="confirm">
                <span>Confirm passphrase</span>
                <span className="uro-auth-input">
                  <UroIcon name="shield" />
                  <input
                    id="confirm"
                    type="password"
                    value={confirm}
                    onChange={(event) => setConfirm(event.target.value)}
                    placeholder="Repeat passphrase"
                    autoComplete="new-password"
                  />
                </span>
              </label>
              {error && <p className="uro-auth-error" role="alert"><UroIcon name="info" />{error}</p>}
              <button className="uro-auth-submit" type="submit" disabled={loading || !password || !confirm}>
                {loading ? <span className="uro-auth-spinner" /> : <UroIcon name="shield" />}
                <span>{loading ? "Sealing boundary" : "Initialize Uroboros"}</span>
                {!loading && <UroIcon name="chevron" />}
              </button>
            </>
          )}

          <footer><span className="uro-system-state-dot" />Local credentials only<code>SETUP/01</code></footer>
        </form>
      </main>
    </div>
  );
}
