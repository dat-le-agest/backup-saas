# Runbook: xoay khoá ký JWT

Áp dụng cho mọi service dùng `core/components/jwtc` để ký token: gateway của
từng app (access token người dùng) và `platform/admin` (session admin). Không
cần restart: signer đọc lại thư mục khoá theo chu kỳ.

## Cấu hình

| Biến | Ý nghĩa |
|---|---|
| `JWT_KEYS_DIR` | Thư mục chứa các khoá Ed25519 `*.pem` (PKCS#8). Mọi khoá đều dùng để verify và có trong JWKS. |
| `JWT_KEYS_RELOAD` | Số giây giữa hai lần đọc lại thư mục, mặc định `60`, `0` = không đọc lại. |
| `JWT_ACCESS_TTL` | Thời gian sống của access token (giây), mặc định `600`. |
| `JWT_PRIVATE_KEY_FILE` | Cách cũ, một khoá duy nhất, không xoay được. Không đặt cùng `JWT_KEYS_DIR`. |

Khoá ký là khoá có tên trong file `active` của thư mục (tên file có hoặc không
có `.pem`, hoặc kid). Không có file `active` thì khoá có **tên file lớn nhất**
theo thứ tự từ điển sẽ ký, nên đặt tên theo ngày (`2026-11-01.pem`).

Signer có prefix khác (ví dụ `jwtc.WithFlagPrefix("internal-jwt")`) dùng các biến
tương ứng: `INTERNAL_JWT_KEYS_DIR`, `INTERNAL_JWT_KEYS_RELOAD`…

Trên Kubernetes: mount thư mục từ một Secret. Mỗi khoá là một key của Secret,
`active` cũng là một key. Kubelet cập nhật Secret bằng cách đổi symlink
`..data`, và signer đọc theo chu kỳ nên luôn thấy thay đổi. Kubelet cần tới
khoảng 1–2 phút để đồng bộ, hãy cộng thêm vào các thời gian chờ bên dưới.

## Quy trình

Thời gian chờ dưới đây tính theo mặc định: reload 60s, JWKS của verifier từ xa
được cache 10 phút, access TTL 600s, leeway 30s.

1. **Thêm khoá mới, chưa dùng để ký.**

   ```sh
   make jwt-key keys=<dir> name=2026-11-01
   ```

   Lệnh này ghi `active` = khoá hiện tại (nếu chưa có file `active`) rồi mới
   thêm khoá mới, nên khoá cũ vẫn tiếp tục ký. Sau lần reload kế tiếp, JWKS có
   cả hai khoá. Kiểm tra log của signer:
   `jwt keys reloaded: active=<kid cũ> keys=[<kid cũ> <kid mới>]`.

   **Đợi ≥ 10 phút** (cộng thời gian đồng bộ Secret) để mọi verifier đọc JWKS
   từ xa đã thấy khoá mới. Verifier nằm trong chính signer (gateway) thấy ngay.

2. **Chuyển sang ký bằng khoá mới.**

   ```sh
   make jwt-key-activate keys=<dir> name=2026-11-01
   ```

   Log: `jwt keys reloaded: active=<kid mới> …`. Token ký bằng khoá cũ vẫn hợp
   lệ vì khoá cũ còn trong thư mục.

3. **Gỡ khoá cũ.** Đợi ≥ access TTL + leeway (**≥ 630s**) sau bước 2, rồi xoá
   file khoá cũ. Có thể xoá luôn file `active` nếu khoá mới có tên lớn nhất.

   Từ lúc này token ký bằng khoá cũ bị từ chối, và chúng đã hết hạn.

## Xoay khẩn cấp (khoá bị lộ)

Bỏ qua thời gian chờ: thêm khoá mới, đặt nó làm `active` và **xoá khoá bị lộ
ngay** trong cùng một lần cập nhật. Mọi access token đang sống bị từ chối,
client phải dùng refresh token để lấy token mới. Refresh token là chuỗi opaque
lưu dạng hash nên không bị ảnh hưởng. Session admin bị đăng xuất.

Khi xoay khẩn cấp, verifier từ xa cache JWKS có thể từ chối token mới tới khi
nó gặp kid lạ và tải lại JWKS (tối đa một lần mỗi 30s).

## Khi có lỗi

- Thư mục không đọc được, PEM hỏng, `active` trỏ tới khoá không tồn tại, hai
  file trùng một khoá: lúc **khởi động**, service dừng với lỗi. Khi **reload**,
  signer giữ bộ khoá hiện tại và log ở mức error:
  `reload jwt keys (keeping the current ones): …`.
- Không đặt cả `JWT_KEYS_DIR` và `JWT_PRIVATE_KEY_FILE`.

## Chuyển từ `JWT_PRIVATE_KEY_FILE` sang `JWT_KEYS_DIR`

Chép file khoá hiện tại vào thư mục (ví dụ `2026-10-27.pem`), đổi biến môi
trường từ `JWT_PRIVATE_KEY_FILE` sang `JWT_KEYS_DIR` rồi deploy lại. Kid không
đổi (tính từ public key), nên token đang sống vẫn hợp lệ.

## Khoá của internal token (gateway → service)

Gateway ký internal token bằng một signer riêng (`INTERNAL_JWT_*`), xoay khoá
theo đúng quy trình ở trên với `INTERNAL_JWT_KEYS_DIR`. Thời gian chờ ngắn hơn:
internal token chỉ sống 60s.

- Service đọc JWKS của gateway (`INTERNAL_JWT_JWKS_URL`, mặc định
  `http://<gateway>:9081/.well-known/internal-jwks.json`): giữ khoá mới ở trạng
  thái chỉ verify **≥ 10 phút** (cache JWKS), như bước 1. Ở bước 3 chỉ cần đợi
  ≥ 90s (60s + leeway 30s).
- Service đọc thư mục public key (`INTERNAL_JWT_PUBLIC_KEYS_DIR`): thêm public
  key của khoá mới vào thư mục đó **trước** bước 1, rồi đợi ≥ 1 phút (service
  đọc lại thư mục mỗi phút hoặc khi gặp kid lạ).

  ```sh
  openssl pkey -in <keys>/2026-11-01.pem -pubout -out <public-keys>/2026-11-01.pem
  ```

  Thư mục này chỉ chứa public key: service từ chối file PEM chứa private key.
