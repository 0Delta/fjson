package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
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
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

var obsolete = []string{
	"1", "1.1", "1.2", "1.3",
}

func main() {
	log.Println("generate...")

	goversRaw, err := GetGoVersions()
	if err != nil {
		log.Fatal(err)
		return
	}
	baseWd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
		return
	}

	// filtering obsolete versions
	var govers []string
	for _, gv := range goversRaw {
		f := true
		for _, o := range obsolete {
			if gv == o {
				f = false
			}
		}
		if f {
			govers = append(govers, gv)
		}
	}

	for idx, gover := range govers {
		err := os.Chdir(baseWd)
		if err != nil {
			log.Fatal(err)
			return
		}
		log.Printf("%d/%d - %s", idx+1, len(govers), gover)
		err = _main(gover)
		if err != nil {
			log.Printf("generate failed - %s", err.Error())
		}
	}
}

var srcfname = "_gosrc.tar.gz"

func _main(gover string) error {
	goverNum := strings.TrimLeft(gover, "go")
	err := os.RemoveAll(goverNum)
	if err != nil {
		return err
	}
	err = os.Mkdir(goverNum, 0755)
	if err != nil {
		return err
	}
	err = os.Chdir(goverNum)
	if err != nil {
		return err
	}
	err = GetSrc(srcfname, gover)
	if err != nil {
		return err
	}
	srcf, err := os.Open(srcfname)
	if err != nil {
		return err
	}
	defer func() {
		nerr := os.Remove(srcfname)
		if nerr != nil {
			err = fmt.Errorf("%s :%w", nerr.Error(), err)
		}
	}()
	err = extract(srcf)
	if err != nil {
		return err
	}
	err = srcf.Close()
	if err != nil {
		return err
	}
	err = override()
	if err != nil {
		return err
	}
	return nil
}

func GetSrc(fname string, version string) error {
	if version == "" {
		version = runtime.Version()
	}
	url := "https://go.dev/dl/" + version + ".src.tar.gz"
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

type GithubTagsApiResponse struct {
	Name       string `json:"name"`
	ZipballURL string `json:"zipball_url"`
	TarballURL string `json:"tarball_url"`
	Commit     struct {
		Sha string `json:"sha"`
		URL string `json:"url"`
	} `json:"commit"`
	NodeID string `json:"node_id"`
}

func GetGoVersions() ([]string, error) {
	regexGoReleaseTag := regexp.MustCompile(`^go[0-9]+(\.[0-9]+)?$`)
	ret := []string{}
	for p := 0; ; p++ {
		url := "https://api.github.com/repos/golang/go/tags?per_page=100&page=" + strconv.Itoa(p)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		ght := os.Getenv("GITHUB_TOKEN")
		if ght != "" {
			req.Header.Set("Authorization", "Bearer "+ght)
		}
		resp, err := new(http.Client).Do(req)
		if err != nil {
			return nil, err
		}

		defer resp.Body.Close()
		byteArray, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		var sresp []GithubTagsApiResponse
		err = json.Unmarshal(byteArray, &sresp)
		if err != nil {
			fmt.Println(string(byteArray))
			return nil, err
		}
		if len(sresp) < 1 {
			break
		}
		for _, sr := range sresp {
			if regexGoReleaseTag.Match([]byte(sr.Name)) {
				ret = append(ret, sr.Name)
			}
		}
	}
	return ret, nil
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
		name := ""
		switch {
		case strings.HasPrefix(header.Name, "go/src/encoding/json/"):
			name = strings.TrimPrefix(header.Name, "go/src/encoding/json/")
			if name == "" {
				continue
			}
		case strings.HasPrefix(header.Name, "go/src/internal/testenv"):
			name = strings.TrimPrefix(header.Name, "go/src/")
			if name == "internal/testenv" {
				strings.TrimLeft(name, "internal/")
				continue
			}
		default:
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
	// encode.go
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "./encode.go", nil, parser.ParseComments)
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
					v.Args[0] =
						&ast.CallExpr{
							Fun: &ast.SelectorExpr{
								X:   &ast.Ident{Name: "fmt"},
								Sel: &ast.Ident{Name: "Sprintf"},
							},
							Args: []ast.Expr{
								&ast.BasicLit{
									Kind:  token.STRING,
									Value: `"\"%s\""`,
								},
								&ast.CallExpr{
									Fun: &ast.SelectorExpr{
										X: &ast.ParenExpr{
											X: v.Args[0],
										},
										Sel: &ast.Ident{Name: "Error"},
									},
									Args: []ast.Expr{},
								},
							},
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
	nf, err := os.Create("./encode.go")
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

	// bench_test.go
	fset = token.NewFileSet()
	f, err = parser.ParseFile(fset, "./bench_test.go", nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("Failed to parse file: %w", err)
	}

	for i := range f.Imports {
		if f.Imports[i].Path.Value == "\"internal/testenv\"" {
			f.Imports[i].Path.Value = "\"testenv\""
		}
	}

	// write
	buf = new(bytes.Buffer)
	err = format.Node(buf, token.NewFileSet(), f)
	if err != nil {
		return fmt.Errorf("Failed node convert: %w", err)
	}
	nf, err = os.Create("./bench_test.go")
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
