import type { Rule, TypeDef } from "./api";

// How the dashboard reads a model: relations holding only assignable subjects
// are stored relationships, can_* are permissions, the rest derived roles.
export type RelationKind = "stored" | "permission" | "derived";

export function relationKind(name: string, rule: Rule): RelationKind {
  if (name.startsWith("can_")) return "permission";
  return rule.assignable ? "stored" : "derived";
}

// assignableTypes: the subject types a relation accepts in stored tuples.
export function assignableTypes(rule: Rule): string[] {
  const out = new Set<string>();
  const walk = (r: Rule) => {
    r.assignable?.forEach((s) => out.add(s));
    r.union?.forEach(walk);
    r.intersection?.forEach(walk);
    if (r.exclude) walk(r.exclude.base);
  };
  walk(rule);
  return [...out];
}

export function relationsOf(types: Record<string, TypeDef> | undefined, type: string): string[] {
  return Object.keys(types?.[type]?.relations ?? {}).sort();
}
