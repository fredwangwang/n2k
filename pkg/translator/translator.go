package translator

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"sigs.k8s.io/yaml"

	"github.com/hashicorp/nomad/api"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
)

const (
	DefaultChangeMode = "restart"
)

var (
	ErrNoNetworkBlock       = errors.New("network mode has to be provided")
	ErrOnlyContainerSupport = errors.New("pod only support container")
)

type TranslateError struct {
	Name string
	Type string
	Err  error
}

func (te *TranslateError) Error() string {
	return fmt.Sprintf("translate error: %s (%s): %s", te.Name, te.Type, te.Err)
}

type NetworkNotSupportedError struct {
	Type string
}

func (ne *NetworkNotSupportedError) Error() string {
	return "network mode " + ne.Type + " is not supported"
}

type DriverNotSupportedError struct {
	Type string
}

func (de *DriverNotSupportedError) Error() string {
	return "task driver " + de.Type + " is not supported"
}

type Translator struct {
	DestPath   string
	NamePrefix string
	Job        *api.Job

	Notices Notices

	K8sCRs         []interface{}
	K8sDeployemnts []*v1.Deployment
	K8sConfigMpas  []*corev1.ConfigMap
	K8sServices    []*corev1.Service
}

// A job in nomad can contain muliple groups. Each group is mapped to a deployment (and a corresponding service)

func (t *Translator) Process() error {
	// TODO t.Job.Affinities to preferredDuringSchedulingIgnoredDuringExecution node affinity
	// https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/

	// TODO t.Job.Constraints to requiredDuringSchedulingIgnoredDuringExecution node affinity or nodeselector

	for _, group := range t.Job.TaskGroups {
		if err := t.ProcessTaskGroup(group); err != nil {
			return err
		}
	}

	for _, cm := range t.K8sConfigMpas {
		f, err := os.Create(path.Join(t.DestPath, t.getConfigMapFileName(cm.Name)))
		if err != nil {
			return err
		}
		content, _ := yaml.Marshal(cm)
		_, _ = f.Write(content)
		f.Close()
	}

	for _, srv := range t.K8sServices {
		f, err := os.Create(path.Join(t.DestPath, t.getServiceFileName(srv.Name)))
		if err != nil {
			return err
		}
		content, _ := yaml.Marshal(srv)
		_, _ = f.Write(content)
		f.Close()
	}

	f, err := os.Create(path.Join(t.DestPath, "PLEASE_READ.md"))
	if err != nil {
		return err
	}
	_, _ = f.WriteString(t.Notices.String())

	return nil
}

func (t *Translator) ProcessTaskGroup(tg *api.TaskGroup) error {
	logger := log.With().Str("group", *tg.Name).Logger()

	logger.Info().Msg("processing task group")
	dep, err := t.genDepoyment(logger, tg)
	if err != nil {
		return err
	}
	f, err := os.Create(path.Join(t.DestPath, t.getDeploymentFilename(*tg.Name)))
	if err != nil {
		return err
	}
	defer f.Close()
	depBytes, _ := yaml.Marshal(dep)
	_, _ = f.Write(depBytes)

	err = t.genService(logger, tg)
	if err != nil {
		return err
	}

	logger.Debug().Int("num", len(tg.Services)).Msg("number of services")

	return nil
}

func (t *Translator) genDepoyment(logger zerolog.Logger, tg *api.TaskGroup) (*v1.Deployment, error) {
	dep := v1.Deployment{}
	dep.APIVersion = "apps/v1"
	dep.Kind = "Deployment"

	dep.SetName(*tg.Name)

	depSepc := v1.DeploymentSpec{}

	podTplLabel := genPodSelector(tg)
	podTplSelector := metav1.LabelSelector{
		MatchLabels: genPodSelector(tg),
	}
	depSepc.Selector = &podTplSelector

	// count
	depSepc.Replicas = pointer.Int32(int32(*tg.Count))

	// scaling
	if tg.Scaling != nil {
		// TODO this might be able to map to HPA?
		// https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/
		logger.Warn().Msg("scaling block is not supported")
	}

	// restart
	if tg.RestartPolicy != nil {
		t.Notices = append(t.Notices, NoticeItem{
			Importance: NoticeInformative,
			Msg:        "the restart policy is not configurable, please review https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#restart-policy",
		})
		// logger.Warn().Msg("restart block is not supported")
	}

	// network
	if len(tg.Networks) == 0 {
		return nil, &TranslateError{Type: "task_group", Name: *tg.Name, Err: ErrNoNetworkBlock}
	}
	network := tg.Networks[0]
	if network.Mode != "bridge" {
		return nil, &TranslateError{Type: "task_group", Name: *tg.Name, Err: &NetworkNotSupportedError{
			Type: network.Mode,
		}}
	}
	// for now ignore any host mappings.
	// looking through the job files that we have, there are two main use cases for having a host port:
	// 1. exposing some endpoints for easier troubleshooting from the host network.
	//    this we try to avoid anyways, so lets by default keep the port shut,
	//    and later ask the developers to review the ports again if needed.
	// 2. exposing the porst so that other services can access.
	//    this is done for services like zk, which is not in the mesh but needs to be connected by other services.
	//    this use case is no longer a valid one, since in K8s the inter-pod communication happens in overlay network,
	//    there is no need to expose the host net port anymore.
	logUnspportedPortUsage(logger, network.ReservedPorts, network.DynamicPorts)

	// TODO handle tg.Affinities && tg.Constraints, this should merge with job level ones

	// TODO handle update block
	// https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#strategy
	// auto rollback is not supported OOTB
	// https://stackoverflow.com/questions/69468977/how-to-achieve-automatic-rollback-in-kubernetes

	// service block handled in genService method

	// check
	// TODO: check stanza to healthcheck

	podTpl := corev1.PodTemplateSpec{}
	podTpl.Labels = podTplLabel

	podSpec := corev1.PodSpec{}

	for _, task := range tg.Tasks {
		// TODO check all members of task
		// https://developer.hashicorp.com/nomad/docs/job-specification/task
		c := corev1.Container{}

		if task.Driver != "docker" {
			return nil, &TranslateError{Type: "task", Name: task.Name, Err: &DriverNotSupportedError{
				Type: task.Driver,
			}}
		}

		c.Name = task.Name
		c.Env = genEnv(logger, task.Env)

		// logs
		// TODO: is it able to be handled? maybe not
		// https://kubernetes.io/docs/concepts/cluster-administration/logging/#log-rotation

		// template
		err := t.setTemplates(logger, &c, task)
		if err != nil {
			return nil, err
		}

		setImage(&c, task.Config)
		setArgs(&c, task.Config)
		delete(task.Config, "cpu_hard_limit")

		if len(task.Config) != 0 {
			logger.Warn().Str("task", task.Name).Any("config", task.Config).Msg("unsupported fields in config")
		}
		podSpec.Containers = append(podSpec.Containers, c)
	}

	podTpl.Spec = podSpec
	depSepc.Template = podTpl
	dep.Spec = depSepc

	t.K8sDeployemnts = append(t.K8sDeployemnts, &dep)

	return &dep, nil
}

func (t *Translator) genService(logger zerolog.Logger, tg *api.TaskGroup) error {
	if len(tg.Services) == 0 {
		return nil
	}

	for _, srv := range tg.Services {
		logger = logger.With().Str("name", srv.Name).Logger()

		k8sSrv := corev1.Service{}
		k8sSrv.APIVersion = "v1"
		k8sSrv.Kind = "Service"

		k8sSrv.Spec.Type = corev1.ServiceTypeClusterIP
		k8sSrv.Name = srv.Name

		port, err := strconv.Atoi(srv.PortLabel)
		if err != nil {
			return fmt.Errorf("service with non-numerical prot label is not supported: %w", err)
		}
		k8sSrv.Spec.Ports = append(k8sSrv.Spec.Ports, corev1.ServicePort{
			Port: int32(port),
			TargetPort: intstr.IntOrString{
				IntVal: int32(port),
			},
		})

		selector := genPodSelector(tg)

		k8sSrv.Labels = selector
		k8sSrv.Spec.Selector = selector

		if srv.Connect != nil {
			// TODO: tho we dont need connect block,
			// there might be some information can be reported
			logger.Debug().Msg("connect block is purposefully left out")
		}

		t.K8sServices = append(t.K8sServices, &k8sSrv)
	}

	return nil
}

func logUnspportedPortUsage(logger zerolog.Logger, resvPort []api.Port, dynPort []api.Port) {
	for _, port := range resvPort {
		logger.Warn().Str("port_label", port.Label).Msg("host port not supported")
	}
	for _, port := range dynPort {
		if strings.ToLower(port.Label) == "exposed" {
			// this is a special keyword used in our job files.
			// exposed port is for health checking against mesh jobs
			continue
		}
		logger.Warn().Str("port_label", port.Label).Msg("host port not supported")
	}
}

func genEnv(logger zerolog.Logger, env map[string]string) []corev1.EnvVar {
	res := make([]corev1.EnvVar, 0, len(env))

	// TODO: process some of the env vars in reasonable ways
	// things such as meta.unique.host_ip..
	for k, v := range env {
		k := k
		v := v
		if strings.Contains(k, ":") {
			orik := k
			k = strings.ReplaceAll(k, ":", "__")
			logger.Info().Str("old", orik).Str("new", k).Msg("env var with : not allowed, renaming")
		}

		res = append(res, corev1.EnvVar{
			Name:  k,
			Value: v,
		})
	}

	return res
}

func setImage(c *corev1.Container, config map[string]interface{}) {
	img, ok := config["image"]
	if !ok {
		panic(errors.Wrap(ErrOnlyContainerSupport, fmt.Sprintf("%v is not supported", config)))
	}
	c.Image = img.(string)
	delete(config, "image")
}

func setArgs(c *corev1.Container, config map[string]interface{}) {
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

func (t *Translator) setTemplates(logger zerolog.Logger, c *corev1.Container, task *api.Task) error {
	for _, tpl := range task.Templates {
		changeModeIsRestart := tpl.ChangeMode == nil || *tpl.ChangeMode == "restart"
		changeModeIsNoop := tpl.ChangeMode != nil && *tpl.ChangeMode == "noop"
		isEnv := tpl.Envvars != nil && *tpl.Envvars
		// baseDest := path.Base(*tpl.DestPath)

		if tpl.EmbeddedTmpl == nil {
			logger.Warn().Str("source", *tpl.SourcePath).Str("dest", *tpl.DestPath).Msg("only embedded data is supported, tpl from remote source ignored")
			continue
		}

		if !changeModeIsRestart && !changeModeIsNoop {
			logger.Warn().Str("mode", *tpl.ChangeMode).Str("dest", *tpl.DestPath).Msg("only restart/noop change modes are supported. will not handle the change mode for this, need to FIX accordingly")
		}

		// tpl := template.New(baseDest)
		// tpl.Mode = parse.SkipFuncCheck
		// tpl.Parse()
		tplContent, err := t.evalTemplate(logger, c, tpl)
		if err != nil {
			t.Notices = append(t.Notices, NoticeItem{
				Importance: NoticeBroken,
				Msg:        fmt.Sprintf("template %s of task %s is not parsable, plz review the translation manually", *tpl.DestPath, task.Name),
			})
			// TODO: how to best handle unparsable?
			logger.Error().Err(err).Msg("parsing failed")
			continue
			// return err
		}
		logger.Debug().Str("content", tplContent).Str("dest", *tpl.DestPath).Msg("rendered default tpl")

		if isEnv {
			logger.Debug().Msg("processing env template")
			// handling it like default env
			// envFrom:
			// - configMapRef:
			//     name: device-state-api-config-default
			// - configMapRef:
			//     name: device-state-api-config

			// https://www.baeldung.com/linux/kubernetes-pod-environment-variables#1-order-of-precedence
			cmName := t.getDefaultConfigMapName(*tpl.DestPath)
			cm, err := formatEnvvarConfigMap(cmName, tplContent)
			if err != nil {
				return err
			}
			t.K8sConfigMpas = append(t.K8sConfigMpas, cm)
			c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{
				ConfigMapRef: &corev1.ConfigMapEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cmName,
					},
				},
			})

			// TODO: should i create different configMap or the same?
			// My Answer:
			// Since for env we are mounting everything, so its better to keep it separate.
			// but for the volume mounted configMap, we have the option to mount individual files
			// so there we can create a single configMap and have different inner entries in there.

		} else {
			// handling it as a layer in projected volumes
			// volumes:
			// - name: all-in-one
			//   projected:
			// 	 sources:
			// 	 - configMap:
			// 	 	name: device-state-api-config-default
			// 	 - configMap:
			// 	 	name: device-state-api-config
			// 	 	optional: true

			// https://kubernetes.io/docs/concepts/storage/projected-volumes/
			// TODO: handle volumes
		}
	}

	return nil
}

func (t *Translator) getServiceFileName(name string) string {
	prefix := t.GetNamePrefix()
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}
	return name + "-service.yml"
}

func (t *Translator) getConfigMapFileName(name string) string {
	prefix := t.GetNamePrefix()
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}
	return name + "-config-map.yml"
}

func (t *Translator) getDeploymentFilename(name string) string {
	prefix := t.GetNamePrefix()
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}

	if strings.HasSuffix(name, "-group") {
		return name[:len(name)-6] + "-deployment.yml"
	}
	return name + "-deployment.yml"
}

func (t *Translator) GetNamePrefix() string {
	if t.NamePrefix != "" {
		return t.NamePrefix
	}

	if strings.HasSuffix(*t.Job.Name, "-job") {
		t.NamePrefix = (*t.Job.Name)[:len(*t.Job.Name)-4]
	} else {
		t.NamePrefix = *t.Job.Name
	}

	return t.NamePrefix
}

func (t *Translator) evalTemplate(logger zerolog.Logger, c *corev1.Container, nmdTpl *api.Template) (string, error) {
	logger = logger.With().Str("dest", *nmdTpl.DestPath).Logger()

	// baseDest := path.Base(*nmdTpl.DestPath)
	var err error

	tpl := template.New(*nmdTpl.DestPath)
	tpl.Funcs(stubTplFuncs(logger))
	tpl, err = tpl.Parse(*nmdTpl.EmbeddedTmpl)
	if err != nil {
		logger.Error().Err(err).Msg("failed to parse template")
		return "", err
	}

	buf := bytes.Buffer{}
	err = tpl.Execute(&buf, nil)

	return buf.String(), err
}

func stubTplFuncs(logger zerolog.Logger) template.FuncMap {
	return template.FuncMap{
		"keyOrDefault": func(s, def string) (string, error) {
			return def, nil
		},
		"secret": func(s ...string) (interface{}, error) {
			logger.Warn().Msg("secret is not supporetd")
			return map[string]interface{}{
				"Data": map[string]interface{}{
					// "data": map[string]interface{}{
					// 	"DEVICE_STATE_LOGGING_TOKEN": "FUCKINGA",
					// },
				},
			}, nil
		},
	}
}

func (t *Translator) getConfigMapName(name string) string {
	basename := t.GetNamePrefix() + "-" + strings.ReplaceAll(name, "/", "-")
	return strings.TrimSuffix(basename, filepath.Ext(basename))
}

func (t *Translator) getDefaultConfigMapName(name string) string {
	return t.getConfigMapName(name) + "-default"
}

func formatEnvvarConfigMap(name, content string) (*corev1.ConfigMap, error) {
	res := corev1.ConfigMap{}
	res.Name = name
	res.APIVersion = "v1"
	res.Kind = "ConfigMap"

	res.Data = map[string]string{}

	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		idx := strings.Index(l, "=")
		if idx == -1 {
			// TODO: add log
			return nil, errors.New("malformated line")
		}
		res.Data[l[:idx]] = l[idx+1:]
	}
	return &res, nil
}

func genPodSelector(tg *api.TaskGroup) map[string]string {
	return map[string]string{
		"app": *tg.Name,
	}
}
