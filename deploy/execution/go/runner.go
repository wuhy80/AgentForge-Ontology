// This trusted launcher is compiled when an administrator builds the runtime
// image. User source is parsed, compiled and executed only inside that image.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type payload struct {
	Source     string                     `json:"source"`
	Entrypoint string                     `json:"entrypoint"`
	Operation  string                     `json:"operation"`
	Input      map[string]json.RawMessage `json:"input"`
}
type diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	Line    int    `json:"line"`
	Column  int    `json:"column,omitempty"`
}
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < b.limit {
		_, _ = b.Buffer.Write(p[:min(n, b.limit-b.Len())])
	}
	return n, nil
}

func fail(code string, line, column int) {
	_ = json.NewEncoder(os.Stderr).Encode(diagnostic{Code: code, Line: line, Column: column})
	os.Exit(10)
}
func column(source string, line, byteColumn int) int {
	lines := strings.Split(source, "\n")
	if line < 1 || line > len(lines) || byteColumn < 1 {
		return 0
	}
	text := lines[line-1]
	if byteColumn > len(text)+1 {
		return 0
	}
	return len([]rune(text[:byteColumn-1])) + 1
}

// Scanner positions may be changed by //line comments. Physical offsets keep
// editor coordinates inside the submitted source even before directive checks.
func sourcePosition(source string, offset int) (int, int) {
	if offset < 0 || offset > len(source) {
		return 0, 0
	}
	prefix := source[:offset]
	line := strings.Count(prefix, "\n") + 1
	start := strings.LastIndex(prefix, "\n") + 1
	return line, len([]rune(prefix[start:])) + 1
}
func main() {
	var p payload
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 400000))
	if err != nil || json.Unmarshal(raw, &p) != nil || len(p.Source) > 65536 {
		fail("go_error", 0, 0)
	}
	fset := token.NewFileSet()
	file, parseErr := parser.ParseFile(fset, "source.go", p.Source, parser.AllErrors|parser.ParseComments)
	if p.Operation == "check" || p.Operation == "format" {
		result := struct {
			Source      string       `json:"source"`
			Diagnostics []diagnostic `json:"diagnostics"`
		}{Source: p.Source, Diagnostics: []diagnostic{}}
		if parseErr != nil {
			if list, ok := parseErr.(scanner.ErrorList); ok {
				for i, item := range list {
					if i >= 50 {
						break
					}
					line, col := sourcePosition(p.Source, item.Pos.Offset)
					result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "syntax_error", Message: "Go语法错误，请检查此处的语句与符号", Line: line, Column: col})
				}
			}
		} else if p.Operation == "format" {
			formatted, e := format.Source([]byte(p.Source))
			if e != nil {
				fail("go_error", 1, 0)
			}
			result.Source = string(formatted)
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		return
	}
	if parseErr != nil {
		if list, ok := parseErr.(scanner.ErrorList); ok && len(list) > 0 {
			line, col := sourcePosition(p.Source, list[0].Pos.Offset)
			fail("syntax_error", line, col)
		}
		fail("syntax_error", 1, 0)
	}
	if file.Name.Name != "main" {
		fail("go_source_rejected", fset.Position(file.Name.Pos()).Line, 0)
	}
	stdRaw, err := os.ReadFile("/opt/ontology/std.txt")
	if err != nil {
		fail("go_error", 0, 0)
	}
	standard := map[string]bool{}
	for _, path := range strings.Fields(string(stdRaw)) {
		standard[path] = true
	}
	for _, imp := range file.Imports {
		path, e := strconv.Unquote(imp.Path.Value)
		if e != nil || !standard[path] || strings.Contains(path, "/internal/") || strings.HasPrefix(path, "internal/") {
			fail("go_source_rejected", fset.Position(imp.Pos()).Line, 0)
		}
	}
	entryLine := 0
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			if fn.Name.Name == "main" || fn.Name.Name == "init" || strings.HasPrefix(fn.Name.Name, "__ontology") {
				fail("go_source_rejected", fset.Position(fn.Pos()).Line, 0)
			}
			if fn.Name.Name == p.Entrypoint {
				entryLine = fset.Position(fn.Pos()).Line
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok && strings.HasPrefix(id.Name, "__ontology") {
			fail("go_source_rejected", fset.Position(id.Pos()).Line, 0)
		}
		return true
	})
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//go:") || strings.HasPrefix(comment.Text, "//line ") || strings.HasPrefix(comment.Text, "/*line ") {
				fail("go_source_rejected", fset.Position(comment.Pos()).Line, 0)
			}
		}
	}
	if entryLine == 0 {
		fail("go_compile_error", 1, 0)
	}
	work, err := os.MkdirTemp("/tmp", "ontology-")
	if err != nil {
		fail("go_error", 0, 0)
	}
	defer os.RemoveAll(work)
	cache := filepath.Join(work, "cache")
	if err = copyCache("/opt/ontology/cache", cache); err != nil {
		fail("go_error", 0, 0)
	}
	sourcePath := filepath.Join(work, "source.go")
	driverPath := filepath.Join(work, "driver.go")
	binaryPath := filepath.Join(work, "invoke")
	if os.WriteFile(sourcePath, []byte(p.Source), 0600) != nil || os.WriteFile(driverPath, []byte(fmt.Sprintf(driver, p.Entrypoint, p.Operation == "execute_data", entryLine)+dataSDK), 0600) != nil {
		fail("go_error", 0, 0)
	}
	compile := exec.Command("/usr/local/go/bin/go", "build", "-trimpath", "-o", binaryPath, sourcePath, driverPath)
	compile.Env = []string{"PATH=/usr/local/go/bin:/usr/bin:/bin", "GOCACHE=" + cache, "GOTMPDIR=" + work, "TMPDIR=" + work, "GOPATH=" + filepath.Join(work, "gopath"), "CGO_ENABLED=0", "GOMAXPROCS=1", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOENV=off", "GOTELEMETRY=off"}
	compilerOutput := &limitedBuffer{limit: 16384}
	compile.Stdout = compilerOutput
	compile.Stderr = compilerOutput
	if err = compile.Run(); err != nil {
		re := regexp.MustCompile(`source\.go:(\d+):(\d+):`)
		match := re.FindStringSubmatch(compilerOutput.String())
		if len(match) == 3 {
			line, _ := strconv.Atoi(match[1])
			col, _ := strconv.Atoi(match[2])
			fail("go_compile_error", line, column(p.Source, line, col))
		}
		fail("go_compile_error", entryLine, 0)
	}
	input, _ := json.Marshal(p.Input)
	cmd := exec.Command(binaryPath)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TMPDIR=" + work, "GOMAXPROCS=1", "GOTRACEBACK=none"}
	protocol, err := os.Create(filepath.Join(work, "output.json"))
	if err != nil {
		fail("go_error", 0, 0)
	}
	defer protocol.Close()
	diagnostics, err := os.Create(filepath.Join(work, "error.json"))
	if err != nil {
		fail("go_error", 0, 0)
	}
	defer diagnostics.Close()
	// Extra files become descriptors 3 and 4. Even user package initializers
	// run with stdout/stderr disconnected from the protocol, before main starts.
	cmd.ExtraFiles = []*os.File{protocol, diagnostics}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err = cmd.Run()
	_, _ = protocol.Seek(0, 0)
	_, _ = diagnostics.Seek(0, 0)
	stdout, _ := io.ReadAll(io.LimitReader(protocol, 262145))
	stderr, _ := io.ReadAll(io.LimitReader(diagnostics, 16385))
	if err != nil {
		var d diagnostic
		if len(stderr) <= 16384 && json.Unmarshal(stderr, &d) == nil && (d.Code == "go_error") {
			fail(d.Code, d.Line, 0)
		}
		fail("go_error", entryLine, 0)
	}
	if len(stdout) > 262144 {
		fail("go_error", entryLine, 0)
	}
	var output map[string]json.RawMessage
	if json.Unmarshal(stdout, &output) != nil || output == nil {
		fail("go_error", entryLine, 0)
	}
	_, _ = os.Stdout.Write(stdout)
}

func copyCache(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(output, input)
		closeErr := output.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

const driver = `package main
import (__ontologyJSON "encoding/json"; __ontologyOS "os"; __ontologyIO "io"; __ontologyRuntime "runtime"; __ontologyStrings "strings")
func main() {
    __ontologyOut := __ontologyOS.NewFile(3, "protocol")
    __ontologyErr := __ontologyOS.NewFile(4, "diagnostic")
    __ontologyQuiet, __ontologyOpenError := __ontologyOS.OpenFile(__ontologyOS.DevNull, __ontologyOS.O_WRONLY, 0)
    if __ontologyOpenError != nil { __ontologyOS.Exit(10) }
    __ontologyOS.Stdout, __ontologyOS.Stderr = __ontologyQuiet, __ontologyQuiet
    __ontologyFail := func(__ontologyLine int) { _ = __ontologyJSON.NewEncoder(__ontologyErr).Encode(map[string]any{"code":"go_error","line":__ontologyLine}); __ontologyOS.Exit(10) }
    defer func() { if recover() != nil { __ontologyPC := make([]uintptr,64); __ontologyCount := __ontologyRuntime.Callers(2,__ontologyPC); __ontologyFrames := __ontologyRuntime.CallersFrames(__ontologyPC[:__ontologyCount]); __ontologyLine:=0;for { __ontologyFrame,__ontologyMore:=__ontologyFrames.Next();if __ontologyFrame.File=="source.go" || __ontologyStrings.HasSuffix(__ontologyFrame.File,"/source.go") {__ontologyLine=__ontologyFrame.Line;break};if !__ontologyMore {break} };__ontologyFail(__ontologyLine) } }()
    var __ontologyInput map[string]any
    __ontologyDecoder := __ontologyJSON.NewDecoder(__ontologyIO.LimitReader(__ontologyOS.Stdin,262145));__ontologyDecoder.UseNumber()
    if __ontologyDecoder.Decode(&__ontologyInput) != nil { __ontologyFail(0) }
    __ontologyEntry := any(%s)
    __ontologyDataMode, __ontologyEntryLine := %t, %d
    __ontologyData := &OntologyData{__ontologyOperations:make([]__ontologyOperation,0),__ontologyKeys:make(map[string]bool)}
    __ontologyContext := &OntologyContext{Data:__ontologyData}
    var __ontologyOutput map[string]any
    var __ontologyCallError error
    switch __ontologyHandler := __ontologyEntry.(type) {
    case func(map[string]any)(map[string]any,error): __ontologyOutput,__ontologyCallError=__ontologyHandler(__ontologyInput)
    case func(map[string]any,*OntologyContext)(map[string]any,error): if !__ontologyDataMode {__ontologyFail(__ontologyEntryLine)};__ontologyOutput,__ontologyCallError=__ontologyHandler(__ontologyInput,__ontologyContext)
    case func(map[string]any,*OntologyContext)map[string]any: if !__ontologyDataMode {__ontologyFail(__ontologyEntryLine)};__ontologyOutput=__ontologyHandler(__ontologyInput,__ontologyContext)
    default: __ontologyFail(__ontologyEntryLine)
    }
    if __ontologyCallError != nil || __ontologyOutput == nil || __ontologyData.__ontologyPoisoned { __ontologyFail(__ontologyEntryLine) }
    for _, __ontologyValue := range __ontologyOutput { switch __ontologyValue.(type) { case nil,string,bool,int,int8,int16,int32,int64,uint,uint8,uint16,uint32,uint64,float32,float64,__ontologyJSON.Number: default: __ontologyFail(0) } }
    var __ontologyResult any = __ontologyOutput
    if __ontologyDataMode { __ontologyResult=map[string]any{"output":__ontologyOutput,"operations":__ontologyData.__ontologyOperations} }
    __ontologyEncoded, __ontologyEncodeError := __ontologyJSON.Marshal(__ontologyResult)
    if __ontologyEncodeError != nil || len(__ontologyEncoded)>262144 { __ontologyFail(0) }
    _, _ = __ontologyOut.Write(__ontologyEncoded)
}
`

// Only the trusted generated driver can name the reserved state fields. The
// public SDK stages operations and returns no IDs or database observations.
const dataSDK = `
type OntologyContext struct { Data *OntologyData }
type OntologyMatch struct {
    ID string ` + "`json:\"id,omitempty\"`" + `
    IndexID string ` + "`json:\"indexId,omitempty\"`" + `
    Values map[string]any ` + "`json:\"values,omitempty\"`" + `
}
type OntologyWriteItem struct {
    ClientKey string
    ID string
    IfMatch string
    Match *OntologyMatch
    Values map[string]any
}
type __ontologyOperation struct {
    SchemaID string ` + "`json:\"schemaId\"`" + `
    Operation string ` + "`json:\"operation\"`" + `
    ClientKey string ` + "`json:\"clientKey\"`" + `
    ID string ` + "`json:\"id,omitempty\"`" + `
    IfMatch string ` + "`json:\"ifMatch,omitempty\"`" + `
    Match *OntologyMatch ` + "`json:\"match,omitempty\"`" + `
    Values map[string]any ` + "`json:\"values,omitempty\"`" + `
    ConflictPolicy string ` + "`json:\"conflictPolicy,omitempty\"`" + `
}
type OntologyData struct {
    __ontologyOperations []__ontologyOperation
    __ontologyKeys map[string]bool
    __ontologyPoisoned bool
}
func __ontologyRequire(ok bool) { if !ok { panic("invalid data write plan") } }
func __ontologyID(value string) string {
    __ontologyRequire(len(value)==36)
    for i,ch:=range value {
        if i==8 || i==13 || i==18 || i==23 { __ontologyRequire(ch=='-') } else { __ontologyRequire(ch>='0'&&ch<='9'||ch>='a'&&ch<='f') }
    }
    return value
}
func __ontologyETag(value string, required bool) string {
    if value=="" && !required { return value }
    __ontologyRequire(len(value)>2 && value[0]=='"' && value[len(value)-1]=='"')
    for _,ch:=range value[1:len(value)-1] { __ontologyRequire(ch>32 && ch!=127 && ch!='"') }
    return value
}
func __ontologyValues(values map[string]any) map[string]any {
    __ontologyRequire(values!=nil)
    copied:=make(map[string]any,len(values))
    for key,value:=range values {
        __ontologyID(key)
        switch value.(type) { case nil,string,bool,int,int8,int16,int32,int64,uint,uint8,uint16,uint32,uint64,float32,float64,__ontologyJSON.Number: default: __ontologyRequire(false) }
        _,err:=__ontologyJSON.Marshal(value);__ontologyRequire(err==nil)
        copied[key]=value
    }
    return copied
}
func __ontologyMatchCopy(match *OntologyMatch) *OntologyMatch {
    __ontologyRequire(match!=nil)
    if match.ID!="" { __ontologyRequire(match.IndexID=="" && match.Values==nil);return &OntologyMatch{ID:__ontologyID(match.ID)} }
    __ontologyRequire(len(match.Values)>0)
    return &OntologyMatch{IndexID:__ontologyID(match.IndexID),Values:__ontologyValues(match.Values)}
}
func (data *OntologyData) __ontologyStage(operation,schema string,item OntologyWriteItem,policy string) {
    defer func(){if failure:=recover();failure!=nil {data.__ontologyPoisoned=true;panic(failure)}}()
    __ontologyRequire(!data.__ontologyPoisoned && len(data.__ontologyOperations)<100)
    __ontologyID(schema)
    switch operation {
    case "create": __ontologyRequire(item.ID=="" && item.IfMatch=="" && item.Match==nil)
    case "update": __ontologyRequire(item.Match==nil)
    case "delete": __ontologyRequire(item.Match==nil && item.Values==nil)
    case "upsert": __ontologyRequire(item.ID=="")
    default: __ontologyRequire(false)
    }
    __ontologyRequire(len(item.ClientKey)>0 && len(item.ClientKey)<=128 && !data.__ontologyKeys[item.ClientKey])
    for _,ch:=range item.ClientKey { __ontologyRequire(ch>=32 && (ch<127||ch>159)) }
    out:=__ontologyOperation{SchemaID:schema,Operation:operation,ClientKey:item.ClientKey}
    if operation!="delete" {out.Values=__ontologyValues(item.Values)}
    if operation=="update" || operation=="delete" {out.ID=__ontologyID(item.ID);out.IfMatch=__ontologyETag(item.IfMatch,true)}
    if operation=="upsert" {
        if policy=="" {policy="check"};__ontologyRequire(policy=="check"||policy=="overwrite")
        out.Match=__ontologyMatchCopy(item.Match);out.IfMatch=__ontologyETag(item.IfMatch,false);out.ConflictPolicy=policy
    }
    if data.__ontologyKeys==nil {data.__ontologyKeys=make(map[string]bool)}
    data.__ontologyOperations=append(data.__ontologyOperations,out);data.__ontologyKeys[item.ClientKey]=true
}
func (data *OntologyData) Create(schema string,values map[string]any,clientKey string) {data.__ontologyStage("create",schema,OntologyWriteItem{Values:values,ClientKey:clientKey},"")}
func (data *OntologyData) Update(schema,id string,values map[string]any,ifMatch,clientKey string) {data.__ontologyStage("update",schema,OntologyWriteItem{ID:id,Values:values,IfMatch:ifMatch,ClientKey:clientKey},"")}
func (data *OntologyData) Delete(schema,id,ifMatch,clientKey string) {data.__ontologyStage("delete",schema,OntologyWriteItem{ID:id,IfMatch:ifMatch,ClientKey:clientKey},"")}
func (data *OntologyData) Upsert(schema string,match OntologyMatch,values map[string]any,ifMatch,clientKey,conflictPolicy string) {data.__ontologyStage("upsert",schema,OntologyWriteItem{Match:&match,Values:values,IfMatch:ifMatch,ClientKey:clientKey},conflictPolicy)}
func (data *OntologyData) __ontologyBulk(operation,schema string,items []OntologyWriteItem,policy string) {
    defer func(){if failure:=recover();failure!=nil {data.__ontologyPoisoned=true;panic(failure)}}()
    __ontologyRequire(len(items)>0 && len(items)<=100 && len(data.__ontologyOperations)+len(items)<=100)
    for _,item:=range items {data.__ontologyStage(operation,schema,item,policy)}
}
func (data *OntologyData) BulkCreate(schema string,items []OntologyWriteItem) {data.__ontologyBulk("create",schema,items,"")}
func (data *OntologyData) BulkUpdate(schema string,items []OntologyWriteItem) {data.__ontologyBulk("update",schema,items,"")}
func (data *OntologyData) BulkDelete(schema string,items []OntologyWriteItem) {data.__ontologyBulk("delete",schema,items,"")}
func (data *OntologyData) BulkUpsert(schema string,items []OntologyWriteItem,conflictPolicy string) {data.__ontologyBulk("upsert",schema,items,conflictPolicy)}
`
