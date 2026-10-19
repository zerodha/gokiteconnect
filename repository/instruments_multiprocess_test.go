package repository

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets a test run this binary as another process holding the
// catalog open.
func TestMain(m *testing.M) {
	if path := os.Getenv("INSTRUMENTS_READER_PATH"); path != "" {
		os.Exit(runCatalogReader(path))
	}
	os.Exit(m.Run())
}

// runCatalogReader opens the catalog at path, then prints INFY's token for
// each line read on stdin.
func runCatalogReader(path string) int {
	ctx := context.Background()
	repository, err := NewInstrumentsDuckDBRepository(ctx, path)
	if err != nil {
		fmt.Println("error", err)
		return 1
	}
	defer repository.Close()
	fmt.Println("ready")
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		instrument, err := repository.Get(ctx, "NSE:INFY")
		if err != nil {
			fmt.Println("error", err)
			continue
		}
		fmt.Println(instrument.InstrumentToken)
	}
	return 0
}

type catalogReader struct {
	t      *testing.T
	stdin  *os.File
	output *bufio.Scanner
	cmd    *exec.Cmd
}

func startCatalogReader(t *testing.T, path string) *catalogReader {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "INSTRUMENTS_READER_PATH="+path)
	stdinReader, stdin, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = stdinReader
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = stdinReader.Close()
	reader := &catalogReader{t: t, stdin: stdin, output: bufio.NewScanner(stdout), cmd: cmd}
	t.Cleanup(reader.stop)
	if line := reader.line(); line != "ready" {
		t.Fatalf("reader: %s", line)
	}
	return reader
}

func (r *catalogReader) line() string {
	r.t.Helper()
	if !r.output.Scan() {
		r.t.Fatalf("reader exited: %v", r.output.Err())
	}
	return strings.TrimSpace(r.output.Text())
}

func (r *catalogReader) token() string {
	r.t.Helper()
	fmt.Fprintln(r.stdin, "read")
	return r.line()
}

func (r *catalogReader) stop() {
	if r.stdin != nil {
		_ = r.stdin.Close()
		r.stdin = nil
		_ = r.cmd.Wait()
	}
}

func TestCatalogIsSharedWithOtherProcessesAcrossRefresh(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "instruments.duckdb")
	transport := &catalogTransport{body: catalogCSV}
	client, _ := openTestClientWithRepository(t, ctx, transport, path)

	// Another process reads alongside this one.
	before := startCatalogReader(t, path)
	if got := before.token(); got != "408065" {
		t.Fatalf("other process token = %s", got)
	}

	// Refreshing doesn't need the other process to let go.
	transport.setBody(strings.Replace(catalogCSV, "408065,1002,INFY", "408066,1002,INFY", 1))
	if err := client.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() with another reader open error = %v", err)
	}
	if instrument, err := client.Get(ctx, "NSE:INFY"); err != nil || instrument.InstrumentToken != 408066 {
		t.Fatalf("Get() after refresh = %d, %v", instrument.InstrumentToken, err)
	}
	// The open reader keeps its snapshot; a new one sees the refresh.
	if got := before.token(); got != "408065" {
		t.Fatalf("open reader token after refresh = %s", got)
	}
	if got := startCatalogReader(t, path).token(); got != "408066" {
		t.Fatalf("new reader token = %s", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "instruments.duckdb" {
			t.Errorf("refresh left %s behind", entry.Name())
		}
	}
}
