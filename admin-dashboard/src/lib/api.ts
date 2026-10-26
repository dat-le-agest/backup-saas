// Client of the admin service's API (/api/*). The session is an HttpOnly
// cookie: requests only need credentials: "same-origin". Per-app calls go
// through /api/apps/:app/admin/*, forwarded to the app's admin API with its key.

export class ApiError extends Error {
  status: number;
  code: string;
  details: { field: string; description: string }[];
  constructor(status: number, code: string, message: string, details: { field: string; description: string }[] = []) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

export type Paging = { page: number; limit: number; total: number };
export type List<T> = { data: T[]; paging: Paging };

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return undefined as T;
  const json = await res.json().catch(() => ({}));
  if (!res.ok) {
    const e = json.error ?? {};
    throw new ApiError(res.status, e.code ?? "error", e.message ?? res.statusText, e.details);
  }
  return json as T;
}

const get = <T>(path: string) => request<{ data: T }>("GET", path).then((r) => r.data);
const send = <T>(method: string, path: string, body?: unknown) => request<{ data: T }>(method, path, body).then((r) => r?.data);
const qs = (params: Record<string, string | number | undefined>) =>
  new URLSearchParams(Object.entries(params).filter(([, v]) => v !== undefined && v !== "") as [string, string][]).toString();
const enc = encodeURIComponent;

// ---- types ----

export type Admin = { id: string; email: string; last_login_at?: string; created_at: string };

export type AppStatus = {
  id: string;
  name: string;
  public_url: string;
  up: boolean;
  error?: string;
  latency_ms: number;
  users: number;
  tenants: number;
};

export type User = {
  id: string;
  email: string;
  disabled: boolean;
  disabled_at?: string;
  created_at: string;
  memberships?: { tenant_id: string; role: string; created_at: string }[];
};

export type Member = { user_id: string; email: string; role: string; created_at: string; updated_at: string };

export type Tenant = { id: string; name: string; members: number; created_at: string; member_list?: Member[] };

export const roles = ["owner", "admin", "member", "viewer"] as const;

// ---- authorization (core/authz/rebac AdminHandler) ----

export type Rule = {
  assignable?: string[];
  computed?: string;
  from?: { relation: string; via: string };
  union?: Rule[];
  intersection?: Rule[];
  exclude?: { base: Rule; subtract: Rule };
};
export type TypeDef = { relations?: Record<string, Rule> };

export type AuthzModel = {
  id: string;
  schema_version: string;
  is_active: boolean;
  created_by: string;
  created_at: string;
  source?: string;
  types?: Record<string, TypeDef>;
};

export type Tuple = { object: string; relation: string; subject: string; tenant_id: string };
export type TupleFilter = { tenant_id?: string; object?: string; relation?: string; subject?: string };
export type Change = Tuple & { seq: number; operation: string; actor: string; changed_at: string };

export const api = {
  // ---- session ----
  login: (email: string, password: string) => send<Admin>("POST", "/api/auth/login", { email, password }),
  logout: () => request<void>("POST", "/api/auth/logout", {}),
  me: () => get<Admin>("/api/me"),
  config: () => get<{ grafana_url: string }>("/api/config"),

  admins: () => get<Admin[]>("/api/admins"),
  createAdmin: (email: string, password: string) => send<Admin>("POST", "/api/admins", { email, password }),

  // ---- apps ----
  apps: () => get<AppStatus[]>("/api/apps"),
  app: (app: string) => get<AppStatus>(`/api/apps/${app}`),

  users: (app: string, q: string, page: number) => request<List<User>>("GET", `/api/apps/${app}/admin/users?${qs({ q, page, limit: 20 })}`),
  user: (app: string, id: string) => get<User>(`/api/apps/${app}/admin/users/${enc(id)}`),
  setDisabled: (app: string, id: string, disabled: boolean) => send<User>("PUT", `/api/apps/${app}/admin/users/${enc(id)}/disabled`, { disabled }),
  setPassword: (app: string, id: string, password: string) => request<void>("PUT", `/api/apps/${app}/admin/users/${enc(id)}/password`, { password }),

  tenants: (app: string, q: string, page: number) => request<List<Tenant>>("GET", `/api/apps/${app}/admin/tenants?${qs({ q, page, limit: 20 })}`),
  tenant: (app: string, id: string) => get<Tenant>(`/api/apps/${app}/admin/tenants/${enc(id)}`),
  deleteTenant: (app: string, id: string) => request<void>("DELETE", `/api/apps/${app}/admin/tenants/${enc(id)}`),
  setRole: (app: string, tenant: string, user: string, role: string) =>
    send<Member>("PATCH", `/api/apps/${app}/admin/tenants/${enc(tenant)}/members/${enc(user)}`, { role }),
  removeMember: (app: string, tenant: string, user: string) => request<void>("DELETE", `/api/apps/${app}/admin/tenants/${enc(tenant)}/members/${enc(user)}`),

  // ---- authorization engines of an app's services ----
  authzServices: (app: string) => get<string[]>(`/api/apps/${app}/admin/authz`),
  models: (app: string, svc: string) => get<AuthzModel[]>(`/api/apps/${app}/admin/authz/${svc}/models`),
  model: (app: string, svc: string, id = "active") => get<AuthzModel>(`/api/apps/${app}/admin/authz/${svc}/models/${enc(id)}`),
  activateModel: (app: string, svc: string, id: string) => request<void>("POST", `/api/apps/${app}/admin/authz/${svc}/models/${enc(id)}/activate`, {}),
  tuples: (app: string, svc: string, f: TupleFilter, page: number) =>
    request<List<Tuple>>("GET", `/api/apps/${app}/admin/authz/${svc}/tuples?${qs({ ...f, page, limit: 50 })}`),
  writeTuples: (app: string, svc: string, writes: Tuple[], deletes: Tuple[] = []) =>
    request<void>("POST", `/api/apps/${app}/admin/authz/${svc}/tuples`, { writes, deletes }),
  check: (app: string, svc: string, body: { object: string; relation: string; subject: string; contextual?: Tuple[] }) =>
    send<{ allowed: boolean }>("POST", `/api/apps/${app}/admin/authz/${svc}/check`, body),
  listObjects: (app: string, svc: string, body: { type: string; relation: string; subject: string; contextual?: Tuple[] }) =>
    send<{ objects: string[] }>("POST", `/api/apps/${app}/admin/authz/${svc}/list-objects`, body),
  changes: (app: string, svc: string, f: { tenant_id?: string; object_type?: string }, before: number) =>
    request<{ data: Change[]; paging: { next: number } }>("GET", `/api/apps/${app}/admin/authz/${svc}/changes?${qs({ ...f, before_seq: before || undefined, limit: 50 })}`),

  openapi: async (app: string): Promise<string> => {
    const res = await fetch(`/api/apps/${enc(app)}/admin/openapi`, { credentials: "same-origin" });
    if (!res.ok) {
      throw new Error(`Failed to load OpenAPI spec: ${res.statusText}`);
    }
    return res.text();
  },

  // ---- outbox / events ----
  outboxServices: (app: string) => get<string[]>(`/api/apps/${enc(app)}/admin/outbox`),
  outboxStats: (app: string, svc: string) => get<OutboxStats>(`/api/apps/${enc(app)}/admin/outbox/${enc(svc)}/stats`),
  outboxMessages: (app: string, svc: string, status: string, page: number) =>
    request<List<OutboxMessage>>(
      "GET",
      `/api/apps/${enc(app)}/admin/outbox/${enc(svc)}/messages?${qs({ status: status === "all" ? undefined : status, page, limit: 20 })}`,
    ),
  retryOutbox: (app: string, svc: string, id: number) =>
    request<void>("POST", `/api/apps/${enc(app)}/admin/outbox/${enc(svc)}/messages/${id}/retry`),
  retryAllOutbox: (app: string, svc: string) =>
    request<{ retried: number }>("POST", `/api/apps/${enc(app)}/admin/outbox/${enc(svc)}/retry-all`),
};

export type OutboxMessage = {
  id: number;
  topic: string;
  key: string;
  payload: string;
  headers: string;
  attempts: number;
  next_attempt_at: string;
  last_error: string;
  created_at: string;
  processed_at?: string;
  failed_at?: string;
  status: "pending" | "failed" | "processed";
};

export type OutboxStats = {
  total: number;
  pending: number;
  processed: number;
  failed: number;
};

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    const details = error.details?.map((d) => `${d.field}: ${d.description}`).join("; ");
    return details ? `${error.message} (${details})` : error.message;
  }
  return error instanceof Error ? error.message : String(error);
}
