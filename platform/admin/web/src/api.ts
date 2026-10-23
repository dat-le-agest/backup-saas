// Client of the admin service's API (/api/*). The session is an HttpOnly
// cookie: requests only need credentials: "same-origin".

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
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
    throw new ApiError(res.status, e.code ?? "error", e.message ?? res.statusText);
  }
  return json as T;
}

const get = <T>(path: string) => request<{ data: T }>("GET", path).then((r) => r.data);

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

// ---- session ----

export const api = {
  login: (email: string, password: string) =>
    request<{ data: Admin }>("POST", "/api/auth/login", { email, password }).then((r) => r.data),
  logout: () => request<void>("POST", "/api/auth/logout", {}),
  me: () => get<Admin>("/api/me"),
  config: () => get<{ grafana_url: string }>("/api/config"),

  admins: () => get<Admin[]>("/api/admins"),
  createAdmin: (email: string, password: string) =>
    request<{ data: Admin }>("POST", "/api/admins", { email, password }).then((r) => r.data),

  // ---- apps ----
  apps: () => get<AppStatus[]>("/api/apps"),
  app: (app: string) => get<AppStatus>(`/api/apps/${app}`),

  users: (app: string, q: string, page: number) =>
    request<List<User>>("GET", `/api/apps/${app}/admin/users?${new URLSearchParams({ q, page: String(page), limit: "20" })}`),
  user: (app: string, id: string) => get<User>(`/api/apps/${app}/admin/users/${encodeURIComponent(id)}`),
  setDisabled: (app: string, id: string, disabled: boolean) =>
    request<{ data: User }>("PUT", `/api/apps/${app}/admin/users/${encodeURIComponent(id)}/disabled`, { disabled }).then((r) => r.data),
  setPassword: (app: string, id: string, password: string) =>
    request<void>("PUT", `/api/apps/${app}/admin/users/${encodeURIComponent(id)}/password`, { password }),

  tenants: (app: string, q: string, page: number) =>
    request<List<Tenant>>("GET", `/api/apps/${app}/admin/tenants?${new URLSearchParams({ q, page: String(page), limit: "20" })}`),
  tenant: (app: string, id: string) => get<Tenant>(`/api/apps/${app}/admin/tenants/${encodeURIComponent(id)}`),
  deleteTenant: (app: string, id: string) => request<void>("DELETE", `/api/apps/${app}/admin/tenants/${encodeURIComponent(id)}`),
  setRole: (app: string, tenant: string, user: string, role: string) =>
    request<{ data: Member }>("PATCH", `/api/apps/${app}/admin/tenants/${encodeURIComponent(tenant)}/members/${encodeURIComponent(user)}`, { role }).then((r) => r.data),
  removeMember: (app: string, tenant: string, user: string) =>
    request<void>("DELETE", `/api/apps/${app}/admin/tenants/${encodeURIComponent(tenant)}/members/${encodeURIComponent(user)}`),
};

export function formatDate(iso?: string): string {
  return iso ? new Date(iso).toLocaleString() : "—";
}
