// Package calculator is the pack's simplest utility: real arithmetic inside
// the version-1 presentation contract, proving the pack pattern end to end.
package calculator

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"

	"gostalgia/sdk"
)

const (
	// ID is the reverse-DNS identifier for the calculator.
	ID = "com.gostalgia.calculator"

	// maxDigits bounds the arithmetic core to an honest 16-digit display.
	maxDigits = 16

	// displayPrec is the number of fractional digits used when rendering a
	// result. It matches maxDigits so the display does not silently round
	// non-zero digits away.
	displayPrec = 16
)

//go:embed manifest.json
var manifestJSON []byte

// Manifest returns the validated manifest declaration.
func Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestJSON)
	if err != nil {
		panic(err) // invalid compiled-in data is a programming error
	}
	return m
}

// ManifestJSON returns a copy of the raw embedded JSON.
func ManifestJSON() []byte { return append([]byte(nil), manifestJSON...) }

// Evaluate computes a left-to-right arithmetic expression with no operator
// precedence. It is the pure arithmetic core used by both the presentation
// action handler and the headless calc route.
func Evaluate(expr string) (string, error) {
	i := 0
	n := len(expr)
	for i < n && expr[i] == ' ' {
		i++
	}
	if i >= n {
		return "", fmt.Errorf("malformed expression")
	}

	left, consumed, err := parseNextNumber(expr[i:])
	if err != nil {
		return "", err
	}
	i += consumed

	for i < n {
		for i < n && expr[i] == ' ' {
			i++
		}
		if i >= n {
			break
		}
		if !isOpChar(expr[i]) {
			return "", fmt.Errorf("malformed expression")
		}
		op := expr[i]
		i++
		for i < n && expr[i] == ' ' {
			i++
		}
		if i >= n {
			return "", fmt.Errorf("malformed expression")
		}
		right, consumed, err := parseNextNumber(expr[i:])
		if err != nil {
			return "", err
		}
		i += consumed
		res, err := compute(left, right, string(op))
		if err != nil {
			return "", err
		}
		left = res
	}

	return formatResult(left)
}

// parseNextNumber parses a decimal literal with an optional leading sign.
// It returns the parsed value and the number of bytes consumed.
func parseNextNumber(s string) (*big.Rat, int, error) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}

	dot := false
	hasDigit := false
	for i < len(s) {
		c := s[i]
		if c == '.' {
			if dot {
				break
			}
			dot = true
			i++
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		hasDigit = true
		i++
	}

	if !hasDigit {
		return nil, 0, fmt.Errorf("malformed number")
	}

	r, err := parseDecimal(s[:i])
	if err != nil {
		return nil, 0, err
	}
	return r, i, nil
}

// parseDecimal converts a signed decimal string into a *big.Rat, reducing the
// fraction and rejecting values that exceed the 16-digit display budget.
func parseDecimal(s string) (*big.Rat, error) {
	if s == "" {
		return nil, fmt.Errorf("malformed number")
	}

	sign := 1
	i := 0
	if s[0] == '+' {
		i++
	} else if s[0] == '-' {
		sign = -1
		i++
	}
	if i == len(s) {
		return nil, fmt.Errorf("malformed number")
	}

	var sb strings.Builder
	frac := 0
	dot := false
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.':
			if dot {
				return nil, fmt.Errorf("malformed number")
			}
			dot = true
		case c >= '0' && c <= '9':
			sb.WriteByte(c)
			if dot {
				frac++
			}
		default:
			return nil, fmt.Errorf("malformed number")
		}
	}

	digits := sb.String()
	if digits == "" {
		return nil, fmt.Errorf("malformed number")
	}
	if len(digits) > maxDigits {
		return nil, fmt.Errorf("overflow")
	}

	num, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, fmt.Errorf("malformed number")
	}
	if sign < 0 {
		num.Neg(num)
	}

	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(frac)), nil)
	r := new(big.Rat).SetFrac(num, den)
	if err := checkOverflow(r); err != nil {
		return nil, err
	}
	return r, nil
}

// compute returns a op b, handling divide-by-zero and overflow.
func compute(a, b *big.Rat, op string) (*big.Rat, error) {
	if a == nil || b == nil {
		return nil, fmt.Errorf("malformed input")
	}

	r := new(big.Rat)
	switch op {
	case "+":
		r.Add(a, b)
	case "-":
		r.Sub(a, b)
	case "*", "x":
		r.Mul(a, b)
	case "/":
		if b.Sign() == 0 {
			return nil, fmt.Errorf("cannot divide by zero")
		}
		r.Quo(a, b)
	default:
		return nil, fmt.Errorf("unknown operator %q", op)
	}

	if err := checkOverflow(r); err != nil {
		return nil, err
	}
	return r, nil
}

// checkOverflow rejects results whose numerator or denominator exceeds the
// 16-digit display budget.
func checkOverflow(r *big.Rat) error {
	num := new(big.Int).Abs(r.Num())
	den := r.Denom()
	if len(num.String()) > maxDigits || len(den.String()) > maxDigits {
		return fmt.Errorf("overflow")
	}
	return nil
}

// formatResult renders a *big.Rat as a trimmed decimal string.
func formatResult(r *big.Rat) (string, error) {
	if r == nil {
		return "", fmt.Errorf("no result")
	}
	if r.Sign() == 0 {
		return "0", nil
	}
	if err := checkOverflow(r); err != nil {
		return "", err
	}

	s := r.FloatString(displayPrec)
	if !strings.Contains(s, ".") {
		return s, nil
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return s, nil
}

func isOpChar(c byte) bool { return c == '+' || c == '-' || c == '*' || c == 'x' || c == '/' }

func opLabel(op string) string {
	switch op {
	case "*":
		return "x"
	case "/":
		return "/"
	case "+":
		return "+"
	case "-":
		return "-"
	}
	return op
}

// Calc is the running in-process instance of the calculator.
type Calc struct {
	app         *sdk.Context
	mu          sync.Mutex
	left        *big.Rat
	leftInput   string
	leftIsInput bool
	rightInput  string
	op          string
	err         string
}

// Factory returns a fresh, uninitialized Calc instance.
func Factory() (sdk.Instance, error) {
	return &Calc{
		left:        big.NewRat(0, 1),
		leftInput:   "0",
		leftIsInput: true,
	}, nil
}

// Init registers the presentation surface and the headless calc route.
func (c *Calc) Init(app *sdk.Context) error {
	c.app = app
	if err := app.Present(c.view, c.act); err != nil {
		return err
	}
	return app.Handle("calc", c.calc)
}

// Run blocks until the application is cancelled.
func (c *Calc) Run(ctx context.Context) error {
	c.app.Log.Info("calculator running")
	<-ctx.Done()
	return nil
}

// Stop is called once on exit, including after a failed Init.
func (c *Calc) Stop(context.Context) error { return nil }

func (c *Calc) view(ctx context.Context) (sdk.View, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.viewLocked(), nil
}

func (c *Calc) viewLocked() sdk.View {
	v := sdk.View{
		Title:   "Calculator",
		State:   sdk.ViewReady,
		Items:   []sdk.Item{{ID: "display", Label: "Display", Detail: c.displayLocked()}},
		Actions: c.actionsLocked(),
		Status:  "Enter a calculation",
	}
	if c.err != "" {
		v.State = sdk.ViewError
		v.Error = c.err
		v.Status = c.err
	}
	return v
}

func (c *Calc) displayLocked() string {
	var leftStr string
	if c.leftIsInput {
		leftStr = c.leftInput
	} else {
		var err error
		leftStr, err = formatResult(c.left)
		if err != nil {
			leftStr = "0"
		}
	}
	if c.op == "" {
		return leftStr
	}
	if c.rightInput == "" {
		return leftStr + " " + opLabel(c.op)
	}
	return leftStr + " " + opLabel(c.op) + " " + c.rightInput
}

func (c *Calc) actionsLocked() []sdk.Action {
	return []sdk.Action{
		{ID: "digit_0", Label: "0"},
		{ID: "digit_1", Label: "1"},
		{ID: "digit_2", Label: "2"},
		{ID: "digit_3", Label: "3"},
		{ID: "digit_4", Label: "4"},
		{ID: "digit_5", Label: "5"},
		{ID: "digit_6", Label: "6"},
		{ID: "digit_7", Label: "7"},
		{ID: "digit_8", Label: "8"},
		{ID: "digit_9", Label: "9"},
		{ID: "add", Label: "+"},
		{ID: "sub", Label: "-"},
		{ID: "mul", Label: "x"},
		{ID: "div", Label: "/"},
		{ID: "eq", Label: "="},
		{ID: "clear", Label: "C"},
	}
}

func (c *Calc) act(ctx context.Context, p sdk.ActionRequest) (sdk.View, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.err != "" && p.Action != "clear" {
		c.reset()
	}

	switch {
	case strings.HasPrefix(p.Action, "digit_"):
		n, err := strconv.Atoi(strings.TrimPrefix(p.Action, "digit_"))
		if err != nil || n < 0 || n > 9 {
			return sdk.View{}, fmt.Errorf("unknown action %q", p.Action)
		}
		c.pressDigit(n)
	case p.Action == "add":
		c.pressOp("+")
	case p.Action == "sub":
		c.pressOp("-")
	case p.Action == "mul":
		c.pressOp("*")
	case p.Action == "div":
		c.pressOp("/")
	case p.Action == "eq":
		c.pressEq()
	case p.Action == "clear":
		c.reset()
	default:
		return sdk.View{}, fmt.Errorf("unknown action %q", p.Action)
	}

	return c.viewLocked(), nil
}

func (c *Calc) pressDigit(n int) {
	digit := strconv.Itoa(n)
	c.err = ""
	if c.op == "" {
		if !c.leftIsInput {
			c.leftInput = digit
			c.left = big.NewRat(int64(n), 1)
			c.leftIsInput = true
			c.rightInput = ""
			c.op = ""
			return
		}
		if c.leftInput == "0" {
			c.leftInput = digit
		} else if len(c.leftInput) < maxDigits {
			c.leftInput += digit
		} else {
			c.err = "overflow"
			return
		}
		left, err := parseDecimal(c.leftInput)
		if err != nil {
			c.err = err.Error()
			return
		}
		c.left = left
		return
	}
	if c.rightInput == "" || c.rightInput == "0" {
		c.rightInput = digit
	} else if len(c.rightInput) < maxDigits {
		c.rightInput += digit
	} else {
		c.err = "overflow"
		return
	}
}

func (c *Calc) pressOp(op string) {
	c.err = ""
	if c.leftIsInput {
		c.leftIsInput = false
	}
	if c.op == "" {
		c.op = op
		c.rightInput = ""
		return
	}
	if c.rightInput != "" {
		right, err := parseDecimal(c.rightInput)
		if err != nil {
			c.err = err.Error()
			c.rightInput = ""
			c.op = ""
			return
		}
		res, err := compute(c.left, right, c.op)
		if err != nil {
			c.err = err.Error()
			c.rightInput = ""
			c.op = ""
			return
		}
		c.left = res
		c.leftIsInput = false
		c.rightInput = ""
	}
	c.op = op
}

func (c *Calc) pressEq() {
	c.err = ""
	if c.op == "" || c.rightInput == "" {
		return
	}
	if c.leftIsInput {
		c.leftIsInput = false
	}
	right, err := parseDecimal(c.rightInput)
	if err != nil {
		c.err = err.Error()
		c.rightInput = ""
		c.op = ""
		return
	}
	res, err := compute(c.left, right, c.op)
	if err != nil {
		c.err = err.Error()
		c.rightInput = ""
		c.op = ""
		return
	}
	c.left = res
	c.leftIsInput = false
	c.rightInput = ""
	c.op = ""
}

func (c *Calc) reset() {
	c.left = big.NewRat(0, 1)
	c.leftInput = "0"
	c.leftIsInput = true
	c.rightInput = ""
	c.op = ""
	c.err = ""
}

// calc is the headless route that evaluates a full expression string.
func (c *Calc) calc(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Expr string `json:"expr"`
	}
	if err := sdk.DecodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.Expr == "" {
		return nil, fmt.Errorf("expr is required")
	}
	res, err := Evaluate(p.Expr)
	if err != nil {
		return nil, err
	}
	return map[string]string{"result": res}, nil
}
