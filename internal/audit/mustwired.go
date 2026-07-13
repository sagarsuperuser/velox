package audit

import (
	"fmt"
	"reflect"
	"strings"
)

// MustWired panics unless every given component carries a non-nil audit
// emitter. Components wire their emitter via Set* injection after
// construction (the router's dependency cycles force two-phase wiring), so
// a forgotten SetAuditLogger line compiles fine and silently drops the
// component's audit coverage — services deliberately skip emission on a nil
// emitter so unit tests stay fake-friendly. This is the loud boot-time
// counterpart (same shape as engine.MustValidate): call it from the
// composition root after the SetAuditLogger cluster, and add every newly
// audited component to that call.
//
// A component satisfies the check when it has at least one field (exported
// or not) whose type is an interface declaring a LogInTx method or whose
// name contains "audit", and every such field is non-nil.
func MustWired(components ...any) {
	for _, c := range components {
		v := reflect.ValueOf(c)
		if !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil()) {
			panic(fmt.Sprintf("audit.MustWired: component %T is nil", c))
		}
		for v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			panic(fmt.Sprintf("audit.MustWired: component %T is not a struct", c))
		}
		t := v.Type()
		found := false
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			isAuditField := strings.Contains(strings.ToLower(f.Name), "audit")
			if !isAuditField && f.Type.Kind() == reflect.Interface {
				if _, ok := f.Type.MethodByName("LogInTx"); ok {
					isAuditField = true
				}
			}
			if !isAuditField {
				continue
			}
			found = true
			fv := v.Field(i)
			switch fv.Kind() {
			case reflect.Interface, reflect.Pointer:
				// IsNil works on unexported fields without Interface().
				if fv.IsNil() {
					panic(fmt.Sprintf("audit.MustWired: %s.%s is nil — SetAuditLogger not called for %T",
						t.Name(), f.Name, c))
				}
			}
		}
		if !found {
			panic(fmt.Sprintf("audit.MustWired: %T declares no audit emitter field — remove it from the call or add the field", c))
		}
	}
}
