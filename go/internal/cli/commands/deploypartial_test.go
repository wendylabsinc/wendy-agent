package commands

import (
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func keysOf(m map[string]*appconfig.ServiceConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestResolveDeployableServices(t *testing.T) {
	// a -> b -> c (a depends on b depends on c); d standalone.
	services := map[string]*appconfig.ServiceConfig{
		"a": {DependsOn: []string{"b"}},
		"b": {DependsOn: []string{"c"}},
		"c": {},
		"d": {},
	}

	t.Run("no failures: all deployable", func(t *testing.T) {
		dep, dropped := resolveDeployableServices(services, map[string]error{})
		if got := keysOf(dep); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
			t.Fatalf("deployable = %v", got)
		}
		if len(dropped) != 0 {
			t.Fatalf("dropped = %v", dropped)
		}
	})

	t.Run("leaf failure cascades to dependents", func(t *testing.T) {
		// c failed -> b and a can't deploy (dependency); d unaffected.
		dep, dropped := resolveDeployableServices(services, map[string]error{"c": errors.New("boom")})
		if got := keysOf(dep); !reflect.DeepEqual(got, []string{"d"}) {
			t.Fatalf("deployable = %v, want [d]", got)
		}
		// a and b are dropped due to dependency; c is failed (not in dropped).
		if _, ok := dropped["a"]; !ok {
			t.Errorf("expected a dropped (failed dependency)")
		}
		if _, ok := dropped["b"]; !ok {
			t.Errorf("expected b dropped (failed dependency)")
		}
		if _, ok := dropped["c"]; ok {
			t.Errorf("c failed itself; must not appear in dropped: %v", dropped)
		}
	})

	t.Run("independent failure does not affect others", func(t *testing.T) {
		dep, dropped := resolveDeployableServices(services, map[string]error{"d": errors.New("boom")})
		if got := keysOf(dep); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
			t.Fatalf("deployable = %v, want [a b c]", got)
		}
		if len(dropped) != 0 {
			t.Fatalf("dropped = %v, want none", dropped)
		}
	})
}

func TestJoinServiceErrorsStableOrder(t *testing.T) {
	failed := map[string]error{
		"zeta":  errors.New("z"),
		"alpha": errors.New("a"),
	}
	if got := sortedServiceErrorKeys(failed); !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("sortedServiceErrorKeys = %v", got)
	}
	if joinServiceErrors(failed) == nil {
		t.Fatal("expected non-nil joined error")
	}
}
