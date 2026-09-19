package natsauth

import (
	"context"
	"testing"
)

// benchStore serves one hash, so the benchmark measures the bcrypt
// comparison and nothing else.
type benchStore struct{ hash string }

func (b benchStore) LookupNatsCredentialHash(_ context.Context, _ string) (string, bool, error) {
	return b.hash, true, nil
}

// BenchmarkAuthenticate measures one credential verification. It is the
// input to the pool sizing: verifications per second is workers divided by
// this cost, bounded by the processor budget.
func BenchmarkAuthenticate(b *testing.B) {
	password, err := NewPassword()
	if err != nil {
		b.Fatalf("NewPassword: %v", err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		b.Fatalf("HashPassword: %v", err)
	}
	store := benchStore{hash: hash}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Authenticate(ctx, store, PrincipalGraphQL, password); err != nil {
			b.Fatalf("Authenticate: %v", err)
		}
	}
}

// BenchmarkAuthenticateParallel reports the throughput the pool actually
// delivers when every worker is busy verifying at once.
func BenchmarkAuthenticateParallel(b *testing.B) {
	password, err := NewPassword()
	if err != nil {
		b.Fatalf("NewPassword: %v", err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		b.Fatalf("HashPassword: %v", err)
	}
	store := benchStore{hash: hash}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			if err := Authenticate(ctx, store, PrincipalGraphQL, password); err != nil {
				b.Fatalf("Authenticate: %v", err)
			}
		}
	})
}
