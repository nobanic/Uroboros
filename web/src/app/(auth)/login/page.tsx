"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";

import { UroIcon } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import { AUTH_DISABLED, auth } from "@/lib/auth";

export default function LoginPage() {
  const router = useRouter();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);

  useEffect(() => {
    if (AUTH_DISABLED) {
      window.location.replace("/function/tasks");
      return;
    }
    const token = auth.getToken();
    if (token) {
      auth.setToken(token);
      window.location.replace("/dashboard");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError("Unable to connect to the control plane."))
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    setLoading(true);
    setError("");
    try {
      const { token } = await api.login(username, password);
      auth.setToken(token);
      window.location.replace("/dashboard");
    } catch {
      setError("Authentication failed. Verify your operator credentials.");
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div className="uro-auth-boot" role="status">
        <span className="uro-auth-spinner" />
        <span>Establishing secure control-plane link</span>
      </div>
    );
  }

  return (
    <div className="uro-auth-shell">
      <div className="uro-auth-grid" aria-hidden="true" />
      <section className="uro-auth-branding" aria-label="Uroboros">
        <div className="uro-auth-brand">
          {/* biome-ignore lint/performance/noImgElement: Static export brand asset. */}
          <img src="/uroboros/dark-blue/logo-stacked.png" alt="Uroboros" width={1254} height={1254} />
        </div>

        <div className="uro-auth-statement">
          <span className="uro-kicker">Control the unknown</span>
          <h1>One assignment.<br />Autonomous<br />execution.</h1>
          <p>
            Enter the control plane where autonomous operations become observable,
            evidence stays attributable and every attack path remains reversible.
          </p>
        </div>

        <section className="uro-auth-telemetry" aria-label="Platform properties">
          <span><UroIcon name="topology" /><strong>Graph native</strong><small>Live lineage</small></span>
          <span><UroIcon name="shield" /><strong>Fail closed</strong><small>Scoped execution</small></span>
          <span><UroIcon name="records" /><strong>Evidence first</strong><small>Durable proof</small></span>
        </section>
      </section>

      <main className="uro-auth-access">
        <form onSubmit={handleSubmit} aria-busy={loading} className="uro-auth-card">
          <header>
            <span className="uro-auth-card-icon"><UroIcon name="user" /></span>
            <span>
              <span className="uro-kicker">Operator access</span>
              <h2>Enter control plane</h2>
              <p>Authenticate to resume secured operations.</p>
            </span>
          </header>

          <label className="uro-auth-field" htmlFor="username">
            <span>Operator</span>
            <span className="uro-auth-input">
              <UroIcon name="user" />
              <input
                id="username"
                name="username"
                type="text"
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                placeholder="operator"
                autoComplete="username"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                // biome-ignore lint/a11y/noAutofocus: Preserve keyboard-first operator access.
                autoFocus
              />
            </span>
          </label>

          <label className="uro-auth-field" htmlFor="password">
            <span>Passphrase</span>
            <span className="uro-auth-input">
              <UroIcon name="intercept" />
              <input
                id="password"
                name="password"
                type={showPassword ? "text" : "password"}
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                placeholder="Enter passphrase"
                autoComplete="current-password"
              />
              <button
                type="button"
                onClick={() => setShowPassword((value) => !value)}
                aria-label={showPassword ? "Hide password" : "Show password"}
                aria-pressed={showPassword}
              >
                <UroIcon name="eye" />
              </button>
            </span>
          </label>

          {error && <p className="uro-auth-error" role="alert"><UroIcon name="info" />{error}</p>}

          <button className="uro-auth-submit" type="submit" disabled={loading || !username || !password}>
            {loading ? <span className="uro-auth-spinner" /> : <UroIcon name="shield" />}
            <span>{loading ? "Authenticating" : "Authenticate"}</span>
            {!loading && <UroIcon name="chevron" />}
          </button>

          <footer>
            <span className="uro-system-state-dot" />
            Encrypted operator boundary
            <code>AUTH/01</code>
          </footer>
        </form>
      </main>
    </div>
  );
}
