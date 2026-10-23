import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useParams, useSearchParams } from "react-router";
import { api } from "./api";

// useParam: one search param as state (lists keep their filters in the URL,
// so they survive reloads and can be linked to). Setting "" removes it.
export function useParam(name: string): [string, (v: string, extra?: Record<string, string>) => void] {
  const [params, setParams] = useSearchParams();
  const set = (v: string, extra: Record<string, string> = {}) =>
    setParams(
      (p) => {
        const next = new URLSearchParams(p);
        for (const [k, val] of Object.entries({ [name]: v, ...extra })) {
          if (val) next.set(k, val);
          else next.delete(k);
        }
        return next;
      },
      { replace: true },
    );
  return [params.get(name) ?? "", set];
}

// useSearchInput: a search box over the search param name: the input updates
// at once, the URL (and the query) after a pause in typing, back to page 1.
export function useSearchInput(name: string): [input: string, onChange: (v: string) => void, value: string] {
  const [value, setValue] = useParam(name);
  const [input, setInput] = useState(value);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const onChange = (v: string) => {
    setInput(v);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setValue(v, { page: "" }), 250);
  };
  return [input, onChange, value];
}

// the app (/apps/:app) and the service of its authorization engine (/authz/:svc)
export function useAppId(): string {
  return useParams().app ?? "";
}

export function useSvc(): string {
  return useParams().svc ?? "";
}

// useActiveModel: the service's active model (relation suggestions, checks).
export function useActiveModel() {
  const app = useAppId();
  const svc = useSvc();
  return useQuery({ queryKey: ["authz", app, svc, "model", "active"], queryFn: () => api.model(app, svc), staleTime: 60_000 });
}
