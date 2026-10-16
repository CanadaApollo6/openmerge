package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schema.V1))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.RegisterFormat(&jsonschema.Format{Name: "openmerge-base", Validate: func(v any) error {
		if text, ok := v.(string); ok {
			_, err := condition.CompileGlob(text)
			return err
		}
		return nil
	}})
	compiler.RegisterFormat(&jsonschema.Format{Name: "openmerge-condition", Validate: func(v any) error {
		if text, ok := v.(string); ok {
			_, err := condition.Parse(text)
			return err
		}
		return nil
	}})
	compiler.RegisterFormat(&jsonschema.Format{Name: "openmerge-duration", Validate: func(v any) error {
		if text, ok := v.(string); ok {
			d, err := time.ParseDuration(text)
			if err != nil {
				return err
			}
			if d < 0 {
				return fmt.Errorf("negative duration")
			}
		}
		return nil
	}})
	if err := compiler.AddResource("openmerge.v1.json", doc); err != nil {
		return nil, err
	}
	return compiler.Compile("openmerge.v1.json")
})

type keySchema struct {
	Ref        string                `json:"$ref"`
	Properties map[string]*keySchema `json:"properties"`
	Items      *keySchema            `json:"items"`
	Defs       map[string]*keySchema `json:"$defs"`
}

func TestSchemaKeysMatchLoader(t *testing.T) {
	var published keySchema
	if err := json.Unmarshal([]byte(schema.V1), &published); err != nil {
		t.Fatal(err)
	}
	var walk func(reflect.Type, *keySchema, string)
	walk = func(typ reflect.Type, node *keySchema, path string) {
		t.Helper()
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if node == nil {
			t.Errorf("%s: schema node missing for %s", path, typ)
			return
		}
		if node.Ref != "" {
			name, ok := strings.CutPrefix(node.Ref, "#/$defs/")
			if !ok || published.Defs[name] == nil {
				t.Fatalf("%s: unsupported schema ref %q", path, node.Ref)
			}
			node = published.Defs[name]
		}
		if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			walk(typ.Elem(), node.Items, path+"[]")
			return
		}
		// Compiled expressions have their own grammar/schema agreement corpus;
		// they are not config mapping structs decoded through YAML field tags.
		if typ.Kind() != reflect.Struct || typ.PkgPath() != reflect.TypeFor[Config]().PkgPath() {
			return
		}
		keys := yamlKeys(typ)
		for name := range node.Properties {
			if !slices.Contains(keys, name) {
				t.Errorf("%s.%s: schema key is not accepted by loader", path, name)
			}
		}
		keyIndex := 0
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() || field.Tag.Get("yaml") == "-" {
				continue
			}
			name := keys[keyIndex]
			keyIndex++
			walk(field.Type, node.Properties[name], path+"."+name)
		}
	}
	walk(reflect.TypeFor[Config](), &published, "config")
}

// JSON Schema operates on the YAML data model after syntax/alias processing.
// YAML-only failures (documents, duplicates, tags, expansion limits) have no
// JSON equivalent. Preserve the loader's strict scalar typing before conversion:
// JSON Schema considers 1.0 an integer, but YAML's !!float is never accepted.
func schemaInput(data []byte) (any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected one document: %v", err)
	}
	budget := 10000
	root, diagnostic := expand(doc.Content[0], make(map[*yaml.Node]bool), 0, &budget)
	if diagnostic != nil {
		return nil, diagnostic
	}
	return jsonValue(root, "")
}

func jsonValue(n *yaml.Node, key string) (any, error) {
	switch n.Kind {
	case yaml.MappingNode:
		value := make(map[string]any)
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
				return nil, fmt.Errorf("non-string mapping key")
			}
			item, err := jsonValue(n.Content[i+1], k.Value)
			if err != nil {
				return nil, err
			}
			value[k.Value] = item
		}
		return value, nil
	case yaml.SequenceNode:
		value := make([]any, 0, len(n.Content))
		for _, child := range n.Content {
			item, err := jsonValue(child, "")
			if err != nil {
				return nil, err
			}
			value = append(value, item)
		}
		return value, nil
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!str", "!!bool", "!!null":
			var value any
			err := n.Decode(&value)
			return value, err
		case "!!int":
			if (key == "check_timeout" || key == "max_wait") && n.Value != "0" {
				return nil, fmt.Errorf("only literal integer 0 is a duration")
			}
			var value int
			if err := n.Decode(&value); err != nil {
				return nil, err
			}
			return json.Number(fmt.Sprint(value)), nil
		}
	}
	return nil, fmt.Errorf("unsupported YAML kind/tag: %d %s", n.Kind, n.ShortTag())
}

func assertSchemaAgreement(t *testing.T, data []byte, loaderError error) {
	t.Helper()
	compiled, err := compiledSchema()
	if err != nil {
		t.Fatal(err)
	}
	value, schemaError := schemaInput(data)
	if schemaError == nil {
		schemaError = compiled.Validate(value)
	}
	if (loaderError == nil) != (schemaError == nil) {
		t.Fatalf("loader/schema disagreement for:\n%s\nloader: %v\nschema: %v", data, loaderError, schemaError)
	}
}

func TestSchemaRepositoryFixturesAndExamples(t *testing.T) {
	// Walk tracked source/docs locations so adding another full config example or
	// fixture automatically adds it to the agreement corpus.
	count := 0
	fenced := regexp.MustCompile("(?s)```(?:yaml|yml)\\n(.*?)```")
	err := filepath.WalkDir("../..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		fixture := name == ".openmerge.yml" || name == ".openmerge.yaml" || strings.Contains(filepath.ToSlash(path), "/testdata/") && (strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml"))
		if !fixture && !strings.HasSuffix(name, ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		examples := [][]byte{data}
		if !fixture {
			examples = nil
			for _, match := range fenced.FindAllSubmatch(data, -1) {
				if strings.Contains(string(match[1]), "version: 1") {
					examples = append(examples, match[1])
				}
			}
		}
		for i, example := range examples {
			count++
			t.Run(fmt.Sprintf("%s/%d", path, i), func(t *testing.T) {
				_, _, err := Load(example)
				assertSchemaAgreement(t, example, err)
				if err != nil {
					t.Fatal(err)
				}
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count < 4 {
		t.Fatalf("only %d fixtures/examples found", count)
	}
}

func TestSchemaGrammarAndSemanticLimits(t *testing.T) {
	for _, atom := range []string{"-draft", "draft!=false", "#label>=-2", "base glob **/main", "title ~= (?i)ready", "check-success=ci", "title = multiline\nvalue", "#files >= 9223372036854775808", "title ~= [", "files glob bad\\", "draft = invalid", "unknown = value"} {
		data, err := yaml.Marshal(map[string]any{"version": 1, "queues": []any{map[string]any{"name": "default", "merge_when": []string{"check-success = ci"}, "enter_when": []string{atom}}}})
		if err != nil {
			t.Fatal(err)
		}
		_, _, loadError := Load(data)
		assertSchemaAgreement(t, data, loadError)
	}
	for _, duration := range []string{"0", "+0", "-0", "-0s", "+1h2m", ".5s", "1.s", "1µs", "1μs", "-1ns", "999999999999999999999h", "1d"} {
		data := []byte("version: 1\nqueues:\n- name: default\n  merge_when: [check-success = ci]\n  check_timeout: '" + duration + "'\n")
		_, _, err := Load(data)
		assertSchemaAgreement(t, data, err)
	}
}

func TestSchemaDefaultsMatchLoader(t *testing.T) {
	var published struct {
		Defs struct {
			Queue struct {
				Properties struct {
					Method struct {
						Default string `json:"default"`
					} `json:"merge_method"`
					Timeout struct {
						Default string `json:"default"`
					} `json:"check_timeout"`
					Batch struct {
						Default struct {
							Size int    `json:"max_size"`
							Wait string `json:"max_wait"`
						} `json:"default"`
						Properties struct {
							Size struct {
								Default int `json:"default"`
							} `json:"max_size"`
							Wait struct {
								Default string `json:"default"`
							} `json:"max_wait"`
						} `json:"properties"`
					} `json:"batch"`
				} `json:"properties"`
			} `json:"queue"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(schema.V1), &published); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load([]byte("version: 1\nqueues: [{name: default, merge_when: [check-success = ci]}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := published.Defs.Queue.Properties
	timeout, err := time.ParseDuration(p.Timeout.Default)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := time.ParseDuration(p.Batch.Default.Wait)
	if err != nil {
		t.Fatal(err)
	}
	q := cfg.Queues[0]
	if string(q.MergeMethod) != p.Method.Default || q.CheckTimeout != timeout || q.Batch.MaxSize != p.Batch.Default.Size || q.Batch.MaxWait != wait || p.Batch.Properties.Size.Default != p.Batch.Default.Size || p.Batch.Properties.Wait.Default != p.Batch.Default.Wait {
		t.Fatal("published defaults differ from loader defaults")
	}
}
