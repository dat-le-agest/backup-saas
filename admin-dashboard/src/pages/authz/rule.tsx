import type { Rule } from "@/lib/api";
import { cn } from "@/lib/utils";

function Token({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("rounded px-1 py-0.5 font-mono text-xs", className)}>{children}</span>;
}

const Op = ({ children }: { children: React.ReactNode }) => <span className="px-1 text-xs font-medium text-muted-foreground uppercase">{children}</span>;

// RuleView renders a rule as an expression: [user, team#member] OR owner OR admin FROM tenant.
export function RuleView({ rule, nested = false }: { rule: Rule; nested?: boolean }) {
  const paren = (n: React.ReactNode) => (nested ? <span className="text-muted-foreground">({n}<span className="text-muted-foreground">)</span></span> : n);
  if (rule.assignable) {
    return (
      <span className="inline-flex flex-wrap items-center gap-1">
        <span className="text-muted-foreground">[</span>
        {rule.assignable.map((s, i) => (
          <span key={s}>
            <Token className="bg-sky-500/10 text-sky-700 dark:text-sky-300">{s}</Token>
            {i < rule.assignable!.length - 1 && <span className="text-muted-foreground">,</span>}
          </span>
        ))}
        <span className="text-muted-foreground">]</span>
      </span>
    );
  }
  if (rule.computed) return <Token className="bg-violet-500/10 text-violet-700 dark:text-violet-300">{rule.computed}</Token>;
  if (rule.from) {
    return (
      <span className="inline-flex items-center">
        <Token className="bg-violet-500/10 text-violet-700 dark:text-violet-300">{rule.from.relation}</Token>
        <Op>from</Op>
        <Token className="bg-amber-500/10 text-amber-700 dark:text-amber-300">{rule.from.via}</Token>
      </span>
    );
  }
  const list = (rules: Rule[], op: string) =>
    paren(
      <span className="inline-flex flex-wrap items-center gap-y-1">
        {rules.map((r, i) => (
          <span key={i} className="inline-flex items-center">
            {i > 0 && <Op>{op}</Op>}
            <RuleView rule={r} nested />
          </span>
        ))}
      </span>,
    );
  if (rule.union) return list(rule.union, "or");
  if (rule.intersection) return list(rule.intersection, "and");
  if (rule.exclude) {
    return paren(
      <span className="inline-flex flex-wrap items-center">
        <RuleView rule={rule.exclude.base} nested />
        <Op>but not</Op>
        <RuleView rule={rule.exclude.subtract} nested />
      </span>,
    );
  }
  return <span className="text-muted-foreground">nobody</span>;
}
