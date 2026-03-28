package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func sshArgs(host, key, user string) []string {
	return []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=10",
		"-i", key,
		user + "@" + host,
	}
}

// sshExec runs a command on the remote host, inheriting stdio.
func sshExec(host, key, user, command string) error {
	args := append(sshArgs(host, key, user), command)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// sshCapture runs a command on the remote host and captures stdout.
func sshCapture(host, key, user, command string) (string, error) {
	args := append(sshArgs(host, key, user), command)
	out, err := exec.Command("ssh", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// compose runs docker compose via SSH.
func compose(host, key, user, dir, composeArgs string) (string, error) {
	cmd := fmt.Sprintf("cd %s && sudo docker compose %s", dir, composeArgs)
	return sshCapture(host, key, user, cmd)
}

// sshInteractive opens an interactive SSH session.
func sshInteractive(host, key, user string) error {
	args := sshArgs(host, key, user)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// scpDownload copies a remote file to local.
func scpDownload(host, key, user, remotePath, localPath string) error {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-i", key,
		user + "@" + host + ":" + remotePath,
		localPath,
	}
	return exec.Command("scp", args...).Run()
}

// rsyncUpload syncs a local directory to the remote host.
func rsyncUpload(host, key, user, localDir, remoteDir string) error {
	sshCmd := fmt.Sprintf("ssh -o StrictHostKeyChecking=no -i %s", key)
	args := []string{
		"-avz", "--exclude", ".git", "--exclude", "mailr",
		"-e", sshCmd,
		localDir + "/",
		user + "@" + host + ":" + remoteDir + "/",
	}
	cmd := exec.Command("rsync", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// scpUpload copies a local file to remote.
func scpUpload(host, key, user, localPath, remotePath string) error {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-i", key,
		localPath,
		user + "@" + host + ":" + remotePath,
	}
	return exec.Command("scp", args...).Run()
}
