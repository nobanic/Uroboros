/** Borrowed demo rows retain array and record identity across related workflows. */
export class DemoRows<T, K extends string | number> {
  constructor(
    readonly rows: T[],
    private readonly key: (row: T) => K,
  ) {}

  get(key: K): T | undefined {
    return this.rows.find((row) => this.key(row) === key);
  }

  remove(key: K): T | undefined {
    const position = this.rows.findIndex((row) => this.key(row) === key);
    return position < 0 ? undefined : this.rows.splice(position, 1)[0];
  }

  rank(order: readonly K[]): void {
    this.rows.sort((left, right) => order.indexOf(this.key(left)) - order.indexOf(this.key(right)));
  }

  nextNumber(): number {
    return this.rows.reduce((maximum, row) => Math.max(maximum, this.key(row) as number), 0) + 1;
  }

  removeWhere(predicate: (row: T) => boolean): number {
    let removed = 0;
    for (let index = this.rows.length - 1; index >= 0; index--) {
      if (!predicate(this.rows[index])) continue;
      this.rows.splice(index, 1);
      removed++;
    }
    return removed;
  }
}

export function patchDemoStrings(target: object, body: Record<string, unknown>, fields: readonly string[]): void {
  for (const field of fields) {
    const value = body[field];
    if (typeof value === "string") Object.assign(target, { [field]: value });
  }
}
