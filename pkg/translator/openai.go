package translator

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "embed"

	"github.com/boltdb/bolt"
	vsov1 "github.com/hashicorp/vault-secrets-operator/api/v1beta1"
	"github.com/rs/zerolog"
	"github.com/sashabaranov/go-openai"

	"sigs.k8s.io/yaml"
)

//go:embed user-context1.txt
var userCtx1 string

//go:embed user-context-toenv.txt
var userCtxToEnv string

//go:embed user-context-tofile.txt
var userCtxToFile string

//go:embed user-req-template.txt
var userReqTpl string

var (
	ErrNoToken              = errors.New("openai token missing, set envvar OPENAI_TOKEN")
	ErrGenerationUnparsable = errors.New("the generated output is not parsable")
)

var (
	dbhandle   *bolt.DB
	bucketName = "secret_translate_cache"
)

type StructuredResponse struct {
	Result      vsov1.VaultStaticSecret `yaml:"result" json:"result"`
	Confidence  float32                 `yaml:"confidence" json:"confidence"`
	Explanation string                  `yaml:"explanation" json:"explanation"`
}

func CachedTranslateSecretUsingOpenAI(logger zerolog.Logger, isEnv bool, tpl string) (*StructuredResponse, error) {
	var err error

	if dbhandle == nil {
		dbhandle, err = bolt.Open("translatecache.bolt", 0o640, &bolt.Options{
			Timeout: 1 * time.Second,
		})
		if err != nil {
			return nil, err
		}
		_ = dbhandle.Update(func(tx *bolt.Tx) error {
			_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
			return err
		})
	}

	key := sha1.Sum([]byte(tpl))
	var val []byte

	_ = dbhandle.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		val = b.Get(key[:])
		return nil
	})

	if val == nil {
		res, err := TranslateSecretUsingOpenAI(logger, isEnv, tpl)
		if err != nil {
			return nil, err
		}
		val, _ = yaml.Marshal(res)
		_ = dbhandle.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte(bucketName))
			return b.Put(key[:], val)
		})

		return res, nil
	}

	res := StructuredResponse{}
	err = yaml.Unmarshal([]byte(replaceDataDataWithSecrets(string(val))), &res)
	if err != nil {
		fmt.Println(string(val))
		fmt.Println(string(y2jmap(val)))
	}
	return &res, err
}

func TranslateSecretUsingOpenAI(logger zerolog.Logger, isEnv bool, tpl string) (*StructuredResponse, error) {
	token := os.Getenv("OPENAI_TOKEN")
	if token == "" {
		return nil, ErrNoToken
	}

	userReq := strings.ReplaceAll(userReqTpl, "ACTUAL_TEMPLATE_PLACEHOLDER", strings.TrimSpace(tpl))

	userCtxExample := ""
	if isEnv {
		userCtxExample = userCtxToEnv
	} else {
		userCtxExample = userCtxToFile
	}

	client := openai.NewClient(token)

	parseble := false
	parseFailed := 0
	numGen := 0

	const scoreThresh = 9.0

	var bestScore float32
	var bestResponse StructuredResponse

	for !parseble || (numGen < 5 && bestScore < scoreThresh) {
		numGen++

		resp, err := client.CreateChatCompletion(
			context.Background(),
			openai.ChatCompletionRequest{
				Model: openai.GPT3Dot5Turbo,
				Messages: []openai.ChatCompletionMessage{
					{
						Role:    openai.ChatMessageRoleSystem,
						Content: `You are a helpful assistant with speciality in coding tasks. Follow the instructions exactly`,
					},
					{
						Role:    openai.ChatMessageRoleUser,
						Content: userCtx1,
					},
					{
						Role:    openai.ChatMessageRoleUser,
						Content: userCtxExample,
					},
					{
						Role:    openai.ChatMessageRoleUser,
						Content: userReq,
					},
				},
			},
		)

		if err != nil {
			return nil, err
			// fmt.Printf("ChatCompletion error: %v\n", err)
			// return
		}

		// fmt.Println(resp.Choices[0].Message.Content)

		// desipte in the context specifically asking to replace .Data.data (consul template kv prefix) with .Secrets,
		// it is not alwasy reliable...

		// doing the replacement here again
		output := StructuredResponse{}
		err = yaml.Unmarshal([]byte(replaceDataDataWithSecrets(resp.Choices[0].Message.Content)), &output)
		if err != nil {
			parseFailed++
		} else {
			parseble = true
			if output.Confidence > bestScore {
				bestScore = output.Confidence
				bestResponse = output
			}

			if output.Confidence < scoreThresh {
				logger.Debug().Float32("confidence", output.Confidence).Str("explanation", AbbrevStr(output.Explanation, 100)).Msg("attempt to regen")
			}
		}

		if parseFailed > 3 {
			fmt.Println(resp.Choices[0].Message.Content, err)
			return nil, ErrGenerationUnparsable
		}
	}

	return &bestResponse, nil
}

func y2jmap(in []byte) []byte {
	out, err := yaml.YAMLToJSON(in)
	if err != nil {
		panic(err)
	}
	return out
}

func replaceDataDataWithSecrets(in string) string {
	return strings.ReplaceAll(in, ".Data.data", ".Secrets")
}
