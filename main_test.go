package main

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/runner"
	"github.com/alecthomas/kong"
)

func TestMain_NoError(t *testing.T) {
	origRun := runFn
	origFatal := fatalf
	defer func() {
		runFn = origRun
		fatalf = origFatal
	}()

	calledFatal := false
	runFn = func(_ []string, _ io.Writer) error {
		return nil
	}
	fatalf = func(args ...interface{}) {
		calledFatal = true
	}

	main()
	if calledFatal {
		t.Fatal("expected fatalf not to be called")
	}
}

func TestMain_WithError(t *testing.T) {
	origRun := runFn
	origFatal := fatalf
	defer func() {
		runFn = origRun
		fatalf = origFatal
	}()

	expected := errors.New("boom")
	var got error
	runFn = func(_ []string, _ io.Writer) error {
		return expected
	}
	fatalf = func(args ...interface{}) {
		if len(args) > 0 {
			if err, ok := args[0].(error); ok {
				got = err
			}
		}
	}

	main()
	if got != expected {
		t.Fatalf("expected fatalf to receive %v, got %v", expected, got)
	}
}

func TestRun_ParseError(t *testing.T) {
	origRunner := runRunner
	defer func() { runRunner = origRunner }()

	// Ensure runner is not invoked for parse errors.
	runRunner = func(_ runner.Options) error {
		return nil
	}

	var out bytes.Buffer
	if err := run([]string{"--definitely-not-a-flag"}, &out); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestRun_Success(t *testing.T) {
	origRunner := runRunner
	defer func() { runRunner = origRunner }()

	called := false
	runRunner = func(_ runner.Options) error {
		called = true
		return nil
	}

	var out bytes.Buffer
	if err := run([]string{}, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected runner to be called")
	}
}

func TestRun_CheckMode(t *testing.T) {
	origRunner := runRunner
	defer func() { runRunner = origRunner }()

	called := false
	runRunner = func(opts runner.Options) error {
		called = true
		if !opts.Check {
			t.Fatal("expected Check to be true")
		}
		return nil
	}

	var out bytes.Buffer
	if err := run([]string{"--check"}, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected runner to be called")
	}
}

func TestRun_RunnerError(t *testing.T) {
	origRunner := runRunner
	defer func() { runRunner = origRunner }()

	expected := errors.New("runner failure")
	runRunner = func(_ runner.Options) error {
		return expected
	}

	var out bytes.Buffer
	if err := run([]string{}, &out); err != expected {
		t.Fatalf("expected %v, got %v", expected, err)
	}
}

func TestRun_KongNewError(t *testing.T) {
	orig := kongNew
	defer func() { kongNew = orig }()

	expected := errors.New("kong new failed")
	kongNew = func(_ interface{}, _ ...kong.Option) (*kong.Kong, error) {
		return nil, expected
	}

	var out bytes.Buffer
	if err := run([]string{}, &out); err != expected {
		t.Fatalf("expected %v, got %v", expected, err)
	}
}
