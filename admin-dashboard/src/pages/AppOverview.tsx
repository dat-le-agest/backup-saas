import { useQueries, useQuery } from "@tanstack/react-query";
import { ArrowRightIcon, ExternalLinkIcon } from "lucide-react";
import { Link } from "react-router";
import { ErrorAlert, Mono } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useAppId } from "@/lib/hooks";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { formatDate } from "@/lib/utils";

function Metric({ label, value, to }: { label: string; value?: number | string; to?: string }) {
  const body = (
    <Card className="gap-1 transition-shadow hover:shadow-md">
      <CardHeader>
        <CardDescription>{label}</CardDescription>
      </CardHeader>
      <CardContent className="flex items-end justify-between">
        <span className="text-3xl font-semibold tabular-nums">{value ?? <Skeleton className="h-9 w-16" />}</span>
        {to && <ArrowRightIcon className="size-4 text-muted-foreground" />}
      </CardContent>
    </Card>
  );
  return to ? <Link to={to}>{body}</Link> : body;
}

export default function AppOverview() {
  const app = useAppId();
  const { grafanaURL } = useSession();
  const status = useQuery({ queryKey: ["app", app], queryFn: () => api.app(app) });
  const services = useQuery({ queryKey: ["authz", app, "services"], queryFn: () => api.authzServices(app) });
  const models = useQueries({
    queries: (services.data ?? []).map((svc) => ({ queryKey: ["authz", app, svc, "model", "active"], queryFn: () => api.model(app, svc) })),
  });
  const s = status.data;

  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-3">
        <Metric label="Users" value={s?.users} to={`/apps/${app}/users`} />
        <Metric label="Tenants" value={s?.tenants} to={`/apps/${app}/tenants`} />
        <Metric label="Admin API latency" value={s ? `${s.latency_ms} ms` : undefined} />
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>App</CardTitle>
          </CardHeader>
          <CardContent>
            <dl className="grid grid-cols-[8rem_1fr] gap-y-3 text-sm">
              <dt className="text-muted-foreground">Id</dt>
              <dd>
                <Mono>{app}</Mono>
              </dd>
              <dt className="text-muted-foreground">Public URL</dt>
              <dd>
                {s?.public_url ? (
                  <a href={s.public_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 hover:underline">
                    {s.public_url} <ExternalLinkIcon className="size-3" />
                  </a>
                ) : (
                  "—"
                )}
              </dd>
              {grafanaURL && (
                <>
                  <dt className="text-muted-foreground">Monitoring</dt>
                  <dd>
                    <a href={`${grafanaURL}/d/saas-overview?var-app=${app}`} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 hover:underline">
                      Grafana dashboard <ExternalLinkIcon className="size-3" />
                    </a>
                  </dd>
                </>
              )}
            </dl>
          </CardContent>
        </Card>

        <Card className="pb-0">
          <CardHeader>
            <CardTitle>Authorization engines</CardTitle>
            <CardDescription>The ReBAC engine of each service and its active model.</CardDescription>
          </CardHeader>
          <ErrorAlert error={services.error} className="mx-5 w-auto" />
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-5">Service</TableHead>
                <TableHead>Active model</TableHead>
                <TableHead>Since</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {services.data?.map((svc, i) => {
                const m = models[i]?.data;
                return (
                  <TableRow key={svc}>
                    <TableCell className="pl-5">
                      <Link to={`/apps/${app}/authz/${svc}/model`} className="font-medium hover:underline">
                        {svc}
                      </Link>
                    </TableCell>
                    <TableCell>
                      {m ? (
                        <span className="inline-flex items-center gap-2">
                          <Mono>{m.id.slice(-8)}</Mono> <Badge variant="outline">{Object.keys(m.types ?? {}).length} types</Badge>
                        </span>
                      ) : models[i]?.error ? (
                        <span className="text-destructive">unavailable</span>
                      ) : (
                        <Skeleton className="h-4 w-24" />
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground">{m ? formatDate(m.created_at) : ""}</TableCell>
                  </TableRow>
                );
              })}
              {services.data?.length === 0 && (
                <TableRow>
                  <TableCell colSpan={3} className="pl-5 text-muted-foreground">
                    No engine.
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </Card>
      </div>
    </div>
  );
}
