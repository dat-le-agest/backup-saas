import { useInfiniteQuery } from "@tanstack/react-query";
import { MinusIcon, PlusIcon } from "lucide-react";
import { EmptyRow, ErrorAlert, Field, LoadingRows, Mono } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { useAppId, useParam, useSearchInput, useSvc } from "@/lib/hooks";
import { formatDate, timeAgo } from "@/lib/utils";

function FilterInput({ name, label, placeholder }: { name: string; label: string; placeholder: string }) {
  const [input, onChange] = useSearchInput(name);
  return (
    <Field label={label} htmlFor={`ch-${name}`}>
      <Input id={`ch-${name}`} className="font-mono text-xs" placeholder={placeholder} value={input} onChange={(e) => onChange(e.target.value.trim())} />
    </Field>
  );
}

function Actor({ actor }: { actor: string }) {
  const [kind, who] = actor.includes(":") ? [actor.slice(0, actor.indexOf(":")), actor.slice(actor.indexOf(":") + 1)] : [actor, ""];
  if (kind === "admin") return <span className="inline-flex items-center gap-1.5"><Badge variant="warning">admin</Badge> {who}</span>;
  if (kind === "user") return <Mono>{actor}</Mono>;
  return <span className="text-muted-foreground">{actor || "—"}</span>;
}

export default function Changes() {
  const app = useAppId();
  const svc = useSvc();
  const [tenant] = useParam("tenant_id");
  const [objectType] = useParam("object_type");
  const f = { tenant_id: tenant, object_type: objectType };

  const changes = useInfiniteQuery({
    queryKey: ["authz", app, svc, "changes", f],
    queryFn: ({ pageParam }) => api.changes(app, svc, f, pageParam),
    initialPageParam: 0,
    getNextPageParam: (last) => last.paging.next || undefined,
  });
  const rows = changes.data?.pages.flatMap((p) => p.data) ?? [];

  return (
    <div className="space-y-4">
      <div className="grid max-w-xl gap-3 sm:grid-cols-2">
        <FilterInput name="tenant_id" label="Tenant" placeholder="tnt_…" />
        <FilterInput name="object_type" label="Object type" placeholder="note" />
      </div>
      <ErrorAlert error={changes.error} />
      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-16">#</TableHead>
              <TableHead>Change</TableHead>
              <TableHead>Tenant</TableHead>
              <TableHead>By</TableHead>
              <TableHead>When</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {changes.isLoading && <LoadingRows cols={5} />}
            {rows.map((c) => {
              const write = c.operation === "write";
              return (
                <TableRow key={c.seq}>
                  <TableCell className="text-muted-foreground tabular-nums">{c.seq}</TableCell>
                  <TableCell>
                    <span className="inline-flex items-center gap-2">
                      <span className={write ? "rounded bg-success/15 p-0.5 text-success" : "rounded bg-destructive/10 p-0.5 text-destructive"} title={c.operation}>
                        {write ? <PlusIcon className="size-3.5" /> : <MinusIcon className="size-3.5" />}
                      </span>
                      <Mono>
                        {c.object}
                        <span className="text-muted-foreground">#</span>
                        <b className="font-medium">{c.relation}</b>
                        <span className="text-muted-foreground">@</span>
                        {c.subject}
                      </Mono>
                    </span>
                  </TableCell>
                  <TableCell>
                    <Mono className="text-muted-foreground">{c.tenant_id}</Mono>
                  </TableCell>
                  <TableCell>
                    <Actor actor={c.actor} />
                  </TableCell>
                  <TableCell className="text-muted-foreground" title={formatDate(c.changed_at)}>
                    {timeAgo(c.changed_at)}
                  </TableCell>
                </TableRow>
              );
            })}
            {changes.data && rows.length === 0 && <EmptyRow cols={5}>No change recorded.</EmptyRow>}
          </TableBody>
        </Table>
      </Card>
      {changes.hasNextPage && (
        <div className="flex justify-center">
          <Button variant="outline" onClick={() => changes.fetchNextPage()} disabled={changes.isFetchingNextPage}>
            {changes.isFetchingNextPage ? "Loading…" : "Load older changes"}
          </Button>
        </div>
      )}
    </div>
  );
}
