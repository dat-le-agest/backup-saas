import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, AppStatus } from "../api";
import { useSession } from "../App";
import { ErrorBox } from "../components/ui";

export default function Apps() {
  const { grafanaURL } = useSession();
  const [apps, setApps] = useState<AppStatus[] | null>(null);
  const [error, setError] = useState<unknown>(null);

  const load = useCallback(() => {
    api.apps().then(setApps, setError);
  }, []);
  useEffect(() => {
    load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, [load]);

  return (
    <>
      <div className="page-head">
        <h2>Apps</h2>
        <button className="ghost" onClick={load}>
          Refresh
        </button>
      </div>
      <ErrorBox error={error} />
      {!apps ? (
        <p className="muted">Loading…</p>
      ) : apps.length === 0 ? (
        <p className="muted">No app configured (ADMIN_APPS).</p>
      ) : (
        <div className="grid">
          {apps.map((a) => (
            <div key={a.id} className="card app">
              <div className="app-head">
                <span className={`dot ${a.up ? "up" : "down"}`} title={a.up ? "up" : "down"} />
                <Link to={`/apps/${a.id}`} className="app-name">
                  {a.name}
                </Link>
                <span className="muted small">{a.id}</span>
              </div>
              {a.up ? (
                <div className="stats">
                  <div>
                    <b>{a.users}</b>
                    <span>users</span>
                  </div>
                  <div>
                    <b>{a.tenants}</b>
                    <span>tenants</span>
                  </div>
                  <div>
                    <b>{a.latency_ms}</b>
                    <span>ms</span>
                  </div>
                </div>
              ) : (
                <div className="error small">{a.error || "unreachable"}</div>
              )}
              <div className="links small">
                {a.public_url && <span className="muted">{a.public_url}</span>}
                {grafanaURL && (
                  <a href={`${grafanaURL}/d/saas-overview?var-app=${a.id}`} target="_blank" rel="noreferrer">
                    Grafana
                  </a>
                )}
                <Link to={`/apps/${a.id}`}>Manage →</Link>
              </div>
            </div>
          ))}
        </div>
      )}
    </>
  );
}
