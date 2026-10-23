import { useMutation } from "@tanstack/react-query";
import { CheckCircle2Icon, ListIcon, ShieldQuestionIcon, XCircleIcon } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link } from "react-router";
import { ErrorAlert, Field } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useActiveModel, useAppId, useSvc } from "@/lib/hooks";
import { api, roles, type Tuple } from "@/lib/api";
import { relationsOf } from "@/lib/model";

const none = "none";

// RoleContext: the tenant role the subject would have (roles come from access
// tokens, not stored tuples), sent as a contextual tuple.
function RoleContext({ tenant, role, onTenant, onRole, idPrefix }: { tenant: string; role: string; onTenant: (v: string) => void; onRole: (v: string) => void; idPrefix: string }) {
  return (
    <div className="grid gap-3 rounded-lg border border-dashed p-3 sm:grid-cols-[1fr_9rem]">
      <Field label="As a member of tenant" htmlFor={`${idPrefix}-tenant`} hint="Optional: roles aren't stored, a request carries them.">
        <Input id={`${idPrefix}-tenant`} className="font-mono" placeholder="tnt_…" value={tenant} onChange={(e) => onTenant(e.target.value.trim())} />
      </Field>
      <Field label="With role">
        <Select value={role} onValueChange={onRole}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={none}>no role</SelectItem>
            {roles.map((r) => (
              <SelectItem key={r} value={r}>
                {r}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
    </div>
  );
}

function contextual(tenant: string, role: string, subject: string): Tuple[] {
  return tenant && role !== none && subject ? [{ object: `tenant:${tenant}`, relation: role, subject, tenant_id: tenant }] : [];
}

function RelationInput({ id, type, value, onChange }: { id: string; type: string; value: string; onChange: (v: string) => void }) {
  const model = useActiveModel();
  const rels = relationsOf(model.data?.types, type);
  return (
    <>
      <Input id={id} className="font-mono" list={`${id}-list`} placeholder="can_read_note" value={value} onChange={(e) => onChange(e.target.value.trim())} required />
      <datalist id={`${id}-list`}>
        {rels.map((r) => (
          <option key={r} value={r} />
        ))}
      </datalist>
    </>
  );
}

function CheckCard() {
  const app = useAppId();
  const svc = useSvc();
  const [object, setObject] = useState("");
  const [relation, setRelation] = useState("");
  const [subject, setSubject] = useState("");
  const [tenant, setTenant] = useState("");
  const [role, setRole] = useState(none);
  const check = useMutation({
    mutationFn: (q: string) => api.check(app, svc, { object, relation, subject, contextual: contextual(tenant, role, subject) }).then((r) => ({ ...r, q })),
  });
  // the answer shows while the form still asks the same question
  const question = JSON.stringify([object, relation, subject, tenant, role]);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    check.mutate(question);
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ShieldQuestionIcon className="size-4" /> Check a permission
        </CardTitle>
        <CardDescription>Does the subject have the relation on the object? Evaluated with the active model and every stored relationship.</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="grid gap-4">
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Subject" htmlFor="c-subject">
              <Input id="c-subject" className="font-mono" placeholder="user:u1" value={subject} onChange={(e) => setSubject(e.target.value.trim())} required />
            </Field>
            <Field label="Relation" htmlFor="c-relation">
              <RelationInput id="c-relation" type={object.split(":")[0]} value={relation} onChange={setRelation} />
            </Field>
            <Field label="Object" htmlFor="c-object">
              <Input id="c-object" className="font-mono" placeholder="note:42" value={object} onChange={(e) => setObject(e.target.value.trim())} required />
            </Field>
          </div>
          <RoleContext idPrefix="c" tenant={tenant} role={role} onTenant={setTenant} onRole={setRole} />
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" disabled={check.isPending}>
              Check
            </Button>
            {check.data?.q === question && (
              <div
                className={
                  check.data.allowed
                    ? "flex items-center gap-2 rounded-md bg-success/15 px-3 py-1.5 text-sm font-medium text-success"
                    : "flex items-center gap-2 rounded-md bg-destructive/10 px-3 py-1.5 text-sm font-medium text-destructive"
                }
              >
                {check.data.allowed ? <CheckCircle2Icon className="size-4" /> : <XCircleIcon className="size-4" />}
                {check.data.allowed ? "Allowed" : "Denied"}
              </div>
            )}
          </div>
          <ErrorAlert error={check.error} title="The check failed" />
        </form>
      </CardContent>
    </Card>
  );
}

function ListObjectsCard() {
  const app = useAppId();
  const svc = useSvc();
  const model = useActiveModel();
  const types = Object.keys(model.data?.types ?? {})
    .filter((t) => Object.keys(model.data?.types?.[t]?.relations ?? {}).length > 0)
    .sort();
  const [type, setType] = useState("");
  const [relation, setRelation] = useState("");
  const [subject, setSubject] = useState("");
  const [tenant, setTenant] = useState("");
  const [role, setRole] = useState(none);
  const list = useMutation({
    mutationFn: () => api.listObjects(app, svc, { type, relation, subject, contextual: contextual(tenant, role, subject) }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    list.mutate();
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ListIcon className="size-4" /> List objects
        </CardTitle>
        <CardDescription>Which objects of a type does the subject have the relation on?</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="grid gap-4">
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Subject" htmlFor="l-subject">
              <Input id="l-subject" className="font-mono" placeholder="user:u1" value={subject} onChange={(e) => setSubject(e.target.value.trim())} required />
            </Field>
            <Field label="Relation" htmlFor="l-relation">
              <RelationInput id="l-relation" type={type} value={relation} onChange={setRelation} />
            </Field>
            <Field label="Type">
              <Select value={type} onValueChange={setType} required>
                <SelectTrigger className="w-full font-mono">
                  <SelectValue placeholder="note" />
                </SelectTrigger>
                <SelectContent>
                  {types.map((t) => (
                    <SelectItem key={t} value={t} className="font-mono">
                      {t}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <RoleContext idPrefix="l" tenant={tenant} role={role} onTenant={setTenant} onRole={setRole} />
          <div>
            <Button type="submit" disabled={list.isPending || !type}>
              List
            </Button>
          </div>
          <ErrorAlert error={list.error} title="Listing failed" />
          {list.data && (
            <div className="rounded-md border">
              <div className="border-b px-3 py-2 text-xs text-muted-foreground">{list.data.objects.length} objects</div>
              <ul className="max-h-72 divide-y overflow-y-auto">
                {list.data.objects.map((o) => (
                  <li key={o} className="px-3 py-1.5">
                    <Link to={`/apps/${app}/authz/${svc}/tuples?object=${encodeURIComponent(o)}`} className="font-mono text-xs hover:underline">
                      {o}
                    </Link>
                  </li>
                ))}
                {list.data.objects.length === 0 && <li className="px-3 py-2 text-sm text-muted-foreground">None.</li>}
              </ul>
            </div>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

export default function Check() {
  return (
    <div className="grid gap-6 xl:grid-cols-2">
      <CheckCard />
      <ListObjectsCard />
    </div>
  );
}
