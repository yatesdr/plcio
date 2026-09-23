package compatibility_test

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func surface(root string) (map[string]string, error) {
	result := make(map[string]string)
	for _, pkg := range []string{"ads", "driver"} {
		files, err := filepath.Glob(filepath.Join(root, pkg, "*.go"))
		if err != nil {
			return nil, err
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return nil, err
			}
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if !d.Name.IsExported() {
						continue
					}
					key := pkg + "." + d.Name.Name
					if d.Recv != nil {
						var receiver bytes.Buffer
						format.Node(&receiver, token.NewFileSet(), d.Recv.List[0].Type)
						key = pkg + "." + receiver.String() + "." + d.Name.Name
					}
					// Parameter identifiers do not affect a published function type.
					ast.Inspect(d.Type, func(n ast.Node) bool {
						if f, ok := n.(*ast.Field); ok {
							f.Names = nil
						}
						return true
					})
					var signature bytes.Buffer
					format.Node(&signature, token.NewFileSet(), d.Type)
					result[key] = signature.String()
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						typ, ok := spec.(*ast.TypeSpec)
						if !ok || !typ.Name.IsExported() {
							continue
						}
						// Types with existing private fields cannot be external unkeyed literals.
						if st, ok := typ.Type.(*ast.StructType); ok {
							private := false
							for _, field := range st.Fields.List {
								for _, name := range field.Names {
									if !name.IsExported() {
										private = true
									}
								}
							}
							if private {
								continue
							}
						}
						var signature bytes.Buffer
						format.Node(&signature, token.NewFileSet(), typ.Type)
						result[pkg+"."+typ.Name.Name] = signature.String()
					}
				}
			}
		}
	}
	return result, nil
}

func TestPublishedSurface(t *testing.T) {
	actual, err := surface("../..")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/public-api-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline map[string]string
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	for key, want := range baseline {
		if got := actual[key]; got != want {
			t.Errorf("published surface changed: %s\nwant %s\ngot  %s", key, want, got)
		}
	}
	allowed := map[string]bool{
		"ads.DecodedTagValue": true, "ads.*Client.ReadDecoded": true, "ads.*Client.Describe": true,
		"ads.WithLocalAmsNetId": true, "ads.WithLocalAmsPort": true, "ads.WithMaxPayload": true,
		"ads.WithMaxBatchItems": true, "ads.WithMetadataLimits": true, "ads.WithExpansionLimits": true,
		"ads.WithStringEncoding": true, "driver.Describer": true, "driver.NewADSAdapterWithOptions": true,
		"driver.*ADSAdapter.Describe": true,
		// Additive hardening APIs: ReadState keepalive, UDP Get Info discovery
		// report, connection-loss classification and discovery error report.
		"ads.*Client.ReadState": true, "ads.DiscoverWithReport": true,
		"driver.IsConnectionLost": true, "driver.DiscoverAllWithReport": true,
	}
	for key := range actual {
		if _, exists := baseline[key]; !exists && !allowed[key] {
			t.Errorf("unplanned public addition: %s", key)
		}
	}
}
