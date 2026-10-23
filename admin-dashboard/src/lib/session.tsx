import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useContext } from "react";
import { api, ApiError, type Admin } from "./api";

type Session = { admin: Admin; grafanaURL: string; signOut: () => Promise<void> };
const SessionContext = createContext<Session | null>(null);

// useMe: the signed-in admin, null when signed out (401).
// eslint-disable-next-line react-refresh/only-export-components
export function useMe() {
  return useQuery({
    queryKey: ["me"],
    queryFn: () =>
      api.me().catch((e) => {
        if (e instanceof ApiError && e.status === 401) return null;
        throw e;
      }),
    staleTime: Infinity,
    retry: false,
  });
}

export function SessionProvider({ admin, children }: { admin: Admin; children: React.ReactNode }) {
  const qc = useQueryClient();
  const config = useQuery({ queryKey: ["config"], queryFn: api.config, staleTime: Infinity });
  const signOut = async () => {
    await api.logout().catch(() => {});
    qc.clear();
    qc.setQueryData(["me"], null);
  };
  return (
    <SessionContext.Provider value={{ admin, grafanaURL: config.data?.grafana_url ?? "", signOut }}>{children}</SessionContext.Provider>
  );
}

// eslint-disable-next-line react-refresh/only-export-components
export function useSession(): Session {
  const s = useContext(SessionContext);
  if (!s) throw new Error("useSession outside SessionProvider");
  return s;
}
