// Command replay compares next actions at each checkpoint of the repository's
// stored legacy journal fixture. It uses no database, forge, or live refs.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	data, err := os.ReadFile("internal/serve/testdata/legacy_rules_log.jsonl")
	if err != nil {
		return err
	}
	now := time.Unix(1700000000, 0).UTC()
	for _, depth := range []int{1, 4} {
		source := fmt.Sprintf("version: 1\nqueues:\n- name: default\n  merge_when: [check-success = ci]\n  speculation: {max_depth: %d, min_depth: 1}\n", depth)
		cfg, _, err := config.Load([]byte(source))
		if err != nil {
			return err
		}
		state := queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
		state = queue.Apply(state, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true})
		state = queue.Apply(state, queue.EnqueueRequested{PR: 1, At: now})
		for i, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			event, err := queue.DecodeEvent(line)
			if err != nil {
				return err
			}
			state = queue.Apply(state, event)
			payloads := []json.RawMessage{}
			keys := []string{}
			for _, action := range queue.Plan(cfg.Queues[0].QueueConfig(), state, now) {
				_, payload, err := queue.EncodeAction(action)
				if err != nil {
					return err
				}
				payloads = append(payloads, payload)
				keys = append(keys, action.Key())
			}
			if err := json.NewEncoder(os.Stdout).Encode(struct {
				Depth, Checkpoint int
				Digest            string
				Actions           []json.RawMessage
				Keys              []string
			}{depth, i + 1, cfg.Queues[0].Digest(true), payloads, keys}); err != nil {
				return err
			}
		}
	}
	return nil
}
