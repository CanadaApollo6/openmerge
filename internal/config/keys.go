package config

import (
	"reflect"
	"strings"
)

var (
	configKeys = yamlKeys(reflect.TypeFor[Config]())
	queueKeys  = yamlKeys(reflect.TypeFor[Queue]())
	checksKeys = yamlKeys(reflect.TypeFor[Checks]())
	batchKeys  = yamlKeys(reflect.TypeFor[Batch]())
)

// The parser and schema drift guard share the config structs' key vocabulary.
func yamlKeys(typ reflect.Type) []string {
	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		keys = append(keys, name)
	}
	return keys
}
