package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	ServerURL  string `json:"serverUrl,omitempty"`
	Token      string `json:"token,omitempty"`
	RemoteHost string `json:"remoteHost,omitempty"`
	SSHKey     string `json:"sshKey,omitempty"`
	SSHUser    string `json:"sshUser,omitempty"`
	RemoteDir  string `json:"remoteDir,omitempty"`
}

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mailr")
}

func configPath() string {
	return filepath.Join(configDir(), "config.json")
}

func loadConfig() *Config {
	data, err := os.ReadFile(configPath())
	if err != nil {
		return &Config{}
	}
	var c Config
	json.Unmarshal(data, &c)
	return &c
}

func saveConfig(c *Config) error {
	if err := os.MkdirAll(configDir(), 0700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(configPath(), data, 0600)
}

func (c *Config) resolveHost() string {
	if v := os.Getenv("MAILR_HOST"); v != "" { return v }
	if c.RemoteHost != "" { return c.RemoteHost }
	return ""
}

func (c *Config) resolveSSHKey() string {
	if v := os.Getenv("MAILR_SSH_KEY"); v != "" { return v }
	if c.SSHKey != "" { return c.SSHKey }
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "mailr-deploy-key.pem")
}

func (c *Config) resolveSSHUser() string {
	if v := os.Getenv("MAILR_SSH_USER"); v != "" { return v }
	if c.SSHUser != "" { return c.SSHUser }
	return "ubuntu"
}

func (c *Config) resolveRemoteDir() string {
	if v := os.Getenv("MAILR_DIR"); v != "" { return v }
	if c.RemoteDir != "" { return c.RemoteDir }
	return "/opt/mailr"
}

func (c *Config) resolveServerURL() string {
	if v := os.Getenv("MAILR_SERVER"); v != "" { return v }
	if c.ServerURL != "" { return c.ServerURL }
	return "http://localhost:4802"
}

func (c *Config) resolveToken() string {
	if v := os.Getenv("MAILR_TOKEN"); v != "" { return v }
	return c.Token
}
