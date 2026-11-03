import type { Task, TaskCategory, TaskTemplate } from "../types";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";

export const taskLabelName = (value: unknown) =>
  String(value ?? "")
    .trim()
    .split(/\s+/)
    .join(" ");
const invalidLabel = () => new Error("uroboros.lab demonstration record.");
const nextLabelID = (items: { id: number }[]) => items.reduce((maximum, item) => Math.max(maximum, item.id), 0) + 1;

/** Shared task/category assignment and template edits keep their established live array references. */
export class TaskLabels {
  constructor(
    readonly categories: TaskCategory[],
    readonly templates: TaskTemplate[],
    private readonly tasks: () => Task[],
  ) {}

  snapshot() {
    return this.categories.map((category) => ({
      ...category,
      task_count: this.tasks().filter((task) => task.category_id === category.id).length,
    }));
  }
  category(value: unknown) {
    if (typeof value !== "number") return undefined;
    const category = this.categories.find((item) => item.id === value);
    if (!category) throw invalidLabel();
    return category;
  }
  private assign(task: Task, category?: TaskCategory) {
    task.category_id = category?.id;
    task.category_name = category?.name;
  }
  private categoryName(value: unknown, except?: number) {
    const name = taskLabelName(value);
    if (!name || this.categories.some((item) => item.id !== except && item.name.toLowerCase() === name.toLowerCase()))
      throw invalidLabel();
    return name;
  }
  private createCategory(b: Record<string, unknown>) {
    const name = this.categoryName(b.name),
      now = new Date().toISOString();
    const category: TaskCategory = {
      id: nextLabelID(this.categories),
      name,
      task_count: 0,
      created_at: now,
      updated_at: now,
    };
    this.categories.push(category);
    return category;
  }
  private renameCategory(id: number, b: Record<string, unknown>) {
    const category = this.category(id);
    if (!category) throw invalidLabel();
    category.name = this.categoryName(b.name, category.id);
    category.updated_at = new Date().toISOString();
    for (const task of this.tasks()) if (task.category_id === id) task.category_name = category.name;
    return { ...category, task_count: this.tasks().filter((task) => task.category_id === id).length };
  }
  private deleteCategory(id: number) {
    const index = this.categories.findIndex((item) => item.id === id);
    if (index < 0) throw invalidLabel();
    this.categories.splice(index, 1);
    for (const task of this.tasks()) if (task.category_id === id) this.assign(task);
    return { deleted: id };
  }
  private assignBatch(b: Record<string, unknown>) {
    const requested = Array.isArray(b.task_ids) ? b.task_ids.map(String) : [];
    const ids = [...new Set(requested)];
    if (!ids.length || ids.length > 100)
      throw new Error("uroboros.lab demonstration record. Context: task_ids, 1-100.");
    const category = this.category(b.category_id);
    const items = ids.map((id) => {
      const task = this.tasks().find((item) => item.id === id);
      if (!task) return { id, ok: false, error: "task not found" };
      this.assign(task, category);
      return { id, ok: true };
    });
    return { items, category: category ? this.snapshot().find((item) => item.id === category.id) : null };
  }
  private assignOne(id: string, b: Record<string, unknown>) {
    const task = this.tasks().find((item) => item.id === id);
    if (!task) throw invalidLabel();
    this.assign(task, this.category(b.category_id));
    return task;
  }
  private templateFields(name: string, description: string, goal: string, except?: number) {
    if (
      !name ||
      !description ||
      !goal ||
      this.templates.some((item) => item.id !== except && taskLabelName(item.name).toLowerCase() === name.toLowerCase())
    )
      throw invalidLabel();
    return { name, description, goal };
  }
  private createTemplate(b: Record<string, unknown>) {
    const now = new Date().toISOString();
    const values = this.templateFields(
      taskLabelName(b.name),
      String(b.description ?? "").trim(),
      String(b.goal ?? "").trim(),
    );
    const template: TaskTemplate = { id: nextLabelID(this.templates), ...values, created_at: now, updated_at: now };
    this.templates.unshift(template);
    return template;
  }
  private updateTemplate(id: number, b: Record<string, unknown>) {
    const template = this.templates.find((item) => item.id === id);
    if (!template) return {};
    const values = this.templateFields(
      typeof b.name === "string" ? taskLabelName(b.name) : template.name,
      typeof b.description === "string" ? b.description.trim() : template.description,
      typeof b.goal === "string" ? b.goal.trim() : template.goal,
      id,
    );
    Object.assign(template, values, { updated_at: new Date().toISOString() });
    this.templates.sort((left, right) => right.updated_at.localeCompare(left.updated_at) || right.id - left.id);
    return template;
  }
  private deleteTemplate(id: number) {
    const index = this.templates.findIndex((item) => item.id === id);
    if (index >= 0) this.templates.splice(index, 1);
    return { deleted: id };
  }
  respond({ method, path, segments, body }: DemoRequest): unknown {
    if (path === "/task-categories") {
      if (method === "GET") return { categories: this.snapshot() };
      if (method === "POST") return this.createCategory(body);
    }
    if (segments[0] === "task-categories" && segments.length === 2) {
      if (method === "PATCH") return this.renameCategory(Number(segments[1]), body);
      if (method === "DELETE") return this.deleteCategory(Number(segments[1]));
    }
    if (path === "/tasks/category/batch" && method === "POST") return this.assignBatch(body);
    if (segments[0] === "tasks" && segments[2] === "category" && segments.length === 3 && method === "PATCH")
      return this.assignOne(segments[1], body);
    if (path === "/task-templates") {
      if (method === "GET") return { templates: this.templates };
      if (method === "POST") return this.createTemplate(body);
    }
    if (segments[0] === "task-templates" && segments.length === 2) {
      if (method === "PATCH") return this.updateTemplate(Number(segments[1]), body);
      if (method === "DELETE") return this.deleteTemplate(Number(segments[1]));
    }
    return UNHANDLED;
  }
}
