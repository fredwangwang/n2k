package main

import (
	"encoding/json"
	"os"

	"github.com/fredwangwang/n2k/translator"
	"github.com/hashicorp/nomad/command"
)

const Namespace = "default"

func main() {
	jobGetter := command.JobGetter{}
	if err := jobGetter.Validate(); err != nil {
		panic(err)
	}
	job, err := jobGetter.Get("/home/huan/workspace/n2k/examples/http-echo/http-echo.nomad")
	if err != nil {
		panic(err)
	}

	dep := translator.ToDeployment(job.TaskGroups[0], Namespace)

	json.NewEncoder(os.Stdout).Encode(dep)
}
