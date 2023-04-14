package upgrade

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
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

func AutoAliasingMigration(resourcesFilePath, providerName string) error {
	// Create the AST by parsing src
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, resourcesFilePath, nil, parser.ParseComments)
	if err != nil {
		return err
	}

	applied, initMetadata, errAssigned := false, true, false
	// check to see if already implemented
	astutil.Apply(file, nil, func(c *astutil.Cursor) bool {
		n := c.Node()
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Rhs) == 1 {
				if c, ok := x.Rhs[0].(*ast.CallExpr); ok {
					if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "AutoAliasing" {
						applied = true
						return true
					}
				}
			}
		}
		return true
	})
	if applied {
		return nil
	}

	astutil.AddImport(fset, file, TfBridgeXPkg)
	astutil.AddImport(fset, file, ContractPkg)
	astutil.AddNamedImport(fset, file, "EMBED_COMMENT_ANCHOR", "embed")

	astutil.Apply(file, nil, func(c *astutil.Cursor) bool {
		n := c.Node()
		switch x := n.(type) {
		// case *ast.ImportSpec:
		// 	if x.Path.Value == "\"embed\"" {
		// 		x.Comment = &ast.CommentGroup{List: []*ast.Comment{
		// 			{Text: "embed package not used directly"},
		// 		}}
		// 		c.Replace(x)
		// 	}
		case *ast.GenDecl:
			if x.Tok == token.VAR {
				if s, ok := x.Specs[0].(*ast.ValueSpec); ok && s.Names[0].Name == "metadata" {
					initMetadata = false
				}
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
		case *ast.ExprStmt:
			var id *ast.SelectorExpr
			call, ok := x.X.(*ast.CallExpr)
			if ok {
				id, ok = call.Fun.(*ast.SelectorExpr)
			}
			if ok {
				if id.Sel.Name == "SetAutonaming" {
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
				}
			}
		}

		return true
	})

	// TODO: figure out how to properly append comments so everything can be done via AST manipulation
	if initMetadata {
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
					Type:  &ast.ArrayType{Elt: &ast.Ident{Name: "byte // EMBED_DIRECTIVE_ANCHOR"}},
				},
			},
		})
	}

	buf := new(bytes.Buffer)
	err = printer.Fprint(buf, fset, file)
	if err != nil {
		return err
	}
	s := string(buf.Bytes())
	s = strings.Replace(s, `EMBED_COMMENT_ANCHOR "embed"`,
		"// embed is used to store bridge-metadata.json in the compiled binary\n    _ \"embed\"", 1)
	s = strings.Replace(s, `var metadata []byte // EMBED_DIRECTIVE_ANCHOR`,
		fmt.Sprintf("//go:embed cmd/pulumi-resource-%s/bridge-metadata.json\nvar metadata []byte",
			providerName), 1)
	// format output
	formatted, err := format.Source([]byte(s))
	if err != nil {
		return err
	}
	err = os.WriteFile(resourcesFilePath, formatted, 0600)

	return err
}
