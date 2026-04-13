package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	sqlitestore "github.com/excalibase/provisioning-poc/internal/storage/sqlite"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	k8sunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// unsealVaultInteractive prompts for Shamir shares and unseals the vault.
// This is the security gate — only someone with unseal keys can proceed.
func unsealVaultInteractive(v *vault.Vault) error {
	if !v.Initialized() {
		return fmt.Errorf("vault is not initialized — start the server first")
	}
	if !v.Sealed() {
		return nil
	}

	fmt.Println("\nVault is sealed. Enter unseal shares to proceed.")
	fmt.Println("(Press Ctrl+C to cancel)")
	scanner := bufio.NewScanner(os.Stdin)

	for i := 1; ; i++ {
		fmt.Printf("Share %d: ", i)
		if !scanner.Scan() {
			return fmt.Errorf("input cancelled")
		}
		share := strings.TrimSpace(scanner.Text())
		if share == "" {
			return fmt.Errorf("empty share")
		}

		progress, err := v.Unseal(share)
		if err != nil {
			return fmt.Errorf("unseal failed: %w", err)
		}
		if progress.Done {
			break
		}
		fmt.Printf("  Progress: %d/%d\n", progress.Progress, progress.Threshold)
	}

	if v.Sealed() {
		return fmt.Errorf("vault still sealed after providing shares")
	}

	fmt.Println("Vault unsealed successfully.")
	fmt.Println()
	return nil
}

// resetPasswordCLI handles the `reset-password` subcommand.
func resetPasswordCLI() {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	username := fs.String("username", "admin", "Username to reset password for")
	dbPath := fs.String("db", "", "SQLite database path (default: from DB_PATH env or ../provisioning-data/excalibase.db)")
	vaultPath := fs.String("vault", "", "Vault database path (default: from STORAGE_PATH env)")
	fs.Parse(os.Args[2:])

	cfg := config.Load()
	if *dbPath == "" {
		*dbPath = cfg.DBPath
	}
	if *vaultPath == "" {
		*vaultPath = filepath.Join(cfg.StoragePath, "vault.bolt")
	}

	// 1. Open vault and require unseal (proof of authority)
	v, err := vault.New(*vaultPath)
	if err != nil {
		log.Fatalf("Open vault: %v", err)
	}

	if err := unsealVaultInteractive(v); err != nil {
		log.Fatalf("Vault: %v", err)
	}

	// 2. Open SQLite
	sqlStore, err := sqlitestore.New(*dbPath)
	if err != nil {
		log.Fatalf("Open database: %v", err)
	}
	defer sqlStore.Close()

	// 3. Find user
	ctx := context.Background()
	user, err := sqlStore.FindUserByUsername(ctx, *username)
	if err != nil {
		log.Fatalf("User '%s' not found: %v", *username, err)
	}

	// 4. Generate new password
	passBytes := make([]byte, 16)
	rand.Read(passBytes)
	newPassword := hex.EncodeToString(passBytes)

	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		log.Fatalf("Hash password: %v", err)
	}

	// 5. Update
	if err := sqlStore.UpdateUserPassword(ctx, user.Username, hash); err != nil {
		log.Fatalf("Update password: %v", err)
	}

	fmt.Println("=== PASSWORD RESET ===")
	fmt.Printf("  Username: %s\n", user.Username)
	fmt.Printf("  Password: %s\n", newPassword)
	fmt.Println("  Login at POST /api/auth/login to get a token")
	fmt.Println("======================")
	fmt.Println("\nStore this password securely. It will not be shown again.")
}

// recoverInstancesCLI handles the `recover-instances` subcommand.
func recoverInstancesCLI() {
	fs := flag.NewFlagSet("recover-instances", flag.ExitOnError)
	dbPath := fs.String("db", "", "SQLite database path")
	vaultPath := fs.String("vault", "", "Vault database path")
	dryRun := fs.Bool("dry-run", false, "Show what would be recovered without writing")
	nsPrefix := fs.String("prefix", "excalibase-", "Namespace prefix to scan")
	fs.Parse(os.Args[2:])

	cfg := config.Load()
	if *dbPath == "" {
		*dbPath = cfg.DBPath
	}
	if *vaultPath == "" {
		*vaultPath = filepath.Join(cfg.StoragePath, "vault.bolt")
	}

	// 1. Open vault and require unseal
	v, err := vault.New(*vaultPath)
	if err != nil {
		log.Fatalf("Open vault: %v", err)
	}

	if err := unsealVaultInteractive(v); err != nil {
		log.Fatalf("Vault: %v", err)
	}

	// 2. Open SQLite
	sqlStore, err := sqlitestore.New(*dbPath)
	if err != nil {
		log.Fatalf("Open database: %v", err)
	}
	defer sqlStore.Close()

	// 3. Bootstrap admin if no users exist
	ctx := context.Background()
	users, _ := sqlStore.FindAllUsers(ctx)
	if len(users) == 0 {
		fmt.Println("No admin users found. Bootstrapping admin account...")
		if err := auth.Bootstrap(ctx, sqlStore); err != nil {
			log.Fatalf("Bootstrap admin: %v", err)
		}
	}

	// 4. Connect to K8s
	k8sClient, err := k8s.NewClient()
	if err != nil {
		log.Fatalf("K8s client: %v", err)
	}

	// 5. List namespaces matching prefix
	namespaces, err := k8sClient.ListNamespaces(ctx, *nsPrefix)
	if err != nil {
		log.Fatalf("List namespaces: %v", err)
	}

	if len(namespaces) == 0 {
		fmt.Printf("No namespaces found with prefix '%s'\n", *nsPrefix)
		return
	}

	// 6. Get existing instances from SQLite
	existing, _ := sqlStore.FindAll()
	existingMap := make(map[string]*domain.DatabaseInstance)
	for _, inst := range existing {
		existingMap[inst.ProjectID] = inst
	}

	// 7. Scan each namespace for CNPG clusters
	type discoveredInstance struct {
		ProjectID string
		Namespace string
		Host      string
		Port      int
		DBName    string
		Status    string
		InSQLite  bool
	}

	var discovered []discoveredInstance

	for _, ns := range namespaces {
		clusters, err := k8sClient.ListCRDs(ctx, k8s.CNPGClusterGVR, ns)
		if err != nil {
			fmt.Printf("  Warning: could not list clusters in %s: %v\n", ns, err)
			continue
		}

		for _, cluster := range clusters {
			clusterName := cluster.GetName()
			phase, _, _ := unstructuredNestedString(cluster, "status", "phase")

			status := "ACTIVE"
			if phase != "" && phase != "Cluster in healthy state" {
				status = "PROVISIONING"
			}

			// CNPG cluster name = "{projectId}-postgres", strip suffix to get project ID
			projectID := strings.TrimSuffix(clusterName, "-postgres")

			// Connection host uses the CNPG service name
			host := fmt.Sprintf("%s-rw.%s.svc.cluster.local", clusterName, ns)

			// Try to get database name from app secret
			dbName := "app"
			secret, err := k8sClient.GetSecret(ctx, ns, clusterName+"-app")
			if err == nil && secret != nil {
				if d, ok := secret["dbname"]; ok {
					dbName = string(d)
				}
			}

			d := discoveredInstance{
				ProjectID: projectID,
				Namespace: ns,
				Host:      host,
				Port:      5432,
				DBName:    dbName,
				Status:    status,
				InSQLite:  existingMap[projectID] != nil,
			}
			discovered = append(discovered, d)
		}
	}

	// 8. Print summary
	fmt.Printf("\n=== RECOVERY SCAN ===\n")
	fmt.Printf("Scanned %d namespaces, found %d CNPG clusters\n\n", len(namespaces), len(discovered))

	newCount := 0
	for _, d := range discovered {
		marker := "EXISTS"
		if !d.InSQLite {
			marker = "NEW"
			newCount++
		}
		fmt.Printf("  [%s] %s in %s (%s) — %s:%d/%s\n",
			marker, d.ProjectID, d.Namespace, d.Status, d.Host, d.Port, d.DBName)
	}

	// Check for orphans (in SQLite but not in K8s)
	discoveredMap := make(map[string]bool)
	for _, d := range discovered {
		discoveredMap[d.ProjectID] = true
	}
	orphanCount := 0
	for _, inst := range existing {
		if !discoveredMap[inst.ProjectID] {
			fmt.Printf("  [ORPHAN] %s — in SQLite but not found in K8s\n", inst.ProjectID)
			orphanCount++
		}
	}

	fmt.Printf("\nSummary: %d found, %d new, %d orphaned\n", len(discovered), newCount, orphanCount)

	if newCount == 0 {
		fmt.Println("Nothing to recover.")
		return
	}

	if *dryRun {
		fmt.Println("\nDry run — no changes made. Remove --dry-run to recover.")
		return
	}

	// 9. Confirm
	fmt.Printf("\nRecover %d instance(s)? [y/N]: ", newCount)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
		fmt.Println("Cancelled.")
		return
	}

	// 10. Insert new instances
	flexNow := &domain.FlexTime{Time: time.Now()}
	for _, d := range discovered {
		if d.InSQLite {
			continue
		}

		orgID := strings.TrimSuffix(d.Namespace, "-"+d.ProjectID)
		inst := &domain.DatabaseInstance{
			ProjectID:    d.ProjectID,
			OrgID:        orgID,
			DBType:       domain.PostgreSQL,
			Tier:         domain.Free,
			Namespace:    d.Namespace,
			Host:         d.Host,
			ReadOnlyHost: fmt.Sprintf("%s-postgres-r.%s.svc.cluster.local", d.ProjectID, d.Namespace), // {projectId}-postgres-r
			Port:         &d.Port,
			DatabaseName: d.DBName,
			Username:     "app",
			SSLMode:      "require",
			Status:       d.Status,
			CurrentStage: domain.StageCompleted,
			CreatedAt:    flexNow,
			UpdatedAt:    flexNow,
		}

		if err := sqlStore.Save(inst); err != nil {
			fmt.Printf("  ERROR saving %s: %v\n", d.ProjectID, err)
		} else {
			fmt.Printf("  Recovered: %s\n", d.ProjectID)
		}
	}

	fmt.Println("\nRecovery complete. Vault credentials should still be intact.")
	fmt.Println("If vault credentials are missing, re-store them via:")
	fmt.Println("  curl -X PUT /api/vault/secrets/projects/<project>/credentials/excalibase_app")
}

// helper to read nested string from unstructured
func unstructuredNestedString(obj *k8sunstructured.Unstructured, fields ...string) (string, bool, error) {
	val, found, err := k8sunstructured.NestedString(obj.Object, fields...)
	return val, found, err
}
