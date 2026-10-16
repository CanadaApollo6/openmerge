package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevDBOnlyUsesOwnedRunningContainers(t *testing.T) {
	for _, tc := range []struct {
		name, state, owner, running string
		allowed                     bool
	}{
		{"new", "absent", "", "", true},
		{"owned-running", "exists", "true", "true", true},
		{"owned-stopped", "exists", "true", "false", false},
		{"foreign-running", "exists", "false", "true", false},
		{"foreign-stopped", "exists", "false", "false", false},
	} {
		for _, action := range []string{"start", "stop"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				dir := t.TempDir()
				logPath := filepath.Join(dir, "calls")
				// A fake Docker executable keeps this test independent of a
				// daemon and proves refused containers receive no mutations.
				docker := `#!/bin/sh
printf '%s\n' "$*" >> "$DEV_DB_TEST_LOG"
case "$1" in
info) exit 0 ;;
inspect)
    if [ "$DEV_DB_TEST_STATE" = absent ]; then exit 1; fi
    if [ "${2:-}" = --format ]; then
        case "$3" in
            *Labels*) printf '%s\n' "$DEV_DB_TEST_OWNER" ;;
            *Running*) printf '%s\n' "$DEV_DB_TEST_RUNNING" ;;
        esac
    fi
    ;;
run|exec|stop) exit 0 ;;
*) exit 2 ;;
esac
`
				if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0700); err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(t.Context(), "sh", "dev-db.sh", action)
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "OPENMERGE_DEV_PGDATA="+filepath.Join(dir, "data"),
					"DEV_DB_TEST_LOG="+logPath, "DEV_DB_TEST_STATE="+tc.state, "DEV_DB_TEST_OWNER="+tc.owner, "DEV_DB_TEST_RUNNING="+tc.running)
				output, err := cmd.CombinedOutput()
				if (err == nil) != tc.allowed {
					t.Fatalf("allowed=%v, error=%v, output=%s", tc.allowed, err, output)
				}
				log, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				calls := string(log)
				if !tc.allowed {
					if !strings.Contains(string(output), "Refusing to use") {
						t.Fatalf("missing refusal: %s", output)
					}
					for _, mutation := range []string{"run ", "exec ", "stop "} {
						if strings.Contains(calls, mutation) {
							t.Fatalf("refused container was used: %s", calls)
						}
					}
				} else if action == "start" {
					if !strings.Contains(string(output), "export OPENMERGE_TEST_DATABASE_URL=") {
						t.Fatalf("missing URL: %s", output)
					}
					if tc.state == "absent" && !strings.Contains(calls, "--label io.openmerge.dev-db=true") {
						t.Fatalf("new container lacks ownership label: %s", calls)
					}
					if tc.state == "exists" && strings.Contains(calls, "run ") {
						t.Fatalf("running container recreated: %s", calls)
					}
				}
			})
		}
	}
}
