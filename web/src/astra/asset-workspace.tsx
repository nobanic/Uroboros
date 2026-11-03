"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";
import { AssetDslSearch } from "@/components/asset-dsl-search";
import { UroIcon } from "@/components/uroboros-icons";
import type { Asset, Company } from "@/lib/types";
import { ApiFailure, collection } from "./client";
import { consoleRequest } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import { useConsoleResource } from "./use-console-resource";
import {
  assetCell,
  assetClasses,
  assetColumns,
  assetPagePath,
  assetPageSizes,
  assetTitle,
  assetWebLink,
  companyScopeDocument,
  companyWriteFeedback,
  companyWriteIncomplete,
  savedCompanyRules,
  type AssetClass,
  type CompanyWriteResult,
} from "./asset-document";
import "./forms.css";
import "./asset-workspace.css";

type AssetPage = { assets: Asset[]; total: number; count: number };
type AssetModal =
  | { kind: "create" | "append" | "replace"; company?: Pick<Company, "id" | "name" | "scope">; name: string; scope: string; reason: string }
  | { kind: "delete-company"; company: Company; deleteAssets: boolean }
  | { kind: "delete-assets"; ids: number[] }
  | { kind: "inspect"; asset: Asset };

type AssetWriteOutcome = { messages: string[]; problem?: string; draft?: AssetModal };

function CompanyMark({ company }: { company: Company }) {
  return (
    <span className="astra-company-mark" aria-hidden="true">
      {company.name.trim().slice(0, 1).toUpperCase()}
      {company.logo && (
        <img
          src={company.logo}
          alt=""
          onError={(event) => {
            event.currentTarget.hidden = true;
          }}
        />
      )}
    </span>
  );
}

function AssetPager({
  page,
  size,
  total,
  busy,
  change,
}: {
  page: number;
  size: number;
  total: number;
  busy: boolean;
  change: (page: number, size: number) => void;
}) {
  const pages = Math.max(1, Math.ceil(total / size));
  return (
    <div className="astra-asset-pagination" aria-label="Asset pagination">
      <span>
        {total} total · Page {page + 1} of {pages}
      </span>
      <button disabled={busy || page === 0} onClick={() => change(page - 1, size)}>
        Previous asset page
      </button>
      <button disabled={busy || page + 1 >= pages} onClick={() => change(page + 1, size)}>
        Next asset page
      </button>
      <label>
        Page size
        <select
          aria-label="Asset page size"
          disabled={busy}
          value={size}
          onChange={(event) => change(0, Number(event.target.value))}
        >
          {assetPageSizes.map((value) => (
            <option key={value} value={value}>
              {value}
            </option>
          ))}
        </select>
      </label>
    </div>
  );
}

export function AssetsWorkspace({ onUnauthorized }: { onUnauthorized?: () => void }) {
  const [kind, setKind] = useState<AssetClass>("company"),
    [focus, setFocus] = useState<number>();
  const [query, setQuery] = useState(""),
    [search, setSearch] = useState("");
  const [page, setPage] = useState(0),
    [size, setSize] = useState(50);
  const [selected, setSelected] = useState(new Set<number>()),
    [modal, setModal] = useState<AssetModal>();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notices, setNotices] = useState<string[]>([]);
  const lock = useRef(false),
    mounted = useRef(true),
    interaction = useRef(0);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      interaction.current++;
    };
  }, []);
  const companies = useConsoleResource<unknown>("/companies"),
    counts = useConsoleResource<Record<string, number>>("/assets/counts");
  const path = assetPagePath(kind, page, size, search),
    records = useConsoleResource<AssetPage>(path);
  const organizations = collection(companies.data, ["companies"]) as unknown as Company[];
  const focused = organizations.find((company) => company.id === focus) || organizations[0];
  const names = new Map(organizations.map((company) => [company.id, company.name]));
  const items = records.data?.assets || [],
    total = records.data?.total ?? records.data?.count ?? 0;
  const ready = companies.data !== undefined && counts.data !== undefined;
  const loading = companies.busy || counts.busy || records.busy;
  const failures = [companies.error, counts.error, records.error];
  useEffect(() => {
    if (failures.some((failure) => failure instanceof ApiFailure && failure.status === 401)) onUnauthorized?.();
  }, [companies.error, counts.error, records.error, onUnauthorized]);
  useEffect(() => {
    const timer = window.setTimeout(
      () => {
        setSearch(query.trim());
        setPage(0);
      },
      query.trim() ? 400 : 0,
    );
    return () => window.clearTimeout(timer);
  }, [query]);
  useEffect(() => {
    if (!records.busy && records.data && page > Math.max(0, Math.ceil(total / size) - 1))
      setPage(Math.max(0, Math.ceil(total / size) - 1));
  }, [records.busy, records.data, total, size, page]);

  function choose(value: AssetClass) {
    setKind(value);
    setQuery("");
    setSearch("");
    setPage(0);
    setSelected(new Set());
  }
  function toggle(ids: number[], checked: boolean) {
    setSelected((previous) => {
      const next = new Set(previous);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }
  async function refresh(clearSelection = false) {
    if (clearSelection) setSelected(new Set());
    const results = await Promise.all([companies.reload(), counts.reload(), records.reload()]);
    return results.every(Boolean);
  }
  async function refreshAssets() {
    if (lock.current || !mounted.current) return;
    const request = ++interaction.current;
    setError("");
    setNotices([]);
    const ok = await refresh(true);
    if (mounted.current && request === interaction.current && ok) setNotices(["Assets refreshed."]);
  }
  async function perform(work: () => Promise<string[] | AssetWriteOutcome>, close = true) {
    if (lock.current || !mounted.current) return;
    const request = ++interaction.current, owner = modal;
    lock.current = true;
    setBusy(true);
    setError("");
    setNotices([]);
    try {
      const saved = await work();
      if (!mounted.current || request !== interaction.current) return;
      const result: AssetWriteOutcome = Array.isArray(saved) ? { messages: saved } : saved;
      if (close) setModal(current => current === owner ? result.draft : current);
      setNotices(result.messages);
      setError(result.problem || "");
      const reloaded = await refresh();
      if (mounted.current && request === interaction.current)
        setError([result.problem, reloaded ? "" : "The write is saved, but some data could not refresh. Retry Refresh assets."].filter(Boolean).join(" "));
    } catch (failure) {
      if (!mounted.current || request !== interaction.current) return;
      if (failure instanceof ApiFailure && failure.status === 401) onUnauthorized?.();
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      lock.current = false;
      if (mounted.current) setBusy(false);
    }
  }
  function openScope(mode: "create" | "append" | "replace", company?: Company) {
    setError("");
    setModal({
      kind: mode,
      company,
      name: "",
      reason: "",
      scope:
        mode === "replace"
          ? savedCompanyRules(company)
              .map((rule) => rule.value)
              .join("\n")
          : "",
    });
  }
  const scopeModal =
    modal && (modal.kind === "create" || modal.kind === "append" || modal.kind === "replace") ? modal : undefined;
  const parsed = scopeModal
    ? companyScopeDocument(scopeModal.scope, scopeModal.kind === "replace" ? scopeModal.company : undefined)
    : undefined;
  const pageIDs = items.map((asset) => asset.id),
    selectedOnPage = pageIDs.filter((id) => selected.has(id)).length;
  const observed = counts.data ? Object.values(counts.data).reduce((sum, count) => sum + count, 0) : undefined;

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!scopeModal || !parsed || parsed.errors.length || (scopeModal.kind === "create" && !scopeModal.name.trim()))
      return;
    const current = scopeModal;
    await perform(async () => {
      const result = await consoleRequest<CompanyWriteResult>(
        current.kind === "create" ? "/companies" : `/companies/${current.company!.id}/scope`,
        "POST",
        current.kind === "create"
          ? { name: current.name.trim(), scope: parsed.rules }
          : { scope: parsed.rules, reason: current.reason, ...(current.kind === "replace" ? { reset: true } : {}) },
      );
      const messages = [current.kind === "create" ? "Company created." : "Company scope saved.", ...companyWriteFeedback(result)];
      if (!companyWriteIncomplete(result)) return messages;
      let draft: AssetModal = current;
      const createdID = result.id;
      if (current.kind === "create" && typeof createdID === "number" && Number.isSafeInteger(createdID) && createdID > 0)
        draft = { ...current, kind: "append", company: { id: createdID, name: current.name.trim() } };
      return {
        messages,
        draft,
        problem: [
          current.kind === "create" && draft.kind === "create"
            ? "The company was saved, but its response has no usable ID. Keep this draft and refresh assets before retrying."
            : "Some scope rules were not saved. Review the retained draft and retry; already saved rules will be skipped.",
          ...(result.errors || []),
          ...(result.scope_errors || []),
        ].join(" "),
      };
    });
  }

  return (
    <div className="astra-model-workspace astra-asset-workspace">
      <header className="astra-model-header">
        <div>
          <small>Observed surface</small>
          <h1>Assets</h1>
          <p>Inspect infrastructure and ownership, with explicit company scope rules.</p>
        </div>
        <div>
          <button
            disabled={busy || loading}
            onClick={() => { void refreshAssets(); }}
          >
            Refresh assets
          </button>
          <button className="is-primary" disabled={busy} onClick={() => openScope("create")}>
            Add company
          </button>
        </div>
      </header>
      {notices.length > 0 && (
        <div className="astra-form-note" role="status">
          {notices.map((notice, index) => (
            <p key={index}>{notice}</p>
          ))}
        </div>
      )}
      {error && !modal && (
        <p className="astra-form-error" role="alert">
          {error}
        </p>
      )}
      {failures.map(
        (failure, index) =>
          failure && (
            <p className="astra-form-error" role="alert" key={index}>
              {failure.message}
            </p>
          ),
      )}
      <div className="astra-asset-overview">
        <div>
          <span>Observed assets</span>
          <strong>{observed ?? "—"}</strong>
        </div>
        <div>
          <span>Companies</span>
          <strong>{companies.data !== undefined ? organizations.length : "—"}</strong>
        </div>
        <div>
          <span>Asset classes</span>
          <strong>6</strong>
        </div>
      </div>
      <nav className="astra-asset-classes" aria-label="Asset classes">
        {assetClasses.map(([value, label]) => (
          <button key={value} aria-pressed={kind === value} disabled={busy} onClick={() => choose(value)}>
            <span>{label}</span>
            <span>
              {value === "company"
                ? companies.data !== undefined
                  ? organizations.length
                  : "—"
                : (counts.data?.[value] ?? (counts.data ? 0 : "—"))}
            </span>
          </button>
        ))}
      </nav>
      {kind === "company" ? (
        <div className="astra-company-layout">
          <aside className="astra-company-list" aria-label="Companies">
            {organizations.map((company) => (
              <button
                key={company.id}
                aria-label={`Inspect company ${company.name}`}
                aria-pressed={focused?.id === company.id}
                onClick={() => setFocus(company.id)}
              >
                <CompanyMark company={company} />
                <span>
                  <strong>{company.name}</strong>
                  <small>{company.scope?.length || 0} scope rules</small>
                </span>
                <b>{company.asset_count}</b>
              </button>
            ))}
            {!organizations.length && (
              <p>
                {companies.busy
                  ? "Loading companies…"
                  : companies.error
                    ? "Companies could not load. Refresh to retry."
                    : "No companies yet. Add a company to define ownership rules."}
              </p>
            )}
          </aside>
          <section className="astra-model-section astra-company-detail">
            {focused ? (
              <>
                <header>
                  <CompanyMark company={focused} />
                  <div>
                    <small>Company scope</small>
                    <h2>{focused.name}</h2>
                    <span>{focused.asset_count} owned assets</span>
                  </div>
                </header>
                <p className="astra-form-note">
                  Ownership and testing authority remain separate. Company keywords are hints; they do not automatically
                  assign assets.
                </p>
                <div className="astra-company-scope">
                  {focused.scope?.map((rule, index) => (
                    <div key={rule.id || `${rule.kind}-${index}`}>
                      <span className="astra-badge">{rule.kind}</span>
                      <code>{rule.raw}</code>
                      {rule.reason && <small>{rule.reason}</small>}
                    </div>
                  ))}
                  {!focused.scope?.length && <p>No company scope rules.</p>}
                </div>
                <div className="astra-asset-actions">
                  <button disabled={busy} onClick={() => openScope("replace", focused)}>
                    Replace scope
                  </button>
                  <button disabled={busy} onClick={() => openScope("append", focused)}>
                    Append scope
                  </button>
                  <button
                    disabled={busy}
                    onClick={() => {
                      setError("");
                      setModal({ kind: "delete-company", company: focused, deleteAssets: false });
                    }}
                  >
                    Delete company
                  </button>
                </div>
                <div className="astra-company-surfaces">
                  <h3>All observed assets</h3>
                  <p>Open a class to inspect the global asset ledger.</p>
                  <div>
                    {assetClasses
                      .filter(([value]) => value !== "company")
                      .map(([value, label]) => (
                        <button key={value} onClick={() => choose(value)}>
                          <UroIcon name="assets" aria-hidden="true" />
                          <span>{label}</span>
                          <strong>{counts.data?.[value] ?? 0}</strong>
                        </button>
                      ))}
                  </div>
                </div>
              </>
            ) : (
              <p>Select or create a company to inspect its ownership scope.</p>
            )}
          </section>
        </div>
      ) : (
        <section className="astra-model-section astra-asset-ledger" aria-busy={records.busy}>
          <div className="astra-asset-ledger-toolbar">
            <div className="astra-asset-search" role="search" aria-label="Asset DSL search">
              <AssetDslSearch
                query={query}
                onChange={setQuery}
                loading={records.busy || query.trim() !== search}
                error={records.error?.message || ""}
                count={records.data ? total : undefined}
              />
            </div>
            <span>{selected.size} selected across pages</span>
            <button disabled={busy || !selected.size} onClick={() => setSelected(new Set())}>
              Clear asset selection
            </button>
            <button
              disabled={busy || !selected.size}
              onClick={() => {
                setError("");
                setModal({ kind: "delete-assets", ids: [...selected] });
              }}
            >
              Delete selected assets
            </button>
          </div>
          <div className="astra-asset-table-scroll">
            <table className="astra-asset-table">
              <thead>
                <tr>
                  <th>
                    <input
                      type="checkbox"
                      aria-label="Select all assets on this page"
                      disabled={busy || !items.length}
                      checked={pageIDs.length > 0 && selectedOnPage === pageIDs.length}
                      ref={(node) => {
                        if (node) node.indeterminate = selectedOnPage > 0 && selectedOnPage < pageIDs.length;
                      }}
                      onChange={(event) => toggle(pageIDs, event.target.checked)}
                    />
                  </th>
                  {assetColumns[kind].map(([key, title]) => (
                    <th key={key}>{title}</th>
                  ))}
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {items.map((asset) => (
                  <tr key={asset.id} data-asset={asset.id} data-selected={selected.has(asset.id)}>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`Select asset ${asset.id}`}
                        checked={selected.has(asset.id)}
                        disabled={busy}
                        onChange={(event) => toggle([asset.id], event.target.checked)}
                      />
                    </td>
                    {assetColumns[kind].map(([key], index) => (
                      <td key={key} title={assetCell(asset, key, names)}>
                        {key === "domain" && assetWebLink(asset) ? (
                          <a href={assetWebLink(asset)} target="_blank" rel="noreferrer">
                            {assetCell(asset, key, names)}
                          </a>
                        ) : index === 0 ? (
                          <button className="astra-asset-title" onClick={() => setModal({ kind: "inspect", asset })}>
                            {assetCell(asset, key, names)}
                          </button>
                        ) : (
                          assetCell(asset, key, names)
                        )}
                      </td>
                    ))}
                    <td>
                      <div className="astra-asset-actions">
                        <button onClick={() => setModal({ kind: "inspect", asset })}>Inspect asset</button>
                        <button
                          disabled={busy}
                          aria-label={`Delete asset ${asset.id}`}
                          onClick={() => {
                            setError("");
                            setModal({ kind: "delete-assets", ids: [asset.id] });
                          }}
                        >
                          Delete
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {!items.length && (
            <p className="astra-asset-empty">
              {records.busy
                ? "Loading assets…"
                : records.error
                  ? "This page could not load. Refresh to retry."
                  : "No assets match this view."}
            </p>
          )}
          <AssetPager
            page={page}
            size={size}
            total={total}
            busy={busy || records.busy}
            change={(nextPage, nextSize) => {
              setPage(nextPage);
              setSize(nextSize);
            }}
          />
        </section>
      )}
      {modal && (
        <EditorDialog
          title={
            modal.kind === "inspect"
              ? assetTitle(modal.asset)
              : modal.kind === "delete-assets"
                ? "Delete assets"
                : modal.kind === "delete-company"
                  ? `Delete company ${modal.company.name}`
                  : modal.kind === "create"
                    ? "Add company"
                    : `${modal.kind === "append" ? "Append" : "Replace"} scope · ${modal.company?.name}`
          }
          eyebrow="Asset registry"
          busy={busy}
          close={() => setModal(undefined)}
        >
          {error && (
            <p className="astra-form-error" role="alert">
              {error}
            </p>
          )}
          {modal.kind === "inspect" ? (
            <div className="astra-asset-inspection">
              <p>Complete observed asset record, including task references and recorded metadata.</p>
              <dl>
                {Object.entries(modal.asset).map(([key, value]) => (
                  <div key={key}>
                    <dt>{key.replaceAll("_", " ")}</dt>
                    <dd>
                      {typeof value === "object" ? <pre>{JSON.stringify(value, null, 2)}</pre> : String(value ?? "—")}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
          ) : modal.kind === "delete-assets" ? (
            <form
              onSubmit={(event) => {
                event.preventDefault();
                const ids = modal.ids;
                void perform(async () => {
                  const result = await consoleRequest<{ deleted: number }>("/assets", "DELETE", { ids });
                  toggle(ids, false);
                  return [`Deleted ${result.deleted} assets.`];
                });
              }}
            >
              <p>Permanently delete {modal.ids.length} global asset records and their linked anchors.</p>
              <p className="astra-form-note">Selected asset IDs: {modal.ids.join(", ")}</p>
              <div className="astra-form-actions">
                <button type="button" disabled={busy} onClick={() => setModal(undefined)}>
                  Cancel
                </button>
                <button disabled={busy} type="submit">
                  Confirm asset deletion
                </button>
              </div>
            </form>
          ) : modal.kind === "delete-company" ? (
            <form
              onSubmit={(event) => {
                event.preventDefault();
                const current = modal;
                void perform(async () => {
                  const result = await consoleRequest<{ deleted: number; assets_deleted: number }>(
                    `/companies/${current.company.id}`,
                    "DELETE",
                    { delete_assets: current.deleteAssets },
                  );
                  if (current.deleteAssets) setSelected(new Set());
                  return [`Company deleted; ${result.assets_deleted} assets deleted.`];
                });
              }}
            >
              <p>Permanently delete this company and its scope rules.</p>
              <label className="astra-form-check">
                <input
                  type="checkbox"
                  name="company_delete_assets"
                  checked={modal.deleteAssets}
                  disabled={busy}
                  onChange={(event) => setModal({ ...modal, deleteAssets: event.target.checked })}
                />
                Also delete assets owned by this company
              </label>
              <p className="astra-form-note">Leave unchecked to retain the assets and remove their ownership link.</p>
              <div className="astra-form-actions">
                <button type="button" disabled={busy} onClick={() => setModal(undefined)}>
                  Cancel
                </button>
                <button disabled={busy} type="submit">
                  Confirm company deletion
                </button>
              </div>
            </form>
          ) : scopeModal && parsed ? (
            <form onSubmit={save} className="astra-company-editor">
              <fieldset disabled={busy}>
                {scopeModal.kind === "create" && (
                  <label>
                    Company name
                    <input
                      name="company_name"
                      required
                      value={scopeModal.name}
                      onChange={(event) => setModal({ ...scopeModal, name: event.target.value })}
                    />
                  </label>
                )}
                <label>
                  Asset scope
                  <textarea
                    name="company_scope"
                    rows={8}
                    value={scopeModal.scope}
                    aria-invalid={parsed.errors.length > 0}
                    onChange={(event) => setModal({ ...scopeModal, scope: event.target.value })}
                    placeholder={"example.com\n203.0.113.10\n198.51.100.0/24\nICP-REGISTRATION-123\ncompany keyword"}
                  />
                </label>
                <p className="astra-form-note">
                  One rule per line: domains, IPs, CIDRs, ICP filings and keywords. Keywords are agent hints.{" "}
                  {scopeModal.kind === "replace" && "Saving replaces all current rules; an empty list clears them."}
                </p>
                {parsed.errors.length > 0 && (
                  <div className="astra-form-error" role="alert">
                    {parsed.errors.map((item) => (
                      <p key={item.line}>
                        Line {item.line}: {item.error}
                      </p>
                    ))}
                  </div>
                )}
                <div className="astra-company-parsed" aria-label="Recognized scope rules">
                  {parsed.rules.map((rule, index) => (
                    <div key={index}>
                      <span className="astra-badge">{rule.kind}</span>
                      <code>{rule.value}</code>
                    </div>
                  ))}
                </div>
                {scopeModal.kind !== "create" && (
                  <label>
                    Reason
                    <input
                      name="company_scope_reason"
                      value={scopeModal.reason}
                      onChange={(event) => setModal({ ...scopeModal, reason: event.target.value })}
                    />
                  </label>
                )}
              </fieldset>
              <div className="astra-form-actions">
                <button type="button" disabled={busy} onClick={() => setModal(undefined)}>
                  Cancel
                </button>
                <button
                  className="is-primary"
                  type="submit"
                  disabled={
                    busy || parsed.errors.length > 0 || (scopeModal.kind === "create" && !scopeModal.name.trim())
                  }
                >
                  Save company scope
                </button>
              </div>
            </form>
          ) : null}
        </EditorDialog>
      )}
      {!ready && !loading && !failures.some(Boolean) && (
        <p className="astra-form-note">Refresh to load asset metadata.</p>
      )}
    </div>
  );
}
