package status

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// reflectFill sets every reachable field, including unexported and nested
// ones, to a non-zero value. Unsupported kinds fail so a new field type cannot
// silently escape the allowlist test.
func reflectFill(t *testing.T, v reflect.Value, path string, seen map[reflect.Type]int) {
	t.Helper()
	if !v.CanSet() {
		v = reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	}
	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(time.Unix(1700000000, 0).UTC()))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("filled:" + path)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Pointer:
		if seen[v.Type()] > 1 {
			return
		}
		seen[v.Type()]++
		defer func() { seen[v.Type()]-- }()
		v.Set(reflect.New(v.Type().Elem()))
		reflectFill(t, v.Elem(), path, seen)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		reflectFill(t, v.Index(0), path+"[]", seen)
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key, value := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		reflectFill(t, key, path+"{key}", seen)
		reflectFill(t, value, path+"{}", seen)
		v.SetMapIndex(key, value)
	case reflect.Struct:
		for i := range v.NumField() {
			reflectFill(t, v.Field(i), path+"."+v.Type().Field(i).Name, seen)
		}
	default:
		t.Fatalf("%s: cannot fill %s", path, v.Type())
	}
}

// reflectLeaves records every non-zero leaf path with its printed value.
func reflectLeaves(v reflect.Value, path string, out map[string]string) {
	if !v.IsValid() || v.IsZero() {
		return
	}
	if v.Type() == reflect.TypeFor[time.Time]() {
		out[path] = fmt.Sprint(v)
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		reflectLeaves(v.Elem(), path, out)
	case reflect.Slice:
		for i := range v.Len() {
			reflectLeaves(v.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			reflectLeaves(v.MapIndex(k), fmt.Sprintf("%s{%v}", path, k), out)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			reflectLeaves(v.Field(i), path+"."+v.Type().Field(i).Name, out)
		}
	default:
		out[path] = fmt.Sprint(v)
	}
}

// publicEntryLeaves is the complete allowlist. Prefixes retain whole public
// subtrees; any other non-zero leaf in a masked entry is a leak.
var publicEntryLeaves = []string{
	".PR", ".HeadSHA", ".Position", ".AdmittedAt", ".Status", ".Reason", ".LastDequeueReason",
	".Change", ".Dependency", ".dependencyWaiting",
	".Landing.Outcome", ".Landing.SHA", ".Landing.EventID",
	".Detail", ".Gate",
}

func allowlistedLeaf(path string) bool {
	for _, prefix := range publicEntryLeaves {
		if path == prefix || strings.HasPrefix(path, prefix+".") || strings.HasPrefix(path, prefix+"[") || strings.HasPrefix(path, prefix+"{") {
			return true
		}
	}
	return false
}

// TestDependencyEntryReflectAllowlist fills every Entry field (including every
// LandingView field and every nested run/batch reference) and requires the
// masked projection to retain only allowlisted leaves, so new fields default to
// absent rather than to public.
func TestDependencyEntryReflectAllowlist(t *testing.T) {
	var filled Entry
	reflectFill(t, reflect.ValueOf(&filled).Elem(), "", map[reflect.Type]int{})
	all := map[string]string{}
	reflectLeaves(reflect.ValueOf(filled), "", all)
	for i := range reflect.TypeFor[Entry]().NumField() {
		name := "." + reflect.TypeFor[Entry]().Field(i).Name
		found := false
		for path := range all {
			if path == name || strings.HasPrefix(path, name+".") || strings.HasPrefix(path, name+"[") {
				found = true
			}
		}
		if !found {
			t.Fatalf("fill missed field %s", name)
		}
	}
	for i := range reflect.TypeFor[LandingView]().NumField() {
		if _, ok := all[".Landing."+reflect.TypeFor[LandingView]().Field(i).Name]; !ok {
			t.Fatalf("fill missed landing field %s", reflect.TypeFor[LandingView]().Field(i).Name)
		}
	}
	for _, status := range []string{"not-queued", "blocked", "waiting-entry", "queued", "building", "testing", "invalidating", "dequeue-pending", "paused", "landing", "merged", "closed", "dequeued", "filled:.Status"} {
		t.Run(status, func(t *testing.T) {
			original := filled
			original.Status = status
			masked := original
			maskDependencyEntry(&masked)
			before, after := map[string]string{}, map[string]string{}
			reflectLeaves(reflect.ValueOf(original), "", before)
			reflectLeaves(reflect.ValueOf(masked), "", after)
			for path, got := range after {
				if !allowlistedLeaf(path) {
					t.Errorf("non-allowlisted leaf %s survived masking: %v", path, got)
					continue
				}
				want, ok := before[path]
				switch {
				case path == ".Status" || path == ".Reason" || path == ".Gate":
					// Progress normalization and the public dequeue gate are
					// checked below; they never copy private values.
				case path == ".Detail" && status != "waiting-entry":
					t.Errorf("batch-derived detail survived for %s", status)
				case !ok || want != got:
					t.Errorf("allowlisted leaf %s changed: %v -> %v", path, want, got)
				}
			}
			for _, path := range publicEntryLeaves {
				if _, kept := after[path]; !kept && path != ".Detail" && path != ".Gate" && path != ".Reason" {
					found := false
					for leaf := range after {
						found = found || strings.HasPrefix(leaf, path)
					}
					if !found {
						t.Errorf("allowlisted field %s was dropped", path)
					}
				}
			}
			progress := status == "queued" || status == "building" || status == "testing" || status == "invalidating"
			wantStatus, wantReason, wantGate := status, original.Reason, ""
			if progress {
				wantStatus, wantReason = "queued", ""
			}
			if status == "dequeue-pending" {
				wantGate = "dequeue"
			}
			if masked.Status != wantStatus || masked.Reason != wantReason || masked.Gate != wantGate {
				t.Errorf("status/reason/gate = %q/%q/%q, want %q/%q/%q", masked.Status, masked.Reason, masked.Gate, wantStatus, wantReason, wantGate)
			}
		})
	}
}

// TestDependencyBatchViewReflectAllowlist fills every batch view field and
// requires the redacted queue to carry no batch presentation at all.
func TestDependencyBatchViewReflectAllowlist(t *testing.T) {
	var batch Batch
	var invalidation queue.BatchesInvalidated
	reflectFill(t, reflect.ValueOf(&batch).Elem(), "", map[reflect.Type]int{})
	reflectFill(t, reflect.ValueOf(&invalidation).Elem(), "", map[reflect.Type]int{})
	for _, v := range []any{batch, invalidation} {
		leaves := map[string]string{}
		reflectLeaves(reflect.ValueOf(v), "", leaves)
		for i := range reflect.TypeOf(v).NumField() {
			name := "." + reflect.TypeOf(v).Field(i).Name
			found := false
			for path := range leaves {
				found = found || path == name || strings.HasPrefix(path, name+".") || strings.HasPrefix(path, name+"[")
			}
			if !found {
				t.Fatalf("fill missed %T field %s", v, name)
			}
		}
	}
	for _, phase := range []string{"building", "testing", "invalidating", "landing", "filled"} {
		t.Run(phase, func(t *testing.T) {
			b := batch
			b.Phase = phase
			q := Queue{Chain: []*Batch{&b}, ActiveBatch: &b, Invalidations: []queue.BatchesInvalidated{invalidation}}
			maskDependencyBatches(&q, store.Journal{DependencyRedacted: true})
			leaves := map[string]string{}
			reflectLeaves(reflect.ValueOf(q), "", leaves)
			for path, v := range leaves {
				t.Errorf("batch presentation leaf %s survived: %v", path, v)
			}
			q = Queue{Chain: []*Batch{&b}, ActiveBatch: &b, Invalidations: []queue.BatchesInvalidated{invalidation}}
			maskDependencyBatches(&q, store.Journal{})
			if q.ActiveBatch != &b || len(q.Chain) != 1 || len(q.Invalidations) != 1 {
				t.Fatal("inactive presentation changed")
			}
		})
	}
}
