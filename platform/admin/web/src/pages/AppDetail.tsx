import { FormEvent, useCallback, useEffect, useState } from "react";
import { Link, NavLink, Navigate, Route, Routes, useParams } from "react-router-dom";
import { api, AppStatus, formatDate, List, roles, Tenant, User } from "../api";
import { ErrorBox, Pager, RoleBadge } from "../components/ui";

export default function AppDetail() {
  const { app = "" } = useParams();
  const [status, setStatus] = useState<AppStatus | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    api.app(app).then(setStatus, setError);
  }, [app]);

  return (
    <>
      <div className="page-head">
        <h2>
          <Link to="/" className="muted">
            Apps
          </Link>{" "}
          / {status?.name ?? app}
        </h2>
        {status && (
          <span className={`pill ${status.up ? "up" : "down"}`}>
            {status.up ? `up · ${status.latency_ms} ms` : "down"}
          </span>
        )}
      </div>
      <ErrorBox error={error} />
      {status && !status.up && <div className="error box">{status.error}</div>}
      <nav className="tabs">
        <NavLink to="users">Users</NavLink>
        <NavLink to="tenants">Tenants</NavLink>
      </nav>
      <Routes>
        <Route index element={<Navigate to="users" replace />} />
        <Route path="users" element={<Users app={app} />} />
        <Route path="tenants" element={<Tenants app={app} />} />
      </Routes>
    </>
  );
}

// ---- users ----

function Users({ app }: { app: string }) {
  const [q, setQ] = useState("");
  const [page, setPage] = useState(1);
  const [list, setList] = useState<List<User> | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [selected, setSelected] = useState<string | null>(null);

  const load = useCallback(() => {
    api.users(app, q, page).then(setList, setError);
  }, [app, q, page]);
  useEffect(() => {
    const t = setTimeout(load, 200); // debounce typing
    return () => clearTimeout(t);
  }, [load]);

  return (
    <div className="split">
      <div className="main-col">
        <input className="search" placeholder="Search by email or id" value={q} onChange={(e) => (setQ(e.target.value), setPage(1))} />
        <ErrorBox error={error} />
        <table>
          <thead>
            <tr>
              <th>Email</th>
              <th>Id</th>
              <th>Status</th>
              <th>Created</th>
            </tr>
          </thead>
          <tbody>
            {list?.data.map((u) => (
              <tr key={u.id} className={selected === u.id ? "selected" : ""} onClick={() => setSelected(u.id)}>
                <td>{u.email || <span className="muted">(no email)</span>}</td>
                <td className="mono small">{u.id}</td>
                <td>{u.disabled ? <span className="badge disabled">disabled</span> : <span className="badge ok">active</span>}</td>
                <td>{formatDate(u.created_at)}</td>
              </tr>
            ))}
            {list?.data.length === 0 && (
              <tr>
                <td colSpan={4} className="muted">
                  No users.
                </td>
              </tr>
            )}
          </tbody>
        </table>
        <Pager paging={list?.paging} onPage={setPage} />
      </div>
      {selected && <UserPanel app={app} id={selected} onChange={load} onClose={() => setSelected(null)} />}
    </div>
  );
}

function UserPanel({ app, id, onChange, onClose }: { app: string; id: string; onChange: () => void; onClose: () => void }) {
  const [user, setUser] = useState<User | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [password, setPassword] = useState("");
  const [notice, setNotice] = useState("");

  const load = useCallback(() => {
    setNotice("");
    api.user(app, id).then(setUser, setError);
  }, [app, id]);
  useEffect(load, [load]);

  const toggle = async () => {
    if (!user) return;
    const disable = !user.disabled;
    if (disable && !confirm(`Disable ${user.email || user.id}? Their sessions end now.`)) return;
    try {
      await api.setDisabled(app, id, disable);
      load();
      onChange();
    } catch (e) {
      setError(e);
    }
  };

  const reset = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await api.setPassword(app, id, password);
      setPassword("");
      setNotice("Password changed; the user's sessions were ended.");
    } catch (err) {
      setError(err);
    }
  };

  return (
    <aside className="panel">
      <div className="panel-head">
        <h3>{user?.email || id}</h3>
        <button className="ghost" onClick={onClose}>
          ✕
        </button>
      </div>
      <ErrorBox error={error} />
      {notice && <div className="notice">{notice}</div>}
      {user && (
        <>
          <dl>
            <dt>Id</dt>
            <dd className="mono">{user.id}</dd>
            <dt>Created</dt>
            <dd>{formatDate(user.created_at)}</dd>
            <dt>Status</dt>
            <dd>{user.disabled ? `disabled since ${formatDate(user.disabled_at)}` : "active"}</dd>
          </dl>
          <button className={user.disabled ? "" : "danger"} onClick={toggle}>
            {user.disabled ? "Enable user" : "Disable user"}
          </button>

          <h4>Tenants</h4>
          {user.memberships?.length ? (
            <ul className="plain">
              {user.memberships.map((m) => (
                <li key={m.tenant_id}>
                  <span className="mono small">{m.tenant_id}</span> <RoleBadge role={m.role} />
                </li>
              ))}
            </ul>
          ) : (
            <p className="muted">No tenant.</p>
          )}

          {user.email && (
            <form onSubmit={reset} className="stack">
              <h4>Set a new password</h4>
              <input type="password" placeholder="10+ characters" value={password} onChange={(e) => setPassword(e.target.value)} minLength={10} required />
              <button type="submit">Set password</button>
            </form>
          )}
        </>
      )}
    </aside>
  );
}

// ---- tenants ----

function Tenants({ app }: { app: string }) {
  const [q, setQ] = useState("");
  const [page, setPage] = useState(1);
  const [list, setList] = useState<List<Tenant> | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [selected, setSelected] = useState<string | null>(null);

  const load = useCallback(() => {
    api.tenants(app, q, page).then(setList, setError);
  }, [app, q, page]);
  useEffect(() => {
    const t = setTimeout(load, 200);
    return () => clearTimeout(t);
  }, [load]);

  return (
    <div className="split">
      <div className="main-col">
        <input className="search" placeholder="Search by name or id" value={q} onChange={(e) => (setQ(e.target.value), setPage(1))} />
        <ErrorBox error={error} />
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Id</th>
              <th>Members</th>
              <th>Created</th>
            </tr>
          </thead>
          <tbody>
            {list?.data.map((t) => (
              <tr key={t.id} className={selected === t.id ? "selected" : ""} onClick={() => setSelected(t.id)}>
                <td>{t.name}</td>
                <td className="mono small">{t.id}</td>
                <td>{t.members}</td>
                <td>{formatDate(t.created_at)}</td>
              </tr>
            ))}
            {list?.data.length === 0 && (
              <tr>
                <td colSpan={4} className="muted">
                  No tenants.
                </td>
              </tr>
            )}
          </tbody>
        </table>
        <Pager paging={list?.paging} onPage={setPage} />
      </div>
      {selected && (
        <TenantPanel
          app={app}
          id={selected}
          onChange={load}
          onClose={() => setSelected(null)}
          onDeleted={() => (setSelected(null), load())}
        />
      )}
    </div>
  );
}

function TenantPanel(props: { app: string; id: string; onChange: () => void; onClose: () => void; onDeleted: () => void }) {
  const { app, id, onChange, onClose, onDeleted } = props;
  const [tenant, setTenant] = useState<Tenant | null>(null);
  const [error, setError] = useState<unknown>(null);

  const load = useCallback(() => {
    setError(null);
    api.tenant(app, id).then(setTenant, setError);
  }, [app, id]);
  useEffect(load, [load]);

  const run = async (f: () => Promise<unknown>) => {
    try {
      await f();
      load();
      onChange();
    } catch (e) {
      setError(e);
    }
  };

  const remove = async () => {
    if (!tenant || !confirm(`Delete tenant "${tenant.name}"? Every member loses access now.`)) return;
    try {
      await api.deleteTenant(app, id);
      onDeleted();
    } catch (e) {
      setError(e);
    }
  };

  return (
    <aside className="panel">
      <div className="panel-head">
        <h3>{tenant?.name ?? id}</h3>
        <button className="ghost" onClick={onClose}>
          ✕
        </button>
      </div>
      <ErrorBox error={error} />
      {tenant && (
        <>
          <dl>
            <dt>Id</dt>
            <dd className="mono">{tenant.id}</dd>
            <dt>Created</dt>
            <dd>{formatDate(tenant.created_at)}</dd>
          </dl>
          <h4>Members</h4>
          <table className="compact">
            <tbody>
              {tenant.member_list?.map((m) => (
                <tr key={m.user_id}>
                  <td>
                    {m.email || <span className="mono small">{m.user_id}</span>}
                  </td>
                  <td>
                    <select value={m.role} onChange={(e) => run(() => api.setRole(app, id, m.user_id, e.target.value))}>
                      {roles.map((r) => (
                        <option key={r}>{r}</option>
                      ))}
                    </select>
                  </td>
                  <td>
                    <button
                      className="ghost small"
                      onClick={() => confirm(`Remove ${m.email || m.user_id}?`) && run(() => api.removeMember(app, id, m.user_id))}
                    >
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <button className="danger" onClick={remove}>
            Delete tenant
          </button>
        </>
      )}
    </aside>
  );
}
