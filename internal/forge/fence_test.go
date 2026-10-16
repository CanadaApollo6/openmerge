package forge_test

import (
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestParseFenceMessage(t *testing.T) {
	const nonce = "0123456789abcdef0123456789abcdef"
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("B", 64)} {
		land := "openmerge fence land/probe tested=" + sha + " nonce=" + nonce
		abandon := "openmerge fence abandon/probe nonce=" + nonce
		for _, tc := range []struct{ message, id, tested string }{
			{land, "land/probe", sha},
			{land + "\n", "land/probe", sha},
			{abandon, "abandon/probe", ""},
			{abandon + "\n", "abandon/probe", ""},
		} {
			id, tested := forge.ParseFenceMessage(tc.message)
			if id != tc.id || tested != tc.tested {
				t.Fatalf("ParseFenceMessage(%q) = (%q, %q), want (%q, %q)", tc.message, id, tested, tc.id, tc.tested)
			}
		}
		for _, message := range []string{
			"ordinary commit", "openmerge fence initial nonce=" + nonce,
			" " + land, land + " ", land + "\n\n", land + "\r\n",
			strings.Replace(land, "land/probe", "land/", 1),
			strings.Replace(land, "land/probe", "land/probe\vother", 1),
			strings.Replace(land, "land/probe", "land/probe\u2028other", 1),
			strings.Replace(land, "land/probe", "land/probe\x00other", 1),
			strings.Replace(land, " fence ", "\tfence ", 1),
			strings.Replace(land, " tested=", "  tested=", 1),
			"openmerge fence land/probe tested=" + sha,
			"openmerge fence land/probe nonce=" + nonce,
			"openmerge fence land/probe nonce=" + nonce + " tested=" + sha,
			"openmerge fence land/probe tested=" + sha + " nonce=not-hex tested=" + sha,
			"openmerge fence land/probe tested=" + sha + " nonce=",
			"openmerge fence land/probe nonce=abcd tested=" + sha,
			"openmerge fence abandon/probe nonce=abcd\nopenmerge fence land/other tested=" + sha,
			strings.Replace(land, sha, sha[:len(sha)-1], 1),
			strings.Replace(land, sha, sha+"0", 1),
			strings.Replace(land, sha, strings.Repeat("g", len(sha)), 1),
			strings.Replace(land, nonce, nonce[:31], 1),
			strings.Replace(land, nonce, nonce+"0", 1),
			strings.Replace(land, nonce, strings.Repeat("z", 32), 1),
			land + " tested=" + sha, land + " nonce=" + nonce, land + " extra",
			abandon + " tested=" + sha, abandon + " nonce=" + nonce,
			abandon + "\n" + land,
		} {
			id, tested := forge.ParseFenceMessage(message)
			if id != "" || tested != "" {
				t.Errorf("malformed message %q recognized as (%q, %q)", message, id, tested)
			}
		}
	}
}
