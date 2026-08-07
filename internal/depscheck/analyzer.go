// Package depscheck implements a go/analysis analyzer that checks whether a
// Caerus component's Init-time peer lookups are declared in GetDependencies.
//
// The framework's convention (AGENTS.md) is that every peer resolved with
// cf.Get / cf.MustGet / cf.GetByName / cf.MustGetByName during Init must be
// listed by name in the component's GetDependencies, so the framework
// initializes the peer before the component. Runtime Validate only catches
// unknown names, cycles and forward-stage edges on the assembled graph; it
// cannot see that Init looks up a peer the component forgot to declare. This
// analyzer is the static, build-time complement.
//
// Analysis is deliberately conservative: names that cannot be resolved to a
// string constant (dynamic GetByName names, types without a ComponentName
// const, GetDependencies built from runtime values) are skipped rather than
// reported, preferring false negatives over false positives. Runtime Validate
// remains authoritative for the live registry.
package depscheck

import (
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strconv"

	"golang.org/x/tools/go/analysis"
)

// frameworkImportPath is the module path of the caerus-framework core. The
// analyzer only treats function calls from this package (cf.Get, ...) as peer
// lookups, so a locally shadowed or unrelated Get is ignored.
const frameworkImportPath = "github.com/caerus-framework/caerus-framework"

// maxHelperDepth bounds how far the same-package helper walk follows calls
// made from Init. Lookups nested deeper are not checked (false-negative-first).
const maxHelperDepth = 4

// Analyzer reports component Init peer lookups that are missing from
// GetDependencies. With the -stale-dep flag it also warns about a
// GetDependencies literal that no code in the package ever references.
var Analyzer = &analysis.Analyzer{
	Name:  "depscheck",
	Doc:   "check that Init peer lookups appear in GetDependencies",
	Flags: *flag.NewFlagSet("depscheck", flag.ExitOnError),
	Run:   run,
}

func init() {
	Analyzer.Flags.Bool("stale-dep", false, "report GetDependencies literals that no code in the package references")
}

// depsResult is the result of statically evaluating a GetDependencies body.
type depsResult struct {
	names    []string        // resolved peer names, in declaration order
	literals map[string]bool // subset of names that came from plain string literals
	complete bool            // true when every element resolved to a constant
}

// lookup is a single Init-time peer lookup statically resolved from the code.
type lookup struct {
	pos      token.Pos
	fn       string // Get, MustGet, GetByName or MustGetByName
	name     string // resolved peer name
	resolved bool   // name resolved to a string constant
	typeName string // resolved Go type argument name (e.g. CFPostgres)
	typePkg  string // package name of the type argument (e.g. cf_postgres)
}

// pkgInfo is the per-package analysis state.
type pkgInfo struct {
	pass         *analysis.Pass
	funcsByObj   map[*types.Func]*ast.FuncDecl
	literals     map[string]int
	literalSwept bool
}

func run(pass *analysis.Pass) (any, error) {
	p := &pkgInfo{
		pass:       pass,
		funcsByObj: map[*types.Func]*ast.FuncDecl{},
	}
	checkStaleDeps := false
	if f := pass.Analyzer.Flags.Lookup("stale-dep"); f != nil {
		checkStaleDeps = f.Value.String() == "true"
	}
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if obj, ok := pass.TypesInfo.Defs[fd.Name].(*types.Func); ok {
				p.funcsByObj[obj] = fd
			}
		}
	}

	for _, comp := range p.components() {
		p.checkComponent(comp, checkStaleDeps)
	}
	return nil, nil
}

// components returns every named type in the package with an
// Init(context.Context, *cf.CaerusFramework) method — the marker of a Caerus
// component.
func (p *pkgInfo) components() []*componentInfo {
	var out []*componentInfo
	for _, name := range p.pass.Pkg.Scope().Names() {
		tobj, ok := p.pass.Pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := tobj.Type().(*types.Named)
		if !ok {
			continue
		}
		initFn := p.initMethod(named)
		if initFn == nil {
			continue
		}
		out = append(out, p.newComponent(named, initFn))
	}
	return out
}

// initMethod returns the Init method of named when it has the component
// signature, or nil otherwise.
func (p *pkgInfo) initMethod(named *types.Named) *types.Func {
	ms := types.NewMethodSet(types.NewPointer(named))
	sel := ms.Lookup(nil, "Init")
	if sel == nil {
		return nil
	}
	fn, ok := sel.Obj().(*types.Func)
	if !ok {
		return nil
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return nil
	}
	params := sig.Params()
	if params.Len() != 2 || !isContextType(params.At(0).Type()) || !isFrameworkType(params.At(1).Type()) {
		return nil
	}
	return fn
}

// isContextType reports whether t is context.Context from the stdlib.
func isContextType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	o := named.Obj()
	return o != nil && o.Pkg() != nil && o.Pkg().Path() == "context" && o.Name() == "Context"
}

// isFrameworkType reports whether t is *cf.CaerusFramework.
func isFrameworkType(t types.Type) bool {
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return false
	}
	o := named.Obj()
	return o != nil && o.Pkg() != nil && o.Pkg().Path() == frameworkImportPath && o.Name() == "CaerusFramework"
}

// componentInfo bundles the analysis state for one component type.
type componentInfo struct {
	info     *pkgInfo
	typeName string // Go type name, e.g. "API"
	initDecl *ast.FuncDecl
	depsDecl *ast.FuncDecl // GetDependencies, nil when not implemented
	deps     depsResult
	lookups  []lookup
}

func (p *pkgInfo) newComponent(named *types.Named, initFn *types.Func) *componentInfo {
	c := &componentInfo{
		info:     p,
		typeName: named.Obj().Name(),
		initDecl: p.funcsByObj[initFn],
	}
	if d := p.dependenciesMethod(named); d != nil {
		c.depsDecl = p.funcsByObj[d]
		if c.depsDecl != nil {
			c.deps = p.resolveDependencies(c.depsDecl)
		}
	}
	if c.initDecl != nil {
		c.lookups = p.collectLookups(initFn)
	}
	return c
}

func (p *pkgInfo) dependenciesMethod(named *types.Named) *types.Func {
	ms := types.NewMethodSet(types.NewPointer(named))
	sel := ms.Lookup(nil, "GetDependencies")
	if sel == nil {
		return nil
	}
	fn, ok := sel.Obj().(*types.Func)
	if !ok {
		return nil
	}
	return fn
}

// resolveDependencies evaluates a GetDependencies body into the set of declared
// peer names. Literals and string-constant selectors (e.g. cf_logs.ComponentName)
// resolve; anything built from runtime values marks the result incomplete so
// the caller skips missing-dep reports (false-negative-first).
func (p *pkgInfo) resolveDependencies(fd *ast.FuncDecl) depsResult {
	res := depsResult{complete: true, literals: map[string]bool{}}
	seen := map[string]bool{}
	add := func(name string, ok bool) {
		if !ok {
			res.complete = false
			return
		}
		if !seen[name] {
			seen[name] = true
			res.names = append(res.names, name)
		}
	}
	for _, r := range returns(fd.Body) {
		for _, result := range r.Results {
			p.collectSliceExpr(result, fd.Body, res.literals, add)
		}
	}
	return res
}

// returns collects every return statement reachable by a shallow walk of body.
func returns(body *ast.BlockStmt) []*ast.ReturnStmt {
	var out []*ast.ReturnStmt
	ast.Inspect(body, func(n ast.Node) bool {
		if r, ok := n.(*ast.ReturnStmt); ok {
			out = append(out, r)
		}
		return true
	})
	return out
}

// collectSliceExpr feeds every element of a []string expression to add. The
// expression may be a composite literal, an append chain, a local variable
// accumulated with append, or nil. Elements resolved from plain string
// literals are recorded in literals for the stale-dep check.
func (p *pkgInfo) collectSliceExpr(expr ast.Expr, body *ast.BlockStmt, literals map[string]bool, add func(string, bool)) {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		for _, el := range e.Elts {
			val := el
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				val = kv.Value
			}
			if name, ok := p.resolveStringExpr(val); ok {
				if isStringLit(val) {
					literals[name] = true
				}
				add(name, true)
			} else {
				add("", false)
			}
		}
	case *ast.CallExpr:
		if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "append" && len(e.Args) > 0 {
			if lit, ok := e.Args[0].(*ast.CompositeLit); ok {
				p.collectSliceExpr(lit, body, literals, add)
			}
			for _, arg := range e.Args[1:] {
				if lit, ok := arg.(*ast.CompositeLit); ok {
					p.collectSliceExpr(lit, body, literals, add)
					continue
				}
				if name, ok := p.resolveStringExpr(arg); ok {
					if isStringLit(arg) {
						literals[name] = true
					}
					add(name, true)
				} else {
					add("", false)
				}
			}
			return
		}
		add("", false)
	case *ast.Ident:
		if e.Name == "nil" {
			return // explicit "no dependencies" — complete
		}
		p.collectVarSlice(e, body, literals, add)
	default:
		add("", false)
	}
}

// collectVarSlice traces a local []string variable: its base assignment plus
// every append accumulation in body, feeding the results to add.
func (p *pkgInfo) collectVarSlice(id *ast.Ident, body *ast.BlockStmt, literals map[string]bool, add func(string, bool)) {
	obj := p.objectOfIdent(id)
	v, ok := obj.(*types.Var)
	if !ok || v.IsField() || v.Parent() == nil {
		add("", false)
		return
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			li, ok := lhs.(*ast.Ident)
			if !ok || li.Name != id.Name || p.objectOfIdent(li) != obj {
				continue
			}
			found = true
			if i < len(as.Rhs) {
				p.collectSliceExpr(as.Rhs[i], body, literals, add)
			}
			break
		}
		return true
	})
	if !found {
		add("", false)
	}
}

// objectOfIdent returns the object referenced by an identifier (a definition
// or a use, whichever applies at the identifier's position).
func (p *pkgInfo) objectOfIdent(id *ast.Ident) types.Object {
	if obj := p.pass.TypesInfo.Defs[id]; obj != nil {
		return obj
	}
	return p.pass.TypesInfo.Uses[id]
}

// resolveStringExpr resolves an expression to a string constant value. It
// handles string literals, const identifiers and const selectors (e.g.
// cf_postgres.ComponentName) via go/types constant evaluation.
func (p *pkgInfo) resolveStringExpr(expr ast.Expr) (string, bool) {
	tv, ok := p.pass.TypesInfo.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// isStringLit reports whether expr is a plain string literal (not a const
// reference) — the stale-dep check only applies to these.
func isStringLit(expr ast.Expr) bool {
	_, ok := expr.(*ast.BasicLit)
	return ok
}

// collectLookups walks Init and every same-package helper it calls (bounded by
// maxHelperDepth) collecting cf.Get / cf.MustGet / cf.GetByName /
// cf.MustGetByName peer lookups.
func (p *pkgInfo) collectLookups(initFn *types.Func) []lookup {
	var out []lookup
	visited := map[*types.Func]bool{}
	p.walkFunc(initFn, 0, visited, &out)
	return out
}

func (p *pkgInfo) walkFunc(fn *types.Func, depth int, visited map[*types.Func]bool, out *[]lookup) {
	if depth > maxHelperDepth || visited[fn] {
		return
	}
	visited[fn] = true
	fd := p.funcsByObj[fn]
	if fd == nil || fd.Body == nil {
		return
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if lu := p.lookupFromCall(call); lu != nil {
			*out = append(*out, *lu)
			return false // the call itself resolved; do not descend into it
		}
		if callee := p.samePkgCallee(call); callee != nil {
			p.walkFunc(callee, depth+1, visited, out)
		}
		return true
	})
}

// samePkgCallee returns the *types.Func of a call when it is a function or
// method declared in the current package, or nil otherwise.
func (p *pkgInfo) samePkgCallee(call *ast.CallExpr) *types.Func {
	var obj types.Object
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		obj = p.pass.TypesInfo.Uses[fun]
	case *ast.SelectorExpr:
		obj = p.pass.TypesInfo.Uses[fun.Sel]
	case *ast.IndexExpr: // generic instantiation: F[X](...)
		switch base := fun.X.(type) {
		case *ast.SelectorExpr:
			obj = p.pass.TypesInfo.Uses[base.Sel]
		case *ast.Ident:
			obj = p.pass.TypesInfo.Uses[base]
		}
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg() != p.pass.Pkg {
		return nil
	}
	if _, ok := p.funcsByObj[fn]; !ok {
		return nil
	}
	return fn
}

// lookupFromCall resolves a call to a framework peer lookup, or nil when the
// call is not one (or its name cannot be statically resolved).
func (p *pkgInfo) lookupFromCall(call *ast.CallExpr) *lookup {
	var obj types.Object
	switch fun := call.Fun.(type) {
	case *ast.IndexExpr: // cf.Get[*T](...)
		sel, ok := fun.X.(*ast.SelectorExpr)
		if !ok {
			return nil
		}
		obj = p.pass.TypesInfo.Uses[sel.Sel]
	case *ast.SelectorExpr: // cf.GetByName[*T](...) with inferred args
		obj = p.pass.TypesInfo.Uses[fun.Sel]
	default:
		return nil
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != frameworkImportPath {
		return nil
	}
	switch fn.Name() {
	case "Get", "MustGet":
		return p.getLookup(call, fn.Name())
	case "GetByName", "MustGetByName":
		return p.getByNameLookup(call, fn.Name())
	default:
		return nil
	}
}

// getLookup resolves cf.Get[*U](fw) / cf.MustGet[*U](fw). The peer name comes
// from the ComponentName const in U's package; without it the lookup is
// skipped (unresolvable, multi-instance types should use GetByName).
func (p *pkgInfo) getLookup(call *ast.CallExpr, fnName string) *lookup {
	idx, ok := call.Fun.(*ast.IndexExpr)
	if !ok {
		return nil
	}
	lu := &lookup{pos: call.Pos(), fn: fnName}
	t := p.pass.TypesInfo.TypeOf(idx.Index)
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	lu.typeName = named.Obj().Name()
	if pkg := named.Obj().Pkg(); pkg != nil {
		lu.typePkg = pkg.Name()
	}
	if name, ok := p.componentNameFor(named); ok {
		lu.name, lu.resolved = name, true
	}
	return lu
}

// getByNameLookup resolves cf.GetByName[*U](fw, "name") /
// cf.MustGetByName[*U](fw, "name"). Only constant name arguments resolve; the
// type argument is best-effort.
func (p *pkgInfo) getByNameLookup(call *ast.CallExpr, fnName string) *lookup {
	if len(call.Args) < 2 {
		return nil
	}
	name, ok := p.resolveStringExpr(call.Args[1])
	if !ok {
		return nil // dynamic name — cannot resolve, skip
	}
	lu := &lookup{pos: call.Pos(), fn: fnName, name: name, resolved: true}
	if idx, ok := call.Fun.(*ast.IndexExpr); ok {
		if t := p.pass.TypesInfo.TypeOf(idx.Index); t != nil {
			if ptr, ok := t.(*types.Pointer); ok {
				t = ptr.Elem()
			}
			if named, ok := t.(*types.Named); ok {
				lu.typeName = named.Obj().Name()
				if pkg := named.Obj().Pkg(); pkg != nil {
					lu.typePkg = pkg.Name()
				}
			}
		}
	}
	return lu
}

// componentNameFor resolves U's package ComponentName const, the name the
// component registers under by default.
func (p *pkgInfo) componentNameFor(named *types.Named) (string, bool) {
	pkg := named.Obj().Pkg()
	if pkg == nil {
		return "", false
	}
	obj := pkg.Scope().Lookup("ComponentName")
	cn, ok := obj.(*types.Const)
	if !ok || cn.Val() == nil || cn.Val().Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(cn.Val()), true
}

// checkComponent emits the diagnostics for one component type. checkStaleDeps
// gates the stale-dep hygiene warning (off by default: a literal dep name may
// legitimately be consumed from another package, e.g. main, which a
// single-package analysis cannot see).
func (p *pkgInfo) checkComponent(c *componentInfo, checkStaleDeps bool) {
	if len(c.lookups) > 0 && c.depsDecl == nil {
		// Hygiene: Init resolves peers but the component never declares them.
		p.pass.Report(analysis.Diagnostic{
			Pos:     c.initDecl.Pos(),
			Message: fmt.Sprintf("%s has Init peer lookups (cf.Get/cf.GetByName) but does not implement Dependencies", c.typeName),
		})
		return
	}
	if c.depsDecl == nil {
		return
	}

	declared := map[string]bool{}
	for _, n := range c.deps.names {
		declared[n] = true
	}

	for _, lu := range c.lookups {
		if !lu.resolved {
			continue
		}
		if declared[lu.name] {
			continue
		}
		if !c.deps.complete {
			continue // GetDependencies has dynamic elements; do not guess
		}
		p.pass.Report(analysis.Diagnostic{
			Pos:     lu.pos,
			Message: fmt.Sprintf("Init looks up %q (%s) but GetDependencies omits it", lu.name, p.lookupDesc(lu)),
		})
	}

	// Hygiene: a literal dep that nothing in the package ever references is
	// stale (const selectors are intentionally not checked — config sources
	// and other wiring reference them without Init lookups). Opt-in via
	// -stale-dep: the literal may legitimately be consumed from another
	// package (e.g. main), which a single-package analysis cannot see.
	if checkStaleDeps && c.deps.complete {
		for _, n := range c.deps.names {
			if !c.deps.literals[n] {
				continue
			}
			if p.literalCount(n) > literalCountInside(c.depsDecl, n) {
				continue // referenced elsewhere in the package
			}
			p.pass.Report(analysis.Diagnostic{
				Pos:     c.depsDecl.Pos(),
				Message: fmt.Sprintf("GetDependencies declares %q but no code in the package references it", n),
			})
		}
	}
}

// lookupDesc renders a lookup for diagnostics, e.g. cf.Get[*cf_postgres.CFPostgres].
func (p *pkgInfo) lookupDesc(lu lookup) string {
	desc := "cf." + lu.fn
	if lu.typeName != "" {
		typ := lu.typeName
		if lu.typePkg != "" {
			typ = lu.typePkg + "." + typ
		}
		desc += "[*" + typ + "]"
	}
	return desc
}

// literalCount returns how many times name appears as a string literal in the
// package.
func (p *pkgInfo) literalCount(name string) int {
	if !p.literalSwept {
		p.literals = map[string]int{}
		for _, file := range p.pass.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if v, err := strconv.Unquote(bl.Value); err == nil {
						p.literals[v]++
					}
				}
				return true
			})
		}
		p.literalSwept = true
	}
	return p.literals[name]
}

// literalCountInside returns how many times name appears as a string literal
// inside the given function (i.e. within GetDependencies itself).
func literalCountInside(fd *ast.FuncDecl, name string) int {
	count := 0
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			if v, err := strconv.Unquote(bl.Value); err == nil && v == name {
				count++
			}
		}
		return true
	})
	return count
}
