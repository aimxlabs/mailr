package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	var serverURL string
	var adminToken string

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Interactive setup wizard — connect to a mailr server and create a domain",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			reader := bufio.NewReader(os.Stdin)

			// Step 1: Server URL
			if serverURL == "" {
				def := cfg.resolveServerURL()
				fmt.Printf("Server URL [%s]: ", def)
				line, _ := reader.ReadString('\n')
				line = strings.TrimSpace(line)
				if line == "" {
					serverURL = def
				} else {
					serverURL = line
				}
			}
			serverURL = strings.TrimRight(serverURL, "/")

			// Step 2: Connection test
			fmt.Printf("Testing connection to %s...\n", serverURL)
			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Get(serverURL + "/health")
			if err != nil {
				return fmt.Errorf("cannot reach server: %w", err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("health check returned %d", resp.StatusCode)
			}
			fmt.Println("Connected!")

			// Step 3: Domain name
			fmt.Print("Domain name (e.g. mail.example.com): ")
			domainName, _ := reader.ReadString('\n')
			domainName = strings.TrimSpace(domainName)
			if domainName == "" {
				return fmt.Errorf("domain name is required")
			}

			// Step 4: Create domain via API
			body, _ := json.Marshal(map[string]string{"name": domainName})
			req, _ := http.NewRequest("POST", serverURL+"/api/domains", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if adminToken == "" {
				adminToken = os.Getenv("MAILR_ADMIN_TOKEN")
			}
			if adminToken != "" {
				req.Header.Set("Authorization", "Bearer "+adminToken)
			}

			resp, err = client.Do(req)
			if err != nil {
				return fmt.Errorf("creating domain: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 201 {
				var errResp map[string]string
				json.NewDecoder(resp.Body).Decode(&errResp)
				return fmt.Errorf("failed to create domain: %s", errResp["error"])
			}

			var domain struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				AuthToken string `json:"auth_token"`
			}
			json.NewDecoder(resp.Body).Decode(&domain)

			// Step 5: Save config
			cfg.ServerURL = serverURL
			cfg.Token = domain.AuthToken
			if err := saveConfig(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not save config: %v\n", err)
			}

			fmt.Println()
			fmt.Println("Domain created!")
			fmt.Printf("  ID:         %s\n", domain.ID)
			fmt.Printf("  Name:       %s\n", domain.Name)
			fmt.Printf("  Auth Token: %s\n", domain.AuthToken)
			fmt.Println()
			fmt.Println("DNS records to add:")
			fmt.Printf("  MX   %s  →  %s  (priority 10)\n", domain.Name, domain.Name)
			fmt.Printf("  A    %s  →  <your-server-ip>\n", domain.Name)
			fmt.Println()
			fmt.Println("Generate DKIM key:")
			fmt.Printf("  curl -X POST %s/api/domains/%s/dkim/generate \\\n", serverURL, domain.ID)
			fmt.Printf("    -H 'Authorization: Bearer <admin-token>'\n")
			fmt.Println()
			fmt.Println("Config saved to", configPath())

			return nil
		},
	}

	cmd.Flags().StringVarP(&serverURL, "server", "s", "", "Server URL")
	cmd.Flags().StringVar(&adminToken, "admin-token", "", "Admin token for domain creation")

	return cmd
}
