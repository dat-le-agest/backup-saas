import { useQuery } from "@tanstack/react-query";
import { LayoutGridIcon, LogOutIcon, MenuIcon, MoonIcon, ShieldCheckIcon, SunIcon, UsersIcon } from "lucide-react";
import { useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

const navItem = ({ isActive }: { isActive: boolean }) =>
  cn(
    "flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm transition-colors [&_svg]:size-4 [&_svg]:shrink-0",
    isActive ? "bg-accent font-medium text-accent-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
  );

export function StatusDot({ up, className }: { up: boolean; className?: string }) {
  return (
    <span className={cn("relative flex size-2 shrink-0", className)} title={up ? "up" : "down"}>
      {up && <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-success opacity-40" />}
      <span className={cn("relative inline-flex size-2 rounded-full", up ? "bg-success" : "bg-destructive")} />
    </span>
  );
}

function Nav({ onNavigate }: { onNavigate?: () => void }) {
  const apps = useQuery({ queryKey: ["apps"], queryFn: api.apps, refetchInterval: 15_000 });
  return (
    <nav className="flex flex-1 flex-col gap-6 overflow-y-auto p-3" onClick={onNavigate}>
      <div className="grid gap-0.5">
        <NavLink to="/" end className={navItem}>
          <LayoutGridIcon /> Overview
        </NavLink>
        <NavLink to="/admins" className={navItem}>
          <UsersIcon /> Admins
        </NavLink>
      </div>
      <div className="grid gap-0.5">
        <div className="px-2.5 pb-1 text-xs font-medium tracking-wide text-muted-foreground uppercase">Apps</div>
        {apps.data?.map((a) => (
          <NavLink key={a.id} to={`/apps/${a.id}`} className={navItem}>
            <StatusDot up={a.up} className="mx-1" />
            <span className="truncate">{a.name}</span>
          </NavLink>
        ))}
        {apps.data?.length === 0 && <p className="px-2.5 text-xs text-muted-foreground">No app configured (ADMIN_APPS).</p>}
        {apps.isLoading && <div className="mx-2.5 h-4 animate-pulse rounded bg-muted" />}
      </div>
    </nav>
  );
}

function AccountMenu() {
  const { admin, signOut } = useSession();
  const { theme, toggle } = useTheme();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button className="flex w-full items-center gap-2.5 rounded-md p-2 text-left text-sm hover:bg-accent">
          <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground uppercase">
            {admin.email[0]}
          </span>
          <span className="truncate">{admin.email}</span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="w-56">
        <DropdownMenuLabel className="truncate font-normal text-muted-foreground">{admin.email}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={toggle}>
          {theme === "dark" ? <SunIcon /> : <MoonIcon />}
          {theme === "dark" ? "Light mode" : "Dark mode"}
        </DropdownMenuItem>
        <DropdownMenuItem variant="destructive" onSelect={() => void signOut()}>
          <LogOutIcon /> Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function Brand() {
  return (
    <Link to="/" className="flex items-center gap-2 font-semibold">
      <span className="flex size-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
        <ShieldCheckIcon className="size-4" />
      </span>
      SaaS Admin
    </Link>
  );
}

export function AppShell() {
  const [open, setOpen] = useState(false);
  const location = useLocation();
  return (
    <div className="flex min-h-svh">
      <div className="hidden w-60 shrink-0 border-r bg-sidebar md:block">
        <aside className="sticky top-0 flex h-svh flex-col">
          <div className="flex h-14 items-center border-b px-4">
            <Brand />
          </div>
          <Nav />
          <div className="border-t p-2">
            <AccountMenu />
          </div>
        </aside>
      </div>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b bg-background/95 px-4 backdrop-blur md:hidden">
          <Button variant="ghost" size="icon-sm" onClick={() => setOpen(true)} aria-label="Menu">
            <MenuIcon />
          </Button>
          <Brand />
        </header>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent className="right-auto left-0 w-64 gap-0 border-r border-l-0 bg-sidebar p-0">
            <SheetTitle className="flex h-14 items-center border-b px-4">
              <Brand />
            </SheetTitle>
            <Nav onNavigate={() => setOpen(false)} />
            <div className="border-t p-2">
              <AccountMenu />
            </div>
          </SheetContent>
        </Sheet>

        <main key={location.pathname.split("/").slice(0, 3).join("/")} className="mx-auto w-full max-w-7xl flex-1 space-y-6 p-4 md:p-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
