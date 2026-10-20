package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
)

// A backup is an immutable intent record. A missing ID denotes a creation;
// retries locate that creation by its managed name AND complete expected shape.
// No token, installation credential, or unrelated ruleset is captured.
type rulesBackup struct {
	Version    int         `json:"version"`
	Repository string      `json:"repository"`
	AppID      int64       `json:"app_id"`
	Binding    string      `json:"plan_sha256"`
	Edits      []rulesEdit `json:"edits"`
}

type rulesEdit struct {
	ID     int64                      `json:"id"`
	Name   string                     `json:"name"`
	Before map[string]json.RawMessage `json:"before"`
	After  map[string]json.RawMessage `json:"after"`
}

func setJSON(fields map[string]json.RawMessage, key string, value any) {
	fields[key], _ = json.Marshal(value)
}

func cloneSettings(fields map[string]json.RawMessage) map[string]json.RawMessage {
	result := map[string]json.RawMessage{}
	for key, value := range fields {
		result[key] = bytes.Clone(value)
	}
	return result
}

func ruleSettings(raw map[string]json.RawMessage) map[string]json.RawMessage {
	result := cloneSettings(raw)
	for _, key := range []string{"id", "source_type", "source", "node_id", "_links", "created_at", "updated_at", "current_user_can_bypass"} {
		delete(result, key)
	}
	return result
}

func sameJSON(a, b any) bool {
	decode := func(v any) any {
		data, _ := json.Marshal(v)
		var out any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		_ = decoder.Decode(&out)
		return out
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

// GitHub omits default-false update parameters. Canonicalize only that known
// default and unordered conditions/actors; retain all unknown fields verbatim.
func explicitUpdateDefaults(raw map[string]json.RawMessage) map[string]json.RawMessage {
	if raw == nil {
		return nil
	}
	result := cloneSettings(raw)
	var rules []map[string]json.RawMessage
	if json.Unmarshal(result["rules"], &rules) == nil {
		for _, rule := range rules {
			if string(rule["type"]) != `"update"` {
				continue
			}
			params := map[string]json.RawMessage{}
			if len(rule["parameters"]) > 0 && string(rule["parameters"]) != "null" {
				if json.Unmarshal(rule["parameters"], &params) != nil {
					continue
				}
			}
			if params == nil {
				params = map[string]json.RawMessage{}
			}
			if _, ok := params["update_allows_fetch_and_merge"]; !ok {
				params["update_allows_fetch_and_merge"] = json.RawMessage(`false`)
			}
			setJSON(rule, "parameters", params)
		}
		setJSON(result, "rules", rules)
	}
	return result
}

func canonicalSettings(raw map[string]json.RawMessage) map[string]json.RawMessage {
	result := explicitUpdateDefaults(raw)
	if result == nil {
		return nil
	}
	var rules []map[string]json.RawMessage
	if json.Unmarshal(result["rules"], &rules) == nil {
		slices.SortFunc(rules, func(a, b map[string]json.RawMessage) int { return bytes.Compare(orderedJSON(a), orderedJSON(b)) })
		setJSON(result, "rules", rules)
	}
	var actors []json.RawMessage
	if json.Unmarshal(result["bypass_actors"], &actors) == nil {
		slices.SortFunc(actors, func(a, b json.RawMessage) int { return bytes.Compare(orderedJSON(a), orderedJSON(b)) })
		setJSON(result, "bypass_actors", actors)
	}
	var conditions map[string]json.RawMessage
	if json.Unmarshal(result["conditions"], &conditions) == nil {
		var ref map[string]json.RawMessage
		if json.Unmarshal(conditions["ref_name"], &ref) == nil {
			for _, key := range []string{"include", "exclude"} {
				var patterns []string
				if json.Unmarshal(ref[key], &patterns) == nil {
					slices.Sort(patterns)
					setJSON(ref, key, patterns)
				}
			}
			setJSON(conditions, "ref_name", ref)
			setJSON(result, "conditions", conditions)
		}
	}
	return result
}

func orderedJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	ordered, _ := json.Marshal(value)
	return ordered
}

func settingsEqual(a, b map[string]json.RawMessage) bool {
	return sameJSON(canonicalSettings(a), canonicalSettings(b))
}

func readRulesBackup(path string) (rulesBackup, error) {
	file, err := os.Open(path)
	if err != nil {
		return rulesBackup{}, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	decoder.DisallowUnknownFields()
	var backup rulesBackup
	if err := decoder.Decode(&backup); err != nil {
		return backup, err
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return backup, errors.New("trailing backup data")
	}
	if backup.Version != 1 || backup.Repository == "" || backup.AppID <= 0 || len(backup.Binding) != 64 || len(backup.Edits) > 2 {
		return backup, errors.New("unsupported or incomplete ruleset backup")
	}
	seen := map[string]bool{}
	for _, edit := range backup.Edits {
		if edit.Name != "openmerge-base" && edit.Name != "openmerge-refs" || seen[edit.Name] || edit.ID < 0 || edit.ID == 0 && edit.Before != nil || edit.ID > 0 && edit.Before == nil || len(edit.After) == 0 {
			return backup, errors.New("invalid managed ruleset backup edit")
		}
		seen[edit.Name] = true
	}
	return backup, nil
}

func writeRulesBackup(path string, backup rulesBackup) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		previous, readErr := readRulesBackup(path)
		if readErr != nil {
			return readErr
		}
		if !sameJSON(previous, backup) {
			return errors.New("backup file exists with a different plan; refusing overwrite")
		}
		file, err = os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		if err := file.Chmod(0600); err != nil {
			return err
		}
		return file.Sync()
	}
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(backup, "", "  ")
	if err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func (c *setupAPI) restoreRules(ctx context.Context, opts RulesOptions, out io.Writer) error {
	backup, err := readRulesBackup(opts.Restore)
	if err != nil {
		return err
	}
	if backup.Repository != opts.Repo || backup.AppID != opts.AppID {
		return errors.New("backup repository/App does not match --repo/--app-id")
	}
	return c.applyRulesBackup(ctx, opts, backup, true, out)
}

// editPosition refuses all unexpected states, including inherited replacements
// and duplicate names. This comparison is also used after an uncertain write.
func editPosition(snapshot ruleSnapshot, edit rulesEdit, repo string) (id int64, applied bool, err error) {
	var found *Ruleset
	var beforeName string
	if edit.Before != nil {
		_ = json.Unmarshal(edit.Before["name"], &beforeName)
	}
	for i := range snapshot.Rulesets {
		r := &snapshot.Rulesets[i]
		if r.ID == edit.ID && edit.ID > 0 || r.Name == edit.Name || beforeName != "" && r.Name == beforeName {
			if found != nil {
				return 0, false, errors.New("ambiguous managed rulesets during recovery")
			}
			found = r
		}
	}
	if found == nil {
		if edit.ID == 0 {
			return 0, false, nil
		}
		return 0, false, errors.New("captured ruleset is missing")
	}
	if edit.ID > 0 && found.ID != edit.ID {
		return 0, false, errors.New("captured ruleset was replaced with a different ID; refusing intervening edits")
	}
	if found.SourceType != "Repository" || !strings.EqualFold(found.Source, repo) {
		return 0, false, errors.New("cannot restore or edit inherited policy")
	}
	raw := ruleSettings(snapshot.Raw[found.ID])
	if settingsEqual(raw, edit.After) {
		return found.ID, true, nil
	}
	if edit.Before != nil && found.ID == edit.ID && settingsEqual(raw, edit.Before) {
		return found.ID, false, nil
	}
	return 0, false, errors.New("intervening ruleset edits; refusing to overwrite operator changes")
}

func sameRuleInventory(a, b ruleSnapshot) bool {
	if a.DefaultBranch != b.DefaultBranch || len(a.Raw) != len(b.Raw) {
		return false
	}
	for id, raw := range a.Raw {
		if !sameJSON(raw, b.Raw[id]) {
			return false
		}
	}
	return true
}

func (c *setupAPI) applyRulesBackup(ctx context.Context, opts RulesOptions, backup rulesBackup, restore bool, out io.Writer) (err error) {
	encoded, _ := json.Marshal(backup)
	if _, redacted := diagnostic.NewSanitizer(c.secrets...).SanitizeTextRedacted(string(encoded)); redacted {
		return errors.New("managed settings contain credential-like data; refusing recovery capture")
	}
	edits := slices.Clone(backup.Edits)
	if restore {
		slices.Reverse(edits)
	}
	completed := 0
	defer func() {
		if err != nil {
			_, _ = fmt.Fprintf(out, "Recovery stopped: completed %d/%d operations; remaining operations pending or uncertain. Re-run with the same %s file to inspect and resume; serialize all policy edits.\n", completed, len(edits), map[bool]string{true: "--restore", false: "--backup"}[restore])
		}
	}()
	snapshot, err := c.rules(ctx, opts.Repo)
	if err != nil {
		return err
	}
	// Preflight the entire captured plan before making any recovery changes.
	for _, edit := range edits {
		if _, _, err := editPosition(snapshot, edit, opts.Repo); err != nil {
			return err
		}
	}
	for _, edit := range edits {
		id, applied, err := editPosition(snapshot, edit, opts.Repo)
		if err != nil {
			return err
		}
		if applied == !restore {
			if _, err := fmt.Fprintf(out, "Completed %s (%d); already in expected state.\n", edit.Name, id); err != nil {
				return err
			}
			completed++
			continue
		}
		if !opts.Apply {
			if _, err := fmt.Fprintf(out, "Pending restore %s (%d); preview only.\n", edit.Name, id); err != nil {
				return err
			}
			continue
		}
		fresh, err := c.rules(ctx, opts.Repo)
		if err != nil {
			return err
		}
		if !sameRuleInventory(snapshot, fresh) {
			return errors.New("repository policy changed before mutation; refusing concurrent edits")
		}
		method, endpoint, body := http.MethodPost, "repos/"+opts.Repo+"/rulesets", edit.After
		if id > 0 {
			method, endpoint = http.MethodPut, endpoint+"/"+strconv.FormatInt(id, 10)
		}
		if restore {
			body = edit.Before
			if edit.Before == nil {
				method = http.MethodDelete
			}
		}
		// A PUT carries all captured settings, including unknown retained fields.
		// The separate operator token is never handed to runtime clients.
		if method == http.MethodDelete {
			err = c.deleteRuleset(ctx, endpoint)
		} else {
			var response json.RawMessage
			err = c.request(ctx, method, endpoint, explicitUpdateDefaults(body), &response)
		}
		if err != nil {
			return c.wrapError(err, "%s %s failed; inspect/resume using the retained recovery file", method, edit.Name)
		}
		next, err := c.rules(ctx, opts.Repo)
		if err != nil {
			return err
		}
		nextID, nowApplied, err := editPosition(next, edit, opts.Repo)
		if err != nil || nowApplied != !restore {
			return errors.New("write read-back differs from captured expected state; stop and inspect recovery file")
		}
		// Detect edits to other policy even during the write/read-back interval.
		oldOther, newOther := snapshot, next
		oldOther.Raw = make(map[int64]map[string]json.RawMessage)
		newOther.Raw = make(map[int64]map[string]json.RawMessage)
		for key, value := range snapshot.Raw {
			if key != id {
				oldOther.Raw[key] = value
			}
		}
		for key, value := range next.Raw {
			if key != nextID && key != id {
				newOther.Raw[key] = value
			}
		}
		if !sameRuleInventory(oldOther, newOther) {
			return errors.New("other repository policy changed during mutation; stop and inspect")
		}
		snapshot = next
		if _, err := fmt.Fprintf(out, "Completed %s (%d).\n", edit.Name, max(id, nextID)); err != nil {
			return err
		}
		completed++
	}
	return nil
}

func (c *setupAPI) deleteRuleset(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base.String()+endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := c.http.Do(req)
	if err != nil {
		return safeError(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNoContent {
		return c.responseError(res)
	}
	return nil
}
