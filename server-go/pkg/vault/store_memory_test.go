package vault

import (
	"reflect"
	"sync"
	"testing"
)

func TestMemoryStore_BarrierRoundTrip(t *testing.T) {
	store := NewMemoryStore()

	barrier, meta, err := store.GetBarrier()
	if err != nil || barrier != nil || meta != nil {
		t.Fatalf("fresh store want (nil, nil, nil), got (%v, %v, %v)", barrier, meta, err)
	}

	if err := store.PutBarrier([]byte("enc"), []byte("meta")); err != nil {
		t.Fatalf("PutBarrier: %v", err)
	}
	barrier, meta, err = store.GetBarrier()
	if err != nil || string(barrier) != "enc" || string(meta) != "meta" {
		t.Fatalf("GetBarrier after put: (%q, %q, %v)", barrier, meta, err)
	}

	if err := store.PutBarrier([]byte("enc2"), []byte("meta2")); err != nil {
		t.Fatalf("PutBarrier overwrite: %v", err)
	}
	barrier, meta, _ = store.GetBarrier()
	if string(barrier) != "enc2" || string(meta) != "meta2" {
		t.Fatalf("PutBarrier must overwrite, got (%q, %q)", barrier, meta)
	}
}

func TestMemoryStore_ReturnsCopies(t *testing.T) {
	store := NewMemoryStore()
	input := []byte("secret")
	if err := store.PutSecret("a", input); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'

	got, _ := store.GetSecret("a")
	if string(got) != "secret" {
		t.Fatalf("store must copy on put, got %q", got)
	}
	got[0] = 'Y'
	again, _ := store.GetSecret("a")
	if string(again) != "secret" {
		t.Fatalf("store must copy on get, got %q", again)
	}
}

func TestMemoryStore_Secrets(t *testing.T) {
	seed := func(store *MemoryStore) {
		for _, path := range []string{"projects/a/creds", "projects/a/token", "projects/b/creds", "pki/ca"} {
			if err := store.PutSecret(path, []byte("v:"+path)); err != nil {
				t.Fatal(err)
			}
		}
	}

	tests := []struct {
		name      string
		run       func(store *MemoryStore) (any, error)
		want      any
		wantError bool
	}{
		{
			name: "get missing returns nil, nil",
			run: func(store *MemoryStore) (any, error) {
				data, err := store.GetSecret("nope")
				return data, err
			},
			want: []byte(nil),
		},
		{
			name: "get existing",
			run: func(store *MemoryStore) (any, error) {
				return store.GetSecret("pki/ca")
			},
			want: []byte("v:pki/ca"),
		},
		{
			name: "put overwrites",
			run: func(store *MemoryStore) (any, error) {
				if err := store.PutSecret("pki/ca", []byte("new")); err != nil {
					return nil, err
				}
				return store.GetSecret("pki/ca")
			},
			want: []byte("new"),
		},
		{
			name: "delete existing then get",
			run: func(store *MemoryStore) (any, error) {
				if err := store.DeleteSecret("pki/ca"); err != nil {
					return nil, err
				}
				return store.GetSecret("pki/ca")
			},
			want: []byte(nil),
		},
		{
			name: "delete missing is not an error",
			run: func(store *MemoryStore) (any, error) {
				return nil, store.DeleteSecret("nope")
			},
			want: nil,
		},
		{
			name: "list all sorted",
			run: func(store *MemoryStore) (any, error) {
				return store.ListSecrets("")
			},
			want: []string{"pki/ca", "projects/a/creds", "projects/a/token", "projects/b/creds"},
		},
		{
			name: "list by prefix",
			run: func(store *MemoryStore) (any, error) {
				return store.ListSecrets("projects/a/")
			},
			want: []string{"projects/a/creds", "projects/a/token"},
		},
		{
			name: "list unmatched prefix is empty",
			run: func(store *MemoryStore) (any, error) {
				return store.ListSecrets("zzz")
			},
			want: []string(nil),
		},
		{
			name: "delete prefix removes matches and reports count",
			run: func(store *MemoryStore) (any, error) {
				deleted, err := store.DeletePrefix("projects/a/")
				if err != nil {
					return nil, err
				}
				remaining, err := store.ListSecrets("")
				return struct {
					Deleted   int
					Remaining []string
				}{deleted, remaining}, err
			},
			want: struct {
				Deleted   int
				Remaining []string
			}{2, []string{"pki/ca", "projects/b/creds"}},
		},
		{
			name: "delete prefix with no match deletes nothing",
			run: func(store *MemoryStore) (any, error) {
				return store.DeletePrefix("zzz")
			},
			want: 0,
		},
		{
			name: "delete empty prefix is rejected",
			run: func(store *MemoryStore) (any, error) {
				return store.DeletePrefix("")
			},
			want:      0,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore()
			seed(store)
			got, err := tt.run(store)
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v, wantError %v", err, tt.wantError)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMemoryStore_CloseKeepsData(t *testing.T) {
	store := NewMemoryStore()
	if err := store.PutSecret("a", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, _ := store.GetSecret("a"); string(got) != "1" {
		t.Fatalf("data must survive Close so a reopened vault handle sees it, got %q", got)
	}
}

func TestMemoryStore_ConcurrentAccess(t *testing.T) {
	store := NewMemoryStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "p/" + string(rune('a'+i%26))
			_ = store.PutSecret(path, []byte("x"))
			_, _ = store.GetSecret(path)
			_, _ = store.ListSecrets("p/")
			_, _ = store.DeletePrefix("p/z")
		}(i)
	}
	wg.Wait()
}
