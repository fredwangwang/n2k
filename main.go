package main

import (
	"os"
	"path"
	"strings"

	"github.com/fredwangwang/n2k/pkg/translator"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/command"
)

const OutputPrefix = "generated-k8s"

func main() {
	log.Logger = log.Level(zerolog.DebugLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	if len(os.Args) != 2 {
		log.Error().Msgf("usage: %s <path to nomad job file>", os.Args[0])
		os.Exit(1)
	}

	jobGetter := command.JobGetter{}
	jobGetter.HCL1 = true // bc, most of the jobs that we have are not HCL2 compatible

	if err := jobGetter.Validate(); err != nil {
		panic(err)
	}

	_, job, err := jobGetter.Get(os.Args[1])
	if err != nil {
		panic(err)
	}

	if job.Type != nil && *job.Type != "service" {
		log.Error().Str("type", *job.Type).Msg("unexpcted job type, only service type is supported")
		os.Exit(1)
	}

	StripLoggingAndMetricsSidecar(job)

	// for _, tg := range job.TaskGroups {
	// 	for _, task := range tg.Tasks {
	// 		for _, tpl := range task.Templates {
	// 			if strings.Contains(*tpl.EmbeddedTmpl, "secret") && (tpl.Envvars == nil || !*tpl.Envvars) {
	// 				fmt.Printf("%s %s:\n%s\n\n", task.Name, *tpl.DestPath, *tpl.EmbeddedTmpl)
	// 			}
	// 		}
	// 	}
	// }
	// os.Exit(0)

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

func StripLoggingAndMetricsSidecar(job *api.Job) {
	for _, tg := range job.TaskGroups {
		var resTasks []*api.Task
		for _, task := range tg.Tasks {
			if strings.Contains(task.Name, "logging") {
				log.Info().Str("group", *tg.Name).Str("task", task.Name).Msg("removing logging sidecar")
				continue
			}
			if strings.Contains(task.Name, "telegraf") {
				log.Info().Str("group", *tg.Name).Str("task", task.Name).Msg("removing metrics sidecar")
				continue
			}
			resTasks = append(resTasks, task)
		}
		tg.Tasks = resTasks
	}
}

func Must(err error) {
	if err != nil {
		log.Panic().Err(err).Send()
	}
}
