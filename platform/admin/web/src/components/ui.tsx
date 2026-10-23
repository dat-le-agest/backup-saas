import { ApiError, Paging } from "../api";

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const msg = error instanceof ApiError ? `${error.message}` : error instanceof Error ? error.message : String(error);
  return <div className="error box">{msg}</div>;
}

export function Pager({ paging, onPage }: { paging?: Paging; onPage: (p: number) => void }) {
  if (!paging) return null;
  const pages = Math.max(1, Math.ceil(paging.total / paging.limit));
  return (
    <div className="pager">
      <button className="ghost" disabled={paging.page <= 1} onClick={() => onPage(paging.page - 1)}>
        ← Prev
      </button>
      <span className="muted">
        page {paging.page} of {pages} · {paging.total} total
      </span>
      <button className="ghost" disabled={paging.page >= pages} onClick={() => onPage(paging.page + 1)}>
        Next →
      </button>
    </div>
  );
}

export function RoleBadge({ role }: { role: string }) {
  return <span className={`badge role-${role}`}>{role}</span>;
}
