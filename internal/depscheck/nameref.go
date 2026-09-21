package depscheck

import (
	"go/ast"
	"go/token"
	"go/types"
)

// nameKind is how a peer name was written in source: a string constant, a
// component field (WithName alias), a zero-arg method on the component
// (peerName), or a union of those from a local variable with several
// assignments.
type nameKind int

const (
	nameUnknown nameKind = iota
	nameConst
	nameField
	nameMethod
	nameUnion
)

// nameRef is a statically matchable peer-name expression. Two refs cover
// each other when they name the same constant, the same field, the same
// method, or when a method/union expands to alternatives the other side
// already declared.
type nameRef struct {
	kind   nameKind
	value  string    // nameConst
	ident  string    // nameField / nameMethod
	alts   []nameRef // nameMethod expansion / nameUnion
	expand bool      // true when alts is a complete expansion
}

func (r nameRef) ok() bool { return r.kind != nameUnknown }

func (r nameRef) label() string {
	switch r.kind {
	case nameConst:
		return `"` + r.value + `"`
	case nameField:
		return "field " + r.ident
	case nameMethod:
		return r.ident + "()"
	case nameUnion:
		return "aliased name"
	default:
		return "dynamic name"
	}
}

// resolveNameRef maps an expression to a peer-name slot. String constants
// (literals and ComponentName selectors) resolve; c.field and c.peerName()
// resolve when they are string fields / zero-arg string methods on recv.
// Local variables are traced through assignments in body. Anything else
// (os.Getenv, maps, parameters) is unresolvable — false-negative-first.
func (p *pkgInfo) resolveNameRef(expr ast.Expr, recv *types.Named, body *ast.BlockStmt, depth int) (nameRef, bool) {
	if depth > maxHelperDepth || expr == nil {
		return nameRef{}, false
	}
	expr = ast.Unparen(expr)
	if s, ok := p.resolveStringExpr(expr); ok {
		return nameRef{kind: nameConst, value: s}, true
	}
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if recv != nil && p.selectorOnRecv(e, recv) && p.isStringField(recv, e.Sel.Name) {
			return nameRef{kind: nameField, ident: e.Sel.Name}, true
		}
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if ok && len(e.Args) == 0 && recv != nil && p.selectorOnRecv(sel, recv) {
			return p.resolveRecvMethod(recv, sel.Sel.Name, depth+1)
		}
	case *ast.Ident:
		return p.resolveAssignedIdent(e, recv, body, depth)
	}
	return nameRef{}, false
}

func (p *pkgInfo) selectorOnRecv(sel *ast.SelectorExpr, recv *types.Named) bool {
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	obj := p.pass.TypesInfo.Uses[id]
	v, ok := obj.(*types.Var)
	if !ok {
		return false
	}
	t := v.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	return ok && named == recv
}

func (p *pkgInfo) isStringField(named *types.Named, name string) bool {
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if f.Name() == name && isStringType(f.Type()) {
			return true
		}
	}
	return false
}

func isStringType(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Kind() == types.String
}

func (p *pkgInfo) methodByName(named *types.Named, name string) *types.Func {
	ms := types.NewMethodSet(types.NewPointer(named))
	pkg := named.Obj().Pkg()
	sel := ms.Lookup(pkg, name)
	if sel == nil {
		return nil
	}
	fn, _ := sel.Obj().(*types.Func)
	return fn
}

func (p *pkgInfo) resolveRecvMethod(recv *types.Named, name string, depth int) (nameRef, bool) {
	fn := p.methodByName(recv, name)
	if fn == nil {
		return nameRef{}, false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Params().Len() != 0 || sig.Results().Len() != 1 || !isStringType(sig.Results().At(0).Type()) {
		return nameRef{}, false
	}
	ref := nameRef{kind: nameMethod, ident: name}
	fd := p.funcsByObj[fn]
	if fd == nil || fd.Body == nil {
		return ref, true
	}
	alts, complete := p.methodReturnAlts(fd, recv, depth)
	ref.alts = alts
	ref.expand = complete
	return ref, true
}

func (p *pkgInfo) methodReturnAlts(fd *ast.FuncDecl, recv *types.Named, depth int) ([]nameRef, bool) {
	complete := true
	var alts []nameRef
	seen := map[string]bool{}
	for _, r := range returns(fd.Body) {
		if len(r.Results) != 1 {
			complete = false
			continue
		}
		ref, ok := p.resolveNameRef(r.Results[0], recv, fd.Body, depth)
		if !ok {
			complete = false
			continue
		}
		k := ref.key()
		if seen[k] {
			continue
		}
		seen[k] = true
		alts = append(alts, ref)
	}
	if len(alts) == 0 {
		return nil, false
	}
	return alts, complete
}

func (r nameRef) key() string {
	switch r.kind {
	case nameConst:
		return "c:" + r.value
	case nameField:
		return "f:" + r.ident
	case nameMethod:
		return "m:" + r.ident
	case nameUnion:
		return "u:" + r.ident
	default:
		return "?"
	}
}

// resolveAssignedIdent traces a local string variable through assignments in
// body and returns their union (peer := ComponentName; peer = c.valkeyName).
func (p *pkgInfo) resolveAssignedIdent(id *ast.Ident, recv *types.Named, body *ast.BlockStmt, depth int) (nameRef, bool) {
	if body == nil {
		return nameRef{}, false
	}
	obj := p.objectOfIdent(id)
	v, ok := obj.(*types.Var)
	if !ok || v.IsField() || v.Parent() == nil {
		return nameRef{}, false
	}
	if !isStringType(v.Type()) {
		return nameRef{}, false
	}
	complete := true
	var alts []nameRef
	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			li, ok := lhs.(*ast.Ident)
			if !ok || p.objectOfIdent(li) != obj || i >= len(as.Rhs) {
				continue
			}
			ref, ok := p.resolveNameRef(as.Rhs[i], recv, body, depth+1)
			if !ok {
				complete = false
				continue
			}
			k := ref.key()
			if seen[k] {
				continue
			}
			seen[k] = true
			alts = append(alts, ref)
		}
		return true
	})
	switch len(alts) {
	case 0:
		return nameRef{}, false
	case 1:
		return alts[0], complete
	default:
		return nameRef{kind: nameUnion, alts: alts, expand: complete}, complete
	}
}

// refBag is the flattened set of peer-name slots declared in GetDependencies.
type refBag struct {
	consts  map[string]bool
	fields  map[string]bool
	methods map[string]bool
}

func flattenBag(refs []nameRef) refBag {
	b := refBag{
		consts:  map[string]bool{},
		fields:  map[string]bool{},
		methods: map[string]bool{},
	}
	var add func(nameRef)
	add = func(r nameRef) {
		switch r.kind {
		case nameConst:
			b.consts[r.value] = true
		case nameField:
			b.fields[r.ident] = true
		case nameMethod:
			b.methods[r.ident] = true
			if r.expand {
				for _, a := range r.alts {
					add(a)
				}
			}
		case nameUnion:
			if r.expand {
				for _, a := range r.alts {
					add(a)
				}
			}
		}
	}
	for _, r := range refs {
		add(r)
	}
	return b
}

// covers reports whether GetDependencies already declares the lookup slot.
// A method lookup is covered when the same method is listed, or when every
// alternative it can return (field or const) is listed. A const lookup is
// covered only by that const (or a method/union that expands to it).
func (b refBag) covers(lu nameRef) bool {
	switch lu.kind {
	case nameConst:
		return b.consts[lu.value]
	case nameField:
		return b.fields[lu.ident]
	case nameMethod:
		if b.methods[lu.ident] {
			return true
		}
		if !lu.expand || len(lu.alts) == 0 {
			return false
		}
		for _, a := range lu.alts {
			if !b.covers(a) {
				return false
			}
		}
		return true
	case nameUnion:
		if !lu.expand || len(lu.alts) == 0 {
			return false
		}
		for _, a := range lu.alts {
			if !b.covers(a) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (p *pkgInfo) isStringSlice(e ast.Expr) bool {
	t := p.pass.TypesInfo.TypeOf(e)
	sl, ok := t.(*types.Slice)
	if !ok {
		return false
	}
	return isStringType(sl.Elem())
}

func spreadLast(call *ast.CallExpr) bool {
	return call.Ellipsis != token.NoPos
}
