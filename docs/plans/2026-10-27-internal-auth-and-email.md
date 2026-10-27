# Kế hoạch: Internal auth, xoay khoá JWT, email và luồng tài khoản

- Ngày: 2026-10-27
- Phạm vi: mục 1 (bảo mật gateway → service, xoay khoá) và mục 2 (mailer, xác minh email, quên mật khẩu, lời mời)
- Quyết định đã chốt: **Internal JWT** · **thư mục khoá** · **`core/mail` interface + SMTP (Mailpit cho dev)**

---

## 0. Hiện trạng (điểm xuất phát)

| Vấn đề | Code liên quan |
|---|---|
| Service tin `X-User-Id / X-Tenant-Id / X-Tenant-Role` như nhận được | `core/rpc/interceptors.go` `ServerMetadataInterceptor` |
| Gateway chỉ gắn header, không ký gì | `core/rpc/interceptors.go` `ClientMetadataInterceptor`, `core/components/rpcc` `Options()` |
| Mỗi signer chỉ có 1 khoá; `Signer.Verifier()` chỉ nhận đúng khoá đó → đổi khoá là mọi access token đang sống bị từ chối | `core/token/token.go`, `core/components/jwtc/jwtc.go` (`--jwt-private-key-file`) |
| `jwtc` cố định prefix flag `jwt-` → không thể có 2 signer trong một service | `core/components/jwtc/jwtc.go` |
| Không có mailer, không có verify email / reset / invitation | `core/authn/*` |
| Outbox có sẵn nhưng chưa ai chạy relay; DB authn chưa có bảng outbox | `core/outbox`, `core/authn/migrations` |

---

## Phần 1b — Xoay khoá JWT (làm trước, vì 1a dùng lại)

### Thiết kế

**`core/token/keyring.go`** (mới)

```go
type KeyRing struct {        // bất biến; thay cả ring khi reload
    active ed25519.PrivateKey
    activeKid string
    public map[string]ed25519.PublicKey  // mọi khoá trong thư mục
}
func LoadKeyRing(dir string) (*KeyRing, error)
```

Quy ước thư mục (mount từ k8s Secret hoặc volume):

```
$JWT_KEYS_DIR/
  2026-10-01.pem      # khoá cũ  (chỉ verify)
  2026-10-27.pem      # khoá mới (ký)
  active              # tuỳ chọn: tên file hoặc kid của khoá ký
```

- Không có `active` → khoá có **tên file lớn nhất theo thứ tự từ điển** sẽ ký (đặt tên theo ngày là đủ).
- Mọi `*.pem` khác đều được publish ở JWKS và verify được.
- Lỗi khi load: thư mục rỗng, PEM hỏng, `active` trỏ tới khoá không tồn tại, hai file trùng kid. **Fail khi khởi động**; khi reload lỗi thì **giữ ring cũ** và log ở mức error.

**`token.Signer`**: thay `key/kid` bằng `atomic.Pointer[KeyRing]`.
- `Sign` đọc ring hiện tại.
- `Verifier(aud)` dùng một `KeySource` đọc **ring hiện tại** (thay `NewStaticKeys(1 khoá)`).
- `PublicKeys()` trả về toàn bộ ring.
- Thêm `Signer.Swap(*KeyRing)`.
- `NewSigner(key, ...)` giữ nguyên chữ ký (tạo ring 1 khoá), nên test và code cũ không phải sửa.

**`core/components/jwtc`**
- Option `jwtc.WithFlagPrefix(p)` (mặc định `jwt`), cùng kiểu với `ginc.WithFlagPrefix`, để dùng được `jwt-*` và `internal-jwt-*` trong cùng một gateway.
- Flag mới `--<p>-keys-dir` / `<P>_KEYS_DIR`. Giữ `--<p>-private-key-file` để tương thích. Đặt cả hai cùng lúc là lỗi.
- `--<p>-keys-reload` (mặc định `60s`): goroutine poll thư mục, so hash nội dung rồi `Swap`. K8s cập nhật Secret bằng cách đổi symlink, nên poll an toàn hơn inotify. Goroutine dừng trong `Stop()`.
- Log `jwt keys reloaded: active=<kid> keys=[...]` và metric gauge `token.signing_keys{kid,active}`.

### Quy trình xoay (runbook, ghi vào `docs/runbooks/jwt-key-rotation.md`)

1. `make jwt-key dir=… name=2026-11-01`: thêm khoá mới, **ghi `active` = khoá cũ**. Đợi ≥ max(chu kỳ reload, `RemoteJWKS.maxAge` = 10 phút) để mọi verifier từ xa đã thấy khoá mới.
2. Đổi `active` sang khoá mới (hoặc xoá file `active`). Từ giờ token mới được ký bằng khoá mới.
3. Đợi ≥ access TTL (600s) + leeway 30s, rồi xoá file khoá cũ.

Refresh token là chuỗi opaque lưu dạng hash nên không bị ảnh hưởng. Session admin (`platform/admin` dùng `jwtc`) cũng hưởng cơ chế này.

### Makefile
- `jwt-key` nhận thêm `dir=` và `name=` (vẫn giữ `out=`).
- Thêm `jwt-key-activate dir= name=`.

### Test
- `token`: load ring (thư mục rỗng, PEM hỏng, `active` sai, trùng kid); ký bằng khoá A, swap sang B, token A vẫn verify được khi A còn trong ring và bị từ chối khi A bị xoá; JWKS chứa đủ khoá.
- `jwtc`: dir và file loại trừ nhau; reload phát hiện thay đổi; reload lỗi thì giữ ring cũ; `Stop` dừng goroutine (chạy với `-race`).

---

## Phần 1a — Internal JWT giữa gateway và service

### Mô hình

```
Client ─Bearer user JWT─▶ gateway ──X-Internal-Token: <JWT ngắn hạn>──▶ service
                          (verify user JWT +                (verify chữ ký, aud, exp;
                           membership version)                identity CHỈ lấy từ token)
```

- Gateway ký một **internal token** cho từng lời gọi upstream:
  - `iss`=`<app>-gateway` (ví dụ `notes-gateway`)
  - `aud`=tên service (`note`)
  - `sub`=user id, `app`, `tid`, `role`
  - `exp` = now + 60s, `iat`, `jti`
- Dùng **khoá riêng**, không dùng chung khoá ký access token của người dùng: `INTERNAL_JWT_KEYS_DIR`, qua `jwtc.NewSigner(..., jwtc.WithFlagPrefix("internal-jwt"))`.
- Header **`X-Internal-Token`**. Không dùng `Authorization`, vì router của service còn có admin API dùng `Authorization: Bearer <admin key>`.
- Service lấy public key qua JWKS ở **cổng admin nội bộ của gateway**: `GET /.well-known/internal-jwks.json` trên `:9081`. Route này không cần admin key, vì public key không phải bí mật. Bản thân `RemoteJWKS` đã có lazy fetch, cache và refresh khi gặp kid lạ.
  - Tuỳ chọn thay thế: `--internal-jwt-public-keys-dir` (mount cùng Secret), cho môi trường không muốn service phụ thuộc mạng vào gateway lúc khởi động.

### Thay đổi code

**`core/token`**
- `InternalClaims{App, TenantID, Role, jwt.RegisteredClaims}`.
- `(*Signer).SignInternal(id reqctx.Identity, audience string, ttl)`.
- `NewInternalVerifier(keys, issuer, audience)`: bắt buộc có `exp`, `aud` đúng tên service, TTL tối đa 5 phút (chặn token sống quá lâu kể cả khi ký đúng).

**`core/rpc`**
- `ClientIdentityInterceptor(signer, audience)`: nếu ctx có identity thì ký token và đặt `X-Internal-Token`. **Không gửi `X-User-Id/X-Tenant-*` nữa.** `X-Request-Id` vẫn gửi như cũ.
- `ServerIdentityInterceptor(verifier, mode)` thay phần identity trong `ServerMetadataInterceptor`. Phần request id giữ nguyên.

  | `INTERNAL_AUTH` | Hành vi |
  |---|---|
  | `enforce` (mặc định khi `APP_ENV≠dev`) | Thiếu hoặc sai token → `Unauthenticated`. Header `X-User-Id/...` bị **bỏ qua hoàn toàn**. |
  | `permissive` (dùng khi rollout) | Token hợp lệ thì dùng token. Không có token thì lấy header, log warn và tăng metric `rpc.internal_auth.fallback`. Token **sai** vẫn bị từ chối. |
  | `off` (chỉ dev/test) | Hành vi cũ. Khởi động bị từ chối nếu `APP_ENV=prd`. |

- `HandlerOptions(l)` → `HandlerOptions(l, rpc.WithIdentityVerifier(v, mode))`. Bản không truyền option chỉ chạy được ở `off`, để test cũ trong `core/rpc` vẫn compile.

**`core/components`**
- `rpcc.NewClient(..., rpcc.WithIdentitySigner(func() *token.Signer))` và flag `--<prefix>-audience` (mặc định là tên service suy ra từ prefix, ví dụ `note-service` → `note`; đặt thẳng `NOTE_SERVICE_AUDIENCE=note` trong compose cho rõ).
- Component mới `internalauthc` cho service: các flag `--internal-auth` (mode), `--internal-jwt-jwks-url` hoặc `--internal-jwt-public-keys-dir`, `--internal-jwt-issuer`, `--internal-jwt-audience` (mặc định `config.ServiceName`). Cung cấp `Verifier()` và `Mode()`.

**Gateway (`apps/notes/gateway`)**
- `main.go`: thêm component `jwtc.NewSigner(config.ComponentInternalJWT, jwtc.WithFlagPrefix("internal-jwt"))`, truyền signer vào `rpcc.NewClient`.
- `routes.go` `registerAdminRoutes`: thêm `GET /.well-known/internal-jwks.json`.

**Service (`apps/notes/services/note` + `templates/service`, sửa cả hai)**
- `main.go`: thêm `internalauthc`. `transport/rpc/note.go`: `rpc.HandlerOptions(h.logger, rpc.WithIdentityVerifier(...))`. Handler nhận verifier qua DI.
- `scripts/new-service.sh` không cần sửa (copy từ template).

**Gọi service → service (ngoài phạm vi, ghi lại cho sau):** mỗi service có signer riêng (issuer = tên service) và verifier nhận danh sách issuer. Interface ở trên đã đủ chỗ cho việc này.

### Compose và env
- `apps/notes/compose.yaml`:
  - note: `INTERNAL_AUTH: enforce`, `INTERNAL_JWT_JWKS_URL: http://notes-gateway:9081/.well-known/internal-jwks.json`, `INTERNAL_JWT_ISSUER: notes-gateway`.
  - gateway: `NOTE_SERVICE_AUDIENCE: note`. Dev không cần `INTERNAL_JWT_KEYS_DIR` (dùng khoá tạm, như `jwtc` đang làm).
- Cập nhật `.env.example` của gateway, note và template.

### Test
- `core/rpc`:
  - token đúng → identity đúng;
  - thiếu token, sai `aud`, hết hạn, ký bằng khoá lạ, `alg=none`, TTL > 5 phút → `Unauthenticated`;
  - **gửi `X-User-Id: admin` kèm token của u1 → identity là u1**;
  - mode `permissive` dùng header và tăng metric fallback.
- `note/cmd/server/main_test.go`: `TestConnectJSON` đang set `reqctx.HeaderUserID` trực tiếp, cần chuyển sang ký token. Thêm `TestSpoofedHeadersIgnored`.
- `core/httpx/httpxtest.GRPCClient`: thêm tham số signer (hoặc biến thể `GRPCClientAs`) để test gateway đi qua đúng luồng ký.
- Gateway `TestNotesThroughGateway` chạy end-to-end với `enforce`.

### Rollout (môi trường đã chạy)
1. Deploy gateway mới (ký token và vẫn gửi header cũ trong một bản phát hành: thêm cờ tạm `--rpc-legacy-headers`).
2. Deploy service với `INTERNAL_AUTH=permissive`, theo dõi `rpc.internal_auth.fallback` cho tới khi về 0.
3. Đổi service sang `enforce`, rồi gỡ header cũ khỏi gateway.

---

## Phần 2 — Mailer và các luồng tài khoản

### 2.1 `core/mail` + component `mailc`

```go
type Message struct {
    To, Subject, Text, HTML string
    Headers map[string]string   // List-Unsubscribe, X-Request-Id...
}
type Sender interface { Send(ctx context.Context, m Message) error }
```

- Các implementation:
  - `mail/smtp`: `net/smtp` với STARTTLS hoặc TLS ngầm định, AUTH PLAIN, timeout, multipart text+HTML, Message-ID.
  - `mail/logsender`: ghi log, dùng cho dev không có Mailpit.
  - `mail/memory`: dùng cho test, đọc lại mail đã gửi.
- Lỗi 5xx vĩnh viễn (địa chỉ không tồn tại) được bọc `mail.ErrPermanent` để relay không retry vô ích.
- `mailc` flags:
  - `MAIL_DRIVER=smtp|log`
  - `MAIL_SMTP_URL=smtp://user:pass@host:587?tls=starttls|implicit|none`
  - `MAIL_FROM="Notes <no-reply@example.com>"`
  - `MAIL_TIMEOUT=10s`
  - Ở `prd`, nếu `none` thì từ chối khởi động.
- Template: `core/authn/mailtmpl/*.{txt,html}.tmpl` (embed), dùng `html/template` để escape. `authn.Config.MailTemplates fs.FS` cho app override (thương hiệu, ngôn ngữ). Mỗi template nhận `{AppName, Link, ExpiresIn, TenantName, InviterEmail}`.

### 2.2 Gửi qua outbox (không gửi trong request)

- Migration authn `00002_outbox.sql` (copy `outbox.MigrationSQL`).
- `authn` ghi message `mail.send` **trong cùng transaction** với token hoặc lời mời. Như vậy không có mail nào cho token bị rollback, và SMTP chậm hay sập không làm chậm hay hỏng request.
- Gateway khởi động `outbox.NewRelay` (handler `mail.send` gọi `Sender.Send`) và dừng nó trong lúc shutdown. Relay dùng advisory lock nên chạy nhiều replica vẫn an toàn.
- **Payload chứa link có token còn sống**, nên thêm vào `core/outbox`:
  - `Relay.HandleSensitive(topic, h)`: sau khi giao xong thì `UPDATE … SET payload='{}'` cùng lúc với `processed_at`;
  - admin API của outbox ẩn payload của topic nhạy cảm.
- Gắn outbox admin của gateway vào dashboard: thêm `"gateway"` vào `outbox.NewAdminProxy` (handler nội bộ, không qua mạng) để xem mail kẹt hoặc thất bại.

### 2.3 Dữ liệu — migration authn `00003_account_flows.sql`

```sql
ALTER TABLE users ADD COLUMN email_verified_at timestamptz;

-- verify email, reset password: single-use, chỉ lưu hash
CREATE TABLE account_tokens (
    id         bigserial   PRIMARY KEY,
    kind       varchar(32) NOT NULL,        -- verify_email | reset_password
    token_hash varchar(64) NOT NULL UNIQUE,
    user_id    varchar(64) NOT NULL REFERENCES users (id),
    email      varchar(254) NOT NULL,       -- email tại thời điểm phát hành
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_account_tokens_user_kind ON account_tokens (user_id, kind, created_at);

CREATE TABLE invitations (
    id          varchar(64)  PRIMARY KEY,   -- inv_<hex>
    tenant_id   varchar(64)  NOT NULL REFERENCES tenants (id),
    email       varchar(254) NOT NULL,
    role        varchar(32)  NOT NULL,
    token_hash  varchar(64)  NOT NULL UNIQUE,
    invited_by  varchar(64)  NOT NULL REFERENCES users (id),
    expires_at  timestamptz  NOT NULL,
    accepted_at timestamptz,
    accepted_by varchar(64),
    revoked_at  timestamptz,
    created_at  timestamptz  NOT NULL DEFAULT now()
);
-- tối đa một lời mời đang chờ cho mỗi (tenant, email)
CREATE UNIQUE INDEX uq_invitations_pending ON invitations (tenant_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
```

- Model và `Store` thêm các hàm: `CreateAccountToken`, `ConsumeAccountToken(kind, hash, now)` (`UPDATE … SET used_at=now() WHERE used_at IS NULL AND expires_at>now() RETURNING *`, bảo đảm dùng đúng một lần kể cả khi có race), `InvalidateAccountTokens(user, kind)`, `CountRecentAccountTokens(email, kind, since)`, `SetEmailVerified`, cùng các hàm CRUD cho invitation.
- Token: 32 byte ngẫu nhiên, base64url, lưu SHA-256 (tái dùng `newRefreshToken` và `hashRefreshToken` trong `service/ids.go`, đổi tên thành `newOpaqueToken`).

### 2.4 Cấu hình mới trong `authn.Config`

```go
Mailer          outbox-backed (authn tự tạo từ Outbox), nil = tắt mọi luồng email
WebURL          "https://notes.example.com"  // link: /verify-email?token=…, /reset-password?token=…, /invite?token=…
RequireVerifiedEmail bool                    // mặc định false (tương thích)
VerifyTTL 24h · ResetTTL 30m · InviteTTL 7d
```

Thiếu `Mailer` thì các endpoint email trả `503 unavailable`. Dev-login và các test hiện tại không bị ảnh hưởng.

### 2.5 Luồng A — Xác minh email

| Endpoint | Hành vi |
|---|---|
| `POST /v1/auth/register` (đã có) | Thêm: phát token `verify_email` và đưa mail vào outbox. Có `invitation_token` khớp email thì đánh dấu verified luôn (xem luồng C). |
| `POST /v1/auth/verify-email` `{token}` (public) | Consume token, đặt `email_verified_at`. Token sai, hết hạn hoặc đã dùng → `400 invalid_input` với cùng một thông điệp. |
| `POST /v1/auth/resend-verification` (có auth) | Đã verify → 204 và không gửi gì. Throttle tối đa 3 lần/giờ/email. Huỷ các token cũ trước khi phát token mới. |

- `GET /v1/me` trả thêm `email_verified`.
- Khi `RequireVerifiedEmail=true`: `switch-tenant`, `POST /tenants` và `accept invitation` trả `403` với code `email_not_verified`. Vẫn cho đăng nhập để người dùng tự gửi lại mail. Cần thêm sentinel hoặc code này vào `apperr` hoặc dùng `FromCode`.

### 2.6 Luồng B — Quên và đặt lại mật khẩu

| Endpoint | Hành vi |
|---|---|
| `POST /v1/auth/forgot-password` `{email}` (public, rate limit theo IP như hiện có) | **Luôn trả `202` giống hệt nhau**, kể cả email không tồn tại, tài khoản bị khoá, không có mật khẩu hay bị throttle, để không lộ email nào tồn tại. Nếu hợp lệ: huỷ các token reset cũ, phát token 30 phút, đưa mail vào outbox. Throttle tối đa 3 lần/giờ/email. |
| `POST /v1/auth/reset-password` `{token, new_password}` (public) | Trong một uow: consume token, `validatePassword`, hash, `UpdateUserPassword`, `RevokeRefreshTokensOfUser`, **`BumpMembershipVersionsOfUser` + publish cache** (access token đang sống bị coi là cũ ngay), `ResetLoginThrottle`, đặt `email_verified_at` nếu chưa có (đã chứng minh sở hữu email). Sau khi commit: gửi mail "mật khẩu đã thay đổi". |

- Đổi mật khẩu (`ChangePassword`) cũng bump version và gửi mail thông báo, cho khớp hành vi.
- Admin: thêm `POST /admin/v1/users/:id/send-password-reset`. Dashboard có nút "Gửi email đặt lại" bên cạnh "đặt mật khẩu" (cần thêm path vào regex `proxyPaths` trong `platform/admin/internal/handler`).
- Thời gian xử lý: phần ghi DB và outbox chạy giống nhau cho cả hai nhánh tồn tại / không tồn tại, nên không cần dummy hash. Có kiểm tra bằng test đo thời gian thô.

### 2.7 Luồng C — Lời mời thành viên

| Endpoint | Ai | Hành vi |
|---|---|---|
| `POST /v1/members/invitations` `{email, role}` | quyền `can_create_member` | Dùng chung luật cấp role với `MemberService.Add` (tách hàm `checkGrant(caller, role)`: admin không mời được owner…). Đã là thành viên → `409`. Đã có lời mời chờ → `409` (gợi ý dùng resend). Phát token 7 ngày, gửi mail. |
| `GET /v1/members/invitations` | `can_read_member` | Danh sách lời mời đang chờ (không trả token). |
| `POST /v1/members/invitations/:id/resend` | `can_create_member` | Phát token mới (token cũ mất hiệu lực), gia hạn, gửi lại. Throttle. |
| `DELETE /v1/members/invitations/:id` | `can_delete_member` | Đặt `revoked_at`. |
| `GET /v1/invitations/:token` | public | Xem trước: tên tenant, email người mời, role, email được mời đã che một phần, hạn. Token sai → `404`. |
| `POST /v1/invitations/accept` `{token}` | có auth (token không cần tenant) | Email **đã verify** của người gọi phải trùng email được mời (không phân biệt hoa thường), nếu không → `403`. Trong uow: `LockTenant`, kiểm tra lời mời còn hiệu lực, tạo hoặc kích hoạt lại membership (version tăng, `publish` cache), đặt `accepted_at`. Trả về membership. Client gọi `switch-tenant` sau đó. |

- **Người được mời chưa có tài khoản:** trang `/invite` gọi `register` kèm `invitation_token`. Email khớp thì đánh dấu verified luôn (mail mời đã chứng minh quyền sở hữu), rồi gọi `accept`.
- `POST /v1/members` (thêm trực tiếp, đang có) giữ nguyên cho tương thích. Thêm cờ `AllowDirectMemberAdd` (mặc định `true`, khuyến nghị tắt ở prod).
- Xoá tenant (`DeleteTenant`) phải revoke mọi lời mời đang chờ của tenant đó.
- Dashboard: trang Tenants hiển thị lời mời đang chờ và cho revoke. Cần admin API `GET/DELETE /admin/v1/tenants/:id/invitations` và cập nhật `proxyPaths`.

### 2.8 Dev và vận hành
- `deploy/platform.compose.yaml`: thêm service `mailpit` (`axllent/mailpit`, UI `:8025`, SMTP `:1025`). Gateway: `MAIL_SMTP_URL=smtp://mailpit:1025?tls=none`, `MAIL_FROM`, `APP_WEB_URL=http://localhost:8081`. Thêm dòng in ra trong `make up`.
- Metric:
  - `iam.mail.sent{template,result}`
  - `iam.auth.password_resets{result}`
  - `iam.auth.email_verifications{result}`
  - `iam.invitations{action,result}`
- Thêm panel vào `deploy/grafana/dashboards/overview.json`.
- Log: **không bao giờ** ghi token hay link. Email chỉ ghi ở mức debug, đã che.
- Header `Referrer-Policy: no-referrer` trên các trang web nhận token, ghi trong tài liệu cho frontend của app.

### 2.9 Hợp đồng API
- `api/openapi/apps/notes/v1.yaml`: thêm mọi endpoint mới, `email_verified` trong `/me`, `invitation_token` trong register, các mã lỗi `email_not_verified`.
- `admin-dashboard/src/lib/api.ts`: thêm type và lời gọi cho send-reset, danh sách và revoke lời mời, cờ verified.

### 2.10 Test
- `core/mail`: SMTP với một server giả trong test (STARTTLS, AUTH, multipart, lỗi 5xx → `ErrPermanent`).
- `core/outbox`: `HandleSensitive` xoá payload sau khi giao; admin API ẩn payload.
- `core/authn` (`authn_test.go`, testdb Postgres):
  - verify: thành công; token hết hạn, đã dùng, hoặc hai request song song chỉ một cái thắng; resend bị throttle.
  - forgot: phản hồi giống hệt nhau cho email có và không có; throttle.
  - reset: refresh token bị thu hồi; access token cũ bị từ chối vì membership version; login throttle được reset.
  - invite: các luật role (admin mời owner → 403); trùng lời mời → 409; accept sai email → 403; accept khi chưa verify với `RequireVerifiedEmail`; accept sau khi bị revoke hoặc hết hạn; register kèm invitation_token; xoá tenant thì lời mời bị revoke.
  - mail: có message `mail.send` khi transaction commit, không có khi rollback.
- Gateway `main_test.go`: end-to-end với `mail/memory`, đọc link từ mail rồi gọi tiếp endpoint.

---

## 3. Thứ tự thực hiện và ước lượng

| # | Hạng mục | Phụ thuộc | Ước lượng |
|---|---|---|---|
| 1 | `token.KeyRing` + `Signer` dùng atomic ring + test | — | 1 ngày |
| 2 | `jwtc`: `WithFlagPrefix`, `keys-dir`, reload + runbook + Makefile | 1 | 1 ngày |
| 3 | `token.SignInternal` / `InternalVerifier`; `rpc` Client/ServerIdentityInterceptor + mode | 1 | 1,5 ngày |
| 4 | `rpcc.WithIdentitySigner`, `internalauthc`, nối vào gateway, note và template, JWKS endpoint, sửa test, compose | 2, 3 | 1,5 ngày |
| 5 | `core/mail` + `mailc` + Mailpit trong compose | — (làm song song với 1–4) | 1 ngày |
| 6 | Outbox trong DB authn, relay ở gateway, `HandleSensitive` | 5 | 1 ngày |
| 7 | Migration 00003 + store + template mail | 6 | 1 ngày |
| 8 | Luồng A (verify) + B (forgot/reset) + bump version khi đổi mật khẩu | 7 | 2 ngày |
| 9 | Luồng C (lời mời) + tách `checkGrant` | 7 | 2 ngày |
| 10 | Admin API, proxy regex, dashboard (verified, send-reset, invitations) | 8, 9 | 1,5 ngày |
| 11 | OpenAPI, `.env.example`, Grafana, tài liệu | tất cả | 0,5 ngày |

Tổng cộng khoảng **14 ngày công**. Mỗi dòng (hoặc cặp 1–2, 3–4) là một PR riêng, tách từ `dev`.

## 4. Rủi ro và cách giảm

| Rủi ro | Cách giảm |
|---|---|
| Bật `enforce` làm gãy service đang chạy | Rollout ba bước qua `permissive` và metric fallback (mục 1a) |
| Gateway sập thì service không lấy được JWKS | Cache của `RemoteJWKS` giữ khoá cũ; tuỳ chọn `public-keys-dir` |
| Lệch đồng hồ giữa các máy với TTL internal 60s | Leeway 30s (giống verifier hiện tại); TTL cấu hình được |
| Token trong payload outbox | `HandleSensitive` + TTL ngắn + retention của outbox |
| Mail bị đánh dấu spam | Ngoài phạm vi code: SPF/DKIM/DMARC cho domain gửi (ghi vào runbook) |
| Lộ email tồn tại qua forgot-password hoặc invitation preview | Phản hồi giống hệt nhau; che email; rate limit theo IP và theo email |
| `templates/service` và `note` lệch nhau | Sửa cả hai trong cùng một PR; có thể thêm CI `diff -r` (ngoài phạm vi) |

## 5. Câu hỏi mở (cần chốt trước khi làm bước 8–9)

1. `RequireVerifiedEmail` mặc định `false` để tương thích. Có muốn bật mặc định cho app mới không?
2. Có giữ `POST /v1/members` (thêm trực tiếp không cần mời) ở prod không?
3. Template mail: chỉ tiếng Anh, hay cần i18n theo `Accept-Language` ngay từ đầu?
4. Có cần cho người dùng tự đổi email không (luồng xác minh email mới)? Hiện kế hoạch chưa có.
