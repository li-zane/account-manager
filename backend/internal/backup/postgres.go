package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

const commandErrorLimit = 32 << 10

type commandRunner interface {
	Run(ctx context.Context, executable string, args, environment []string, stdin io.Reader, stdout io.Writer) error
}

// PostgreSQLTools produces pg_dump custom-format snapshots and restores them
// with pg_restore. The database URL is passed through PGDATABASE so credentials
// are not exposed in the child process argument list.
type PostgreSQLTools struct {
	databaseURL string
	dumpPath    string
	restorePath string
	runner      commandRunner
}

func NewPostgreSQLTools(databaseURL, dumpPath, restorePath string) (*PostgreSQLTools, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return nil, fmt.Errorf("%w: PostgreSQL database URL is required", domain.ErrInvalid)
	}
	if strings.TrimSpace(dumpPath) == "" {
		dumpPath = "pg_dump"
	}
	if strings.TrimSpace(restorePath) == "" {
		restorePath = "pg_restore"
	}
	redactions := []string{databaseURL}
	if parsed, err := url.Parse(databaseURL); err == nil && parsed.User != nil {
		if password, ok := parsed.User.Password(); ok && password != "" {
			redactions = append(redactions, password)
		}
	}
	return &PostgreSQLTools{
		databaseURL: databaseURL,
		dumpPath:    strings.TrimSpace(dumpPath),
		restorePath: strings.TrimSpace(restorePath),
		runner:      execCommandRunner{redactions: redactions},
	}, nil
}

func (p *PostgreSQLTools) Snapshot(ctx context.Context) (io.ReadCloser, error) {
	if p == nil || p.runner == nil {
		return nil, fmt.Errorf("%w: PostgreSQL dump runner is required", domain.ErrInvalid)
	}
	var output bytes.Buffer
	err := p.runner.Run(ctx, p.dumpPath, []string{
		"--format=custom",
		"--no-owner",
		"--no-privileges",
	}, p.environment(), nil, &output)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL snapshot: %w", err)
	}
	return io.NopCloser(bytes.NewReader(output.Bytes())), nil
}

func (p *PostgreSQLTools) Restore(ctx context.Context, snapshot io.Reader) error {
	if p == nil || p.runner == nil || snapshot == nil {
		return fmt.Errorf("%w: PostgreSQL restore runner and snapshot are required", domain.ErrInvalid)
	}
	err := p.runner.Run(ctx, p.restorePath, []string{
		"--clean",
		"--if-exists",
		"--no-owner",
		"--no-privileges",
		"--exit-on-error",
		"--single-transaction",
	}, p.environment(), snapshot, io.Discard)
	if err != nil {
		return fmt.Errorf("restore PostgreSQL snapshot: %w", err)
	}
	return nil
}

func (p *PostgreSQLTools) environment() []string {
	return []string{"PGDATABASE=" + p.databaseURL}
}

type execCommandRunner struct {
	redactions []string
}

func (r execCommandRunner) Run(ctx context.Context, executable string, args, environment []string, stdin io.Reader, stdout io.Writer) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = append(os.Environ(), environment...)
	command.Stdin = stdin
	command.Stdout = stdout
	stderr := &limitedBuffer{limit: commandErrorLimit}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		message := sanitizeCommandMessage(stderr.String(), r.redactions...)
		if message == "" {
			return fmt.Errorf("%s failed: %w", filepath.Base(executable), err)
		}
		return fmt.Errorf("%s failed: %w: %s", filepath.Base(executable), err, message)
	}
	return nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.buffer.Write(value)
	}
	return originalLength, nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

func sanitizeCommandMessage(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "<redacted>")
		}
	}
	return strings.Join(strings.Fields(value), " ")
}

var _ SnapshotSource = (*PostgreSQLTools)(nil)
var _ SnapshotRestorer = (*PostgreSQLTools)(nil)
