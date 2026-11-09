package flate_test

// This file is shared: flate/apiparity_test.go, gzip/apiparity_test.go and
// zlib/apiparity_test.go are identical apart from the package clause. The
// package-specific parts (stdPath, ourPath, apiExtras, reflectPairs) are in
// each package's apiparity_format_test.go; newReader, stdNewReader and
// bodyStart come from reader_format_test.go, newWriter, stdWriter and
// streamWriter from writer_format_test.go, stdEncode from format_test.go.
// TestAPIParitySharedFileInSync checks the copies.

import (
	stdflate "compress/flate"
	"errors"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/flate"
)

// modulePath prefixes the packages of this module that replace a standard
// library package.
const modulePath = "github.com/vahid-sohrabloo/fastzstd/"

// canonPkg maps compress/X and this module's X (X = flate, gzip, zlib) to X,
// so that types of the two implementations compare equal by name.
func canonPkg(path string) string {
	for _, p := range []string{"flate", "gzip", "zlib"} {
		if path == "compress/"+p || path == modulePath+p {
			return p
		}
	}
	return path
}

// loadPackages type-checks std and ours from their export data (go list
// -export builds it), so the comparison sees exactly the exported API.
func loadPackages(t *testing.T) (stdPkg, ourPkg *types.Package) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not found:", err)
	}
	out, err := exec.Command(goBin, "list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}", stdPath, ourPath).Output()
	if err != nil {
		t.Fatalf("go list -export: %v", err)
	}
	exports := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p, f, ok := strings.Cut(l, "="); ok && f != "" {
			exports[p] = f
		}
	}
	imp := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		f, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(f)
	})
	if stdPkg, err = imp.Import(stdPath); err != nil {
		t.Fatal(err)
	}
	if ourPkg, err = imp.Import(ourPath); err != nil {
		t.Fatal(err)
	}
	return stdPkg, ourPkg
}

func qualifier(p *types.Package) string { return canonPkg(p.Path()) }

func typeString(t types.Type) string { return types.TypeString(t, qualifier) }

// sigString is a signature without parameter names (they may differ).
func sigString(sig *types.Signature) string {
	tuple := func(tp *types.Tuple, variadic bool) string {
		var s []string
		for i := range tp.Len() {
			ts := typeString(tp.At(i).Type())
			if variadic && i == tp.Len()-1 {
				ts = "..." + strings.TrimPrefix(ts, "[]")
			}
			s = append(s, ts)
		}
		return "(" + strings.Join(s, ", ") + ")"
	}
	return "func" + tuple(sig.Params(), sig.Variadic()) + " " + tuple(sig.Results(), false)
}

// declString describes an exported package-level object: its kind, type and,
// for a constant, its exact value.
func declString(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Const:
		return "const " + typeString(o.Type()) + " = " + o.Val().ExactString()
	case *types.Var:
		return "var " + typeString(o.Type())
	case *types.Func:
		return sigString(o.Type().(*types.Signature))
	case *types.TypeName:
		u := o.Type().Underlying()
		s := "type"
		if o.IsAlias() {
			s = "type alias"
		}
		switch u := u.(type) {
		case *types.Struct:
			return s + " struct"
		case *types.Interface:
			return s + " interface"
		default:
			return s + " " + typeString(u)
		}
	}
	return fmt.Sprintf("%T", obj)
}

// members lists the exported fields of a struct type (".F") and the exported
// methods of T (".M") and *T ("*.M").
func members(obj *types.TypeName) map[string]string {
	m := map[string]string{}
	if st, ok := obj.Type().Underlying().(*types.Struct); ok {
		for i := range st.NumFields() {
			f := st.Field(i)
			if !f.Exported() {
				continue
			}
			d := typeString(f.Type())
			if f.Embedded() {
				d = "embedded " + d
			}
			m["."+f.Name()] = "field " + d + " " + st.Tag(i)
		}
	}
	for _, ptr := range []bool{false, true} {
		t := obj.Type()
		prefix := ""
		if ptr {
			if types.IsInterface(t) {
				continue
			}
			t, prefix = types.NewPointer(t), "*"
		}
		ms := types.NewMethodSet(t)
		for i := range ms.Len() {
			f := ms.At(i).Obj()
			if f.Exported() {
				m[prefix+"."+f.Name()] = sigString(f.Type().(*types.Signature))
			}
		}
	}
	return m
}

// TestAPIParity: every exported identifier of the standard library package
// exists here with the same kind, type (signature, constant value, struct
// fields, method sets of T and *T; a type is comparable here if and only if
// it is there), and every identifier added on top of it is listed in
// apiExtras.
func TestAPIParity(t *testing.T) {
	stdPkg, ourPkg := loadPackages(t)
	used := map[string]bool{}
	extra := func(name, what string) {
		if _, ok := apiExtras[name]; ok {
			used[name] = true
			return
		}
		t.Errorf("%s %s is not in the standard library's %s and not in apiExtras", what, name, stdPath)
	}
	stdScope, ourScope := stdPkg.Scope(), ourPkg.Scope()
	for _, name := range ourScope.Names() {
		obj := ourScope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		stdObj := stdScope.Lookup(name)
		if stdObj == nil {
			extra(name, "identifier")
			if tn, ok := obj.(*types.TypeName); ok {
				for k := range members(tn) {
					extra(name+strings.TrimPrefix(k, "*"), "member")
				}
			}
			continue
		}
		if a, b := declString(stdObj), declString(obj); a != b {
			t.Errorf("%s: standard library %q, ours %q", name, a, b)
		}
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		// Not comparable here would break code using ==; comparable here
		// only would let code compile here that does not compile with the
		// standard library.
		if a, b := types.Comparable(stdObj.Type()), types.Comparable(tn.Type()); a != b {
			t.Errorf("%s: comparable in the standard library %v, here %v", name, a, b)
		}
		stdMembers, ourMembers := members(stdObj.(*types.TypeName)), members(tn)
		for k, a := range stdMembers {
			if b, ok := ourMembers[k]; !ok {
				t.Errorf("%s: member %s (%s) is missing", name, k, a)
			} else if a != b {
				t.Errorf("%s: member %s: standard library %q, ours %q", name, k, a, b)
			}
		}
		for k := range ourMembers {
			if _, ok := stdMembers[k]; !ok {
				extra(name+strings.TrimPrefix(k, "*"), "member")
			}
		}
	}
	for _, name := range stdScope.Names() {
		if stdScope.Lookup(name).Exported() && ourScope.Lookup(name) == nil {
			t.Errorf("%s (%s) of %s is missing", name, declString(stdScope.Lookup(name)), stdPath)
		}
	}
	for name := range apiExtras {
		if !used[name] {
			t.Errorf("apiExtras lists %s, which is not an extra exported identifier (stale entry)", name)
		}
	}
}

// TestAPIParityReflect compares the standard library's and our functions,
// variables and types with reflect (reflectPairs), and checks that
// reflectPairs covers every exported function, variable and type of the
// standard library package.
func TestAPIParityReflect(t *testing.T) {
	stdPkg, _ := loadPackages(t)
	for _, name := range stdPkg.Scope().Names() {
		obj := stdPkg.Scope().Lookup(name)
		if _, isConst := obj.(*types.Const); obj.Exported() && !isConst {
			if _, ok := reflectPairs[name]; !ok {
				t.Errorf("reflectPairs has no entry for %s", name)
			}
		}
	}
	names := make([]string, 0, len(reflectPairs))
	for name := range reflectPairs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := reflectPairs[name]
		a, b := asType(p[0]), asType(p[1])
		if diff := sameType(a, b, map[[2]reflect.Type]bool{}); diff != "" {
			t.Errorf("%s: %s", name, diff)
		}
		if _, isType := p[0].(reflect.Type); isType {
			for _, d := range methodDiff(name, a, b) {
				t.Errorf("%s", d)
			}
		}
	}
}

// asType is the type a reflectPairs entry stands for: a reflect.Type itself,
// the element type of a pointer to a variable, or a function's type.
func asType(v any) reflect.Type {
	if rt, ok := v.(reflect.Type); ok {
		return rt
	}
	rt := reflect.TypeOf(v)
	if rt.Kind() == reflect.Pointer {
		return rt.Elem()
	}
	return rt
}

// methodDiff compares the exported method sets of a and b, and of *a and *b;
// a method of b that a lacks must be in apiExtras as "T.M".
func methodDiff(name string, a, b reflect.Type) []string {
	var diffs []string
	for _, ptr := range []bool{false, true} {
		ta, tb := a, b
		if ptr {
			if a.Kind() == reflect.Interface {
				continue
			}
			ta, tb = reflect.PointerTo(a), reflect.PointerTo(b)
		}
		for i := range ta.NumMethod() {
			ma := ta.Method(i)
			mb, ok := tb.MethodByName(ma.Name)
			if !ok {
				diffs = append(diffs, fmt.Sprintf("%v: method %s is missing", tb, ma.Name))
				continue
			}
			if d := sameType(ma.Type, mb.Type, map[[2]reflect.Type]bool{}); d != "" {
				diffs = append(diffs, fmt.Sprintf("%v.%s: %s", tb, ma.Name, d))
			}
		}
		for i := range tb.NumMethod() {
			if _, ok := ta.MethodByName(tb.Method(i).Name); !ok {
				if _, ok := apiExtras[name+"."+tb.Method(i).Name]; !ok {
					diffs = append(diffs, fmt.Sprintf("%v: extra method %s not in apiExtras", tb, tb.Method(i).Name))
				}
			}
		}
	}
	return diffs
}

// sameType compares two types structurally, treating a named type of the
// standard library package and the one of this module with the same name
// (canonPkg) as the same type if their exported structure and method sets
// match. It returns "" if they are the same, else a description.
func sameType(a, b reflect.Type, seen map[[2]reflect.Type]bool) string {
	if a == b || seen[[2]reflect.Type{a, b}] {
		return ""
	}
	seen[[2]reflect.Type{a, b}] = true
	if a.Name() != "" || b.Name() != "" {
		if a.Name() != b.Name() || canonPkg(a.PkgPath()) != canonPkg(b.PkgPath()) {
			return fmt.Sprintf("type %v vs %v", a, b)
		}
		if a.PkgPath() == b.PkgPath() {
			return fmt.Sprintf("type %v vs %v (same name, different types)", a, b)
		}
		if a.Comparable() != b.Comparable() { // see TestAPIParity
			return fmt.Sprintf("%s.%s comparable %v, %s.%s comparable %v", a.PkgPath(), a.Name(), a.Comparable(), b.PkgPath(), b.Name(), b.Comparable())
		}
		for _, ptr := range []bool{false, true} {
			ta, tb := a, b
			if ptr {
				if a.Kind() == reflect.Interface {
					continue
				}
				ta, tb = reflect.PointerTo(a), reflect.PointerTo(b)
			}
			for i := range ta.NumMethod() {
				mb, ok := tb.MethodByName(ta.Method(i).Name)
				if !ok {
					return fmt.Sprintf("%v lacks method %s", tb, ta.Method(i).Name)
				}
				if d := sameType(ta.Method(i).Type, mb.Type, seen); d != "" {
					return fmt.Sprintf("%v.%s: %s", tb, mb.Name, d)
				}
			}
		}
	}
	if a.Kind() != b.Kind() {
		return fmt.Sprintf("kind %v (%v) vs %v (%v)", a.Kind(), a, b.Kind(), b)
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Slice:
		return sameType(a.Elem(), b.Elem(), seen)
	case reflect.Array:
		if a.Len() != b.Len() {
			return fmt.Sprintf("array length %d vs %d", a.Len(), b.Len())
		}
		return sameType(a.Elem(), b.Elem(), seen)
	case reflect.Chan:
		if a.ChanDir() != b.ChanDir() {
			return fmt.Sprintf("channel direction %v vs %v", a.ChanDir(), b.ChanDir())
		}
		return sameType(a.Elem(), b.Elem(), seen)
	case reflect.Map:
		if d := sameType(a.Key(), b.Key(), seen); d != "" {
			return d
		}
		return sameType(a.Elem(), b.Elem(), seen)
	case reflect.Func:
		if a.NumIn() != b.NumIn() || a.NumOut() != b.NumOut() || a.IsVariadic() != b.IsVariadic() {
			return fmt.Sprintf("signature %v vs %v", a, b)
		}
		for i := range a.NumIn() {
			if d := sameType(a.In(i), b.In(i), seen); d != "" {
				return fmt.Sprintf("parameter %d: %s", i, d)
			}
		}
		for i := range a.NumOut() {
			if d := sameType(a.Out(i), b.Out(i), seen); d != "" {
				return fmt.Sprintf("result %d: %s", i, d)
			}
		}
	case reflect.Struct:
		fa, fb := exportedFields(a), exportedFields(b)
		if len(fa) != len(fb) {
			return fmt.Sprintf("exported fields of %v %v vs %v", a, fieldNames(fa), fieldNames(fb))
		}
		for i := range fa {
			if fa[i].Name != fb[i].Name || fa[i].Anonymous != fb[i].Anonymous || fa[i].Tag != fb[i].Tag {
				return fmt.Sprintf("field %d of %v: %s vs %s", i, a, fa[i].Name, fb[i].Name)
			}
			if d := sameType(fa[i].Type, fb[i].Type, seen); d != "" {
				return fmt.Sprintf("field %s: %s", fa[i].Name, d)
			}
		}
	case reflect.Interface:
		if a.NumMethod() != b.NumMethod() {
			return fmt.Sprintf("interface %v has %d methods, %v has %d", a, a.NumMethod(), b, b.NumMethod())
		}
		for i := range a.NumMethod() {
			ma, mb := a.Method(i), b.Method(i)
			if ma.Name != mb.Name {
				return fmt.Sprintf("interface method %s vs %s", ma.Name, mb.Name)
			}
			if d := sameType(ma.Type, mb.Type, seen); d != "" {
				return fmt.Sprintf("interface method %s: %s", ma.Name, d)
			}
		}
	}
	return ""
}

func exportedFields(t reflect.Type) []reflect.StructField {
	var fs []reflect.StructField
	for i := range t.NumField() {
		if t.Field(i).IsExported() {
			fs = append(fs, t.Field(i))
		}
	}
	return fs
}

func fieldNames(fs []reflect.StructField) []string {
	var s []string
	for _, f := range fs {
		s = append(s, f.Name)
	}
	return s
}

// errInjected is the error of a failing underlying reader or writer.
var errInjected = errors.New("injected I/O error")

// failReader returns data, then errInjected.
type failReader struct{ data []byte }

func (r *failReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errInjected
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// limitWriter accepts n bytes, then returns errInjected.
type limitWriter struct{ n int }

func (w *limitWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		n := w.n
		w.n = 0
		return n, errInjected
	}
	w.n -= len(p)
	return len(p), nil
}

// readAll opens a reader with open and reads it to the end.
func readAll(open func(io.Reader) (io.ReadCloser, error), r io.Reader) error {
	z, err := open(r)
	if err != nil {
		return err
	}
	_, err = io.ReadAll(z)
	return err
}

// errorClasses is what errors.As/errors.Is report for err, with the
// CorruptInputError, InternalError, ReadError and WriteError types of the
// flate package given (ours or the standard library's).
func errorClasses[C, I, R, W any](err error) string {
	var c C
	var i I
	var r R
	var w W
	return fmt.Sprintf("corrupt=%v internal=%v read=%v write=%v injected=%v unexpectedEOF=%v",
		errors.As(err, &c), errors.As(err, &i), errors.As(err, &r), errors.As(err, &w),
		errors.Is(err, errInjected), errors.Is(err, io.ErrUnexpectedEOF))
}

func ourClasses(err error) string {
	return errorClasses[flate.CorruptInputError, flate.InternalError, *flate.ReadError, *flate.WriteError](err)
}

func stdClasses(err error) string {
	return errorClasses[stdflate.CorruptInputError, stdflate.InternalError, *stdflate.ReadError, *stdflate.WriteError](err)
}

// TestAPIParityErrorsAs: errors.As and errors.Is classify the errors of our
// Reader and Writer like the standard library's (each with its own flate
// package's error types): corrupt data, a failing underlying reader (at every
// position, header and trailer included), a truncated stream, and a failing
// underlying writer. The four error types also work with errors.As when
// wrapped.
func TestAPIParityErrorsAs(t *testing.T) {
	data := []byte(strings.Repeat("parity of the error types through the reader. ", 200))
	good := stdEncode(t, 6, data)

	corrupt := append([]byte(nil), good...)
	corrupt[bodyStart] = 0xff // BFINAL 1, BTYPE 3 (reserved): invalid in both
	if a, b := stdClasses(readAll(stdNewReader, &failReader{data: corrupt})), ourClasses(readAll(newReader, &failReader{data: corrupt})); a != b || !strings.Contains(a, "corrupt=true") {
		t.Errorf("corrupt input: standard library %s, ours %s", a, b)
	}
	for n := 0; n < len(good); n++ {
		a := stdClasses(readAll(stdNewReader, &failReader{data: good[:n]}))
		b := ourClasses(readAll(newReader, &failReader{data: good[:n]}))
		if a != b {
			t.Errorf("read error after %d of %d bytes: standard library %s, ours %s", n, len(good), a, b)
		}
		a = stdClasses(readAll(stdNewReader, strings.NewReader(string(good[:n]))))
		b = ourClasses(readAll(newReader, strings.NewReader(string(good[:n]))))
		if a != b {
			t.Errorf("truncated after %d of %d bytes: standard library %s, ours %s", n, len(good), a, b)
		}
	}
	for _, n := range []int{0, 1, 5, 20, 100} {
		write := func(w streamWriter) error {
			_, err := w.Write(data)
			if err == nil {
				err = w.Close()
			}
			return err
		}
		sw, err := stdWriter(&limitWriter{n: n}, -1)
		if err != nil {
			t.Fatal(err)
		}
		ow, err := newWriter(&limitWriter{n: n}, -1)
		if err != nil {
			t.Fatal(err)
		}
		a, b := stdClasses(write(sw)), ourClasses(write(ow))
		if a != b {
			t.Errorf("write error after %d bytes: standard library %s, ours %s", n, a, b)
		}
	}

	inner := errors.New("inner")
	for _, err := range []error{
		flate.CorruptInputError(3), flate.InternalError("x"),
		&flate.ReadError{Offset: 1, Err: inner}, &flate.WriteError{Offset: 2, Err: inner},
	} {
		wrapped := fmt.Errorf("wrapped: %w", err)
		std := stdClasses(stdEquivalent(err))
		if got := ourClasses(wrapped); got != std {
			t.Errorf("%T wrapped: %s, the standard library's %s", err, got, std)
		}
	}
}

// stdEquivalent is the standard library's error of the same type and value,
// wrapped like in TestAPIParityErrorsAs.
func stdEquivalent(err error) error {
	switch e := err.(type) {
	case flate.CorruptInputError:
		err = stdflate.CorruptInputError(e)
	case flate.InternalError:
		err = stdflate.InternalError(e)
	case *flate.ReadError:
		err = &stdflate.ReadError{Offset: e.Offset, Err: e.Err}
	case *flate.WriteError:
		err = &stdflate.WriteError{Offset: e.Offset, Err: e.Err}
	}
	return fmt.Errorf("wrapped: %w", err)
}

// TestAPIParityExtrasDocumented: every apiExtras entry has a reason.
func TestAPIParityExtrasDocumented(t *testing.T) {
	for name, why := range apiExtras {
		if strings.TrimSpace(why) == "" {
			t.Errorf("apiExtras[%q] has no reason", name)
		}
	}
}

// TestAPIParitySharedFileInSync: the three copies of this file differ only
// in the package clause.
func TestAPIParitySharedFileInSync(t *testing.T) {
	norm := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "package ") {
				continue
			}
			lines = append(lines, l)
		}
		return strings.Join(lines, "\n")
	}
	me := norm("apiparity_test.go")
	for _, other := range []string{"flate", "gzip", "zlib"} {
		if norm(filepath.Join("..", other, "apiparity_test.go")) != me {
			t.Errorf("../%s/apiparity_test.go is out of sync with this copy", other)
		}
	}
}
