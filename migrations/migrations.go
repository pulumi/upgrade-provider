package migrations

import (
	"go/ast"
	"go/printer"
	"go/token"
	"os"

	"golang.org/x/tools/go/ast/astutil"
)

const (
	TfBridgeXPkg = "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/x"
	ContractPkg  = "github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

func AddAutoAliasingSourceCode(fset *token.FileSet, file *ast.File, savePath string) (bool, error) {
	changesMade := false

	changesMade = astutil.AddImport(fset, file, TfBridgeXPkg)
	changesMade = astutil.AddImport(fset, file, ContractPkg) || changesMade
	changesMade = astutil.AddNamedImport(fset, file, "_", "embed") || changesMade

	applied := false
	astutil.Apply(file, nil, func(c *astutil.Cursor) bool {
		n := c.Node()
		switch x := n.(type) {
		case *ast.ImportSpec:
			if x.Path.Value == "\"embed\"" {
				x.Comment = &ast.CommentGroup{List: []*ast.Comment{
					{Text: "embed package not used directly"},
				}}
				c.Replace(x)
			}
		case *ast.GenDecl:
			if x.Tok == token.CONST {
				c.InsertBefore(&ast.GenDecl{
					Doc: &ast.CommentGroup{
						List: []*ast.Comment{
							{Text: "go:embed cmd/pulumi-resource-databricks/bridge-metadata.json"},
						},
					},
					Tok: token.VAR,
					Specs: []ast.Spec{
						&ast.ValueSpec{
							Names: []*ast.Ident{{Name: "metadata", Obj: &ast.Object{Kind: ast.Var, Name: "metadata"}}},
							Type:  &ast.ArrayType{Elt: &ast.Ident{Name: "byte"}},
						},
					},
				})
			}
		case *ast.CompositeLit:
			if s, ok := x.Type.(*ast.SelectorExpr); ok && s.Sel.Name == "ProviderInfo" {
				x.Elts = append(x.Elts, &ast.KeyValueExpr{
					Key: &ast.Ident{Name: "MetadataInfo"},
					Value: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   &ast.Ident{Name: "tfbridge"},
							Sel: &ast.Ident{Name: "NewProviderMetadata"},
						},
						Args: []ast.Expr{&ast.Ident{Name: "metadata"}},
					},
				})
			}
		case *ast.AssignStmt:
			if len(x.Rhs) == 1 {
				if c, ok := x.Rhs[0].(*ast.CallExpr); ok {
					if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "AutoAliasing" {
						applied = true
						return true
					}
				}
			}
		case *ast.ExprStmt:
			var id *ast.SelectorExpr
			call, ok := x.X.(*ast.CallExpr)
			if ok {
				id, ok = call.Fun.(*ast.SelectorExpr)
			}
			if ok {
				if id.Sel.Name == "SetAutonaming" && !applied {
					c.InsertBefore(&ast.AssignStmt{
						Tok: token.DEFINE,
						Lhs: []ast.Expr{&ast.Ident{Name: "err", Obj: &ast.Object{Kind: ast.Var, Name: "err"}}},
						Rhs: []ast.Expr{&ast.CallExpr{
							Fun: &ast.SelectorExpr{
								X:   &ast.Ident{Name: "x"},
								Sel: &ast.Ident{Name: "AutoAliasing"},
							},
							Args: []ast.Expr{
								&ast.UnaryExpr{
									Op: token.AND,
									X:  &ast.Ident{Name: "prov", Obj: &ast.Object{Kind: ast.Var, Name: "prov"}},
								},
								&ast.CallExpr{
									Fun: &ast.SelectorExpr{
										X:   &ast.Ident{Name: "prov", Obj: &ast.Object{Kind: ast.Var, Name: "prov"}},
										Sel: &ast.Ident{Name: "GetMetadata"},
									},
								},
							},
						}},
					})
					c.InsertBefore(&ast.ExprStmt{
						X: &ast.CallExpr{
							Fun: &ast.SelectorExpr{
								X:   &ast.Ident{Name: "contract"},
								Sel: &ast.Ident{Name: "AssertNoErrorf"},
							},
							Args: []ast.Expr{
								&ast.Ident{Name: "err", Obj: &ast.Object{Kind: ast.Var, Name: "err"}},
								&ast.BasicLit{Kind: token.STRING, Value: "\"auto aliasing apply failed\""},
							},
						}})
					changesMade = true
				}
			}
		}

		return true
	})

	out, err := os.Create(savePath)
	err = printer.Fprint(out, fset, file)
	return changesMade, err
}
