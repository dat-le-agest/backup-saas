import { createContext, useContext, useEffect, useState } from "react";
import { Link, Navigate, NavLink, Route, Routes, useNavigate } from "react-router-dom";
import { Admin, api, ApiError } from "./api";
import Login from "./pages/Login";
import Apps from "./pages/Apps";
import AppDetail from "./pages/AppDetail";
import Admins from "./pages/Admins";

type Session = { admin: Admin | null; setAdmin: (a: Admin | null) => void; grafanaURL: string };
const SessionContext = createContext<Session>({ admin: null, setAdmin: () => {}, grafanaURL: "" });
export const useSession = () => useContext(SessionContext);

export default function App() {
  const [admin, setAdmin] = useState<Admin | null>(null);
  const [loading, setLoading] = useState(true);
  const [grafanaURL, setGrafanaURL] = useState("");

  useEffect(() => {
    api.config().then((c) => setGrafanaURL(c.grafana_url)).catch(() => {});
    api
      .me()
      .then(setAdmin)
      .catch((e) => {
        if (!(e instanceof ApiError && e.status === 401)) console.error(e);
      })
      .finally(() => setLoading(false));
  }, []);

  if (loading) return <div className="center muted">Loading…</div>;

  return (
    <SessionContext.Provider value={{ admin, setAdmin, grafanaURL }}>
      {admin ? (
        <Shell>
          <Routes>
            <Route path="/" element={<Apps />} />
            <Route path="/apps/:app/*" element={<AppDetail />} />
            <Route path="/admins" element={<Admins />} />
            <Route path="/login" element={<Navigate to="/" replace />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Shell>
      ) : (
        <Routes>
          <Route path="*" element={<Login />} />
        </Routes>
      )}
    </SessionContext.Provider>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  const { admin, setAdmin } = useSession();
  const navigate = useNavigate();
  const logout = async () => {
    await api.logout().catch(() => {});
    setAdmin(null);
    navigate("/");
  };
  return (
    <div className="shell">
      <header className="topbar">
        <Link to="/" className="brand">
          SaaS Admin
        </Link>
        <nav>
          <NavLink to="/" end>
            Apps
          </NavLink>
          <NavLink to="/admins">Admins</NavLink>
        </nav>
        <div className="spacer" />
        <span className="muted">{admin?.email}</span>
        <button className="ghost" onClick={logout}>
          Sign out
        </button>
      </header>
      <main className="content">{children}</main>
    </div>
  );
}
