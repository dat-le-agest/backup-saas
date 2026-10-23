# Admin dashboard

The platform's admin UI: React 19, Vite, Tailwind v4 with shadcn/ui-style
components (Radix), TanStack Query and React Router. It talks only to the admin
service's API (`/api/*`, `platform/admin`), which forwards per-app calls to each
app's internal admin API with the app's key.

- **Overview**: every app's status, users and tenants.
- **Per app**: users (disable, reset password), tenants (members, roles, delete),
  and **Authorization**, for each service's ReBAC engine: the active model and its
  versions (types, relations, YAML source, activate), stored relationships (filter,
  add, delete), permission checks and object listing (with a tenant role as
  context), and the changelog (who changed what).
- **Admins**: dashboard accounts.

```sh
make admin-web-dev   # dev server on :5173, /api proxied to the admin service on :8090
make admin-web       # build into platform/admin/web/dist, embedded in the admin binary
```

`ADMIN_API_URL` points the dev server at another admin service.
