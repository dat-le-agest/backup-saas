import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRoundIcon, SearchIcon } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import { ConfirmDialog, CopyText, EmptyRow, ErrorAlert, LoadingRows, Mono, Pager, RoleBadge } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { useAppId, useParam, useSearchInput } from "@/lib/hooks";
import { formatDate } from "@/lib/utils";

export default function Users() {
  const app = useAppId();
  const [search, setSearch, q] = useSearchInput("q");
  const [page, setPage] = useParam("page");
  const [selected, setSelected] = useParam("user");

  const n = Number(page) || 1;
  const users = useQuery({ queryKey: ["users", app, q, n], queryFn: () => api.users(app, q, n), placeholderData: keepPreviousData });

  return (
    <div className="space-y-4">
      <div className="relative max-w-sm">
        <SearchIcon className="absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
        <Input className="pl-8" placeholder="Search by email or id" value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>
      <ErrorAlert error={users.error} />
      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Email</TableHead>
              <TableHead>Id</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Created</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {users.isLoading && <LoadingRows cols={4} />}
            {users.data?.data.map((u) => (
              <TableRow key={u.id} className="cursor-pointer" data-state={selected === u.id ? "selected" : undefined} onClick={() => setSelected(u.id)}>
                <TableCell className="font-medium">{u.email || <span className="text-muted-foreground italic">no email</span>}</TableCell>
                <TableCell>
                  <Mono className="text-muted-foreground">{u.id}</Mono>
                </TableCell>
                <TableCell>{u.disabled ? <Badge variant="destructive">disabled</Badge> : <Badge variant="success">active</Badge>}</TableCell>
                <TableCell className="text-muted-foreground">{formatDate(u.created_at)}</TableCell>
              </TableRow>
            ))}
            {users.data?.data.length === 0 && <EmptyRow cols={4}>No user{q && ` matching “${q}”`}.</EmptyRow>}
          </TableBody>
        </Table>
      </Card>
      <Pager paging={users.data?.paging} onPage={(p) => setPage(String(p))} />
      <Sheet open={!!selected} onOpenChange={(o) => !o && setSelected("")}>
        <SheetContent>{selected && <UserPanel app={app} id={selected} />}</SheetContent>
      </Sheet>
    </div>
  );
}

function UserPanel({ app, id }: { app: string; id: string }) {
  const qc = useQueryClient();
  const user = useQuery({ queryKey: ["user", app, id], queryFn: () => api.user(app, id) });
  const [confirmDisable, setConfirmDisable] = useState(false);
  const [password, setPassword] = useState("");
  const u = user.data;

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["user", app, id] });
    void qc.invalidateQueries({ queryKey: ["users", app] });
  };
  const setDisabled = useMutation({
    mutationFn: (disabled: boolean) => api.setDisabled(app, id, disabled),
    onSuccess: (u) => (toast.success(u.disabled ? "User disabled; their sessions ended" : "User enabled"), refresh()),
    onError: (e) => toast.error(e.message),
  });
  const setPw = useMutation({
    mutationFn: () => api.setPassword(app, id, password),
    onSuccess: () => (toast.success("Password changed; the user's sessions ended"), setPassword("")),
  });
  const submitPw = (e: FormEvent) => {
    e.preventDefault();
    setPw.mutate();
  };

  return (
    <>
      <SheetHeader>
        <SheetTitle>{u?.email || id}</SheetTitle>
        <SheetDescription asChild>
          <div>
            <CopyText text={id} />
          </div>
        </SheetDescription>
      </SheetHeader>
      <div className="space-y-6 px-5 pb-6">
        <ErrorAlert error={user.error} />
        {u && (
          <>
            <dl className="grid grid-cols-[6rem_1fr] gap-y-2 text-sm">
              <dt className="text-muted-foreground">Status</dt>
              <dd>
                {u.disabled ? (
                  <span className="text-destructive">disabled since {formatDate(u.disabled_at)}</span>
                ) : (
                  <Badge variant="success">active</Badge>
                )}
              </dd>
              <dt className="text-muted-foreground">Created</dt>
              <dd>{formatDate(u.created_at)}</dd>
            </dl>
            {u.disabled ? (
              <Button variant="outline" onClick={() => setDisabled.mutate(false)} disabled={setDisabled.isPending}>
                Enable user
              </Button>
            ) : (
              <Button variant="destructive" onClick={() => setConfirmDisable(true)}>
                Disable user
              </Button>
            )}

            <Separator />
            <section className="space-y-3">
              <h3 className="text-sm font-semibold">Tenants</h3>
              {u.memberships?.length ? (
                <ul className="space-y-2">
                  {u.memberships.map((m) => (
                    <li key={m.tenant_id} className="flex items-center justify-between gap-2 rounded-md border px-3 py-2">
                      <Link to={`/apps/${app}/tenants?tenant=${encodeURIComponent(m.tenant_id)}`} className="truncate hover:underline">
                        <Mono>{m.tenant_id}</Mono>
                      </Link>
                      <RoleBadge role={m.role} />
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-sm text-muted-foreground">Not a member of any tenant.</p>
              )}
              <Button variant="link" className="h-auto px-0" asChild>
                <Link to={`/apps/${app}/authz?tab=tuples&subject=${encodeURIComponent(`user:${id}`)}`}>
                  <KeyRoundIcon /> Relationships of this user
                </Link>
              </Button>
            </section>

            {u.email && (
              <>
                <Separator />
                <form onSubmit={submitPw} className="space-y-3">
                  <h3 className="text-sm font-semibold">Set a new password</h3>
                  <Input type="password" autoComplete="new-password" placeholder="10 characters or more" value={password} onChange={(e) => setPassword(e.target.value)} minLength={10} required />
                  <ErrorAlert error={setPw.error} title="Password not changed" />
                  <Button type="submit" variant="outline" disabled={setPw.isPending}>
                    Set password
                  </Button>
                </form>
              </>
            )}
          </>
        )}
      </div>
      <ConfirmDialog
        open={confirmDisable}
        onOpenChange={setConfirmDisable}
        title="Disable this user?"
        description={`${u?.email || id} is signed out everywhere and can't sign in until enabled again.`}
        confirmLabel="Disable"
        destructive
        onConfirm={() => setDisabled.mutateAsync(true)}
      />
    </>
  );
}
