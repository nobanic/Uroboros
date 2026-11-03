export type FormValues = Record<string, unknown>;
export type FormField = {
  key: string;
  label: string;
  type?: "text" | "password" | "number" | "checkbox" | "textarea" | "list";
  options?: ReadonlyArray<readonly [string, string]>;
  hint?: string;
  required?: boolean;
  disabled?: boolean;
  min?: number;
  max?: number;
  step?: string | number;
  when?: (values: FormValues) => boolean;
};
export type FormGroup = { title: string; description?: string; fields: FormField[] };

export function fieldValue(values: FormValues, path: string): unknown {
  let current: unknown = values;
  for (const part of path.split(".")) {
    if (!current || typeof current !== "object") return undefined;
    current = (current as FormValues)[part];
  }
  return current;
}

// Clone only the edited branch, retaining the other settings and intermediate
// text values (including an empty number field) until validation on submit.
export function changeField(values: FormValues, path: string, value: unknown): FormValues {
  const [head, ...tail] = path.split(".");
  if (!head || ["__proto__", "prototype", "constructor"].includes(head)) throw new Error("Invalid form field");
  if (!tail.length) return { ...values, [head]: value };
  const previous = values[head];
  return {
    ...values,
    [head]: changeField(
      previous && typeof previous === "object" ? (previous as FormValues) : {},
      tail.join("."),
      value,
    ),
  };
}

export function formDocument(groups: FormGroup[], values: FormValues): FormValues {
  let result: FormValues = {};
  for (const group of groups)
    for (const field of group.fields) {
      if (field.when && !field.when(values)) continue;
      const original = fieldValue(values, field.key);
      const value =
        field.type === "list"
          ? Array.isArray(original)
            ? original.map(String)
            : []
          : field.type === "checkbox"
            ? Boolean(original)
            : field.type === "number"
              ? Number(original ?? 0)
              : String(original ?? "");
      if (field.required && !String(value).trim()) throw new Error(`${field.label} is required`);
      if (
        typeof value === "number" &&
        (!Number.isFinite(value) ||
          (field.min !== undefined && value < field.min) ||
          (field.max !== undefined && value > field.max))
      )
        throw new Error(`${field.label} is outside the supported range`);
      if (field.options && !field.options.some(([option]) => option === value))
        throw new Error(`Choose a valid ${field.label.toLowerCase()}`);
      result = changeField(result, field.key, value);
    }
  return result;
}
