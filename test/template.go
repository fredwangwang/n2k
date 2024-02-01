package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"
	_ "unsafe"

	"github.com/agiledragon/gomonkey/v2"
)

const TplText1 = `
{{ keyOrDefault "control-plane/device-state/logConfig" "" }}
`

const TplText2 = `
{{ with secret "kvv2/data/webapp/config" }}
test133="{{ .Data.data.username }}"
test2="{{ .Data.data.password }}"
{{ end }}
`

const TplText3 = `
startadsfasdfasdf
{{ with secret "control-plane-services/data/and-yats" }}
{{range $v := .Data.data.ClientCredentials}}
	{{if eq $v.Client_Name "android-management-service"}}
		AccessTokenServiceOptions__ClientId={{ $v.Client_Id }}
		AccessTokenServiceOptions__ClientSecret={{$v.Client_Secret}}
	{{ end }}
{{ end }}
{{ end }}
end
`

const TplText4 = `
{{$y := .}}

{{ range $y = .sub }}
{{ . }}
{{ end }}

{{ $y }}
`

func init() {

	gomonkey.ApplyFunc(evalFieldChain, func(s *state, dot, receiver reflect.Value, node parse.Node, ident []string, args []parse.Node, final reflect.Value) reflect.Value {
		return s.evalFieldChain(dot, receiver, node, ident, args, final)
	})

	gomonkey.ApplyFunc(walkIfOrWith, func(s *state, typ parse.NodeType, dot reflect.Value, pipe *parse.PipeNode, list, elseList *parse.ListNode) {
		s.walkIfOrWith(typ, dot, pipe, list, elseList)
	})
}

func main() {
	// oldevalFieldChain := evalFieldChain

	// s := state{}
	// rv := reflect.ValueOf(s)
	// evalFieldChain(s, rv, rv, &parse.BoolNode{}, []string{}, nil, rv)

	var err error
	tpl := template.New("name").Option()
	tpl.Funcs(stubFuncs())
	tpl, err = tpl.Parse(TplText3)
	if err != nil {
		panic(err)
	}
	println(tpl.Name())
	// err = tpl.Execute(os.Stdout, nil)
	// if err != nil {
	// 	panic(err)
	// }

	// for _, n := range tpl.Tree.Root.Nodes {
	// 	fmt.Print(n.String())
	// }
	ctx := WalkCtx{
		ModifiedTpl: &strings.Builder{},
	}
	customWalk(&ctx, tpl.Tree.Root)
	fmt.Println(ctx.ModifiedTpl.String())
}

type WalkCtx struct {
	VaultMount      string
	VaultSecretPath string
	ModifiedTpl     *strings.Builder
}

var (
	walkWithHandled = errors.New("withHandled")
)

func customWalk(ctx *WalkCtx, node parse.Node) {
	switch node := node.(type) {
	case *parse.ActionNode:
		ctx.ModifiedTpl.WriteString("{{")
		customWalk(ctx, node.Pipe)
		ctx.ModifiedTpl.WriteString("}}")
	case *parse.PipeNode:
		if len(node.Decl) > 0 {
			for i, v := range node.Decl {
				if i > 0 {
					ctx.ModifiedTpl.WriteString(", ")
				}
				ctx.ModifiedTpl.WriteString(v.String())
			}
			if node.IsAssign {
				ctx.ModifiedTpl.WriteString(" = ")
			} else {
				ctx.ModifiedTpl.WriteString(" := ")
			}
		}
		for i, c := range node.Cmds {
			if i > 0 {
				ctx.ModifiedTpl.WriteString(" | ")
			}
			customWalk(ctx, c)
		}
	case *parse.CommandNode:
		// refering to func (s *state) evalCommand(dot reflect.Value, cmd *parse.CommandNode, final reflect.Value) reflect.Value
		firstWord := node.Args[0]
		commandHandled := false
		if in, ok := firstWord.(*parse.IdentifierNode); ok {
			commandHandled = true
			// Must be a function.
			switch in.Ident {
			case "secret":
				args := node.Args[1:]
				if len(args) != 1 {
					panic(fmt.Errorf("only read secret supported, found %s", node.String()))
				}
				arg := args[0]
				if sn, ok := arg.(*parse.StringNode); !ok {
					panic(fmt.Errorf("only string node supported for secret's arg, found %s", node.String()))
				} else {
					parts := strings.Split(sn.Text, "/")
					if len(parts) < 2 {
						panic(fmt.Errorf("failed to parse secret mount and path: %s", sn.Text))
					}
					ctx.VaultMount = parts[0]
					if parts[1] == "data" {
						ctx.VaultSecretPath = strings.Join(parts[2:], "/")
					} else {
						ctx.VaultSecretPath = strings.Join(parts[1:], "/")
					}
				}
				panic(walkWithHandled)
			default:
				commandHandled = false
			}
		}
		if commandHandled {
			break
		}

		for i, arg := range node.Args {
			if i > 0 {
				ctx.ModifiedTpl.WriteByte(' ')
			}
			if arg, ok := arg.(*parse.PipeNode); ok {
				ctx.ModifiedTpl.WriteByte('(')
				customWalk(ctx, arg)
				ctx.ModifiedTpl.WriteByte(')')
				continue
			}
			customWalk(ctx, arg)
		}
	case *parse.ChainNode:
		customWalk(ctx, node.Node)
		for _, field := range node.Field {
			ctx.ModifiedTpl.WriteByte('.')
			ctx.ModifiedTpl.WriteString(field)
		}
	case *parse.FieldNode:
		ctx.ModifiedTpl.WriteString(node.String())
	case *parse.IdentifierNode:
		ctx.ModifiedTpl.WriteString(node.String())
	case *parse.BreakNode:
		ctx.ModifiedTpl.WriteString(node.String())
	case *parse.StringNode:
		ctx.ModifiedTpl.WriteString(node.String())
	case *parse.VariableNode:
		ctx.ModifiedTpl.WriteString(node.String())
	case *parse.CommentNode:
		// ctx.ModifiedTpl.WriteString(node.String())
	case *parse.ContinueNode:
		ctx.ModifiedTpl.WriteString(node.String())
	case *parse.IfNode:
		ctx.ModifiedTpl.WriteString("{{if ")
		customWalk(ctx, node.Pipe)
		ctx.ModifiedTpl.WriteString("}}")
		customWalk(ctx, node.List)
		if node.ElseList != nil {
			ctx.ModifiedTpl.WriteString("{{else}}")
			customWalk(ctx, node.ElseList)
		}
		ctx.ModifiedTpl.WriteString("{{end}}")
	case *parse.ListNode:
		for _, node := range node.Nodes {
			customWalk(ctx, node)
		}
	case *parse.RangeNode:
		ctx.ModifiedTpl.WriteString("{{range ")
		customWalk(ctx, node.Pipe)
		ctx.ModifiedTpl.WriteString("}}")
		customWalk(ctx, node.List)
		ctx.ModifiedTpl.WriteString("{{end}}")
	case *parse.TemplateNode:
		// not gonna support this..
		panic("template action not supported")
	case *parse.TextNode:
		ctx.ModifiedTpl.WriteString(node.String())
	case *parse.WithNode:
		ctxWriter := ctx.ModifiedTpl

		defer func() {
			if r := recover(); r != nil && r != walkWithHandled {
				panic(r)
			}
			ctx.ModifiedTpl = ctxWriter
			customWalk(ctx, node.List)
		}()

		ctx.ModifiedTpl = &strings.Builder{}
		ctx.ModifiedTpl.WriteString("{{with ")
		customWalk(ctx, node.Pipe)
		ctx.ModifiedTpl.WriteString("}}")

		customWalk(ctx, node.List)
		ctx.ModifiedTpl.WriteString("{{end}}")

		ctxWriter.WriteString(ctx.ModifiedTpl.String())
		ctx.ModifiedTpl = ctxWriter
	default:
		panic(fmt.Errorf("unknown node: %d: %s", node.Type(), node.String()))
	}
}

func stubFuncs() template.FuncMap {
	return template.FuncMap{
		"keyOrDefault": func(s, def string) (string, error) {
			return def, nil
		},
		"secret": func(s ...string) (interface{}, error) {
			return map[string]interface{}{
				"_": nil,
			}, nil
		},
	}
}

// https://medium.com/@yardenlaif/accessing-private-functions-methods-types-and-variables-in-go-951acccc05a6

type state struct{}

//go:linkname missingVal text/template.missingVal
var missingVal reflect.Value

//go:linkname walk text/template.(*state).walk
func walk(s *state, dot reflect.Value, node parse.Node)

//go:linkname walkIfOrWith text/template.(*state).walkIfOrWith
func walkIfOrWith(s *state, typ parse.NodeType, dot reflect.Value, pipe *parse.PipeNode, list, elseList *parse.ListNode)

//go:linkname evalFieldChain text/template.(*state).evalFieldChain
func evalFieldChain(s *state, dot, receiver reflect.Value, node parse.Node, ident []string, args []parse.Node, final reflect.Value) reflect.Value

//go:linkname evalField text/template.(*state).evalField
func evalField(s *state, dot reflect.Value, fieldName string, node parse.Node, args []parse.Node, final, receiver reflect.Value) reflect.Value

//go:linkname evalPipeline text/template.(*state).evalPipeline
func evalPipeline(s *state, dot reflect.Value, pipe *parse.PipeNode) (value reflect.Value)

//go:linkname pop text/template.(*state).pop
func pop(s *state, mark int)

//go:linkname mark text/template.(*state).mark
func mark(s *state) int

//go:linkname errorf text/template.(*state).errorf
func errorf(s *state, format string, args ...any)

//go:linkname isTrue text/template.isTrue
func isTrue(val reflect.Value) (truth, ok bool)

//go:linkname indirectInterface text/template.indirectInterface
func indirectInterface(v reflect.Value) reflect.Value

func (s *state) evalFieldChain(dot, receiver reflect.Value, node parse.Node, ident []string, args []parse.Node, final reflect.Value) reflect.Value {
	n := len(ident)
	for i := 0; i < n-1; i++ {
		receiver = evalField(s, dot, ident[i], node, nil, missingVal, receiver)
	}
	// Now if it's a method, it gets the arguments.
	return evalField(s, dot, ident[n-1], node, args, final, receiver)
}

func (s *state) walkIfOrWith(typ parse.NodeType, dot reflect.Value, pipe *parse.PipeNode, list, elseList *parse.ListNode) {
	defer pop(s, mark(s))

	originalImpl := func() {
		val := evalPipeline(s, dot, pipe)
		truth, ok := isTrue(indirectInterface(val))
		if !ok {
			errorf(s, "if/with can't use %v", val)
		}
		if truth {
			if typ == parse.NodeWith {
				walk(s, val, list)
			} else {
				walk(s, dot, list)
			}
		} else if elseList != nil {
			walk(s, dot, elseList)
		}
	}

	if typ == parse.NodeWith {
		originalImpl()
	} else {
		originalImpl()
	}

}
