import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FilterXIcon, PlusIcon, Trash2Icon } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import { ConfirmDialog, EmptyRow, ErrorAlert, Field, LoadingRows, Mono, Pager } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, type Tuple } from "@/lib/api";
import { useActiveModel, useAppId, useParam, useSearchInput, useSvc } from "@/lib/hooks";
import { assignableTypes, relationsOf } from "@/lib/model";

const filters = [
  { name: "tenant_id", label: "Tenant", placeholder: "tnt_…" },
  { name: "object", label: "Object", placeholder: "note or note:42" },
  { name: "relation", label: "Relation", placeholder: "viewer" },
  { name: "subject", label: "Subject", placeholder: "user:u1 or team:eng#member" },
] as const;

function FilterInput({ name, label, placeholder }: (typeof filters)[number]) {
  const [input, onChange] = useSearchInput(name);
  return (
    <Field label={label} htmlFor={`f-${name}`}>
      <Input id={`f-${name}`} className="font-mono text-xs" placeholder={placeholder} value={input} onChange={(e) => onChange(e.target.value)} />
    </Field>
  );
}

export default function Tuples() {
  const app = useAppId();
  const svc = useSvc();
  const qc = useQueryClient();
  const [tenant] = useParam("tenant_id");
  const [object] = useParam("object");
  const [relation] = useParam("relation");
  const [subject] = useParam("subject");
  const [page, setPage] = useParam("page");
  const [deleting, setDeleting] = useState<Tuple | null>(null);
  const [resetKey, setResetKey] = useState(0); // remounts the filter inputs when cleared
  const f = { tenant_id: tenant, object, relation, subject };
  const filtered = Object.values(f).some(Boolean);
  const n = Number(page) || 1;

  const tuples = useQuery({
    queryKey: ["authz", app, svc, "tuples", f, n],
    queryFn: () => api.tuples(app, svc, f, n),
    placeholderData: keepPreviousData,
  });
  const del = useMutation({
    mutationFn: (t: Tuple) => api.writeTuples(app, svc, [], [t]),
    onSuccess: () => {
      toast.success("Relationship deleted");
      void qc.invalidateQueries({ queryKey: ["authz", app, svc] });
    },
    onError: (e) => toast.error(e.message),
  });

  return (
    <div className="space-y-4">
      <Card className="py-4">
        <div key={resetKey} className="grid gap-3 px-5 sm:grid-cols-2 lg:grid-cols-[repeat(4,1fr)_auto] lg:items-end">
          {filters.map((fl) => (
            <FilterInput key={fl.name} {...fl} />
          ))}
          <div className="flex gap-2">
            {filtered && <ClearFilters onCleared={() => setResetKey((k) => k + 1)} />}
            <AddTuple defaults={{ tenant_id: tenant, object: object.includes(":") ? object : "" }} />
          </div>
        </div>
      </Card>
      <ErrorAlert error={tuples.error} />
      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Object</TableHead>
              <TableHead>Relation</TableHead>
              <TableHead>Subject</TableHead>
              <TableHead>Tenant</TableHead>
              <TableHead className="w-10" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {tuples.isLoading && <LoadingRows cols={5} />}
            {tuples.data?.data.map((t) => (
              <TableRow key={`${t.object}#${t.relation}@${t.subject}`}>
                <TableCell>
                  <Mono>{t.object}</Mono>
                </TableCell>
                <TableCell>
                  <Badge variant="secondary" className="font-mono">
                    {t.relation}
                  </Badge>
                </TableCell>
                <TableCell>
                  <Mono>{t.subject}</Mono>
                </TableCell>
                <TableCell>
                  <Mono className="text-muted-foreground">{t.tenant_id}</Mono>
                </TableCell>
                <TableCell>
                  <Button variant="ghost" size="icon-sm" onClick={() => setDeleting(t)} aria-label="Delete relationship">
                    <Trash2Icon />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {tuples.data?.data.length === 0 && <EmptyRow cols={5}>{filtered ? "No relationship matches these filters." : "No stored relationship yet."}</EmptyRow>}
          </TableBody>
        </Table>
      </Card>
      <Pager paging={tuples.data?.paging} onPage={(p) => setPage(String(p))} />
      <p className="text-xs text-muted-foreground">
        Tenant roles (owner, admin, member, viewer) aren't stored here: each check adds the caller's role from their access token.
      </p>
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title="Delete this relationship?"
        description={
          <span className="font-mono text-xs">
            {deleting?.object}#{deleting?.relation}@{deleting?.subject}
          </span>
        }
        confirmLabel="Delete"
        destructive
        onConfirm={() => deleting && del.mutateAsync(deleting)}
      />
    </div>
  );
}

function ClearFilters({ onCleared }: { onCleared: () => void }) {
  const [, set] = useParam("tenant_id");
  return (
    <Button variant="ghost" size="icon" onClick={() => (set("", { object: "", relation: "", subject: "", page: "" }), onCleared())} aria-label="Clear filters" title="Clear filters">
      <FilterXIcon />
    </Button>
  );
}

function AddTuple({ defaults }: { defaults: { tenant_id: string; object: string } }) {
  const app = useAppId();
  const svc = useSvc();
  const qc = useQueryClient();
  const model = useActiveModel();
  const [open, setOpen] = useState(false);
  const [t, setT] = useState<Tuple>({ object: "", relation: "", subject: "", tenant_id: "" });
  const objType = t.object.split(":")[0];
  const relations = relationsOf(model.data?.types, objType).filter((r) => model.data?.types?.[objType]?.relations?.[r] && assignableTypes(model.data.types[objType].relations![r]).length > 0);
  const accepts = t.relation ? assignableTypes(model.data?.types?.[objType]?.relations?.[t.relation] ?? {}) : [];

  const write = useMutation({
    mutationFn: () => api.writeTuples(app, svc, [t]),
    onSuccess: () => {
      toast.success("Relationship added");
      void qc.invalidateQueries({ queryKey: ["authz", app, svc] });
      setOpen(false);
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    write.mutate();
  };
  const set = (k: keyof Tuple) => (e: React.ChangeEvent<HTMLInputElement>) => setT({ ...t, [k]: e.target.value.trim() });

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (o) setT({ object: defaults.object, relation: "", subject: "", tenant_id: defaults.tenant_id });
        write.reset();
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <PlusIcon /> Add
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Add a relationship</DialogTitle>
            <DialogDescription>Written as object#relation@subject, validated against the active model.</DialogDescription>
          </DialogHeader>
          <Field label="Object" htmlFor="t-object" hint={model.data && `Types: ${Object.keys(model.data.types ?? {}).sort().join(", ")}`}>
            <Input id="t-object" className="font-mono" placeholder="note:42" value={t.object} onChange={set("object")} required />
          </Field>
          <Field label="Relation" htmlFor="t-relation">
            <Input id="t-relation" className="font-mono" list="t-relations" placeholder="viewer" value={t.relation} onChange={set("relation")} required />
            <datalist id="t-relations">
              {relations.map((r) => (
                <option key={r} value={r} />
              ))}
            </datalist>
          </Field>
          <Field label="Subject" htmlFor="t-subject" hint={accepts.length > 0 && `Accepts: ${accepts.join(", ")}`}>
            <Input id="t-subject" className="font-mono" placeholder="user:u1, user:*, team:eng#member" value={t.subject} onChange={set("subject")} required />
          </Field>
          <Field label="Tenant" htmlFor="t-tenant" hint="The tenant owning the object.">
            <Input id="t-tenant" className="font-mono" placeholder="tnt_…" value={t.tenant_id} onChange={set("tenant_id")} required />
          </Field>
          <ErrorAlert error={write.error} title="Not added" />
          <DialogFooter>
            <Button type="submit" disabled={write.isPending}>
              Add relationship
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
