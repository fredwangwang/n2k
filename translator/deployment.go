package translator

import (
	"log"

	"github.com/hashicorp/nomad/api"
	k8sv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	corev1 "k8s.io/client-go/applyconfigurations/core/v1"
)

func ToDeployment(group *api.TaskGroup, namespace string) *k8sv1.DeploymentApplyConfiguration {
	dep := k8sv1.Deployment(*group.Name, namespace)

	depSepc := k8sv1.DeploymentSpec()
	depSepc.WithTemplate(ToPodTemplateSpec(group))

	dep.WithSpec(depSepc)
	return dep
}

func ToPodTemplateSpec(group *api.TaskGroup) *corev1.PodTemplateSpecApplyConfiguration {
	podTpl := corev1.PodTemplateSpec()
	podTpl.WithSpec(ToPodSpec(*group))
	return podTpl
}

func ToPodSpec(group api.TaskGroup) *corev1.PodSpecApplyConfiguration {
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
	return podSpec
}

// func ToReplicaSet(group *api.TaskGroup) *k8sv1.ReplicaSetApplyConfiguration {
// 	rs := k8sv1.ReplicaSet(*group.Name, "namespace")
// 	rsSpec := k8sv1.ReplicaSetSpec()
// 	rsSpec.WithTemplate(ToPod(*group))
// }
