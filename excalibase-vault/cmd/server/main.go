package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/excalibase/excalibase-vault/internal/handler"
	"github.com/excalibase/provisioning-poc/pkg/kmsseal"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/lib/pq"
)

func main() {
	port := envOr("PORT", "24010")
	dbURL, err := vaultDBURL()
	if err != nil {
		log.Fatal(err)
	}
	corsOrigins := envOr("CORS_ORIGINS", "*")
	accessTokens := parseTokens(os.Getenv("VAULT_ACCESS_TOKENS"))

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Failed to connect Postgres: %v", err)
	}
	defer db.Close()

	v, err := vault.NewWithStore(vault.NewPostgresStore(db))
	if err != nil {
		log.Fatalf("Failed to init vault (postgres): %v", err)
	}
	defer v.Close()
	log.Println("Using PostgreSQL vault store")

	if err := unsealAtBoot(v); err != nil {
		log.Fatalf("vault unseal: %v", err)
	}

	// Auto-unseal
	if unsealKey := os.Getenv("VAULT_UNSEAL_KEY"); unsealKey != "" && v.Initialized() && v.Sealed() {
		progress, err := v.Unseal(unsealKey)
		if err != nil {
			log.Printf("WARN: auto-unseal failed: %v", err)
		} else if progress.Done {
			log.Println("Vault auto-unsealed")
		}
	}

	h := handler.NewVaultHandler(v)
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware(corsOrigins))

	h.Routes(r, accessTokens)

	addr := fmt.Sprintf(":%s", port)
	log.Printf("Excalibase vault starting on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func corsMiddleware(origins string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", origins)
			w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// vaultDBURL is the Postgres connection the vault stores its barrier and
// secrets in. There is no file-backed fallback: the vault is Postgres-only.
func vaultDBURL() (string, error) {
	url := strings.TrimSpace(os.Getenv("VAULT_DB_URL"))
	if url == "" {
		return "", errors.New("VAULT_DB_URL is required (Postgres connection string for the vault store)")
	}
	return url, nil
}

// unsealAtBoot opens the vault from a KMS-wrapped unseal key when one is
// configured, and refuses to start rather than run sealed when it cannot.
// Mixing it with a plaintext VAULT_UNSEAL_KEY is refused: no fallback.
func unsealAtBoot(v *vault.Vault) error {
	ciphertext := os.Getenv("VAULT_UNSEAL_KEY_CIPHERTEXT")
	if ciphertext == "" {
		return nil
	}
	if os.Getenv("VAULT_UNSEAL_KEY") != "" {
		return errors.New("set either VAULT_UNSEAL_KEY_CIPHERTEXT or VAULT_UNSEAL_KEY, not both")
	}
	client, err := kmsseal.NewClient(context.Background())
	if err != nil {
		return err
	}
	return kmsseal.UnsealAtBoot(context.Background(), v, client, ciphertext)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseTokens(raw string) []string {
	if raw == "" {
		return nil
	}
	var tokens []string
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tokens = append(tokens, t)
		}
	}
	return tokens
}
