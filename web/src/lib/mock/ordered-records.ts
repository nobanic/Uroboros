/** A runtime owns its cloned rows, stable numeric identities and insertion order.
 * Reserving an identity precedes row construction: a failed creation still consumes that identity.
 */
export class OrderedRecords<Row extends { id: number }> {
  readonly rows: Row[];
  private readonly identities: Map<number, Row>;
  private next: number;

  constructor(seed: readonly Row[]) {
    this.rows = structuredClone(seed) as Row[];
    this.identities = new Map(this.rows.map((row) => [row.id, row]));
    if (this.identities.size !== this.rows.length) throw new Error("Duplicate demonstration record identity");
    this.next = Math.max(0, ...this.rows.map((row) => row.id)) + 1;
  }

  get(id: number): Row | undefined {
    return this.identities.get(id);
  }

  append(create: (id: number) => Row): Row {
    const id = this.next++;
    const row = create(id);
    if (row.id !== id) throw new Error("Demonstration record identity changed during creation");
    this.rows.push(row);
    this.identities.set(id, row);
    return row;
  }

  replace(id: number, row: Row): void {
    const previous = this.get(id);
    if (!previous || row.id !== id) throw new Error("Demonstration record replacement changed its identity");
    this.rows[this.rows.indexOf(previous)] = row;
    this.identities.set(id, row);
  }

  remove(id: number): void {
    const row = this.get(id);
    if (!row) return;
    this.rows.splice(this.rows.indexOf(row), 1);
    this.identities.delete(id);
  }
}
