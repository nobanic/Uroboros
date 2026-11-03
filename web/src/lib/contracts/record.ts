// Wire facets describe existing JSON fields. They neither decode values nor
// confer ownership, visibility or execution authority.
export interface CatalogEntry<Identifier extends string | number = number> {
  id: Identifier;
  name: string;
}

export interface RecordTimestamps {
  created_at: string;
  updated_at: string;
}

export interface SourceLineage<Identifier extends string | number = string> {
  source_task_id?: Identifier;
  inherited?: boolean;
}

export interface PageItems<Item> {
  items: Item[];
  total: number;
  page: number;
}

export interface BatchOutcome {
  ok: boolean;
  error?: string;
}
