package toml

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/naoina/toml/ast"
)

//go:generate peg -switch -inline parse.peg

var errParse = errors.New("invalid TOML syntax")

func Parse(data []byte) (*ast.Table, error) {
	d := &parseState{p: &tomlParser{Buffer: string(data)}}
	d.init()

	if err := d.parse(); err != nil {
		return nil, err
	}

	return d.p.toml.table, nil
}

type parseState struct {
	p *tomlParser
}

func (d *parseState) init() {
	d.p.Init()
	d.p.toml.init(d.p.buffer)
}

func (d *parseState) parse() error {
	if err := d.p.Parse(); err != nil {
		if err, ok := err.(*parseError); ok {
			return lineError(err.Line(), errParse)
		}
		return err
	}
	return d.execute()
}

func (d *parseState) execute() (err error) {
	defer func() {
		if e := recover(); e != nil {
			lerr, ok := e.(*LineError)
			if !ok {
				panic(e)
			}
			err = lerr
		}
	}()
	d.p.Execute()
	return nil
}

func (e *parseError) Line() int {
	tokens := []token32{e.max}
	positions, p := make([]int, 2*len(tokens)), 0
	for _, token := range tokens {
		positions[p], p = int(token.begin), p+1
		positions[p], p = int(token.end), p+1
	}
	for _, t := range translatePositions(e.p.buffer, positions) {
		if e.p.line < t.line {
			e.p.line = t.line
		}
	}
	return e.p.line
}

type stack struct {
	key   string
	table *ast.Table
}

type array struct {
	parent  *array
	child   *array
	current *ast.Array
	line    int
}

type toml struct {
	table        *ast.Table
	line         int
	currentTable *ast.Table
	s            string
	key          string
	val          ast.Value
	arr          *array
	stack        []*stack
	skip         bool
}

func (p *toml) init(data []rune) {
	p.line = 1
	p.table = p.newTable(ast.TableTypeNormal, "")
	p.table.Position.End = len(data) - 1
	p.table.Data = data[:len(data)-1] 
	p.currentTable = p.table
}

func (p *toml) Error(err error) {
	panic(lineError(p.line, err))
}

func (p *tomlParser) SetTime(begin, end int) {
	p.val = &ast.Datetime{
		Position: ast.Position{Begin: begin, End: end},
		Data:     p.buffer[begin:end],
		Value:    string(p.buffer[begin:end]),
	}
}

func (p *tomlParser) SetFloat64(begin, end int) {
	p.val = &ast.Float{
		Position: ast.Position{Begin: begin, End: end},
		Data:     p.buffer[begin:end],
		Value:    underscoreReplacer.Replace(string(p.buffer[begin:end])),
	}
}

func (p *tomlParser) SetInt64(begin, end int) {
	p.val = &ast.Integer{
		Position: ast.Position{Begin: begin, End: end},
		Data:     p.buffer[begin:end],
		Value:    underscoreReplacer.Replace(string(p.buffer[begin:end])),
	}
}

func (p *tomlParser) SetString(begin, end int) {
	p.val = &ast.String{
		Position: ast.Position{Begin: begin, End: end},
		Data:     p.buffer[begin:end],
		Value:    p.s,
	}
	p.s = ""
}

func (p *tomlParser) SetBool(begin, end int) {
	p.val = &ast.Boolean{
		Position: ast.Position{Begin: begin, End: end},
		Data:     p.buffer[begin:end],
		Value:    string(p.buffer[begin:end]),
	}
}

func (p *tomlParser) StartArray() {
	if p.arr == nil {
		p.arr = &array{line: p.line, current: &ast.Array{}}
		return
	}
	p.arr.child = &array{parent: p.arr, line: p.line, current: &ast.Array{}}
	p.arr = p.arr.child
}

func (p *tomlParser) AddArrayVal() {
	if p.arr.current == nil {
		p.arr.current = &ast.Array{}
	}
	p.arr.current.Value = append(p.arr.current.Value, p.val)
}

func (p *tomlParser) SetArray(begin, end int) {
	p.arr.current.Position = ast.Position{Begin: begin, End: end}
	p.arr.current.Data = p.buffer[begin:end]
	p.val = p.arr.current
	p.arr = p.arr.parent
}

func (p *toml) SetTable(buf []rune, begin, end int) {
	p.setTable(p.table, buf, begin, end)
}

func (p *toml) setTable(parent *ast.Table, buf []rune, begin, end int) {
	name := string(buf[begin:end])
	names := splitTableKey(name)
	parent, err := p.lookupTable(parent, names[:len(names)-1])
	if err != nil {
		p.Error(err)
	}
	last := names[len(names)-1]
	tbl := p.newTable(ast.TableTypeNormal, last)
	switch v := parent.Fields[last].(type) {
	case nil:
		parent.Fields[last] = tbl
	case []*ast.Table:
		p.Error(fmt.Errorf("table `%s' is in conflict with array table in line %d", name, v[0].Line))
	case *ast.Table:
		if (v.Position == ast.Position{}) {

			tbl.Fields = v.Fields
			parent.Fields[last] = tbl
		} else {
			p.Error(fmt.Errorf("table `%s' is in conflict with table in line %d", name, v.Line))
		}
	case *ast.KeyValue:
		p.Error(fmt.Errorf("table `%s' is in conflict with line %d", name, v.Line))
	default:
		p.Error(fmt.Errorf("BUG: table `%s' is in conflict but it's unknown type `%T'", last, v))
	}
	p.currentTable = tbl
}

func (p *toml) newTable(typ ast.TableType, name string) *ast.Table {
	return &ast.Table{
		Line:   p.line,
		Name:   name,
		Type:   typ,
		Fields: make(map[string]interface{}),
	}
}

func (p *tomlParser) SetTableString(begin, end int) {
	p.currentTable.Data = p.buffer[begin:end]
	p.currentTable.Position.Begin = begin
	p.currentTable.Position.End = end
}

func (p *toml) SetArrayTable(buf []rune, begin, end int) {
	p.setArrayTable(p.table, buf, begin, end)
}

func (p *toml) setArrayTable(parent *ast.Table, buf []rune, begin, end int) {
	name := string(buf[begin:end])
	names := splitTableKey(name)
	parent, err := p.lookupTable(parent, names[:len(names)-1])
	if err != nil {
		p.Error(err)
	}
	last := names[len(names)-1]
	tbl := p.newTable(ast.TableTypeArray, last)
	switch v := parent.Fields[last].(type) {
	case nil:
		parent.Fields[last] = []*ast.Table{tbl}
	case []*ast.Table:
		parent.Fields[last] = append(v, tbl)
	case *ast.Table:
		p.Error(fmt.Errorf("array table `%s' is in conflict with table in line %d", name, v.Line))
	case *ast.KeyValue:
		p.Error(fmt.Errorf("array table `%s' is in conflict with line %d", name, v.Line))
	default:
		p.Error(fmt.Errorf("BUG: array table `%s' is in conflict but it's unknown type `%T'", name, v))
	}
	p.currentTable = tbl
}

func (p *toml) StartInlineTable() {
	p.skip = false
	p.stack = append(p.stack, &stack{p.key, p.currentTable})
	buf := []rune(p.key)
	if p.arr == nil {
		p.setTable(p.currentTable, buf, 0, len(buf))
	} else {
		p.setArrayTable(p.currentTable, buf, 0, len(buf))
	}
}

func (p *toml) EndInlineTable() {
	st := p.stack[len(p.stack)-1]
	p.key, p.currentTable = st.key, st.table
	p.stack[len(p.stack)-1] = nil
	p.stack = p.stack[:len(p.stack)-1]
	p.skip = true
}

func (p *toml) AddLineCount(i int) {
	p.line += i
}

func (p *toml) SetKey(buf []rune, begin, end int) {
	p.key = string(buf[begin:end])
}

func (p *toml) AddKeyValue() {
	if p.skip {
		p.skip = false
		return
	}
	if val, exists := p.currentTable.Fields[p.key]; exists {
		switch v := val.(type) {
		case *ast.Table:
			p.Error(fmt.Errorf("key `%s' is in conflict with table in line %d", p.key, v.Line))
		case *ast.KeyValue:
			p.Error(fmt.Errorf("key `%s' is in conflict with line %xd", p.key, v.Line))
		default:
			p.Error(fmt.Errorf("BUG: key `%s' is in conflict but it's unknown type `%T'", p.key, v))
		}
	}
	p.currentTable.Fields[p.key] = &ast.KeyValue{Key: p.key, Value: p.val, Line: p.line}
}

func (p *toml) SetBasicString(buf []rune, begin, end int) {
	p.s = p.unquote(string(buf[begin:end]))
}

func (p *toml) SetMultilineString() {
	p.s = p.unquote(`"` + escapeReplacer.Replace(strings.TrimLeft(p.s, "\r\n")) + `"`)
}

func (p *toml) AddMultilineBasicBody(buf []rune, begin, end int) {
	p.s += string(buf[begin:end])
}

func (p *toml) SetLiteralString(buf []rune, begin, end int) {
	p.s = string(buf[begin:end])
}

func (p *toml) SetMultilineLiteralString(buf []rune, begin, end int) {
	p.s = strings.TrimLeft(string(buf[begin:end]), "\r\n")
}

func (p *toml) unquote(s string) string {
	s, err := strconv.Unquote(s)
	if err != nil {
		p.Error(err)
	}
	return s
}

func (p *toml) lookupTable(t *ast.Table, keys []string) (*ast.Table, error) {
	for _, s := range keys {
		val, exists := t.Fields[s]
		if !exists {
			tbl := p.newTable(ast.TableTypeNormal, s)
			t.Fields[s] = tbl
			t = tbl
			continue
		}
		switch v := val.(type) {
		case *ast.Table:
			t = v
		case []*ast.Table:
			t = v[len(v)-1]
		case *ast.KeyValue:
			return nil, fmt.Errorf("key `%s' is in conflict with line %d", s, v.Line)
		default:
			return nil, fmt.Errorf("BUG: key `%s' is in conflict but it's unknown type `%T'", s, v)
		}
	}
	return t, nil
}

func splitTableKey(tk string) []string {
	key := make([]byte, 0, 1)
	keys := make([]string, 0, 1)
	inQuote := false
	for i := 0; i < len(tk); i++ {
		k := tk[i]
		switch {
		case k == tableSeparator && !inQuote:
			keys = append(keys, string(key))
			key = key[:0] 
		case k == '"':
			inQuote = !inQuote
		case (k == ' ' || k == '\t') && !inQuote:

		default:
			key = append(key, k)
		}
	}
	keys = append(keys, string(key))
	return keys
}
