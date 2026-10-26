import { useQuery } from "@tanstack/react-query";
import { Building2Icon, ChevronRightIcon, FileCode2Icon, GaugeIcon, KeyRoundIcon, RadioIcon, UsersIcon } from "lucide-react";
import { Link, Outlet } from "react-router";
import { ErrorAlert } from "@/components/common";
import { StatusDot } from "@/components/layout";
import { Badge } from "@/components/ui/badge";
import { TabsNav } from "@/components/ui/tabs-nav";
import { api } from "@/lib/api";
import { useAppId } from "@/lib/hooks";

export default function AppLayout() {
  const app = useAppId();
  const status = useQuery({ queryKey: ["app", app], queryFn: () => api.app(app), refetchInterval: 15_000 });
  const s = status.data;
  return (
    <>
      <div className="space-y-3">
        <div className="flex items-center gap-1 text-sm text-muted-foreground">
          <Link to="/" className="hover:text-foreground">
            Apps
          </Link>
          <ChevronRightIcon className="size-3.5" />
          <span className="text-foreground">{s?.name ?? app}</span>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="text-2xl font-semibold tracking-tight">{s?.name ?? app}</h1>
          {s && (
            <Badge variant={s.up ? "success" : "destructive"}>
              <StatusDot up={s.up} /> {s.up ? `up · ${s.latency_ms} ms` : "down"}
            </Badge>
          )}
        </div>
      </div>
      <ErrorAlert error={status.error} />
      {s && !s.up && <ErrorAlert error={new Error(s.error || "unreachable")} title="The app's admin API is unreachable" />}
      <TabsNav
        items={[
          { to: `/apps/${app}`, end: true, label: <><GaugeIcon /> Overview</> },
          { to: `/apps/${app}/users`, label: <><UsersIcon /> Users</> },
          { to: `/apps/${app}/tenants`, label: <><Building2Icon /> Tenants</> },
          { to: `/apps/${app}/authz`, label: <><KeyRoundIcon /> Authorization</> },
          { to: `/apps/${app}/outbox`, label: <><RadioIcon /> Outbox Events</> },
          { to: `/apps/${app}/docs`, label: <><FileCode2Icon /> API Docs</> },
        ]}
      />
      <Outlet />
    </>
  );
}
