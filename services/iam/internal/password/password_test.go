package password

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// cheap parameters keep tests fast; the format and logic are the same
var testParams = Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func TestHashVerify(t *testing.T) {
	h := NewHasher(testParams, 0)
	ctx := context.Background()

	encoded, err := h.Hash(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("format: %s", encoded)
	}

	again, _ := h.Hash(ctx, "correct horse battery staple")
	if again == encoded {
		t.Fatal("salt not random")
	}

	if ok, rehash, err := h.Verify(ctx, "correct horse battery staple", encoded); !ok || rehash || err != nil {
		t.Fatalf("verify: %v %v %v", ok, rehash, err)
	}
	if ok, _, err := h.Verify(ctx, "Correct horse battery staple", encoded); ok || err != nil {
		t.Fatalf("wrong password accepted: %v %v", ok, err)
	}
}

func TestNeedsRehashWhenParamsChange(t *testing.T) {
	old := NewHasher(testParams, 0)
	encoded, _ := old.Hash(context.Background(), "s3cret-password")

	stronger := testParams
	stronger.Iterations = 2
	ok, rehash, err := NewHasher(stronger, 0).Verify(context.Background(), "s3cret-password", encoded)
	if !ok || !rehash || err != nil {
		t.Fatalf("expected ok + rehash, got %v %v %v", ok, rehash, err)
	}
}

func TestMalformedHashes(t *testing.T) {
	h := NewHasher(testParams, 0)
	good, _ := h.Hash(context.Background(), "pw-pw-pw-pw")
	parts := strings.Split(good, "$")

	cases := map[string]string{
		"empty":         "",
		"bcrypt":        "$2a$10$abcdefghijklmnopqrstuuWzVpG1b0kq2bQq1e5mY8zG7m0Y1lX6",
		"wrong version": strings.Replace(good, "v=19", "v=16", 1),
		"huge memory":   strings.Replace(good, "m=64", "m=99999999", 1),
		"zero time":     strings.Replace(good, "t=1", "t=0", 1),
		"bad salt":      strings.Join([]string{"", parts[1], parts[2], parts[3], "!!!", parts[5]}, "$"),
		"short key":     strings.Join([]string{"", parts[1], parts[2], parts[3], parts[4], "AAAA"}, "$"),
	}
	for name, encoded := range cases {
		if ok, _, err := h.Verify(context.Background(), "pw-pw-pw-pw", encoded); ok || err == nil {
			t.Errorf("%s: expected error, got ok=%v err=%v", name, ok, err)
		}
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	h := NewHasher(Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 2)
	var inFlight, peak atomic.Int32

	// wrap acquire/release by observing slot usage while hashing
	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				n := int32(len(h.slots))
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				inFlight.Store(n)
			}
		}
	}()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.Hash(context.Background(), "x")
		}()
	}
	wg.Wait()
	close(stop)
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrency %d > 2", p)
	}
}

func TestHashRespectsContext(t *testing.T) {
	h := NewHasher(testParams, 1)
	h.slots <- struct{}{} // all slots busy
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "x"); err == nil {
		t.Fatal("expected context error while waiting for a slot")
	}
}
