import { useQuery } from "@tanstack/react-query";
import { ActivityIcon, ArrowRightIcon, Building2Icon, ExternalLinkIcon, RefreshCwIcon, UsersIcon } from "lucide-react";
import { Link } from "react-router";
import { ErrorAlert, PageHeader } from "@/components/common";
import { StatusDot } from "@/components/layout";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type AppStatus } from "@/lib/api";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";

function Stat({ icon, label, value }: { icon: React.ReactNode; label: string; value: React.ReactNode }) {
  return (
    <Card className="gap-2">
      <CardHeader className="flex-row items-center justify-between">
        <span className="text-sm text-muted-foreground">{label}</span>
        <span className="text-muted-foreground [&_svg]:size-4">{icon}</span>
      </CardHeader>
      <CardContent className="text-2xl font-semibold tabular-nums">{value}</CardContent>
    </Card>
  );
}

function AppCard({ app }: { app: AppStatus }) {
  const { grafanaURL } = useSession();
  return (
    <Card className="gap-0 py-0 transition-shadow hover:shadow-md">
      <Link to={`/apps/${app.id}`} className="block p-5">
        <div className="flex items-center gap-2.5">
          <StatusDot up={app.up} />
          <CardTitle className="truncate">{app.name}</CardTitle>
          <span className="font-mono text-xs text-muted-foreground">{app.id}</span>
          <Badge variant={app.up ? "success" : "destructive"} className="ml-auto">
            {app.up ? `${app.latency_ms} ms` : "down"}
          </Badge>
        </div>
        {app.up ? (
          <div className="mt-5 grid grid-cols-2 gap-4">
            <div>
              <div className="text-2xl font-semibold tabular-nums">{app.users}</div>
              <div className="text-xs text-muted-foreground">users</div>
            </div>
            <div>
              <div className="text-2xl font-semibold tabular-nums">{app.tenants}</div>
              <div className="text-xs text-muted-foreground">tenants</div>
            </div>
          </div>
        ) : (
          <p className="mt-5 line-clamp-2 text-sm text-destructive">{app.error || "unreachable"}</p>
        )}
      </Link>
      <div className="flex items-center gap-3 border-t px-5 py-3 text-sm">
        {app.public_url && (
          <a href={app.public_url} target="_blank" rel="noreferrer" className="truncate text-muted-foreground hover:text-foreground">
            {app.public_url}
          </a>
        )}
        {grafanaURL && (
          <a href={`${grafanaURL}/d/saas-overview?var-app=${app.id}`} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground">
            Grafana <ExternalLinkIcon className="size-3" />
          </a>
        )}
        <Link to={`/apps/${app.id}`} className="ml-auto inline-flex items-center gap-1 font-medium">
          Manage <ArrowRightIcon className="size-3.5" />
        </Link>
      </div>
    </Card>
  );
}

export default function Overview() {
  const apps = useQuery({ queryKey: ["apps"], queryFn: api.apps, refetchInterval: 15_000 });
  const list = apps.data ?? [];
  const up = list.filter((a) => a.up);
  const sum = (k: "users" | "tenants") => up.reduce((n, a) => n + a[k], 0);

  return (
    <>
      <PageHeader
        title="Overview"
        description="Every app managed by this dashboard, refreshed every 15 seconds."
        actions={
          <Button variant="outline" size="sm" onClick={() => apps.refetch()} disabled={apps.isFetching}>
            <RefreshCwIcon className={cn(apps.isFetching && "animate-spin")} /> Refresh
          </Button>
        }
      />
      <ErrorAlert error={apps.error} />
      <div className="grid gap-4 sm:grid-cols-3">
        <Stat icon={<ActivityIcon />} label="Apps up" value={apps.data ? `${up.length} / ${list.length}` : <Skeleton className="h-8 w-16" />} />
        <Stat icon={<UsersIcon />} label="Users" value={apps.data ? sum("users") : <Skeleton className="h-8 w-16" />} />
        <Stat icon={<Building2Icon />} label="Tenants" value={apps.data ? sum("tenants") : <Skeleton className="h-8 w-16" />} />
      </div>
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {apps.isLoading && [0, 1].map((i) => <Skeleton key={i} className="h-44 rounded-xl" />)}
        {list.map((a) => (
          <AppCard key={a.id} app={a} />
        ))}
      </div>
      {apps.data?.length === 0 && <p className="text-sm text-muted-foreground">No app configured: set ADMIN_APPS on the admin service.</p>}
    </>
  );
}
