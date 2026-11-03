"use client";

import { useId } from "react";
import { changeField, fieldValue, type FormGroup, type FormValues } from "./forms";
import "./forms.css";

export function FormFields({
  groups,
  value,
  onChange,
  disabled,
}: {
  groups: FormGroup[];
  value: FormValues;
  onChange: (next: FormValues) => void;
  disabled?: boolean;
}) {
  const prefix = useId();
  return (
    <div className="astra-form-groups">
      {groups.map((group) => (
        <fieldset key={`${group.title}:${group.fields.map((field) => field.key).join(",")}`} disabled={disabled}>
          <legend>{group.title}</legend>
          {group.description && <p>{group.description}</p>}
          <div className="astra-form-grid">
            {group.fields
              .filter((field) => !field.when || field.when(value))
              .map((field) => {
                const id = `${prefix}-${field.key}`,
                  current = fieldValue(value, field.key);
                const common = {
                  id,
                  name: field.key,
                  required: field.required,
                  disabled: disabled || field.disabled,
                  "aria-describedby": field.hint ? `${id}-help` : undefined,
                };
                const edit = (next: unknown) => onChange(changeField(value, field.key, next));
                return (
                  <div
                    className="astra-form-field"
                    key={field.key}
                    data-wide={field.type === "textarea" || field.type === "list"}
                    data-check={field.type === "checkbox"}
                  >
                    <label htmlFor={id} id={`${id}-label`}>
                      {field.label}
                    </label>
                    {field.options ? (
                      <select {...common} value={String(current ?? "")} onChange={(event) => edit(event.target.value)}>
                        {field.options.map(([key, title]) => (
                          <option key={key} value={key}>
                            {title}
                          </option>
                        ))}
                      </select>
                    ) : field.type === "textarea" ? (
                      <textarea
                        {...common}
                        value={String(current ?? "")}
                        onChange={(event) => edit(event.target.value)}
                      />
                    ) : field.type === "checkbox" ? (
                      <input
                        {...common}
                        type="checkbox"
                        checked={Boolean(current)}
                        onChange={(event) => edit(event.target.checked)}
                      />
                    ) : field.type === "list" ? (
                      <div className="astra-form-list" role="group" id={id} aria-labelledby={`${id}-label`}>
                        {(Array.isArray(current) ? current : []).map((item, index, items) => (
                          <div key={index}>
                            <input
                              aria-label={`${field.label} ${index + 1}`}
                              name={`${field.key}.${index}`}
                              value={String(item)}
                              onChange={(event) =>
                                edit(items.map((value, position) => (position === index ? event.target.value : value)))
                              }
                            />
                            <button
                              type="button"
                              disabled={index === 0}
                              aria-label={`Move ${field.label.toLowerCase()} ${index + 1} up`}
                              onClick={() => {
                                const next = [...items];
                                [next[index - 1], next[index]] = [next[index], next[index - 1]];
                                edit(next);
                              }}
                            >
                              ↑
                            </button>
                            <button
                              type="button"
                              disabled={index === items.length - 1}
                              aria-label={`Move ${field.label.toLowerCase()} ${index + 1} down`}
                              onClick={() => {
                                const next = [...items];
                                [next[index + 1], next[index]] = [next[index], next[index + 1]];
                                edit(next);
                              }}
                            >
                              ↓
                            </button>
                            <button
                              type="button"
                              aria-label={`Remove ${field.label.toLowerCase()} ${index + 1}`}
                              onClick={() => edit(items.filter((_, position) => position !== index))}
                            >
                              ×
                            </button>
                          </div>
                        ))}
                        <button type="button" onClick={() => edit([...(Array.isArray(current) ? current : []), ""])}>
                          Add {field.label.toLowerCase()}
                        </button>
                      </div>
                    ) : (
                      <input
                        {...common}
                        type={field.type ?? "text"}
                        value={String(current ?? "")}
                        min={field.min}
                        max={field.max}
                        step={field.step}
                        autoComplete={field.type === "password" ? "new-password" : "off"}
                        onChange={(event) => edit(event.target.value)}
                      />
                    )}
                    {field.hint && <small id={`${id}-help`}>{field.hint}</small>}
                  </div>
                );
              })}
          </div>
        </fieldset>
      ))}
    </div>
  );
}
