import { FormEvent, useEffect, useState } from "react";
import { Admin, api, formatDate } from "../api";
import { ErrorBox } from "../components/ui";

export default function Admins() {
  const [admins, setAdmins] = useState<Admin[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const load = () => api.admins().then(setAdmins, setError);
  useEffect(() => {
    load();
  }, []);

  const add = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      await api.createAdmin(email, password);
      setEmail("");
      setPassword("");
      load();
    } catch (err) {
      setError(err);
    }
  };

  return (
    <>
      <div className="page-head">
        <h2>Admins</h2>
      </div>
      <ErrorBox error={error} />
      <table>
        <thead>
          <tr>
            <th>Email</th>
            <th>Last sign-in</th>
            <th>Created</th>
          </tr>
        </thead>
        <tbody>
          {admins.map((a) => (
            <tr key={a.id}>
              <td>{a.email}</td>
              <td>{formatDate(a.last_login_at)}</td>
              <td>{formatDate(a.created_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <form className="card inline-form" onSubmit={add}>
        <h3>Add an admin</h3>
        <input type="email" placeholder="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
        <input type="password" placeholder="password (12+ characters)" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={12} />
        <button type="submit">Add</button>
      </form>
    </>
  );
}
