package eval

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"

	"github.com/elves/elvish/util"
)

// Definitions for Value interfaces, some simple Value types and some common
// Value helpers.

const (
	NoPretty = util.MinInt
)

// Value is an elvish value.
type Value interface {
	Kinder
	Reprer
}

// Kinder is anything with kind string.
type Kinder interface {
	Kind() string
}

// Reprer is anything with a Repr method.
type Reprer interface {
	// Repr returns a string that represents a Value. The string either be a
	// literal of that Value that is preferably deep-equal to it (like `[a b c]`
	// for a list), or a string enclosed in "<>" containing the kind and
	// identity of the Value(like `<fn 0xdeadcafe>`).
	//
	// If indent is at least 0, it should be pretty-printed with the current
	// indentation level of indent; the indent of the first line has already
	// been written and shall not be written in Repr. The returned string
	// should never contain a trailing newline.
	Repr(indent int) string
}

func IncIndent(indent, inc int) int {
	if indent < 0 {
		return indent
	}
	return indent + inc
}

// Booler is anything that can be converted to a bool.
type Booler interface {
	Bool() bool
}

// Stringer is anything that can be converted to a string.
type Stringer interface {
	String() string
}

// Lener is anything with a length.
type Lener interface {
	Len() int
}

// Iterator is anything that can be iterated.
type Iterator interface {
	Iterate(func(Value) bool)
}

type IteratorValue interface {
	Iterator
	Value
}

func collectFromIterator(it Iterator) []Value {
	var vs []Value
	if lener, ok := it.(Lener); ok {
		vs = make([]Value, 0, lener.Len())
	}
	it.Iterate(func(v Value) bool {
		vs = append(vs, v)
		return true
	})
	return vs
}

var NoOpts = map[string]Value{}

// Fn is anything may be called on an evalCtx with a list of Value's.
type Fn interface {
	Call(ec *EvalCtx, args []Value, opts map[string]Value)
}

type FnValue interface {
	Value
	Fn
}

func mustFn(v Value) Fn {
	caller, ok := getFn(v, true)
	if !ok {
		throw(fmt.Errorf("a %s is not callable", v.Kind()))
	}
	return caller
}

// getFn adapts a Value to a Fn if there is an adapter. It adapts an Indexer if
// adaptIndexer is true.
func getFn(v Value, adaptIndexer bool) (Fn, bool) {
	if caller, ok := v.(Fn); ok {
		return caller, true
	}
	if adaptIndexer {
		if indexer, ok := getIndexer(v, nil); ok {
			return IndexerAsFn{indexer}, true
		}
	}
	return nil, false
}

// IndexerAsFn adapts an Indexer to a Fn.
type IndexerAsFn struct {
	Indexer
}

var ErrIndexerOpts = errors.New("option not accepted")

func (ic IndexerAsFn) Call(ec *EvalCtx, args []Value, opts map[string]Value) {
	if len(opts) > 0 {
		throw(ErrIndexerOpts)
	}
	results := ic.Indexer.Index(args)
	for _, v := range results {
		ec.ports[1].Chan <- v
	}
}

// Indexer is anything that can be indexed by Values and yields Values.
type Indexer interface {
	Index(idx []Value) []Value
}

// IndexOneer is anything that can be indexed by one Value and yields one Value.
type IndexOneer interface {
	IndexOne(idx Value) Value
}

// IndexSetter is a Value whose elements can be get as well as set.
type IndexSetter interface {
	IndexOneer
	IndexSet(idx Value, v Value)
}

func mustIndexer(v Value, ec *EvalCtx) Indexer {
	indexer, ok := getIndexer(v, ec)
	if !ok {
		throw(fmt.Errorf("a %s is not indexable", v.Kind()))
	}
	return indexer
}

// getIndexer adapts a Value to an Indexer if there is an adapter. It adapts a
// Fn if ec is not nil.
func getIndexer(v Value, ec *EvalCtx) (Indexer, bool) {
	if indexer, ok := v.(Indexer); ok {
		return indexer, true
	}
	if indexOneer, ok := v.(IndexOneer); ok {
		return IndexOneerIndexer{indexOneer}, true
	}
	if ec != nil {
		if caller, ok := v.(Fn); ok {
			return FnAsIndexer{caller, ec}, true
		}
	}
	return nil, false
}

// IndexOneerIndexer adapts an IndexOneer to an Indexer by calling all the
// indicies on the IndexOner and collect the results.
type IndexOneerIndexer struct {
	IndexOneer
}

func (ioi IndexOneerIndexer) Index(vs []Value) []Value {
	results := make([]Value, len(vs))
	for i, v := range vs {
		results[i] = ioi.IndexOneer.IndexOne(v)
	}
	return results
}

// FnAsIndexer adapts a Fn to an Indexer.
type FnAsIndexer struct {
	Fn
	ec *EvalCtx
}

func (ci FnAsIndexer) Index(idx []Value) []Value {
	// XXX We don't have location information.
	return captureOutput(ci.ec, Op{func(ec *EvalCtx) {
		ci.Fn.Call(ec, idx, NoOpts)
	}, -1, -1})
}

// Error definitions.
var (
	ErrOnlyStrOrRat = errors.New("only str or rat may be converted to rat")
)

// Bool represents truthness.
type Bool bool

func (Bool) Kind() string {
	return "bool"
}

func (b Bool) Repr(int) string {
	if b {
		return "$true"
	}
	return "$false"
}

func (b Bool) Bool() bool {
	return bool(b)
}

// ToBool converts a Value to bool. When the Value type implements Bool(), it
// is used. Otherwise it is considered true.
func ToBool(v Value) bool {
	if b, ok := v.(Booler); ok {
		return b.Bool()
	}
	return true
}

// Rat is a rational number.
type Rat struct {
	b *big.Rat
}

func (Rat) Kind() string {
	return "string"
}

func (r Rat) Repr(int) string {
	return "(rat " + r.String() + ")"
}

func (r Rat) String() string {
	if r.b.IsInt() {
		return r.b.Num().String()
	}
	return r.b.String()
}

// ToRat converts a Value to rat. A str can be converted to a rat if it can be
// parsed. A rat is returned as-is. Other types of values cannot be converted.
func ToRat(v Value) (Rat, error) {
	switch v := v.(type) {
	case Rat:
		return v, nil
	case String:
		r := big.Rat{}
		_, err := fmt.Sscanln(string(v), &r)
		if err != nil {
			return Rat{}, fmt.Errorf("%s cannot be parsed as rat", v.Repr(NoPretty))
		}
		return Rat{&r}, nil
	default:
		return Rat{}, ErrOnlyStrOrRat
	}
}

// FromJSONInterface converts a interface{} that results from json.Unmarshal to
// a Value.
func FromJSONInterface(v interface{}) Value {
	if v == nil {
		// TODO Use a more appropriate type
		return String("")
	}
	switch v.(type) {
	case bool:
		return Bool(v.(bool))
	case float64, string:
		// TODO Use a numeric type for float64
		return String(fmt.Sprint(v))
	case []interface{}:
		a := v.([]interface{})
		vs := make([]Value, len(a))
		for i, v := range a {
			vs[i] = FromJSONInterface(v)
		}
		return List{&vs}
	case map[string]interface{}:
		m := v.(map[string]interface{})
		m_ := make(map[Value]Value)
		for k, v := range m {
			m_[String(k)] = FromJSONInterface(v)
		}
		return Map{&m_}
	default:
		throw(fmt.Errorf("unexpected json type: %T", v))
		return nil // not reached
	}
}

// DeepEq compares two Value's deeply.
func DeepEq(a, b Value) bool {
	return reflect.DeepEqual(a, b)
}
