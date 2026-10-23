import { NavLink } from "react-router";
import { cn } from "@/lib/utils";

// TabsNav: tabs that are routes (the active tab is in the URL).
export function TabsNav({ items, className }: { items: { to: string; label: React.ReactNode; end?: boolean }[]; className?: string }) {
  return (
    <nav className={cn("flex max-w-full gap-1 overflow-x-auto border-b [scrollbar-width:none]", className)}>
      {items.map((it) => (
        <NavLink
          key={it.to}
          to={it.to}
          end={it.end}
          className={({ isActive }) =>
            cn(
              "-mb-px inline-flex shrink-0 items-center gap-2 border-b-2 px-3 py-2 text-sm font-medium transition-colors [&_svg]:size-4",
              isActive ? "border-foreground text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
            )
          }
        >
          {it.label}
        </NavLink>
      ))}
    </nav>
  );
}
