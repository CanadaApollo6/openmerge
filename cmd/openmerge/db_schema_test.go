package main

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func testSchemaName(t *testing.T, prefix string) string {
	t.Helper()
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	return prefix + hex.EncodeToString(token[:])
}
