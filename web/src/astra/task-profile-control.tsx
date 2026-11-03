"use client";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { UroIcon } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import type { LLMProfile, Task } from "@/lib/types";
import { EditorDialog } from "./editor-dialog";
import { taskControls, taskProfileChain, taskProfileCursor } from "./task-document";

function ChainEditor({
  task,
  profiles,
  close,
  updated,
}: {
  task: Task;
  profiles: LLMProfile[];
  close: () => void;
  updated: () => void;
}) {
  const initial = taskProfileChain(task),
    cursor = taskProfileCursor(task, profiles);
  const [chain, setChain] = useState(initial),
    [current, choose] = useState(task.active_llm_profile_id ? String(task.active_llm_profile_id) : initial[0] || ""),
    [search, find] = useState(""),
    [busy, setBusy] = useState(false),
    [error, fail] = useState("");
  const alive = useRef(true),
    lock = useRef(false);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  function change(ids: string[]) {
    setChain(ids);
    if (!ids.includes(current)) choose(ids[0] || "");
  }
  function move(index: number, by: number) {
    const next = [...chain];
    [next[index], next[index + by]] = [next[index + by], next[index]];
    change(next);
  }
  async function save() {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    fail("");
    try {
      const result = await api.updateTaskLLMProfiles(task.id, chain.map(Number), current ? Number(current) : undefined);
      if (!alive.current) return;
      if (result.switch_event)
        toast.info(result.switch_event.summary, { id: `task-${task.id}-llm-${result.switch_event.seq}` });
      else
        toast.success(
          result.reopened_intents > 0
            ? `LLM profile updated, and ${result.reopened_intents} quota-blocked intents resumed`
            : "LLM profile updated",
        );
      updated();
      close();
    } catch (error) {
      if (alive.current) fail(`Update failed: ${(error as Error).message}`);
    } finally {
      lock.current = false;
      if (alive.current) setBusy(false);
    }
  }
  const selectedIndex = chain.indexOf(current);
  return (
    <EditorDialog title="Task LLM profile chain" eyebrow="Task model chain" close={close} busy={busy}>
      <p>
        {taskControls(task).terminal
          ? "The task has ended; changes only affect subsequent main Agent conversations."
          : "Changes to the order or the current profile take effect from the next LLM call."}
      </p>
      {cursor.exhausted && (
        <aside className="astra-task-warning">
          <strong>Profile chain quota exhausted</strong>
          <p>
            {task.llm_failover_reason ||
              "All selected profiles were deemed to be out of quota. Saving the profile chain resets the failure state."}
          </p>
        </aside>
      )}
      <fieldset disabled={busy} className="astra-task-chain">
        <legend>Ordered model profiles</legend>
        {chain.length ? (
          <ol>
            {chain.map((id, index) => {
              const profile = profiles.find((p) => String(p.id) === id);
              return (
                <li key={id}>
                  <span className="astra-task-chain-number">{index + 1}</span>
                  <div>
                    <strong>{profile?.name || `Profile #${id}`}</strong>
                    <small>{profile?.model || "Profile no longer available"}</small>
                    <em>
                      {index === selectedIndex
                        ? "Current"
                        : index < selectedIndex
                          ? "Skipped"
                          : `Backup ${index - selectedIndex}`}
                    </em>
                  </div>
                  <button
                    type="button"
                    aria-label={`Move profile ${index + 1} up`}
                    disabled={!index}
                    onClick={() => move(index, -1)}
                  >
                    ↑
                  </button>
                  <button
                    type="button"
                    aria-label={`Move profile ${index + 1} down`}
                    disabled={index === chain.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    ↓
                  </button>
                  <button
                    type="button"
                    aria-label={`Remove profile ${index + 1}`}
                    onClick={() => change(chain.filter((value) => value !== id))}
                  >
                    ×
                  </button>
                </li>
              );
            })}
          </ol>
        ) : (
          <p>When no profile is specified, the task follows the Agent or the global active profile.</p>
        )}
        {chain.length > 0 && (
          <label>
            Current profile
            <select aria-label="Current task model profile" value={current} onChange={(e) => choose(e.target.value)}>
              {chain.map((id) => (
                <option key={id} value={id} disabled={!profiles.some((p) => String(p.id) === id)}>
                  {profiles.find((p) => String(p.id) === id)?.name || `Profile #${id}`}
                </option>
              ))}
            </select>
          </label>
        )}
        <label>
          Find profiles
          <input
            type="search"
            aria-label="Find task model profiles"
            value={search}
            onChange={(e) => find(e.target.value)}
            placeholder="Name or model…"
          />
        </label>
        <div className="astra-task-chain-available">
          {profiles
            .filter(
              (p) =>
                !chain.includes(String(p.id)) &&
                `${p.name} ${p.model}`.toLocaleLowerCase().includes(search.toLocaleLowerCase()),
            )
            .map((p) => (
              <button
                type="button"
                key={p.id}
                aria-label={`Add task profile ${p.id}`}
                onClick={() => change([...chain, String(p.id)])}
              >
                <span>
                  <strong>
                    {p.name}
                    {p.is_default ? " · active default" : ""}
                  </strong>
                  <small>{p.model}</small>
                </span>
                <span>+ Add</span>
              </button>
            ))}
        </div>
        {!profiles.length && <p>No LLM profiles available. Existing profile references are retained.</p>}
      </fieldset>
      {error && (
        <p role="alert" className="astra-task-warning">
          {error}
        </p>
      )}
      <footer className="astra-task-dialog-actions">
        <button type="button" disabled={busy} onClick={close}>
          Close
        </button>
        <button type="button" className="is-primary" disabled={busy} onClick={() => void save()}>
          {busy ? "Saving" : cursor.exhausted ? "Save and reset" : "Save"}
        </button>
      </footer>
    </EditorDialog>
  );
}
export function TaskProfileControl({
  task,
  profiles,
  updated,
}: {
  task: Task;
  profiles: LLMProfile[];
  updated: () => void;
}) {
  const [open, show] = useState(false),
    cursor = taskProfileCursor(task, profiles);
  return (
    <>
      <button
        className="astra-task-profile"
        data-exhausted={cursor.exhausted}
        aria-label="View or switch the task's LLM profile"
        title={[cursor.label, cursor.model, cursor.backup ? `${cursor.backup} backup` : ""].filter(Boolean).join(" · ")}
        onClick={() => show(true)}
      >
        <UroIcon name="llm" />
        <span>{cursor.label}</span>
        {cursor.backup > 0 && <small>+{cursor.backup}</small>}
      </button>
      {open && <ChainEditor task={task} profiles={profiles} close={() => show(false)} updated={updated} />}
    </>
  );
}
