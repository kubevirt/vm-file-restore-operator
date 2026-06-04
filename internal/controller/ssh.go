package controller

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHClient wraps an SSH connection for executing remote commands.
type SSHClient struct {
	client *ssh.Client
}

// ConnectSSH establishes an SSH connection to the specified IP using the provided private key.
// Validates inputs to prevent panics.
func ConnectSSH(ip string, privateKey []byte) (*SSHClient, error) {
	// Validate inputs
	if ip == "" {
		return nil, fmt.Errorf("IP address cannot be empty")
	}
	if len(privateKey) == 0 {
		return nil, fmt.Errorf("private key cannot be empty")
	}

	// Parse the private key
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	// Create SSH client configuration
	config := &ssh.ClientConfig{
		User: "filerestore",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // VMs can be recreated with same IP
		Timeout:         10 * time.Second,
	}

	// Dial the SSH connection
	addr := net.JoinHostPort(ip, "22")
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("failed to dial SSH: %w", err)
	}

	return &SSHClient{client: client}, nil
}

// RunCommand executes a command via SSH and returns stdout, stderr, and error.
// The command execution respects the provided context for cancellation and timeout.
// If the context is cancelled, the session is closed and ctx.Err() is returned.
// Partial stdout/stderr output captured before cancellation is still returned.
func (c *SSHClient) RunCommand(ctx context.Context, command string) (stdout, stderr string, err error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close() //nolint:errcheck // Closing in defer is idiomatic

	var stdoutBuf, stderrBuf bytes.Buffer
	session.Stdout = &stdoutBuf
	session.Stderr = &stderrBuf

	// Run command in goroutine to support context cancellation
	errCh := make(chan error, 1)
	go func() {
		errCh <- session.Run(command)
	}()

	select {
	case <-ctx.Done():
		// Context cancelled or timed out (issue #14)
		// Try to signal the remote process before terminating
		_ = session.Signal(ssh.SIGTERM)
		// Brief grace period for cleanup
		select {
		case <-time.After(200 * time.Millisecond):
		case <-errCh: // Process exited cleanly
		}
		// Note: defer will close session, which terminates the goroutine
		// Return partial output for debugging
		return stdoutBuf.String(), stderrBuf.String(), fmt.Errorf("command cancelled: %w", ctx.Err())
	case err = <-errCh:
		return stdoutBuf.String(), stderrBuf.String(), err
	}
}

// Close closes the SSH connection.
func (c *SSHClient) Close() error {
	return c.client.Close()
}

// BuildSSHCommand constructs the restore command to execute on the guest VM.
// If sourcePath is empty, manual mode is assumed (no automatic restore).
// Panics if volumeName or mountPath are empty (caller error).
func BuildSSHCommand(osType, volumeName, mountPath, sourcePath string) string {
	if volumeName == "" {
		panic("BuildSSHCommand called with empty volumeName")
	}
	if mountPath == "" {
		panic("BuildSSHCommand called with empty mountPath")
	}

	scriptPath := GetHelperScriptPath(osType)

	var cmd string
	if osType == osTypeWindows {
		// Windows: quote mount-path and source-path
		if sourcePath != "" {
			cmd = fmt.Sprintf(`%s restore --serial %s --mount-path "%s" --source-path "%s"`,
				scriptPath, volumeName, mountPath, sourcePath)
		} else {
			cmd = fmt.Sprintf(`%s restore --serial %s --mount-path "%s"`,
				scriptPath, volumeName, mountPath)
		}
	} else {
		// Linux: script handles sudo internally (see filerestore.sh)
		if sourcePath != "" {
			cmd = fmt.Sprintf(`%s restore --serial %s --mount-path %s --source-path %s`,
				scriptPath, volumeName, mountPath, sourcePath)
		} else {
			cmd = fmt.Sprintf(`%s restore --serial %s --mount-path %s`,
				scriptPath, volumeName, mountPath)
		}
	}

	return cmd
}

// BuildCleanupCommand constructs the cleanup command to execute on the guest VM.
// Panics if mountPath is empty (caller error).
func BuildCleanupCommand(osType, mountPath string) string {
	if mountPath == "" {
		panic("BuildCleanupCommand called with empty mountPath")
	}

	scriptPath := GetHelperScriptPath(osType)

	var cmd string
	if osType == osTypeWindows {
		// Windows: quote mount-path
		cmd = fmt.Sprintf(`%s cleanup --mount-path "%s"`, scriptPath, mountPath)
	} else {
		// Linux: script handles sudo internally (see filerestore.sh)
		cmd = fmt.Sprintf(`%s cleanup --mount-path %s`, scriptPath, mountPath)
	}

	return cmd
}

// TruncateOutput limits the output to the last maxLines lines.
// This helps keep error messages manageable in logs and events.
func TruncateOutput(output string, maxLines int) string {
	if output == "" {
		return ""
	}

	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}

	// Return last maxLines lines
	truncated := lines[len(lines)-maxLines:]
	return strings.Join(truncated, "\n")
}
