package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newManageCmd() *cobra.Command {
	var host, key, user, dir string

	cmd := &cobra.Command{
		Use:   "manage",
		Short: "Manage a remote mailr server via SSH",
	}

	cmd.PersistentFlags().StringVar(&host, "host", "", "Server hostname/IP")
	cmd.PersistentFlags().StringVar(&key, "key", "", "SSH private key path")
	cmd.PersistentFlags().StringVar(&user, "user", "", "SSH username")
	cmd.PersistentFlags().StringVar(&dir, "dir", "", "Remote mailr directory")

	resolve := func() (string, string, string, string) {
		cfg := loadConfig()
		h := host
		if h == "" { h = cfg.resolveHost() }
		k := key
		if k == "" { k = cfg.resolveSSHKey() }
		u := user
		if u == "" { u = cfg.resolveSSHUser() }
		d := dir
		if d == "" { d = cfg.resolveRemoteDir() }
		return h, k, u, d
	}

	requireRemote := func() (string, string, string, string, error) {
		h, k, u, d := resolve()
		if h == "" {
			return "", "", "", "", fmt.Errorf("no remote host configured — run 'mailr manage init' first")
		}
		return h, k, u, d, nil
	}

	// --- init ---
	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Configure SSH connection to remote server",
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(os.Stdin)
			cfg := loadConfig()

			fmt.Printf("Remote host [%s]: ", cfg.resolveHost())
			if line, _ := reader.ReadString('\n'); strings.TrimSpace(line) != "" {
				cfg.RemoteHost = strings.TrimSpace(line)
			}

			fmt.Printf("SSH key path [%s]: ", cfg.resolveSSHKey())
			if line, _ := reader.ReadString('\n'); strings.TrimSpace(line) != "" {
				cfg.SSHKey = strings.TrimSpace(line)
			}

			fmt.Printf("SSH user [%s]: ", cfg.resolveSSHUser())
			if line, _ := reader.ReadString('\n'); strings.TrimSpace(line) != "" {
				cfg.SSHUser = strings.TrimSpace(line)
			}

			fmt.Printf("Remote directory [%s]: ", cfg.resolveRemoteDir())
			if line, _ := reader.ReadString('\n'); strings.TrimSpace(line) != "" {
				cfg.RemoteDir = strings.TrimSpace(line)
			}

			if err := saveConfig(cfg); err != nil {
				return err
			}
			fmt.Println("Config saved to", configPath())
			return nil
		},
	})

	// --- status ---
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show server status",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			fmt.Println("Container status:")
			out, _ := compose(h, k, u, d, "ps")
			fmt.Println(out)

			fmt.Println("\nDisk usage:")
			out, _ = sshCapture(h, k, u, "df -h /")
			fmt.Println(out)

			return nil
		},
	})

	// --- start ---
	cmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start mailr containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			fmt.Println("Starting...")
			out, err := compose(h, k, u, d, "up -d")
			if err != nil { return err }
			fmt.Println(out)

			fmt.Println("Checking health...")
			cfg := loadConfig()
			url := cfg.resolveServerURL()
			if err := waitForHealth(url, 60*time.Second); err != nil {
				fmt.Println("Warning: health check failed")
			} else {
				fmt.Println("Healthy!")
			}
			return nil
		},
	})

	// --- stop ---
	cmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop mailr containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			fmt.Println("Stopping...")
			out, err := compose(h, k, u, d, "down")
			if err != nil { return err }
			fmt.Println(out)
			return nil
		},
	})

	// --- restart ---
	cmd.AddCommand(&cobra.Command{
		Use:   "restart",
		Short: "Restart mailr containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			fmt.Println("Restarting...")
			out, err := compose(h, k, u, d, "restart")
			if err != nil { return err }
			fmt.Println(out)
			return nil
		},
	})

	// --- logs ---
	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "View container logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			lines, _ := cmd.Flags().GetInt("lines")
			noFollow, _ := cmd.Flags().GetBool("no-follow")
			service, _ := cmd.Flags().GetString("service")

			composeArgs := fmt.Sprintf("logs --tail=%d", lines)
			if !noFollow {
				composeArgs += " -f"
			}
			if service != "" {
				composeArgs += " " + service
			}

			remoteCmd := fmt.Sprintf("cd %s && sudo docker compose %s", d, composeArgs)
			return sshExec(h, k, u, remoteCmd)
		},
	}
	logsCmd.Flags().Int("lines", 100, "Number of lines to show")
	logsCmd.Flags().Bool("no-follow", false, "Don't follow log output")
	logsCmd.Flags().String("service", "", "Specific service to show logs for")
	cmd.AddCommand(logsCmd)

	// --- update ---
	cmd.AddCommand(&cobra.Command{
		Use:   "update",
		Short: "Pull latest code, rebuild, and restart",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			fmt.Println("Pulling latest code...")
			out, _ := sshCapture(h, k, u, fmt.Sprintf("cd %s && git pull", d))
			fmt.Println(out)

			fmt.Println("Rebuilding...")
			out, err = compose(h, k, u, d, "up -d --build")
			if err != nil { return err }
			fmt.Println(out)

			sshCapture(h, k, u, "sudo docker image prune -f")

			fmt.Println("Checking health...")
			cfg := loadConfig()
			if err := waitForHealth(cfg.resolveServerURL(), 60*time.Second); err != nil {
				fmt.Println("Warning: health check failed")
			} else {
				fmt.Println("Healthy!")
			}
			return nil
		},
	})

	// --- backup ---
	cmd.AddCommand(&cobra.Command{
		Use:   "backup",
		Short: "Download database backup",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			ts := time.Now().Format("2006-01-02-15-04-05")
			localFile := fmt.Sprintf("mailr-backup-%s.db", ts)
			remoteTmp := "/tmp/mailr-backup.db"

			fmt.Println("Creating backup...")
			compose(h, k, u, d, "stop mailr")
			sshCapture(h, k, u, fmt.Sprintf("sudo docker compose -f %s/docker-compose.yml cp mailr:/data/mailr.db %s", d, remoteTmp))
			compose(h, k, u, d, "start mailr")

			fmt.Println("Downloading...")
			if err := scpDownload(h, k, u, remoteTmp, localFile); err != nil {
				return fmt.Errorf("download: %w", err)
			}
			sshCapture(h, k, u, "rm -f "+remoteTmp)

			fmt.Printf("Backup saved to %s\n", localFile)
			return nil
		},
	})

	// --- restore ---
	cmd.AddCommand(&cobra.Command{
		Use:   "restore <backup-file>",
		Short: "Restore database from backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			localFile := args[0]
			remoteTmp := "/tmp/mailr-restore.db"

			fmt.Print("This will replace the database. Type 'restore' to confirm: ")
			var confirm string
			fmt.Scanln(&confirm)
			if confirm != "restore" {
				return fmt.Errorf("aborted")
			}

			fmt.Println("Uploading...")
			if err := scpUpload(h, k, u, localFile, remoteTmp); err != nil {
				return fmt.Errorf("upload: %w", err)
			}

			compose(h, k, u, d, "stop mailr")
			sshCapture(h, k, u, fmt.Sprintf("sudo docker compose -f %s/docker-compose.yml cp %s mailr:/data/mailr.db", d, remoteTmp))
			sshCapture(h, k, u, "rm -f "+remoteTmp)
			compose(h, k, u, d, "start mailr")

			fmt.Println("Restored!")
			return nil
		},
	})

	// --- domain ---
	cmd.AddCommand(&cobra.Command{
		Use:   "domain <new-domain>",
		Short: "Update the server domain name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			newDomain := args[0]
			fmt.Printf("Updating domain to %s...\n", newDomain)

			sshCapture(h, k, u, fmt.Sprintf("cd %s && sudo sed -i 's/MAILR_DOMAIN=.*/MAILR_DOMAIN=%s/' .env", d, newDomain))
			compose(h, k, u, d, "down")
			compose(h, k, u, d, "up -d")

			fmt.Println("Domain updated. Remember to update your DNS A and MX records.")
			return nil
		},
	})

	// --- cleanup ---
	cmd.AddCommand(&cobra.Command{
		Use:   "cleanup",
		Short: "Clean up Docker resources on the server",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, _, err := requireRemote()
			if err != nil { return err }

			fmt.Println("Cleaning up Docker resources...")
			sshCapture(h, k, u, "sudo docker image prune -af")
			sshCapture(h, k, u, "sudo docker volume prune -f")
			sshCapture(h, k, u, "sudo docker builder prune -af")

			out, _ := sshCapture(h, k, u, "sudo docker system df")
			fmt.Println(out)
			return nil
		},
	})

	// --- ssh ---
	cmd.AddCommand(&cobra.Command{
		Use:   "ssh",
		Short: "Open interactive SSH session",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, _, err := requireRemote()
			if err != nil { return err }
			return sshInteractive(h, k, u)
		},
	})

	// --- env ---
	cmd.AddCommand(&cobra.Command{
		Use:   "env",
		Short: "Show remote .env file",
		RunE: func(cmd *cobra.Command, args []string) error {
			h, k, u, d, err := requireRemote()
			if err != nil { return err }

			out, err := sshCapture(h, k, u, fmt.Sprintf("cat %s/.env 2>/dev/null || echo 'No .env file found'", d))
			if err != nil { return err }
			fmt.Println(out)
			return nil
		},
	})

	return cmd
}
