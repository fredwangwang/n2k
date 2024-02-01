package main

import (
	"context"
	"fmt"
	"strings"

	_ "embed"

	"github.com/sashabaranov/go-openai"
	"sigs.k8s.io/yaml"
)

//go:embed system-prompt
var systemPrompt string

//go:embed user-req-template
var userReqTpl string

var tpl string = `
{{ with secret "somedifferentpath/data/subpathforoem/yats/secrets" }}
	{{range $v := .Data.data.ClientCredentials}}
		{{if eq $v.Client_Name "android-oem-integration-api-client"}}
			TokenService__ClientId={{ $v.Client_Id }}
			TokenService__ClientSecret={{$v.Client_Secret}}
		{{ end }}
	{{ end }}
	{{ end }}
	`

func main() {
	userReq := strings.ReplaceAll(userReqTpl, "ACTUAL_TEMPLATE_PLACEHOLDER", strings.TrimSpace(tpl))

	client := openai.NewClient("sdfsdf")
	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: openai.GPT3Dot5Turbo,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: systemPrompt,
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: userReq},
			},
		},
	)

	if err != nil {
		fmt.Printf("ChatCompletion error: %v\n", err)
		return
	}

	fmt.Println(resp.Choices[0].Message.Content)

	output := map[string]interface{}{}
	err = yaml.Unmarshal([]byte(resp.Choices[0].Message.Content), &output)
	if err != nil {
		panic(err)
	}

	fmt.Println("\n\n\n")
}
