package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"renameplanner/internal/planner"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "planner:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var data []byte
	var err error

	switch {
	case len(args) == 0 || args[0] == "-":
		data, err = io.ReadAll(stdin)
		if err != nil {
			return fmt.Errorf("read manifest from stdin: %w", err)
		}
	case len(args) == 1:
		data, err = os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}
	default:
		return fmt.Errorf("usage: planner [manifest.json | -]")
	}

	plan, err := planner.ParseManifest(data)
	if err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plan: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := stdout.Write(encoded); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}
	return nil
}
