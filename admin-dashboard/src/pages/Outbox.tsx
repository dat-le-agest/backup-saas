import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangleIcon, CheckCircle2Icon, ClockIcon, RefreshCwIcon, RotateCcwIcon } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { ConfirmDialog, CopyText, EmptyRow, ErrorAlert, LoadingRows, Mono, Pager } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, type OutboxMessage } from "@/lib/api";
import { useAppId, useParam } from "@/lib/hooks";
import { formatDate } from "@/lib/utils";

function StatusBadge({ status }: { status: OutboxMessage["status"] }) {
  switch (status) {
    case "processed":
      return <Badge variant="success">processed</Badge>;
    case "failed":
      return <Badge variant="destructive">failed</Badge>;
    case "pending":
    default:
      return <Badge variant="outline" className="border-amber-500/50 text-amber-500">pending</Badge>;
  }
}

function Stat({ icon, label, value }: { icon: React.ReactNode; label: string; value: React.ReactNode }) {
  return (
    <Card className="gap-2">
      <CardHeader className="flex-row items-center justify-between pb-1">
        <span className="text-sm text-muted-foreground">{label}</span>
        <span className="text-muted-foreground [&_svg]:size-4">{icon}</span>
      </CardHeader>
      <CardContent className="text-2xl font-semibold tabular-nums">{value}</CardContent>
    </Card>
  );
}

export default function Outbox() {
  const app = useAppId();
  const qc = useQueryClient();
  const [selectedService, setSelectedService] = useState<string>("");
  const [statusFilter, setStatusFilter] = useParam("status");
  const [page, setPage] = useParam("page");
  const [selectedMsg, setSelectedMsg] = useState<OutboxMessage | null>(null);
  const [confirmRetryAll, setConfirmRetryAll] = useState(false);

  const services = useQuery({
    queryKey: ["outbox", app, "services"],
    queryFn: () => api.outboxServices(app),
  });

  const svcList = services.data ?? [];
  const currentSvc = selectedService || svcList[0] || "";

  const stats = useQuery({
    queryKey: ["outbox", app, currentSvc, "stats"],
    queryFn: () => api.outboxStats(app, currentSvc),
    enabled: !!currentSvc,
    refetchInterval: 5_000,
  });

  const n = Number(page) || 1;
  const currentStatus = statusFilter || "all";

  const messages = useQuery({
    queryKey: ["outbox", app, currentSvc, "messages", currentStatus, n],
    queryFn: () => api.outboxMessages(app, currentSvc, currentStatus, n),
    enabled: !!currentSvc,
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  });

  const retrySingle = useMutation({
    mutationFn: (id: number) => api.retryOutbox(app, currentSvc, id),
    onSuccess: () => {
      toast.success("Message requeued for immediate delivery");
      qc.invalidateQueries({ queryKey: ["outbox", app, currentSvc] });
      setSelectedMsg(null);
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to retry message"),
  });

  const retryAll = useMutation({
    mutationFn: () => api.retryAllOutbox(app, currentSvc),
    onSuccess: (data) => {
      toast.success(`Requeued ${data?.retried ?? 0} failed messages`);
      qc.invalidateQueries({ queryKey: ["outbox", app, currentSvc] });
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to retry all messages"),
  });

  return (
    <div className="space-y-6">
      {/* Service selector if multiple */}
      {svcList.length > 1 && (
        <div className="flex items-center gap-2">
          <span className="text-sm text-muted-foreground">Service:</span>
          {svcList.map((s) => (
            <Button
              key={s}
              variant={currentSvc === s ? "default" : "outline"}
              size="sm"
              onClick={() => {
                setSelectedService(s);
                setPage("1");
              }}
            >
              {s}
            </Button>
          ))}
        </div>
      )}

      {/* Stats Cards */}
      <div className="grid gap-4 sm:grid-cols-4">
        <Stat icon={<RefreshCwIcon />} label="Total Events" value={stats.data?.total ?? "—"} />
        <Stat icon={<ClockIcon className="text-amber-500" />} label="Pending" value={stats.data?.pending ?? "—"} />
        <Stat icon={<CheckCircle2Icon className="text-emerald-500" />} label="Processed" value={stats.data?.processed ?? "—"} />
        <Stat icon={<AlertTriangleIcon className="text-destructive" />} label="Failed (Dead Letter)" value={stats.data?.failed ?? "—"} />
      </div>

      {/* Filter and Actions Bar */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-1.5">
          {(["all", "pending", "failed", "processed"] as const).map((s) => (
            <Button
              key={s}
              variant={currentStatus === s ? "secondary" : "ghost"}
              size="sm"
              className="capitalize"
              onClick={() => {
                setStatusFilter(s === "all" ? "" : s);
                setPage("1");
              }}
            >
              {s}
              {s === "failed" && (stats.data?.failed ?? 0) > 0 && (
                <Badge variant="destructive" className="ml-1.5 px-1.5 py-0 text-xs">
                  {stats.data?.failed}
                </Badge>
              )}
            </Button>
          ))}
        </div>

        {(stats.data?.failed ?? 0) > 0 && (
          <>
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5 text-destructive hover:bg-destructive/10"
              disabled={retryAll.isPending}
              onClick={() => setConfirmRetryAll(true)}
            >
              <RotateCcwIcon className="size-3.5" />
              Retry All Failed ({stats.data?.failed})
            </Button>
            <ConfirmDialog
              open={confirmRetryAll}
              onOpenChange={setConfirmRetryAll}
              title="Retry All Failed Messages?"
              description={`This will immediately requeue all ${stats.data?.failed} failed messages with reset retry counters.`}
              confirmLabel="Retry All"
              destructive
              onConfirm={() => retryAll.mutateAsync()}
            />
          </>
        )}
      </div>

      <ErrorAlert error={services.error || stats.error || messages.error} />

      {/* Messages Table */}
      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-20">ID</TableHead>
              <TableHead>Topic</TableHead>
              <TableHead>Key</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Attempts</TableHead>
              <TableHead>Created</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {messages.isLoading && <LoadingRows cols={7} />}
            {messages.data?.data.map((m) => (
              <TableRow key={m.id} className="cursor-pointer" onClick={() => setSelectedMsg(m)}>
                <TableCell>
                  <Mono>{m.id}</Mono>
                </TableCell>
                <TableCell className="font-medium">{m.topic}</TableCell>
                <TableCell>
                  <Mono className="text-muted-foreground">{m.key || "—"}</Mono>
                </TableCell>
                <TableCell>
                  <StatusBadge status={m.status} />
                </TableCell>
                <TableCell>
                  <span className={m.attempts > 0 ? "font-semibold text-amber-500" : "text-muted-foreground"}>
                    {m.attempts}
                  </span>
                </TableCell>
                <TableCell className="text-muted-foreground">{formatDate(m.created_at)}</TableCell>
                <TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
                  {m.status === "failed" && (
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-7 gap-1 text-xs"
                      disabled={retrySingle.isPending}
                      onClick={() => retrySingle.mutate(m.id)}
                    >
                      <RotateCcwIcon className="size-3" /> Retry
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
            {messages.data?.data.length === 0 && (
              <EmptyRow cols={7}>
                No {currentStatus === "all" ? "" : currentStatus} outbox messages found.
              </EmptyRow>
            )}
          </TableBody>
        </Table>
      </Card>

      <Pager paging={messages.data?.paging} onPage={(p) => setPage(String(p))} />

      {/* Drawer / Sheet to inspect message details */}
      <Sheet open={!!selectedMsg} onOpenChange={(open) => !open && setSelectedMsg(null)}>
        <SheetContent className="w-full sm:max-w-lg overflow-y-auto">
          {selectedMsg && (
            <>
              <SheetHeader>
                <div className="flex items-center gap-2">
                  <StatusBadge status={selectedMsg.status} />
                  <SheetTitle>Message #{selectedMsg.id}</SheetTitle>
                </div>
                <SheetDescription>Topic: <Mono className="text-foreground">{selectedMsg.topic}</Mono></SheetDescription>
              </SheetHeader>

              <div className="mt-6 space-y-4 text-sm">
                {selectedMsg.last_error && (
                  <div className="rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-destructive">
                    <div className="font-semibold text-xs tracking-wide uppercase mb-1">Last Error</div>
                    <pre className="text-xs font-mono whitespace-pre-wrap break-all">{selectedMsg.last_error}</pre>
                  </div>
                )}

                <div className="grid grid-cols-2 gap-4">
                  <div>
                    <span className="text-xs text-muted-foreground block">Key</span>
                    <Mono>{selectedMsg.key || "—"}</Mono>
                  </div>
                  <div>
                    <span className="text-xs text-muted-foreground block">Attempts</span>
                    <span>{selectedMsg.attempts}</span>
                  </div>
                  <div>
                    <span className="text-xs text-muted-foreground block">Created At</span>
                    <span className="text-muted-foreground">{formatDate(selectedMsg.created_at)}</span>
                  </div>
                  <div>
                    <span className="text-xs text-muted-foreground block">
                      {selectedMsg.processed_at ? "Processed At" : selectedMsg.failed_at ? "Failed At" : "Next Attempt"}
                    </span>
                    <span className="text-muted-foreground">
                      {selectedMsg.processed_at
                        ? formatDate(selectedMsg.processed_at)
                        : selectedMsg.failed_at
                        ? formatDate(selectedMsg.failed_at)
                        : formatDate(selectedMsg.next_attempt_at)}
                    </span>
                  </div>
                </div>

                <div>
                  <div className="flex items-center justify-between mb-1">
                    <span className="text-xs font-medium text-muted-foreground">Payload (JSON)</span>
                    <CopyText text={selectedMsg.payload} />
                  </div>
                  <pre className="max-h-60 overflow-auto rounded-md bg-muted p-3 text-xs font-mono">
                    {(() => {
                      try {
                        return JSON.stringify(JSON.parse(selectedMsg.payload), null, 2);
                      } catch {
                        return selectedMsg.payload;
                      }
                    })()}
                  </pre>
                </div>

                {selectedMsg.headers && selectedMsg.headers !== "{}" && (
                  <div>
                    <div className="flex items-center justify-between mb-1">
                      <span className="text-xs font-medium text-muted-foreground">Headers (Trace & Context)</span>
                      <CopyText text={selectedMsg.headers} />
                    </div>
                    <pre className="max-h-40 overflow-auto rounded-md bg-muted p-3 text-xs font-mono">
                      {(() => {
                        try {
                          return JSON.stringify(JSON.parse(selectedMsg.headers), null, 2);
                        } catch {
                          return selectedMsg.headers;
                        }
                      })()}
                    </pre>
                  </div>
                )}

                {selectedMsg.status === "failed" && (
                  <div className="pt-4 border-t">
                    <Button
                      className="w-full gap-2"
                      disabled={retrySingle.isPending}
                      onClick={() => retrySingle.mutate(selectedMsg.id)}
                    >
                      <RotateCcwIcon className="size-4" /> Requeue Message
                    </Button>
                  </div>
                )}
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
