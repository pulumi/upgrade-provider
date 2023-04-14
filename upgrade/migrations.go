package upgrade

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/printer"
	"go/token"
	"os"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

const (
	TfBridgeXPkg = "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/x"
	ContractPkg  = "github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

func AutoAliasingMigration(fset *token.FileSet, file *ast.File, savePath, providerName string) (bool, error) {
	changesMade := false

	changesMade = astutil.AddImport(fset, file, TfBridgeXPkg)
	changesMade = astutil.AddImport(fset, file, ContractPkg) || changesMade
	changesMade = astutil.AddNamedImport(fset, file, "_", "embed") || changesMade

	applied, errAssigned := false, false
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
			for _, l := range x.Lhs {
				if e, ok := l.(*ast.Ident); ok && e.Name == "err" {
					errAssigned = true
				}
			}
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
					tok := token.DEFINE
					if errAssigned {
						tok = token.ASSIGN
					}
					c.InsertBefore(&ast.AssignStmt{
						Tok: tok,
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

	file.Decls = append(file.Decls, &ast.GenDecl{
		// Doc: &ast.CommentGroup{
		// 	List: []*ast.Comment{
		// 		{Text: "go:embed cmd/pulumi-resource-databricks/bridge-metadata.json"},
		// 	},
		// },
		Tok: token.VAR,
		Specs: []ast.Spec{
			&ast.ValueSpec{
				Names: []*ast.Ident{{Name: "metadata", Obj: &ast.Object{Kind: ast.Var, Name: "metadata"}}},
				Type:  &ast.ArrayType{Elt: &ast.Ident{Name: "byte"}},
			},
		},
	})

	out, err := os.Create(savePath)
	if err != nil {
		return false, err
	}
	err = printer.Fprint(out, fset, file)
	if err != nil {
		return false, err
	}

	// TODO: figure out how to properly append comments so everything can be done via AST manipulation
	b, err := os.ReadFile(savePath)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		if strings.Contains(line, `_ "embed"`) {
			lines[i] = "    // embed package blank import\n" + lines[i]
		} else if strings.Contains(line, "var metadata []byte") {
			lines[i] = fmt.Sprintf("//go:embed cmd/pulumi-resource-%s/bridge-metadata.json\n", providerName) + lines[i]
		}
	}
	b = []byte(strings.Join(lines, "\n"))
	formatted, err := format.Source(b)
	if err != nil {
		return false, err
	}
	err = os.WriteFile(savePath, formatted, 0600)

	return changesMade, err
}
