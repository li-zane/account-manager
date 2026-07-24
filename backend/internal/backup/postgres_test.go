package backup

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

type recordedCommand struct {
	executable  string
	args        []string
	environment []string
	stdin       []byte
}

type recordingCommandRunner struct {
	calls      []recordedCommand
	dumpOutput []byte
	err        error
}

func (r *recordingCommandRunner) Run(_ context.Context, executable string, args, environment []string, stdin io.Reader, stdout io.Writer) error {
	call := recordedCommand{
		executable:  executable,
		args:        append([]string(nil), args...),
		environment: append([]string(nil), environment...),
	}
	if stdin != nil {
		call.stdin, _ = io.ReadAll(stdin)
	}
	r.calls = append(r.calls, call)
	if stdout != nil && len(r.dumpOutput) > 0 {
		_, _ = stdout.Write(r.dumpOutput)
	}
	return r.err
}

func TestPostgreSQLToolsUseCustomFormatAndEnvironmentConnection(t *testing.T) {
	const databaseURL = "postgres://backup:secret@db.internal:5432/account_manager?sslmode=require"
	tools, err := NewPostgreSQLTools(databaseURL, "/tools/pg_dump", "/tools/pg_restore")
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{dumpOutput: []byte("postgres-custom-format")}
	tools.runner = runner

	snapshot, err := tools.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dumped, err := io.ReadAll(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	_ = snapshot.Close()
	if string(dumped) != "postgres-custom-format" {
		t.Fatalf("snapshot = %q", dumped)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("dump calls = %d, want 1", len(runner.calls))
	}
	dumpCall := runner.calls[0]
	if dumpCall.executable != "/tools/pg_dump" {
		t.Fatalf("dump executable = %q", dumpCall.executable)
	}
	if !slices.Equal(dumpCall.args, []string{"--format=custom", "--no-owner", "--no-privileges"}) {
		t.Fatalf("dump args = %q", dumpCall.args)
	}
	if strings.Contains(strings.Join(dumpCall.args, " "), databaseURL) {
		t.Fatal("database URL was exposed in pg_dump arguments")
	}
	if !slices.Equal(dumpCall.environment, []string{"PGDATABASE=" + databaseURL}) {
		t.Fatalf("dump environment = %q", dumpCall.environment)
	}

	if err := tools.Restore(context.Background(), bytes.NewReader(dumped)); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("total calls = %d, want 2", len(runner.calls))
	}
	restoreCall := runner.calls[1]
	if restoreCall.executable != "/tools/pg_restore" {
		t.Fatalf("restore executable = %q", restoreCall.executable)
	}
	wantRestoreArgs := []string{"--clean", "--if-exists", "--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction"}
	if !slices.Equal(restoreCall.args, wantRestoreArgs) {
		t.Fatalf("restore args = %q", restoreCall.args)
	}
	if !bytes.Equal(restoreCall.stdin, dumped) {
		t.Fatalf("restore stdin = %q", restoreCall.stdin)
	}
}

func TestSanitizeCommandMessageRedactsConnectionSecrets(t *testing.T) {
	message := sanitizeCommandMessage(" connection postgres://user:secret@db failed with secret \n", "postgres://user:secret@db", "secret")
	if strings.Contains(message, "secret") || strings.Contains(message, "postgres://") {
		t.Fatalf("sanitized message leaked a secret: %q", message)
	}
}
