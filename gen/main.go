package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

func main() {
	log.Println("generate...")

	srcfname := "_gosrc.tar.gz"
	err := GetSrc(srcfname)
	if err != nil {
		log.Fatal(err)
		return
	}
	srcf, err := os.Open(srcfname)
	if err != nil {
		log.Fatal(err)
		return
	}
	err = extract(srcf)
	if err != nil {
		log.Fatal(err)
		return
	}
	err = srcf.Close()
	if err != nil {
		log.Fatal(err)
		return
	}
	err = os.Remove(srcfname)
	if err != nil {
		log.Fatal(err)
	}

	err = override()
	if err != nil {
		log.Fatal(err)
		return
	}

}

func GetSrc(fname string) error {
	url := "https://go.dev/dl/" + runtime.Version() + ".src.tar.gz"
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	byteArray, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	f, err := os.Create(fname)
	if err != nil {
		return err
	}
	defer func() {
		var err error
		err = f.Close()
		if err != nil {
			log.Fatal(err)
		}
	}()
	_, err = f.Write(byteArray)
	if err != nil {
		return err
	}
	return nil
}

func extract(gzipStream io.Reader) error {
	uncompressedStream, err := gzip.NewReader(gzipStream)
	if err != nil {
		return fmt.Errorf("ExtractTarGz: NewReader failed :%w", err)
	}

	tarReader := tar.NewReader(uncompressedStream)
	for true {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("ExtractTarGz: Next() failed :%w", err)
		}

		// extract json package
		if !strings.HasPrefix(header.Name, "go/src/encoding/json/") {
			continue
		}
		name := strings.TrimPrefix(header.Name, "go/src/encoding/json/")
		if name == "" {
			continue
		}
		name = "./" + name

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(name, 0755); err != nil {
				return fmt.Errorf("ExtractTarGz: Mkdir() failed :%w", err)
			}
		case tar.TypeReg:
			outFile, err := os.Create(name)
			if err != nil {
				return fmt.Errorf("ExtractTarGz: Create() failed :%w", err)
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				return fmt.Errorf("ExtractTarGz: Copy() failed :%w", err)
			}
			outFile.Close()
		default:
			return fmt.Errorf("ExtractTarGz: uknown type: %s in %s", string(header.Typeflag), header.Name)
		}
	}
	return nil
}

func override() error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../encode.go", nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("Failed to parse file: %w", err)
	}

	_ = astutil.Apply(f, func(cr *astutil.Cursor) bool {
		if cr.Node() != nil {
			l := fset.Position((cr.Node().Pos())).Line
			switch v := cr.Node().(type) {
			case *ast.CallExpr:
				s, ok := v.Fun.(*ast.SelectorExpr)
				if !ok {
					break
				}
				if s.Sel.Name != "error" {
					break
				}
				if s.X.(*ast.Ident).Obj.Decl.(*ast.Field).Type.(*ast.StarExpr).X.(*ast.Ident).Name == "encodeState" {
					fmt.Printf("%d : %s %#v\n", l, v.Fun, v.Args)
					v.Fun.(*ast.SelectorExpr).Sel = &ast.Ident{Name: "WriteString"}
					v.Args[0] = &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X: &ast.ParenExpr{
								X: v.Args[0],
							},
							Sel: &ast.Ident{Name: "Error"},
						},
						Args: []ast.Expr{},
					}
					cr.Replace(v)
				}
			}
		}
		return true
	}, nil)

	// write
	buf := new(bytes.Buffer)
	err = format.Node(buf, token.NewFileSet(), f)
	if err != nil {
		return fmt.Errorf("Failed node convert: %w", err)
	}
	nf, err := os.Create("../encode.go")
	if err != nil {
		return fmt.Errorf("Failed open file: %w", err)
	}
	_, err = nf.Write(buf.Bytes())
	if err != nil {
		return fmt.Errorf("Failed write file: %w", err)
	}
	err = nf.Close()
	if err != nil {
		return fmt.Errorf("Failed close file: %w", err)
	}
	return nil
}
