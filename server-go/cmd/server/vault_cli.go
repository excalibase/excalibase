package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// vault status|unseal talk to the RUNNING server over HTTP: the unsealed key
// lives in that process's memory, so another process cannot open it directly
// (EXC-579). The key and the admin token are read without echo and never
// passed on the command line, where any user could read them from ps.
const vaultCLIUsage = `Usage: excalibase-provisioning vault <status|unseal> [--url URL]

  status   show whether the vault is initialized and sealed
  unseal   submit an unseal key; repeat until the threshold is reached

The admin token comes from EXCALIBASE_TOKEN or a prompt; the unseal key is
always prompted for (or piped on stdin). --url defaults to the local server
(http://127.0.0.1:$PORT), so on Kubernetes run it inside the provisioning pod:
  kubectl -n excalibase-platform exec -it deploy/provisioning -- excalibase-provisioning vault unseal`

type vaultCLIStatus struct {
	Initialized    bool   `json:"initialized"`
	Sealed         bool   `json:"sealed"`
	Threshold      int    `json:"threshold"`
	Progress       int    `json:"progress"`
	UnsealProvider string `json:"unsealProvider"`
}

func vaultCLIDefaultURL(getenv func(string) string) string {
	port := getenv("PORT")
	if port == "" {
		port = "24005"
	}
	return "http://127.0.0.1:" + port
}

// runVaultCLI runs one vault subcommand. readSecret prompts without echo.
func runVaultCLI(args []string, getenv func(string) string, out io.Writer, readSecret func(prompt string) (string, error)) error {
	if len(args) == 0 {
		return errors.New(vaultCLIUsage)
	}
	flags := flag.NewFlagSet("vault "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	baseURL := flags.String("url", vaultCLIDefaultURL(getenv), "server URL")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n%s", err, vaultCLIUsage)
	}
	client := &vaultCLIClient{baseURL: strings.TrimRight(*baseURL, "/"), http: &http.Client{Timeout: 30 * time.Second}}
	switch args[0] {
	case "status":
		status, err := client.status()
		if err != nil {
			return err
		}
		printVaultStatus(out, status)
		return nil
	case "unseal":
		return vaultCLIUnseal(client, getenv, out, readSecret)
	}
	return fmt.Errorf("unknown vault command %q\n%s", args[0], vaultCLIUsage)
}

func printVaultStatus(out io.Writer, status vaultCLIStatus) {
	fmt.Fprintf(out, "initialized: %t\nsealed: %t\nunseal progress: %d/%d\nunseal provider: %s\n",
		status.Initialized, status.Sealed, status.Progress, status.Threshold, status.UnsealProvider)
}

func vaultCLIUnseal(client *vaultCLIClient, getenv func(string) string, out io.Writer, readSecret func(string) (string, error)) error {
	status, err := client.status()
	if err != nil {
		return err
	}
	if !status.Initialized {
		return errors.New("the vault is not initialized: initialize it in Studio (/setup) first")
	}
	if !status.Sealed {
		fmt.Fprintln(out, "The vault is already unsealed.")
		return nil
	}
	token := strings.TrimSpace(getenv("EXCALIBASE_TOKEN"))
	if token == "" {
		if token, err = readSecret("Platform admin token: "); err != nil {
			return err
		}
	}
	for status.Sealed {
		key, err := readSecret(fmt.Sprintf("Unseal key (%d of %d): ", status.Progress+1, status.Threshold))
		if err != nil {
			return err
		}
		sealed, progress, err := client.unseal(strings.TrimSpace(token), strings.TrimSpace(key))
		if err != nil {
			return err
		}
		status.Sealed, status.Progress = sealed, progress
	}
	fmt.Fprintln(out, "The vault is unsealed.")
	return nil
}

type vaultCLIClient struct {
	baseURL string
	http    *http.Client
}

func (c *vaultCLIClient) status() (vaultCLIStatus, error) {
	var status vaultCLIStatus
	resp, err := c.http.Get(c.baseURL + "/api/vault/status")
	if err != nil {
		return status, fmt.Errorf("reach the server at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return status, fmt.Errorf("vault status: HTTP %d", resp.StatusCode)
	}
	return status, json.NewDecoder(resp.Body).Decode(&status)
}

func (c *vaultCLIClient) unseal(token, key string) (sealed bool, progress int, err error) {
	body, _ := json.Marshal(map[string]string{"share": key})
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/vault/unseal", bytes.NewReader(body))
	if err != nil {
		return true, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return true, 0, fmt.Errorf("reach the server at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	var reply struct {
		Sealed   bool   `json:"sealed"`
		Progress int    `json:"progress"`
		Error    string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&reply)
	if resp.StatusCode != http.StatusOK {
		return true, 0, fmt.Errorf("unseal refused (HTTP %d): %s", resp.StatusCode, reply.Error)
	}
	return reply.Sealed, reply.Progress, nil
}

// readSecretFromTerminal prompts on stderr and reads without echo; piped
// input (a password manager) is read one line at a time.
func readSecretFromTerminal(stdin *os.File) func(string) (string, error) {
	var piped []byte
	return func(prompt string) (string, error) {
		if term.IsTerminal(int(stdin.Fd())) {
			fmt.Fprint(os.Stderr, prompt)
			secret, err := term.ReadPassword(int(stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(secret), err
		}
		if piped == nil {
			all, err := io.ReadAll(stdin)
			if err != nil {
				return "", err
			}
			piped = all
		}
		line, rest, _ := bytes.Cut(piped, []byte("\n"))
		piped = rest
		if len(bytes.TrimSpace(line)) == 0 {
			return "", fmt.Errorf("no input for %q", strings.TrimSpace(prompt))
		}
		return string(line), nil
	}
}

func vaultCLI() {
	err := runVaultCLI(os.Args[2:], os.Getenv, os.Stdout, readSecretFromTerminal(os.Stdin))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
