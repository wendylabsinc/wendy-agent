package interceptor

import (
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
)

const securityDoc = "../../cli/assets/docs/security/index.mdx"

// viewerMethodsDoc renders every viewer-level entry of methodRoles, one line
// per service, as the security doc lists them.
func viewerMethodsDoc() string {
	bySvc := map[string][]string{}
	for full, role := range methodRoles {
		if role != trustedkeys.RoleViewer {
			continue
		}
		svc, m, _ := strings.Cut(strings.TrimPrefix(full, "/"), "/")
		if svc == strings.Trim(BLEMethodPrefix, "/") {
			svc = "BLE"
		}
		bySvc[svc] = append(bySvc[svc], m)
	}
	var b strings.Builder
	for _, svc := range slices.Sorted(maps.Keys(bySvc)) {
		slices.Sort(bySvc[svc])
		b.WriteString("- `" + svc + "`: " + strings.Join(bySvc[svc], ", ") + "\n")
	}
	return b.String()
}

// The security doc's viewer list is exactly the role table's viewer entries.
func TestSecurityDocListsViewerMethods(t *testing.T) {
	doc, err := os.ReadFile(securityDoc)
	if err != nil {
		t.Fatal(err)
	}
	const begin, end = "{/* viewer-methods:begin */}\n", "{/* viewer-methods:end */}"
	_, rest, ok1 := strings.Cut(string(doc), begin)
	got, _, ok2 := strings.Cut(rest, end)
	if want := viewerMethodsDoc(); !ok1 || !ok2 || got != want {
		t.Fatalf("%s viewer list is out of date with authz_table.go; between the markers it must read:\n%s", securityDoc, want)
	}
}
