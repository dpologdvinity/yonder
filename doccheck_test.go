// doccheck_test.go enforces the documentation rules of this repository, so they cannot
// quietly rot: every Go file explains its role, and every top-level declaration has a
// doc comment that starts with its own name (the standard Go convention).

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// minDocWords is the shortest doc comment we accept. It rejects placeholders such as
// "// foo does foo." that pass the "starts with the name" rule but explain nothing.
const minDocWords = 4

// TestEveryFileAndDeclarationIsDocumented parses every .go file in this directory and
// reports each file without a header comment and each top-level declaration without a
// proper doc comment.
func TestEveryFileAndDeclarationIsDocumented(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files found: %v", err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		checkFileHeader(t, fset, name, f)
		for _, decl := range f.Decls {
			checkDecl(t, fset, decl)
		}
	}
}

// checkFileHeader reports a file whose first comment does not come before the package
// clause, or is too short to explain what the file is for.
func checkFileHeader(t *testing.T, fset *token.FileSet, name string, f *ast.File) {
	t.Helper()
	if len(f.Comments) == 0 || f.Comments[0].Pos() > f.Package {
		t.Errorf("%s: missing a header comment before the package clause", name)
		return
	}
	if words(f.Comments[0].Text()) < minDocWords {
		t.Errorf("%s: header comment is too short to explain the file", name)
	}
}

// checkDecl reports a top-level function, method, type, variable or constant whose doc
// comment is missing, too short, or does not start with the declared name.
func checkDecl(t *testing.T, fset *token.FileSet, decl ast.Decl) {
	t.Helper()
	switch d := decl.(type) {
	case *ast.FuncDecl:
		checkDoc(t, fset, d.Pos(), d.Name.Name, d.Doc, true)
	case *ast.GenDecl:
		if d.Tok == token.IMPORT {
			return
		}
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				checkDoc(t, fset, s.Pos(), s.Name.Name, docFor(d, s.Doc), true)
			case *ast.ValueSpec:
				// A grouped const or var block may share one doc comment on the block.
				grouped := d.Lparen.IsValid()
				doc := s.Doc
				if !grouped || doc == nil {
					doc = docFor(d, s.Doc)
				}
				checkDoc(t, fset, s.Pos(), s.Names[0].Name, doc, !grouped)
			}
		}
	}
}

// docFor returns the spec's own doc comment, or the block's when the spec has none and
// the block holds a single spec.
func docFor(d *ast.GenDecl, own *ast.CommentGroup) *ast.CommentGroup {
	if own != nil {
		return own
	}
	if !d.Lparen.IsValid() {
		return d.Doc
	}
	return nil
}

// checkDoc applies the rules to one declaration. startsWithName is false for members of
// a grouped block, whose comment may describe the whole group.
func checkDoc(t *testing.T, fset *token.FileSet, pos token.Pos, name string, doc *ast.CommentGroup, startsWithName bool) {
	t.Helper()
	where := fset.Position(pos)
	if doc == nil {
		t.Errorf("%s:%d: %s has no doc comment", where.Filename, where.Line, name)
		return
	}
	text := strings.TrimSpace(doc.Text())
	if words(text) < minDocWords {
		t.Errorf("%s:%d: doc comment of %s is too short to explain it", where.Filename, where.Line, name)
	}
	if startsWithName && !strings.HasPrefix(text, name+" ") && !strings.HasPrefix(text, name+"\n") {
		t.Errorf("%s:%d: doc comment of %s should start with %q", where.Filename, where.Line, name, name)
	}
}

// words counts the whitespace-separated words in s.
func words(s string) int { return len(strings.Fields(s)) }
