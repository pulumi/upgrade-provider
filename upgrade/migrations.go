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

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

const (
	TfBridgeXPkg = "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/x"
	ContractPkg  = "github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

func AutoAliasingMigration(resourcesFilePath, providerName string) (bool, error) {
	// Create the AST by parsing src
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, resourcesFilePath, nil, parser.ParseComments)
	if err != nil {
		return false, err
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
		return false, nil
	}

	astutil.AddImport(fset, file, TfBridgeXPkg)
	astutil.AddImport(fset, file, ContractPkg)
	contract.Assertf(astutil.AddNamedImport(fset, file, "EMBED_COMMENT_ANCHOR", "embed"), "duplicate import")

	astutil.Apply(file, nil, func(c *astutil.Cursor) bool {
		n := c.Node()
		switch x := n.(type) {
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
		return false, err
	}
	replaceAnchors := func(s string) string {
		s = strings.Replace(s, "EMBED_COMMENT_ANCHOR \"embed\"",
			"// embed is used to store bridge-metadata.json in the compiled binary\n    _ \"embed\"", 1)
		s = strings.Replace(s, `var metadata []byte // EMBED_DIRECTIVE_ANCHOR`,
			fmt.Sprintf("//go:embed cmd/pulumi-resource-%s/bridge-metadata.json\nvar metadata []byte",
				providerName), 1)
		return s
	}
	s := buf.String()
	s = replaceAnchors(s)
	// format output
	formatted, err := format.Source([]byte(s))
	if err != nil {
		return false, err
	}
	err = os.WriteFile(resourcesFilePath, formatted, 0600)
	if err != nil {
		return false, err
	}
	return true, nil
}

func AssertNoErrorMigration(resourcesFilePath, providerName string) (bool, error) {
	return replaceAstFunction(resourcesFilePath,
		&ast.SelectorExpr{Sel: &ast.Ident{Name: "AssertNoError"}},
		&ast.CallExpr{
			Fun: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "contract"},
				Sel: &ast.Ident{Name: "AssertNoErrorf"},
			},
			Args: []ast.Expr{
				&ast.Ident{Name: "err", Obj: &ast.Object{Kind: ast.Var, Name: "err"}},
				&ast.BasicLit{Kind: token.STRING, Value: "\"failed to apply auto token mapping\""},
			},
		})
}

func replaceAstFunction(filePath string, oldNode *ast.SelectorExpr, newNode *ast.CallExpr) (bool, error) {
	// Create the AST by parsing src
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return false, err
	}
	changesMade := false
	astutil.Apply(file, nil, func(c *astutil.Cursor) bool {
		n := c.Node()
		switch x := n.(type) {
		case *ast.CallExpr:
			if s, ok := x.Fun.(*ast.SelectorExpr); ok {
				if s.Sel.Name == oldNode.Sel.Name {
					changesMade = true
					c.Replace(newNode)
				}
			}
		}
		return true
	})

	buf := new(bytes.Buffer)
	err = printer.Fprint(buf, fset, file)
	if err != nil {
		return false, err
	}
	// format output
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return false, err
	}
	err = os.WriteFile(filePath, formatted, 0600)
	if err != nil {
		return false, err
	}
	return changesMade, err

}
