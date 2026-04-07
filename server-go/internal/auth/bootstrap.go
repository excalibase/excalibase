package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Bootstrap creates the initial admin user if no users exist.
// Prints the password once to stdout. User must login to get a token.
func Bootstrap(ctx context.Context, userStore storage.UserStore) error {
	users, err := userStore.FindAllUsers(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}
	if len(users) > 0 {
		return nil // already bootstrapped
	}

	passBytes := make([]byte, 16)
	rand.Read(passBytes)
	adminPass := hex.EncodeToString(passBytes)

	hash, err := HashPassword(adminPass)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := time.Now()
	user := &domain.User{
		ID:           GenerateID(),
		Username:     "admin",
		Email:        "admin@excalibase.local",
		PasswordHash: hash,
		Role:         "platform_admin",
		Active:       true,
		CreatedAt:    &now,
		UpdatedAt:    &now,
	}

	if err := userStore.CreateUser(ctx, user); err != nil {
		return fmt.Errorf("create admin: %w", err)
	}

	log.Println("=== FIRST RUN ===")
	log.Printf("  Username: admin")
	log.Printf("  Password: %s", adminPass)
	log.Println("  Login at POST /api/auth/login to get a token")
	log.Println("=================")

	return nil
}

func GenerateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
