import { Navigate, Route, Routes } from "react-router";
import { AppShell } from "@/components/layout";
import { ErrorAlert } from "@/components/common";
import { SessionProvider, useMe } from "@/lib/session";
import Admins from "@/pages/Admins";
import AppLayout from "@/pages/AppLayout";
import AppOverview from "@/pages/AppOverview";
import ApiDocs from "@/pages/ApiDocs";
import Outbox from "@/pages/Outbox";
import Login from "@/pages/Login";
import Overview from "@/pages/Overview";
import Tenants from "@/pages/Tenants";
import Users from "@/pages/Users";
import AuthzLayout, { AuthzIndex } from "@/pages/authz/AuthzLayout";
import Changes from "@/pages/authz/Changes";
import Check from "@/pages/authz/Check";
import Model from "@/pages/authz/Model";
import Tuples from "@/pages/authz/Tuples";

export default function App() {
  const me = useMe();

  if (me.isPending) {
    return <div className="flex min-h-svh items-center justify-center text-sm text-muted-foreground">Loading…</div>;
  }
  if (me.isError) {
    return (
      <div className="mx-auto max-w-md p-8">
        <ErrorAlert error={me.error} title="The admin service is unreachable" />
      </div>
    );
  }
  if (!me.data) {
    return (
      <Routes>
        <Route path="*" element={<Login />} />
      </Routes>
    );
  }

  return (
    <SessionProvider admin={me.data}>
      <Routes>
        <Route element={<AppShell />}>
          <Route index element={<Overview />} />
          <Route path="admins" element={<Admins />} />
          <Route path="apps/:app" element={<AppLayout />}>
            <Route index element={<AppOverview />} />
            <Route path="users" element={<Users />} />
            <Route path="tenants" element={<Tenants />} />
            <Route path="outbox" element={<Outbox />} />
            <Route path="docs" element={<ApiDocs />} />
            <Route path="authz" element={<AuthzIndex />} />
            <Route path="authz/:svc" element={<AuthzLayout />}>
              <Route index element={<Navigate to="model" replace />} />
              <Route path="model" element={<Model />} />
              <Route path="tuples" element={<Tuples />} />
              <Route path="check" element={<Check />} />
              <Route path="changes" element={<Changes />} />
            </Route>
          </Route>
          <Route path="login" element={<Navigate to="/" replace />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </SessionProvider>
  );
}
