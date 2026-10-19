package ascii

import "testing"

func TestIdentityFold(t *testing.T) {
	if !EqualFold("Org/Sam[BOT]", "org/sam[bot]") {
		t.Fatal("ASCII case changed")
	}
	for _, name := range []string{"ſam", "Kam", "Ａam", "ıam", "İam"} {
		if Lower(name) != name || EqualFold(name, "sam") || EqualFold(name, "kam") || EqualFold(name, "aam") || EqualFold(name, "iam") {
			t.Fatalf("Unicode alias: %q", name)
		}
	}
}
