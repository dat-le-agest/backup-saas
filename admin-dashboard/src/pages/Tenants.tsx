import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRoundIcon, SearchIcon, Trash2Icon, UserMinusIcon } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import { ConfirmDialog, CopyText, EmptyRow, ErrorAlert, LoadingRows, Mono, Pager } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, roles, type Member } from "@/lib/api";
import { useAppId, useParam, useSearchInput } from "@/lib/hooks";
import { formatDate } from "@/lib/utils";

export default function Tenants() {
  const app = useAppId();
  const [search, setSearch, q] = useSearchInput("q");
  const [page, setPage] = useParam("page");
  const [selected, setSelected] = useParam("tenant");

  const n = Number(page) || 1;
  const tenants = useQuery({ queryKey: ["tenants", app, q, n], queryFn: () => api.tenants(app, q, n), placeholderData: keepPreviousData });

  return (
    <div className="space-y-4">
      <div className="relative max-w-sm">
        <SearchIcon className="absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
        <Input className="pl-8" placeholder="Search by name or id" value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>
      <ErrorAlert error={tenants.error} />
      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Id</TableHead>
              <TableHead className="text-right">Members</TableHead>
              <TableHead>Created</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {tenants.isLoading && <LoadingRows cols={4} />}
            {tenants.data?.data.map((t) => (
              <TableRow key={t.id} className="cursor-pointer" data-state={selected === t.id ? "selected" : undefined} onClick={() => setSelected(t.id)}>
                <TableCell className="font-medium">{t.name}</TableCell>
                <TableCell>
                  <Mono className="text-muted-foreground">{t.id}</Mono>
                </TableCell>
                <TableCell className="text-right tabular-nums">{t.members}</TableCell>
                <TableCell className="text-muted-foreground">{formatDate(t.created_at)}</TableCell>
              </TableRow>
            ))}
            {tenants.data?.data.length === 0 && <EmptyRow cols={4}>No tenant{q && ` matching “${q}”`}.</EmptyRow>}
          </TableBody>
        </Table>
      </Card>
      <Pager paging={tenants.data?.paging} onPage={(p) => setPage(String(p))} />
      <Sheet open={!!selected} onOpenChange={(o) => !o && setSelected("")}>
        <SheetContent className="sm:max-w-lg">{selected && <TenantPanel app={app} id={selected} onDeleted={() => setSelected("")} />}</SheetContent>
      </Sheet>
    </div>
  );
}

function TenantPanel({ app, id, onDeleted }: { app: string; id: string; onDeleted: () => void }) {
  const qc = useQueryClient();
  const tenant = useQuery({ queryKey: ["tenant", app, id], queryFn: () => api.tenant(app, id) });
  const [removing, setRemoving] = useState<Member | null>(null);
  const [deleting, setDeleting] = useState(false);
  const t = tenant.data;

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["tenant", app, id] });
    void qc.invalidateQueries({ queryKey: ["tenants", app] });
    void qc.invalidateQueries({ queryKey: ["user", app] });
  };
  const setRole = useMutation({
    mutationFn: ({ user, role }: { user: string; role: string }) => api.setRole(app, id, user, role),
    onSuccess: (m) => (toast.success(`${m.email || m.user_id} is now ${m.role}`), refresh()),
    onError: (e) => toast.error(e.message),
  });
  const remove = useMutation({
    mutationFn: (user: string) => api.removeMember(app, id, user),
    onSuccess: () => (toast.success("Member removed"), refresh()),
    onError: (e) => toast.error(e.message),
  });
  const del = useMutation({
    mutationFn: () => api.deleteTenant(app, id),
    onSuccess: () => {
      toast.success("Tenant deleted");
      void qc.invalidateQueries({ queryKey: ["tenants", app] });
      void qc.invalidateQueries({ queryKey: ["app", app] });
      onDeleted();
    },
    onError: (e) => toast.error(e.message),
  });

  return (
    <>
      <SheetHeader>
        <SheetTitle>{t?.name ?? id}</SheetTitle>
        <SheetDescription asChild>
          <div>
            <CopyText text={id} />
          </div>
        </SheetDescription>
      </SheetHeader>
      <div className="space-y-6 px-5 pb-6">
        <ErrorAlert error={tenant.error} />
        {t && (
          <>
            <dl className="grid grid-cols-[6rem_1fr] gap-y-2 text-sm">
              <dt className="text-muted-foreground">Created</dt>
              <dd>{formatDate(t.created_at)}</dd>
              <dt className="text-muted-foreground">Members</dt>
              <dd>{t.member_list?.length ?? t.members}</dd>
            </dl>

            <section className="space-y-3">
              <h3 className="text-sm font-semibold">Members</h3>
              <div className="divide-y rounded-md border">
                {t.member_list?.map((m) => (
                  <div key={m.user_id} className="flex items-center gap-2 px-3 py-2">
                    <Link to={`/apps/${app}/users?user=${encodeURIComponent(m.user_id)}`} className="min-w-0 flex-1 truncate text-sm hover:underline">
                      {m.email || <Mono>{m.user_id}</Mono>}
                    </Link>
                    <Select value={m.role} onValueChange={(role) => setRole.mutate({ user: m.user_id, role })}>
                      <SelectTrigger size="sm" className="w-28">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {roles.map((r) => (
                          <SelectItem key={r} value={r}>
                            {r}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Button variant="ghost" size="icon-sm" onClick={() => setRemoving(m)} aria-label="Remove member">
                      <UserMinusIcon />
                    </Button>
                  </div>
                ))}
                {t.member_list?.length === 0 && <p className="px-3 py-2 text-sm text-muted-foreground">No member.</p>}
              </div>
              <p className="text-xs text-muted-foreground">A tenant always keeps an owner: the last one can't be demoted or removed.</p>
              <Button variant="link" className="h-auto px-0" asChild>
                <Link to={`/apps/${app}/authz?tab=tuples&tenant_id=${encodeURIComponent(id)}`}>
                  <KeyRoundIcon /> Relationships in this tenant
                </Link>
              </Button>
            </section>

            <Separator />
            <section className="space-y-3">
              <h3 className="text-sm font-semibold text-destructive">Danger zone</h3>
              <Button variant="destructive" onClick={() => setDeleting(true)}>
                <Trash2Icon /> Delete tenant
              </Button>
            </section>
          </>
        )}
      </div>
      <ConfirmDialog
        open={!!removing}
        onOpenChange={(o) => !o && setRemoving(null)}
        title="Remove this member?"
        description={`${removing?.email || removing?.user_id} loses access to ${t?.name} now.`}
        confirmLabel="Remove"
        destructive
        onConfirm={() => removing && remove.mutateAsync(removing.user_id)}
      />
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete “${t?.name}”?`}
        description="Every member loses access now. This can't be undone."
        confirmLabel="Delete tenant"
        destructive
        onConfirm={() => del.mutateAsync()}
      />
    </>
  );
}
