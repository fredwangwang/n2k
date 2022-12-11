package translator

import (
	"fmt"
	"log"

	"github.com/hashicorp/nomad/api"
	"github.com/pkg/errors"
	corev1 "k8s.io/client-go/applyconfigurations/core/v1"
)

var (
	ErrOnlyContainerSupport = errors.New("pod only support container")
)

func ToPod(group api.TaskGroup) *corev1.PodApplyConfiguration {

	podSpec := corev1.PodSpec()
	for _, task := range group.Tasks {
		c := corev1.Container()
		c.WithName(task.Name)
		setImage(c, task.Config)
		setArgs(c, task.Config)

		if len(task.Config) != 0 {
			log.Printf("warn: unknown field(s) in config: %v", task.Config)
		}
		podSpec.Containers = append(podSpec.Containers, *c)
	}

	return corev1.Pod(*group.Name, "namespace").WithSpec(podSpec)
}

func setImage(c *corev1.ContainerApplyConfiguration, config map[string]interface{}) {
	img, ok := config["image"]
	if !ok {
		panic(errors.Wrap(ErrOnlyContainerSupport, fmt.Sprintf("%v is not supported", config)))
	}
	c.WithImage(img.(string))
	delete(config, "image")
}

func setArgs(c *corev1.ContainerApplyConfiguration, config map[string]interface{}) {
	args, ok := config["args"]
	if !ok {
		return
	}
	argsI := args.([]interface{})
	argsS := make([]string, len(argsI))
	for i, argI := range argsI {
		argsS[i] = argI.(string)
	}
	c.Args = argsS
	delete(config, "args")
}
