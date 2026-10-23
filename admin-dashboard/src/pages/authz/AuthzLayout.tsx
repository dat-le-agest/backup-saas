import { useQuery } from "@tanstack/react-query";
import { FileCode2Icon, HistoryIcon, NetworkIcon, ShieldQuestionIcon } from "lucide-react";
import { Navigate, Outlet, useLocation, useNavigate, useSearchParams } from "react-router";
import { ErrorAlert } from "@/components/common";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { TabsNav } from "@/components/ui/tabs-nav";
import { api } from "@/lib/api";
import { useAppId, useSvc } from "@/lib/hooks";

function useServices() {
  const app = useAppId();
  return useQuery({ queryKey: ["authz", app, "services"], queryFn: () => api.authzServices(app), staleTime: 60_000 });
}

// AuthzIndex (/apps/:app/authz?tab=tuples&...): opens the first service's
// tab, keeping the other params (links from users and tenants).
export function AuthzIndex() {
  const services = useServices();
  const [params] = useSearchParams();
  if (services.error) return <ErrorAlert error={services.error} title="The authorization engines are unreachable" />;
  if (!services.data) return <Skeleton className="h-64 rounded-xl" />;
  if (services.data.length === 0) return <p className="text-sm text-muted-foreground">This app has no authorization engine.</p>;
  const rest = new URLSearchParams(params);
  const tab = rest.get("tab") || "model";
  rest.delete("tab");
  return <Navigate to={`${services.data[0]}/${tab}${rest.size ? `?${rest}` : ""}`} replace />;
}

export default function AuthzLayout() {
  const app = useAppId();
  const svc = useSvc();
  const services = useServices();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const tab = pathname.split("/").pop();
  const base = `/apps/${app}/authz/${svc}`;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <TabsNav
          className="min-w-0 border-b-0"
          items={[
            { to: `${base}/model`, label: <><FileCode2Icon /> Model</> },
            { to: `${base}/tuples`, label: <><NetworkIcon /> Relationships</> },
            { to: `${base}/check`, label: <><ShieldQuestionIcon /> Check</> },
            { to: `${base}/changes`, label: <><HistoryIcon /> Changelog</> },
          ]}
        />
        {services.data && services.data.length > 1 && (
          <Select value={svc} onValueChange={(s) => navigate(`/apps/${app}/authz/${s}/${tab}`)}>
            <SelectTrigger size="sm" className="w-44">
              <span className="text-muted-foreground">Service</span> <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {services.data.map((s) => (
                <SelectItem key={s} value={s}>
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {services.data?.length === 1 && <span className="text-sm text-muted-foreground">service <b className="font-medium text-foreground">{svc}</b></span>}
      </div>
      <Outlet />
    </div>
  );
}
