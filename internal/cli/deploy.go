package cli

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

//go:embed cloud-init.sh
var cloudInitScript string

const defaultRepo = "https://github.com/aimxlabs/mailr.git"

func newDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy mailr to a cloud server",
	}
	cmd.AddCommand(newDeployAWSCmd())
	cmd.AddCommand(newDeployDOCmd())
	cmd.AddCommand(newTeardownCmd())
	return cmd
}

// --- AWS ---

func newDeployAWSCmd() *cobra.Command {
	var instanceType, keyName, sgName, vpcID, subnetID, repo string

	cmd := &cobra.Command{
		Use:   "aws <domain> [region]",
		Short: "Deploy to AWS EC2",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			domain := args[0]
			region := "us-east-1"
			if len(args) > 1 {
				region = args[1]
			}

			fmt.Printf("Deploying mailr to AWS (%s) for %s...\n", region, domain)

			// 1. Security group
			sgID, err := ensureSecurityGroup(region, sgName, vpcID)
			if err != nil {
				return fmt.Errorf("security group: %w", err)
			}
			fmt.Printf("  Security group: %s\n", sgID)

			// 2. SSH key pair
			keyPath, err := ensureKeyPair(region, keyName)
			if err != nil {
				return fmt.Errorf("key pair: %w", err)
			}
			fmt.Printf("  SSH key: %s\n", keyPath)

			// 3. Find AMI
			amiID, err := findUbuntuAMI(region)
			if err != nil {
				return fmt.Errorf("AMI lookup: %w", err)
			}
			fmt.Printf("  AMI: %s\n", amiID)

			// 4. Prepare cloud-init
			userData := prepareUserData(domain, repo)

			// 5. Launch instance
			fmt.Println("  Launching instance...")
			launchArgs := []string{
				"ec2", "run-instances",
				"--region", region,
				"--image-id", amiID,
				"--instance-type", instanceType,
				"--key-name", keyName,
				"--security-group-ids", sgID,
				"--user-data", userData,
				"--block-device-mappings", `[{"DeviceName":"/dev/sda1","Ebs":{"VolumeSize":20,"VolumeType":"gp3"}}]`,
				"--tag-specifications", `ResourceType=instance,Tags=[{Key=Name,Value=mailr-server},{Key=mailr-domain,Value=` + domain + `}]`,
				"--query", "Instances[0].InstanceId",
				"--output", "text",
			}
			if subnetID != "" {
				launchArgs = append(launchArgs, "--subnet-id", subnetID)
			}
			instanceID, err := awsCLI(launchArgs...)
			if err != nil {
				return fmt.Errorf("launch: %w", err)
			}
			fmt.Printf("  Instance: %s\n", instanceID)

			// Wait for running
			fmt.Println("  Waiting for instance to start...")
			awsCLI("ec2", "wait", "instance-running", "--region", region, "--instance-ids", instanceID)

			// 6. Elastic IP
			allocOut, err := awsCLI("ec2", "allocate-address", "--region", region, "--query", "AllocationId", "--output", "text")
			if err != nil {
				return fmt.Errorf("allocate IP: %w", err)
			}
			awsCLI("ec2", "associate-address", "--region", region, "--instance-id", instanceID, "--allocation-id", allocOut)

			publicIP, _ := awsCLI("ec2", "describe-instances", "--region", region,
				"--instance-ids", instanceID,
				"--query", "Reservations[0].Instances[0].PublicIpAddress",
				"--output", "text")
			fmt.Printf("  Public IP: %s\n", publicIP)

			// 7. Health check
			fmt.Println("  Waiting for mailr to become healthy...")
			if err := waitForHealth("http://"+publicIP+":4802", 5*time.Minute); err != nil {
				return fmt.Errorf("health check: %w", err)
			}
			fmt.Println("  Healthy!")

			// Save config
			cfg := loadConfig()
			cfg.RemoteHost = publicIP
			cfg.SSHKey = keyPath
			cfg.SSHUser = "ubuntu"
			cfg.RemoteDir = "/opt/mailr"
			cfg.ServerURL = "https://" + domain
			saveConfig(cfg)

			fmt.Println()
			fmt.Printf("mailr deployed!\n")
			fmt.Printf("  Server:   https://%s\n", domain)
			fmt.Printf("  SMTP:     %s:25\n", publicIP)
			fmt.Printf("  SSH:      ssh -i %s ubuntu@%s\n", keyPath, publicIP)
			fmt.Println()
			fmt.Println("Point your DNS:")
			fmt.Printf("  A    %s  →  %s\n", domain, publicIP)
			fmt.Printf("  MX   %s  →  %s  (priority 10)\n", domain, domain)
			fmt.Println()
			fmt.Printf("Get admin token: ssh -i %s ubuntu@%s 'sudo cat /opt/mailr/.admin-token'\n", keyPath, publicIP)

			return nil
		},
	}

	cmd.Flags().StringVar(&instanceType, "instance-type", "t3.small", "EC2 instance type")
	cmd.Flags().StringVar(&keyName, "key-name", "mailr-deploy-key", "SSH key pair name")
	cmd.Flags().StringVar(&sgName, "sg-name", "mailr-server", "Security group name")
	cmd.Flags().StringVar(&vpcID, "vpc-id", "", "VPC ID (default VPC if empty)")
	cmd.Flags().StringVar(&subnetID, "subnet-id", "", "Subnet ID")
	cmd.Flags().StringVar(&repo, "repo", defaultRepo, "Git repository URL")

	return cmd
}

func ensureSecurityGroup(region, name, vpcID string) (string, error) {
	// Check if exists
	query := fmt.Sprintf("--filters Name=group-name,Values=%s", name)
	if vpcID != "" {
		query += fmt.Sprintf(" Name=vpc-id,Values=%s", vpcID)
	}
	existing, _ := awsCLI("ec2", "describe-security-groups", "--region", region,
		"--filters", "Name=group-name,Values="+name,
		"--query", "SecurityGroups[0].GroupId", "--output", "text")
	if existing != "" && existing != "None" {
		return existing, nil
	}

	// Create
	createArgs := []string{"ec2", "create-security-group", "--region", region,
		"--group-name", name, "--description", "mailr mail relay server",
		"--query", "GroupId", "--output", "text"}
	if vpcID != "" {
		createArgs = append(createArgs, "--vpc-id", vpcID)
	}
	sgID, err := awsCLI(createArgs...)
	if err != nil {
		return "", err
	}

	// Ingress rules: SSH, SMTP, HTTP, HTTPS
	ports := []struct{ port, proto string }{
		{"22", "tcp"}, {"25", "tcp"}, {"80", "tcp"}, {"443", "tcp"},
	}
	for _, p := range ports {
		awsCLI("ec2", "authorize-security-group-ingress", "--region", region,
			"--group-id", sgID, "--protocol", p.proto, "--port", p.port, "--cidr", "0.0.0.0/0")
	}
	return sgID, nil
}

func ensureKeyPair(region, keyName string) (string, error) {
	home, _ := os.UserHomeDir()
	keyPath := filepath.Join(home, ".ssh", keyName+".pem")

	// Check if exists in AWS
	existing, _ := awsCLI("ec2", "describe-key-pairs", "--region", region,
		"--key-names", keyName, "--query", "KeyPairs[0].KeyName", "--output", "text")
	if existing == keyName {
		return keyPath, nil
	}

	// Create
	privKey, err := awsCLI("ec2", "create-key-pair", "--region", region,
		"--key-name", keyName, "--query", "KeyMaterial", "--output", "text")
	if err != nil {
		return "", err
	}

	os.MkdirAll(filepath.Dir(keyPath), 0700)
	if err := os.WriteFile(keyPath, []byte(privKey), 0600); err != nil {
		return "", err
	}
	return keyPath, nil
}

func findUbuntuAMI(region string) (string, error) {
	return awsCLI("ec2", "describe-images", "--region", region,
		"--owners", "099720109477",
		"--filters",
		"Name=name,Values=ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*",
		"Name=state,Values=available",
		"--query", "sort_by(Images,&CreationDate)[-1].ImageId",
		"--output", "text")
}

// --- DigitalOcean ---

func newDeployDOCmd() *cobra.Command {
	var size, name, repo string

	cmd := &cobra.Command{
		Use:     "digitalocean <domain> [region]",
		Aliases: []string{"do"},
		Short:   "Deploy to DigitalOcean",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			domain := args[0]
			region := "nyc1"
			if len(args) > 1 {
				region = args[1]
			}

			fmt.Printf("Deploying mailr to DigitalOcean (%s) for %s...\n", region, domain)

			// 1. SSH key
			keyPath, fingerprint, err := ensureDOSSHKey()
			if err != nil {
				return fmt.Errorf("SSH key: %w", err)
			}
			fmt.Printf("  SSH key: %s\n", keyPath)

			// 2. Import key to DO
			doKeyID, err := importDOKey(fingerprint, keyPath)
			if err != nil {
				return fmt.Errorf("import key: %w", err)
			}

			// 3. Prepare cloud-init
			userData := prepareUserData(domain, repo)

			// 4. Create droplet
			fmt.Println("  Creating droplet...")
			dropletOut, err := doCLI("compute", "droplet", "create", name,
				"--region", region, "--size", size,
				"--image", "ubuntu-22-04-x64",
				"--ssh-keys", doKeyID,
				"--user-data", userData,
				"--tag-name", "mailr",
				"--format", "ID", "--no-header", "--wait")
			if err != nil {
				return fmt.Errorf("create droplet: %w", err)
			}
			dropletID := strings.TrimSpace(dropletOut)
			fmt.Printf("  Droplet: %s\n", dropletID)

			// 5. Reserved IP
			ipOut, err := doCLI("compute", "reserved-ip", "create",
				"--droplet-id", dropletID, "--region", region,
				"--format", "IP", "--no-header")
			if err != nil {
				return fmt.Errorf("reserve IP: %w", err)
			}
			publicIP := strings.TrimSpace(ipOut)
			fmt.Printf("  Public IP: %s\n", publicIP)

			// 6. Health check
			fmt.Println("  Waiting for mailr to become healthy...")
			if err := waitForHealth("http://"+publicIP+":4802", 5*time.Minute); err != nil {
				return fmt.Errorf("health check: %w", err)
			}
			fmt.Println("  Healthy!")

			cfg := loadConfig()
			cfg.RemoteHost = publicIP
			cfg.SSHKey = keyPath
			cfg.SSHUser = "root"
			cfg.RemoteDir = "/opt/mailr"
			cfg.ServerURL = "https://" + domain
			saveConfig(cfg)

			fmt.Println()
			fmt.Printf("mailr deployed!\n")
			fmt.Printf("  Server:   https://%s\n", domain)
			fmt.Printf("  SMTP:     %s:25\n", publicIP)
			fmt.Printf("  SSH:      ssh -i %s root@%s\n", keyPath, publicIP)
			fmt.Println()
			fmt.Println("Point your DNS:")
			fmt.Printf("  A    %s  →  %s\n", domain, publicIP)
			fmt.Printf("  MX   %s  →  %s  (priority 10)\n", domain, domain)
			fmt.Println()
			fmt.Printf("Get admin token: ssh -i %s root@%s 'cat /opt/mailr/.admin-token'\n", keyPath, publicIP)

			return nil
		},
	}

	cmd.Flags().StringVar(&size, "size", "s-1vcpu-1gb", "Droplet size")
	cmd.Flags().StringVar(&name, "name", "mailr-server", "Droplet name")
	cmd.Flags().StringVar(&repo, "repo", defaultRepo, "Git repository URL")

	return cmd
}

func ensureDOSSHKey() (keyPath, fingerprint string, err error) {
	home, _ := os.UserHomeDir()
	keyPath = filepath.Join(home, ".ssh", "mailr-deploy-key")

	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		if err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "").Run(); err != nil {
			return "", "", fmt.Errorf("generating key: %w", err)
		}
	}

	out, err := exec.Command("ssh-keygen", "-l", "-E", "md5", "-f", keyPath+".pub").Output()
	if err != nil {
		return "", "", fmt.Errorf("fingerprint: %w", err)
	}
	parts := strings.Fields(string(out))
	if len(parts) >= 2 {
		fingerprint = strings.TrimPrefix(parts[1], "MD5:")
	}
	return keyPath, fingerprint, nil
}

func importDOKey(fingerprint, keyPath string) (string, error) {
	// Check if already imported
	out, _ := doCLI("compute", "ssh-key", "list", "--format", "ID,FingerPrint", "--no-header")
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == fingerprint {
			return parts[0], nil
		}
	}

	// Import
	out, err := doCLI("compute", "ssh-key", "import", "mailr-deploy-key",
		"--public-key-file", keyPath+".pub",
		"--format", "ID", "--no-header")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// --- Teardown ---

func newTeardownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "teardown <provider> [region]",
		Short: "Destroy all mailr cloud resources",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]
			region := ""
			if len(args) > 1 {
				region = args[1]
			}

			switch provider {
			case "aws":
				return teardownAWS(region)
			case "digitalocean", "do":
				return teardownDO()
			default:
				return fmt.Errorf("unknown provider: %s (use aws or digitalocean)", provider)
			}
		},
	}
}

func teardownAWS(region string) error {
	if region == "" {
		region = "us-east-1"
	}

	// Find instance
	instanceID, _ := awsCLI("ec2", "describe-instances", "--region", region,
		"--filters", "Name=tag:Name,Values=mailr-server", "Name=instance-state-name,Values=running,stopped",
		"--query", "Reservations[0].Instances[0].InstanceId", "--output", "text")

	fmt.Println("Resources to destroy:")
	if instanceID != "" && instanceID != "None" {
		fmt.Printf("  EC2 instance: %s\n", instanceID)
	}
	fmt.Printf("  Security group: mailr-server\n")
	fmt.Printf("  Key pair: mailr-deploy-key\n")

	fmt.Print("\nType 'destroy' to confirm: ")
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "destroy" {
		return fmt.Errorf("aborted")
	}

	if instanceID != "" && instanceID != "None" {
		fmt.Println("Terminating instance...")
		awsCLI("ec2", "terminate-instances", "--region", region, "--instance-ids", instanceID)
		awsCLI("ec2", "wait", "instance-terminated", "--region", region, "--instance-ids", instanceID)
	}

	// Release unassociated elastic IPs
	allocsOut, _ := awsCLI("ec2", "describe-addresses", "--region", region,
		"--filters", "Name=domain,Values=vpc",
		"--query", "Addresses[?AssociationId==null].AllocationId", "--output", "json")
	var allocs []string
	json.Unmarshal([]byte(allocsOut), &allocs)
	for _, a := range allocs {
		awsCLI("ec2", "release-address", "--region", region, "--allocation-id", a)
	}

	awsCLI("ec2", "delete-key-pair", "--region", region, "--key-name", "mailr-deploy-key")

	// Delete security group (may fail if instance still terminating)
	sgID, _ := awsCLI("ec2", "describe-security-groups", "--region", region,
		"--filters", "Name=group-name,Values=mailr-server",
		"--query", "SecurityGroups[0].GroupId", "--output", "text")
	if sgID != "" && sgID != "None" {
		awsCLI("ec2", "delete-security-group", "--region", region, "--group-id", sgID)
	}

	// Remove local key
	home, _ := os.UserHomeDir()
	os.Remove(filepath.Join(home, ".ssh", "mailr-deploy-key.pem"))

	fmt.Println("Teardown complete.")
	return nil
}

func teardownDO() error {
	// Find droplet
	out, _ := doCLI("compute", "droplet", "list", "--tag-name", "mailr",
		"--format", "ID,Name,PublicIPv4", "--no-header")

	fmt.Println("Resources to destroy:")
	if out != "" {
		fmt.Printf("  Droplets:\n")
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			fmt.Printf("    %s\n", line)
		}
	}

	fmt.Print("\nType 'destroy' to confirm: ")
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "destroy" {
		return fmt.Errorf("aborted")
	}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 1 {
			doCLI("compute", "droplet", "delete", parts[0], "--force")
		}
	}

	home, _ := os.UserHomeDir()
	os.Remove(filepath.Join(home, ".ssh", "mailr-deploy-key"))
	os.Remove(filepath.Join(home, ".ssh", "mailr-deploy-key.pub"))

	fmt.Println("Teardown complete.")
	return nil
}

// --- Helpers ---

func prepareUserData(domain, repo string) string {
	if repo == "" {
		repo = defaultRepo
	}
	script := strings.ReplaceAll(cloudInitScript, "__MAILR_DOMAIN__", domain)
	script = strings.ReplaceAll(script, "__MAILR_REPO__", repo)
	return base64.StdEncoding.EncodeToString([]byte(script))
}

func awsCLI(args ...string) (string, error) {
	out, err := exec.Command("aws", args...).CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		return result, fmt.Errorf("aws %s: %s", args[0]+" "+args[1], result)
	}
	return result, nil
}

func doCLI(args ...string) (string, error) {
	out, err := exec.Command("doctl", args...).CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		return result, fmt.Errorf("doctl: %s", result)
	}
	return result, nil
}

func waitForHealth(baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/health")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timed out after %v", timeout)
}
