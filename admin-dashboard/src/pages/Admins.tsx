import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PlusIcon } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import { EmptyRow, ErrorAlert, Field, LoadingRows, PageHeader } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { formatDate, timeAgo } from "@/lib/utils";

function AddAdmin() {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const create = useMutation({
    mutationFn: () => api.createAdmin(email, password),
    onSuccess: (a) => {
      toast.success(`${a.email} can now sign in`);
      void qc.invalidateQueries({ queryKey: ["admins"] });
      setOpen(false);
      setEmail("");
      setPassword("");
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  return (
    <Dialog open={open} onOpenChange={(o) => (setOpen(o), create.reset())}>
      <DialogTrigger asChild>
        <Button size="sm">
          <PlusIcon /> Add admin
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Add an admin</DialogTitle>
            <DialogDescription>Admins manage every app: users, tenants and authorization.</DialogDescription>
          </DialogHeader>
          <Field label="Email" htmlFor="admin-email">
            <Input id="admin-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </Field>
          <Field label="Password" htmlFor="admin-password" hint="12 characters or more.">
            <Input id="admin-password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={12} required />
          </Field>
          <ErrorAlert error={create.error} title="Could not add the admin" />
          <DialogFooter>
            <Button type="submit" disabled={create.isPending}>
              Add admin
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export default function Admins() {
  const { admin: me } = useSession();
  const admins = useQuery({ queryKey: ["admins"], queryFn: api.admins });
  return (
    <>
      <PageHeader title="Admins" description="Accounts that can sign in to this dashboard." actions={<AddAdmin />} />
      <ErrorAlert error={admins.error} />
      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Email</TableHead>
              <TableHead>Last sign-in</TableHead>
              <TableHead>Created</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {admins.isLoading && <LoadingRows cols={3} rows={2} />}
            {admins.data?.map((a) => (
              <TableRow key={a.id}>
                <TableCell className="font-medium">
                  {a.email} {a.id === me.id && <Badge variant="secondary">you</Badge>}
                </TableCell>
                <TableCell title={formatDate(a.last_login_at)}>{timeAgo(a.last_login_at)}</TableCell>
                <TableCell>{formatDate(a.created_at)}</TableCell>
              </TableRow>
            ))}
            {admins.data?.length === 0 && <EmptyRow cols={3}>No admin.</EmptyRow>}
          </TableBody>
        </Table>
      </Card>
    </>
  );
}
