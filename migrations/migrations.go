package migrations

import (
	"go/token"
	"os"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"github.com/dave/dst/decorator/resolver/gopackages"
	"github.com/dave/dst/dstutil"
)

const (
	TfBridgeXPkg = "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/x"
	ContractPkg  = "github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

func AddAutoAliasingSourceCode(fset *token.FileSet, file *dst.File, savePath string) (bool, error) {
	changesMade := false

	// changesMade = astutil.AddImport(fset, file, TfBridgeXPkg)
	// changesMade = astutil.AddImport(fset, file, ContractPkg) || changesMade
	// changesMade = astutil.AddNamedImport(fset, file, "_", "embed") || changesMade

	applied := false
	dstutil.Apply(file, nil, func(c *dstutil.Cursor) bool {
		n := c.Node()
		switch x := n.(type) {
		case *dst.ImportSpec:
			c.InsertBefore(&dst.ImportSpec{Path: &dst.BasicLit{Value: TfBridgeXPkg}})
			c.InsertBefore(&dst.ImportSpec{Path: &dst.BasicLit{Value: ContractPkg}})
			embedImp := dst.ImportSpec{Path: &dst.BasicLit{Value: "_ embed"}}
			embedImp.Decs.Start.Append("embed package blank import")
			c.InsertBefore(&embedImp)
		case *dst.GenDecl:
			if x.Tok == token.CONST {
				d := &dst.GenDecl{
					// Doc: &dst.CommentGroup{
					// 	List: []*dst.Comment{
					// 		{Text: "go:embed cmd/pulumi-resource-databricks/bridge-metadata.json"},
					// 	},
					// },
					Tok: token.VAR,
					Specs: []dst.Spec{
						&dst.ValueSpec{
							Names: []*dst.Ident{{Name: "metadata", Obj: &dst.Object{Kind: dst.Var, Name: "metadata"}}},
							Type:  &dst.ArrayType{Elt: &dst.Ident{Name: "byte"}},
						},
					},
				}
				d.Decs.Start.Append("go:embed cmd/pulumi-resource-databricks/bridge-metadata.json")
				c.InsertBefore(d)
			}
		case *dst.CompositeLit:
			if s, ok := x.Type.(*dst.SelectorExpr); ok && s.Sel.Name == "ProviderInfo" {
				x.Elts = append(x.Elts, &dst.KeyValueExpr{
					Key: &dst.Ident{Name: "MetadataInfo"},
					Value: &dst.CallExpr{
						Fun: &dst.SelectorExpr{
							X:   &dst.Ident{Name: "tfbridge"},
							Sel: &dst.Ident{Name: "NewProviderMetadata"},
						},
						Args: []dst.Expr{&dst.Ident{Name: "metadata"}},
					},
				})
			}
		case *dst.AssignStmt:
			if len(x.Rhs) == 1 {
				if c, ok := x.Rhs[0].(*dst.CallExpr); ok {
					if s, ok := c.Fun.(*dst.SelectorExpr); ok && s.Sel.Name == "AutoAliasing" {
						applied = true
						return true
					}
				}
			}
		case *dst.ExprStmt:
			var id *dst.SelectorExpr
			call, ok := x.X.(*dst.CallExpr)
			if ok {
				id, ok = call.Fun.(*dst.SelectorExpr)
			}
			if ok {
				if id.Sel.Name == "SetAutonaming" && !applied {
					c.InsertBefore(&dst.AssignStmt{
						Tok: token.DEFINE,
						Lhs: []dst.Expr{&dst.Ident{Name: "err", Obj: &dst.Object{Kind: dst.Var, Name: "err"}}},
						Rhs: []dst.Expr{&dst.CallExpr{
							Fun: &dst.SelectorExpr{
								X:   &dst.Ident{Name: "x"},
								Sel: &dst.Ident{Name: "AutoAliasing"},
							},
							Args: []dst.Expr{
								&dst.UnaryExpr{
									Op: token.AND,
									X:  &dst.Ident{Name: "prov", Obj: &dst.Object{Kind: dst.Var, Name: "prov"}},
								},
								&dst.CallExpr{
									Fun: &dst.SelectorExpr{
										X:   &dst.Ident{Name: "prov", Obj: &dst.Object{Kind: dst.Var, Name: "prov"}},
										Sel: &dst.Ident{Name: "GetMetadata"},
									},
								},
							},
						}},
					})
					c.InsertBefore(&dst.ExprStmt{
						X: &dst.CallExpr{
							Fun: &dst.SelectorExpr{
								X:   &dst.Ident{Name: "contract"},
								Sel: &dst.Ident{Name: "AssertNoErrorf"},
							},
							Args: []dst.Expr{
								&dst.Ident{Name: "err", Obj: &dst.Object{Kind: dst.Var, Name: "err"}},
								&dst.BasicLit{Kind: token.STRING, Value: "\"auto aliasing apply failed\""},
							},
						}})
					changesMade = true
				}
			}
		}

		return true
	})

	out, err := os.Create(savePath)
	if err != nil {
		return false, err
	}
	restorer := decorator.NewRestorerWithImports("st", gopackages.New(""))
	err = restorer.Fprint(out, file)
	if err != nil {
		return false, err
	}

	// decorator.Print(file)
	// dst.Fprint(out, file, nil)
	// // printer.Fprint(out, fset, file)
	return changesMade, err
}
