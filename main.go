package main

import (
	"os"
	"path"

	"github.com/fredwangwang/n2k/pkg/translator"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/hashicorp/nomad/command"
)

const OutputPrefix = "generated-k8s"

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	jobGetter := command.JobGetter{}
	jobGetter.HCL1 = true // bc, most of the jobs that we have are not HCL2 compatible

	if err := jobGetter.Validate(); err != nil {
		panic(err)
	}

	_, job, err := jobGetter.Get("/home/huan/workspace/n2k/examples/device-state/device-state.nomad")
	if err != nil {
		panic(err)
	}

	if job.Type != nil && *job.Type != "service" {
		log.Error().Str("type", *job.Type).Msg("unexpcted job type, only service type is supported")
		os.Exit(1)
	}

	outputPath := path.Join(OutputPrefix, *job.Name)
	_, err = os.Stat(outputPath)
	if err != nil && !os.IsNotExist(err) {
		Must(err)
	} else {
		log.Info().Str("dir", outputPath).Msg("folder exists, force removing and recreating")
		Must(os.RemoveAll(outputPath))
	}

	log.Debug().Str("dir", outputPath).Msg("create folder for transformed resources")
	Must(os.MkdirAll(outputPath, 0o755))

	trans := translator.Translator{
		DestPath: outputPath,
		Job:      job,
	}

	Must(trans.Process())

	// dep := translator.ToDeployment(job.TaskGroups[0], Namespace)

	// json.NewEncoder(os.Stdout).Encode(dep)
}

func Must(err error) {
	if err != nil {
		log.Panic().Err(err).Send()
	}
}
