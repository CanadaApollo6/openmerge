package main

import (
	"bytes"
	"testing"

	"github.com/CanadaApollo6/openmerge/schema"
)

func TestConfigSchemaPrintsEmbeddedDocument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "schema"}, &stdout, &stderr); code != 0 || stdout.String() != schema.V1 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	if code := run([]string{"config", "schema"}, brokenWriter{}, &stderr); code != 1 {
		t.Fatalf("failed writer exit=%d", code)
	}
}
