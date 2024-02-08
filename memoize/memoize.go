package memoize

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strings"
	"text/template"

	"github.com/Mzack9999/gcache"
	"golang.org/x/sync/singleflight"
	"golang.org/x/tools/imports"
)

type Memoizer struct {
	cache gcache.Cache[string, interface{}]
	group singleflight.Group
}

type MemoizeOption func(m *Memoizer) error

func WithMaxSize(size int) MemoizeOption {
	return func(m *Memoizer) error {
		m.cache = gcache.New[string, interface{}](size).Build()
		return nil
	}
}

func New(options ...MemoizeOption) (*Memoizer, error) {
	m := &Memoizer{}
	for _, option := range options {
		if err := option(m); err != nil {
			return nil, err
		}
	}

	return m, nil
}

func (m *Memoizer) Do(funcHash string, fn func() (interface{}, error)) (interface{}, error, bool) {
	if value, err := m.cache.GetIFPresent(funcHash); !errors.Is(err, gcache.KeyNotFoundError) {
		return value, err, true
	}

	value, err, _ := m.group.Do(funcHash, func() (interface{}, error) {
		data, err := fn()

		if err == nil {
			m.cache.Set(funcHash, data)
		}

		return data, err
	})

	return value, err, false
}

func File(sourceFile, packageName string) ([]byte, error) {
	data, err := os.ReadFile(sourceFile)
	if err != nil {
		return nil, err
	}

	return Src(sourceFile, data, packageName)
}

func Src(sourcePath string, source []byte, packageName string) ([]byte, error) {
	var (
		fileData FileData
		content  bytes.Buffer
	)

	tmpl, err := template.New("package_template").Parse(packageTemplate)
	if err != nil {
		return nil, err
	}

	fileData.PackageName = packageName

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, sourcePath, source, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	for _, nn := range node.Imports {
		var packageImport PackageImport
		if nn.Name != nil {
			packageImport.Name = nn.Name.Name
		}

		if nn.Path != nil {
			packageImport.Path = nn.Path.Value
		}

		fileData.Imports = append(fileData.Imports, packageImport)
	}

	fileData.SourcePackage = node.Name.Name

	ast.Inspect(node, func(n ast.Node) bool {
		switch nn := n.(type) {
		case *ast.FuncDecl:
			if !nn.Name.IsExported() {
				return false
			}
			if nn.Doc == nil {
				return false
			}

			var funcDeclaration FunctionDeclaration
			funcDeclaration.IsExported = true
			funcDeclaration.Name = nn.Name.Name
			funcDeclaration.SourcePackage = fileData.SourcePackage
			var funcSign strings.Builder
			printer.Fprint(&funcSign, fset, nn.Type)
			funcDeclaration.Signature = strings.Replace(funcSign.String(), "func", "func "+funcDeclaration.Name, 1)

			for _, comment := range nn.Doc.List {
				if comment.Text == "// @memo" {
					if nn.Type.Params != nil {
						for idx, param := range nn.Type.Params.List {
							var funcParam FuncValue
							funcParam.Index = idx
							for _, name := range param.Names {
								funcParam.Name = name.String()
							}
							funcParam.Type = fmt.Sprint(param.Type)
							funcDeclaration.Params = append(funcDeclaration.Params, funcParam)
						}
					}

					if nn.Type.Results != nil {
						for idx, res := range nn.Type.Results.List {
							var result FuncValue
							result.Index = idx
							for _, name := range res.Names {
								result.Name = name.String()
							}
							result.Type = fmt.Sprint(res.Type)
							funcDeclaration.Results = append(funcDeclaration.Results, result)
						}
					}
				}
			}
			fileData.Functions = append(fileData.Functions, funcDeclaration)
			return false
		default:
			return true
		}
	})

	err = tmpl.Execute(&content, fileData)
	if err != nil {
		return nil, err
	}

	out, err := imports.Process(sourcePath, content.Bytes(), nil)
	if err != nil {
		return nil, err
	}

	return format.Source(out)
}

type PackageImport struct {
	Name string
	Path string
}

type FuncValue struct {
	Index int
	Name  string
	Type  string
}

func (f FuncValue) ResultName() string {
	return fmt.Sprintf("result%d", f.Index)
}

type FunctionDeclaration struct {
	SourcePackage string
	IsExported    bool
	Name          string
	Params        []FuncValue
	Results       []FuncValue
	Signature     string
}

func (f FunctionDeclaration) HasParams() bool {
	return len(f.Params) > 0
}

func (f FunctionDeclaration) ParamsNames() string {
	var params []string
	for _, param := range f.Params {
		params = append(params, param.Name)
	}
	return strings.Join(params, ",")
}

func (f FunctionDeclaration) HasReturn() bool {
	return len(f.Results) > 0
}

func (f FunctionDeclaration) WantSyncOnce() bool {
	return !f.HasParams()
}

func (f FunctionDeclaration) SyncOnceVarName() string {
	return fmt.Sprintf("once%s", f.Name)
}

func (f FunctionDeclaration) WantReturn() bool {
	return f.HasReturn()
}

func (f FunctionDeclaration) ResultStructType() string {
	return fmt.Sprintf("result%s", f.Name)
}

func (f FunctionDeclaration) ResultStructVarName() string {
	return fmt.Sprintf("v%s", f.ResultStructType())
}

func (f FunctionDeclaration) ResultStructFields() string {
	var results []string
	for _, result := range f.Results {
		results = append(results, fmt.Sprintf("%s.%s", f.ResultStructVarName(), result.ResultName()))
	}
	return strings.Join(results, ",")
}

type FileData struct {
	PackageName   string
	SourcePackage string
	Imports       []PackageImport
	Functions     []FunctionDeclaration
}
