import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2Icon, CodeIcon, LayoutListIcon, RotateCcwIcon, SearchIcon } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import { ConfirmDialog, ErrorAlert, Mono } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type AuthzModel, type TypeDef } from "@/lib/api";
import { useAppId, useParam, useSvc } from "@/lib/hooks";
import { cn, formatDate, timeAgo } from "@/lib/utils";
import { relationKind, type RelationKind } from "@/lib/model";
import { RuleView } from "./rule";

const kindBadge: Record<RelationKind, { label: string; variant: "secondary" | "outline" | "success" }> = {
  stored: { label: "stored", variant: "outline" },
  derived: { label: "derived", variant: "secondary" },
  permission: { label: "permission", variant: "success" },
};

export default function Model() {
  const app = useAppId();
  const svc = useSvc();
  const [version, setVersion] = useParam("v");
  const models = useQuery({ queryKey: ["authz", app, svc, "models"], queryFn: () => api.models(app, svc) });
  const active = models.data?.find((m) => m.is_active);
  const id = version || active?.id || "";
  const model = useQuery({
    queryKey: ["authz", app, svc, "model", id],
    queryFn: () => api.model(app, svc, id),
    enabled: !!id,
  });

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_18rem]">
      <div className="min-w-0 space-y-4">
        <ErrorAlert error={models.error ?? model.error} title="The model is unavailable" />
        {model.data ? <ModelView model={model.data} /> : !models.error && <Skeleton className="h-96 rounded-xl" />}
      </div>
      <Versions models={models.data} selected={id} onSelect={(v) => setVersion(v === active?.id ? "" : v)} />
    </div>
  );
}

function ModelView({ model }: { model: AuthzModel }) {
  const [view, setView] = useState<"types" | "source">("types");
  const [filter, setFilter] = useState("");
  const types = Object.entries(model.types ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const relationCount = types.reduce((n, [, t]) => n + Object.keys(t.relations ?? {}).length, 0);
  const shown = filter
    ? types.filter(([name, t]) => name.includes(filter) || Object.keys(t.relations ?? {}).some((r) => r.includes(filter)))
    : types;

  return (
    <>
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex items-center gap-2 text-sm">
          <Mono>{model.id}</Mono>
          {model.is_active ? <Badge variant="success">active</Badge> : <Badge variant="outline">inactive</Badge>}
          <span className="text-muted-foreground">
            {types.length} types · {relationCount} relations
          </span>
        </div>
        <div className="ml-auto flex rounded-md border p-0.5">
          {(
            [
              ["types", <LayoutListIcon key="i" />, "Types"],
              ["source", <CodeIcon key="i" />, "YAML"],
            ] as const
          ).map(([v, icon, label]) => (
            <Button key={v} variant={view === v ? "secondary" : "ghost"} size="sm" className="h-7" onClick={() => setView(v)}>
              {icon} {label}
            </Button>
          ))}
        </div>
      </div>

      {view === "source" ? (
        <Card className="py-0">
          <pre className="overflow-x-auto p-5 font-mono text-xs leading-relaxed">{model.source || "(no source)"}</pre>
        </Card>
      ) : (
        <>
          <div className="relative max-w-xs">
            <SearchIcon className="absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
            <Input className="pl-8" placeholder="Filter types and relations" value={filter} onChange={(e) => setFilter(e.target.value)} />
          </div>
          <div className="grid gap-4">
            {shown.map(([name, t]) => (
              <TypeCard key={name} name={name} type={t} highlight={filter} />
            ))}
          </div>
        </>
      )}
    </>
  );
}

function TypeCard({ name, type, highlight }: { name: string; type: TypeDef; highlight: string }) {
  const app = useAppId();
  const svc = useSvc();
  const rels = Object.entries(type.relations ?? {}).sort(([a], [b]) => a.localeCompare(b));
  return (
    <Card className="gap-0 py-0">
      <CardHeader className="flex-row items-center gap-2 border-b py-3">
        <CardTitle className="font-mono text-base">{name}</CardTitle>
        <span className="text-xs text-muted-foreground">{rels.length} relations</span>
        <Link to={`/apps/${app}/authz/${svc}/tuples?object=${name}`} className="ml-auto text-xs text-muted-foreground hover:text-foreground">
          Relationships →
        </Link>
      </CardHeader>
      {rels.length === 0 ? (
        <p className="px-5 py-3 text-sm text-muted-foreground">No relations: a subject type only.</p>
      ) : (
        <div className="divide-y">
          {rels.map(([rel, rule]) => {
            const kind = kindBadge[relationKind(rel, rule)];
            return (
              <div
                key={rel}
                className={cn("grid gap-2 px-5 py-2.5 sm:grid-cols-[14rem_1fr] sm:items-center", highlight && rel.includes(highlight) && "bg-warning/10")}
              >
                <div className="flex items-center gap-2">
                  <span className="font-mono text-sm font-medium">{rel}</span>
                  <Badge variant={kind.variant} className="text-[10px]">
                    {kind.label}
                  </Badge>
                </div>
                <div className="min-w-0 text-sm">
                  <RuleView rule={rule} />
                </div>
              </div>
            );
          })}
        </div>
      )}
    </Card>
  );
}

function Versions({ models, selected, onSelect }: { models?: AuthzModel[]; selected: string; onSelect: (id: string) => void }) {
  const app = useAppId();
  const svc = useSvc();
  const qc = useQueryClient();
  const [activating, setActivating] = useState<AuthzModel | null>(null);
  const activate = useMutation({
    mutationFn: (id: string) => api.activateModel(app, svc, id),
    onSuccess: () => {
      toast.success("Model activated");
      void qc.invalidateQueries({ queryKey: ["authz", app, svc] });
    },
    onError: (e) => toast.error(e.message),
  });

  return (
    <Card className="h-fit gap-0 py-0">
      <CardHeader className="border-b py-4">
        <CardTitle className="text-sm">Versions</CardTitle>
        <CardDescription className="text-xs">A new version is stored when the service starts with changed model files.</CardDescription>
      </CardHeader>
      <CardContent className="px-0">
        {!models && <Skeleton className="m-4 h-20" />}
        <ul className="max-h-[32rem] divide-y overflow-y-auto">
          {models?.map((m) => (
            <li key={m.id}>
              <button
                className={cn("flex w-full flex-col gap-1 px-4 py-3 text-left text-sm hover:bg-accent/60", m.id === selected && "bg-accent")}
                onClick={() => onSelect(m.id)}
              >
                <span className="flex items-center gap-2">
                  <Mono>{m.id.slice(-10)}</Mono>
                  {m.is_active && <CheckCircle2Icon className="size-3.5 text-success" />}
                </span>
                <span className="text-xs text-muted-foreground" title={formatDate(m.created_at)}>
                  {timeAgo(m.created_at)} · {m.created_by || "service"}
                </span>
              </button>
              {m.id === selected && !m.is_active && (
                <div className="px-4 pb-3">
                  <Button size="sm" variant="outline" className="w-full" onClick={() => setActivating(m)}>
                    <RotateCcwIcon /> Activate this version
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      </CardContent>
      <ConfirmDialog
        open={!!activating}
        onOpenChange={(o) => !o && setActivating(null)}
        title="Activate this model version?"
        description="Every check of this service uses it within seconds. The service activates its model files again at its next start if they differ."
        confirmLabel="Activate"
        onConfirm={() => activating && activate.mutateAsync(activating.id)}
      />
    </Card>
  );
}
