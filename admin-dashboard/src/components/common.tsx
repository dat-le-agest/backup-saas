import { AlertCircleIcon, CheckIcon, ChevronLeftIcon, ChevronRightIcon, CopyIcon } from "lucide-react";
import { useState } from "react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { TableCell, TableRow } from "@/components/ui/table";
import { errorMessage, type Paging } from "@/lib/api";
import { cn } from "@/lib/utils";

export function PageHeader({ title, description, actions }: { title: React.ReactNode; description?: React.ReactNode; actions?: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-4">
      <div className="min-w-0 space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

export function ErrorAlert({ error, title = "Something went wrong", className }: { error: unknown; title?: string; className?: string }) {
  if (!error) return null;
  return (
    <Alert variant="destructive" className={className}>
      <AlertCircleIcon />
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription>{errorMessage(error)}</AlertDescription>
    </Alert>
  );
}

export function Pager({ paging, onPage }: { paging?: Paging; onPage: (p: number) => void }) {
  if (!paging || paging.total === 0) return null;
  const pages = Math.max(1, Math.ceil(paging.total / paging.limit));
  const from = (paging.page - 1) * paging.limit + 1;
  const to = Math.min(paging.total, paging.page * paging.limit);
  return (
    <div className="flex items-center justify-between gap-2 text-sm text-muted-foreground">
      <span>
        {from}–{to} of {paging.total}
      </span>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="icon-sm" disabled={paging.page <= 1} onClick={() => onPage(paging.page - 1)} aria-label="Previous page">
          <ChevronLeftIcon />
        </Button>
        <span>
          Page {paging.page} / {pages}
        </span>
        <Button variant="outline" size="icon-sm" disabled={paging.page >= pages} onClick={() => onPage(paging.page + 1)} aria-label="Next page">
          <ChevronRightIcon />
        </Button>
      </div>
    </div>
  );
}

export function EmptyRow({ cols, children }: { cols: number; children: React.ReactNode }) {
  return (
    <TableRow className="hover:bg-transparent">
      <TableCell colSpan={cols} className="h-24 text-center text-muted-foreground">
        {children}
      </TableCell>
    </TableRow>
  );
}

export function LoadingRows({ cols, rows = 5 }: { cols: number; rows?: number }) {
  return (
    <>
      {Array.from({ length: rows }, (_, i) => (
        <TableRow key={i} className="hover:bg-transparent">
          {Array.from({ length: cols }, (_, j) => (
            <TableCell key={j}>
              <div className="h-4 animate-pulse rounded bg-muted" />
            </TableCell>
          ))}
        </TableRow>
      ))}
    </>
  );
}

const roleVariant: Record<string, "default" | "secondary" | "outline"> = { owner: "default", admin: "secondary" };

export function RoleBadge({ role }: { role: string }) {
  return <Badge variant={roleVariant[role] ?? "outline"}>{role}</Badge>;
}

export function Mono({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("font-mono text-xs", className)}>{children}</span>;
}

export function CopyText({ text, className }: { text: string; className?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <span className={cn("group inline-flex items-center gap-1", className)}>
      <Mono className="break-all">{text}</Mono>
      <button
        type="button"
        className="text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 hover:text-foreground"
        onClick={(e) => {
          e.stopPropagation();
          void navigator.clipboard?.writeText(text).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1200);
          });
        }}
        aria-label="Copy"
      >
        {copied ? <CheckIcon className="size-3.5" /> : <CopyIcon className="size-3.5" />}
      </button>
    </span>
  );
}

// ConfirmDialog: controlled; onConfirm may be async (the button waits).
export function ConfirmDialog(props: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: React.ReactNode;
  confirmLabel?: string;
  destructive?: boolean;
  onConfirm: () => Promise<unknown> | void | null;
}) {
  const [busy, setBusy] = useState(false);
  const confirm = async () => {
    setBusy(true);
    try {
      await props.onConfirm();
      props.onOpenChange(false);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{props.title}</DialogTitle>
          <DialogDescription>{props.description}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={() => props.onOpenChange(false)}>
            Cancel
          </Button>
          <Button variant={props.destructive ? "destructive" : "default"} onClick={confirm} disabled={busy}>
            {props.confirmLabel ?? "Confirm"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function Field({ label, htmlFor, hint, children }: { label: string; htmlFor?: string; hint?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <label htmlFor={htmlFor} className="text-sm font-medium">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}
