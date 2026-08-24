package main

import (
	"context"
	"database/sql"
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
	storagePath := envOr("VAULT_STORAGE_PATH", "./vault-data")
	dbURL := os.Getenv("VAULT_DB_URL")
	corsOrigins := envOr("CORS_ORIGINS", "*")
	accessTokens := parseTokens(os.Getenv("VAULT_ACCESS_TOKENS"))

	// KMS-wrapped unseal: if VAULT_UNSEAL_KEY_CIPHERTEXT is set, decrypt it into
	// VAULT_UNSEAL_KEY so the auto-unseal below works with no plaintext key stored.
	if err := kmsseal.ResolveUnsealKeyEnv(context.Background()); err != nil {
		log.Fatalf("KMS unseal-key resolve: %v", err)
	}

	var v *vault.Vault
	if dbURL != "" {
		db, err := sql.Open("postgres", dbURL)
		if err != nil {
			log.Fatalf("Failed to connect Postgres: %v", err)
		}
		defer db.Close()

		vaultStore := vault.NewPostgresStore(db)
		v, err = vault.NewWithStore(vaultStore)
		if err != nil {
			log.Fatalf("Failed to init vault (postgres): %v", err)
		}
		log.Println("Using PostgreSQL vault store")
	} else {
		vaultPath := storagePath + "/vault.bolt"
		var err error
		v, err = vault.New(vaultPath)
		if err != nil {
			log.Fatalf("Failed to init vault (bbolt): %v", err)
		}
		log.Println("Using bbolt vault store")
	}
	defer v.Close()

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
