package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/CanadaApollo6/openmerge/internal/config"
)

func validateConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		if _, err := fmt.Fprintln(stdout, "Usage: openmerge config validate [path]"); err != nil {
			return 1
		}
		return 0
	}
	var path string
	var data []byte
	var err error
	if len(args) == 1 {
		path = args[0]
		data, err = os.ReadFile(path)
	} else {
		path, data, err = config.Find(func(path string) ([]byte, bool, error) {
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				return nil, false, nil
			}
			return data, err == nil, err
		})
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, warnings, err := config.Load(data)
	if err != nil {
		var diagnostics config.Errors
		if errors.As(err, &diagnostics) {
			for _, diagnostic := range diagnostics {
				message := diagnostic.Message
				if diagnostic.Path != "" {
					message = diagnostic.Path + ": " + message
				}
				_, _ = fmt.Fprintf(stderr, "%s:%d: %s\n", path, diagnostic.Line, message)
			}
		} else {
			_, _ = fmt.Fprintf(stderr, "%s:1: %s\n", path, err)
		}
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "ok"); err != nil {
		return 1
	}
	for _, warning := range warnings {
		if _, err := fmt.Fprintf(stderr, "%s:%d: warning: %s: %s\n", path, warning.Line, warning.Path, warning.Message); err != nil {
			return 1
		}
	}
	return 0
}
