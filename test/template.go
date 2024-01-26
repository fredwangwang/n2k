package main

import (
	"os"
	"reflect"
	"text/template"
	"text/template/parse"
	_ "unsafe"

	"github.com/agiledragon/gomonkey/v2"
)

const TplText1 = `
{{ keyOrDefault "control-plane/device-state/logConfig" "" }}
`

const TplText2 = `
        {{ with secret "control-plane-services/data/device-state-global" }}
        LOGZIO_TOKEN="{{ .Data.data.DEVICE_STATE_LOGGING_TOKEN }}"
        {{ end }}
`

func main() {
	// oldevalFieldChain := evalFieldChain

	// s := state{}
	// rv := reflect.ValueOf(s)
	// evalFieldChain(s, rv, rv, &parse.BoolNode{}, []string{}, nil, rv)

	var err error
	tpl := template.New("name")
	tpl.Funcs(stubFuncs())
	tpl, err = tpl.Parse(TplText2)
	if err != nil {
		panic(err)
	}
	println(tpl.Name())
	err = tpl.Execute(os.Stdout, nil)
	if err != nil {
		panic(err)
	}
}

func stubFuncs() template.FuncMap {
	return template.FuncMap{
		"keyOrDefault": func(s, def string) (string, error) {
			return def, nil
		},
		"secret": func(s ...string) (interface{}, error) {
			return map[string]interface{}{
				"Data": map[string]interface{}{
					"data": map[string]interface{}{
						"DEVICE_STATE_LOGGING_TOKEN": "FUCKINGA",
					},
				},
			}, nil
		},
	}
}

// https://medium.com/@yardenlaif/accessing-private-functions-methods-types-and-variables-in-go-951acccc05a6

type state struct{}

//go:linkname missingVal text/template.missingVal
var missingVal reflect.Value

//go:linkname evalFieldChain text/template.(*state).evalFieldChain
func evalFieldChain(s *state, dot, receiver reflect.Value, node parse.Node, ident []string, args []parse.Node, final reflect.Value) reflect.Value

//go:linkname evalField text/template.(*state).evalField
func evalField(s *state, dot reflect.Value, fieldName string, node parse.Node, args []parse.Node, final, receiver reflect.Value) reflect.Value

func (s *state) evalFieldChain(dot, receiver reflect.Value, node parse.Node, ident []string, args []parse.Node, final reflect.Value) reflect.Value {
	// fmt.Println("yeahooo")
	n := len(ident)
	for i := 0; i < n-1; i++ {
		receiver = evalField(s, dot, ident[i], node, nil, missingVal, receiver)
	}
	// Now if it's a method, it gets the arguments.
	return evalField(s, dot, ident[n-1], node, args, final, receiver)
}

func init() {
	gomonkey.ApplyFunc(evalFieldChain, func(s *state, dot, receiver reflect.Value, node parse.Node, ident []string, args []parse.Node, final reflect.Value) reflect.Value {
		return s.evalFieldChain(dot, receiver, node, ident, args, final)
	})
}
