package translator

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/hashicorp/nomad/api"
	vsov1 "github.com/hashicorp/vault-secrets-operator/api/v1beta1"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	DefaultChangeMode              = "restart"
	ReloaderConfigMapAnnotationKey = "configmap.reloader.stakater.com/reload"
	ReloaderSecretAnnotationKey    = "secret.reloader.stakater.com/reload"
)

var (
	ErrNoNetworkBlock       = errors.New("network mode has to be provided")
	ErrOnlyContainerSupport = errors.New("pod only support container")
)

var shouldOpenAIGen = true

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

type K8sResourceInfo struct {
	metav1.TypeMeta `json:",inline"`
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`
}

type Translator struct {
	DestPath   string
	NamePrefix string
	Job        *api.Job

	Notices Notices

	K8sCRs         []client.Object
	K8sDeployemnts []*v1.Deployment

	// When Generation CMs, the name could collide based on the original nomad template
	// destination attribute
	// thus using a map to better test collision than slice
	K8sConfigMaps map[string]*corev1.ConfigMap

	K8sSecret map[string]*corev1.Secret

	K8sServices []*corev1.Service

	K8sHTTPRoute []*gwv1.HTTPRoute
}

// A job in nomad can contain muliple groups. Each group is mapped to a deployment (and a corresponding service)

func (t *Translator) Process() error {
	if t.K8sConfigMaps == nil {
		t.K8sConfigMaps = make(map[string]*corev1.ConfigMap)
	}
	if t.K8sSecret == nil {
		t.K8sSecret = make(map[string]*corev1.Secret)
	}

	// TODO t.Job.Affinities to preferredDuringSchedulingIgnoredDuringExecution node affinity
	// https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/

	// TODO t.Job.Constraints to requiredDuringSchedulingIgnoredDuringExecution node affinity or nodeselector

	for _, group := range t.Job.TaskGroups {
		if err := t.ProcessTaskGroup(group); err != nil {
			return err
		}
	}

	for _, cm := range t.K8sConfigMaps {
		f, err := os.Create(path.Join(t.DestPath, t.getConfigMapFileName(cm.Name)))
		if err != nil {
			return err
		}
		content, _ := yaml.Marshal(cm)
		_, _ = f.Write(content)
		f.Close()
	}

	// if openai generation is enabled, the secret will be translated into dynamic ones using Vault Secret Operator
	if !shouldOpenAIGen {
		for _, sec := range t.K8sSecret {
			f, err := os.Create(path.Join(t.DestPath, t.getSecretFileName(sec.Name)))
			if err != nil {
				return err
			}
			content, _ := yaml.Marshal(sec)
			_, _ = f.Write(content)
			f.Close()
		}
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

	for _, r := range t.K8sHTTPRoute {
		f, err := os.Create(path.Join(t.DestPath, t.getRouteFileName(r.Name)))
		if err != nil {
			return err
		}
		content, _ := yaml.Marshal(r)
		_, _ = f.Write(content)
		f.Close()
	}

	for _, cr := range t.K8sCRs {
		cr.GetName()
		f, err := os.Create(path.Join(t.DestPath, t.getCRFileName(cr)))
		if err != nil {
			return err
		}
		content, _ := yaml.Marshal(cr)
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
	dep.Labels = make(map[string]string)

	dep.SetName(*tg.Name)

	depSepc := v1.DeploymentSpec{}

	podTplLabel := genPodSelector(tg)
	podTplSelector := metav1.LabelSelector{
		MatchLabels: genPodSelector(tg),
	}
	depSepc.Selector = &podTplSelector

	// count
	depSepc.Replicas = ptr.To(int32(*tg.Count))

	// scaling
	if tg.Scaling != nil {
		// TODO this might be able to map to HPA?
		// https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/
		// logger.Warn().Msg("scaling block is not supported")
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
	t.logUnspportedPortUsage(logger, tg, network.ReservedPorts, network.DynamicPorts)

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
		logger = logger.With().Str("task", task.Name).Logger()
		// TODO check all members of task
		// https://developer.hashicorp.com/nomad/docs/job-specification/task
		c := corev1.Container{}

		if task.Driver != "docker" {
			return nil, &TranslateError{Type: "task", Name: task.Name, Err: &DriverNotSupportedError{
				Type: task.Driver,
			}}
		}

		c.Name = task.Name
		c.Env = t.genEnv(logger, task.Env)

		// logs
		// TODO: is it able to be handled? maybe not
		// https://kubernetes.io/docs/concepts/cluster-administration/logging/#log-rotation

		// template
		cmReloaderTags, secretReloaderTags, err := t.setTemplates(logger, &c, &podSpec, task)
		if err != nil {
			return nil, err
		}

		dep.Labels[ReloaderConfigMapAnnotationKey] = cmReloaderTags
		dep.Labels[ReloaderSecretAnnotationKey] = secretReloaderTags

		setImage(&c, task.Config)
		setArgs(&c, task.Config)
		delete(task.Config, "cpu_hard_limit")

		if mnt, ok := task.Config["mount"]; ok {
			t.Notices = append(t.Notices, NoticeItem{
				Importance: NoticeBroken,
				Msg:        fmt.Sprintf("task %s has mount (%v) block in config which is not supported. For remounting templates, directly mount the configmap/secret to the destination. For mounting from host, add a hostpath volume", task.Name, mnt),
			})
			delete(task.Config, "mount")
		}
		if mnt, ok := task.Config["volumes"]; ok {
			t.Notices = append(t.Notices, NoticeItem{
				Importance: NoticeBroken,
				Msg:        fmt.Sprintf("task %s has volumes (%v) block in config which is not supported. For remounting templates, directly mount the configmap/secret to the destination. For mounting from host, add a hostpath volume", task.Name, mnt),
			})
			delete(task.Config, "volumes")
		}

		if len(task.Config) != 0 {
			logger.Warn().Any("config", task.Config).Msg("unsupported fields in config")
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
		logger = logger.With().Str("service", srv.Name).Logger()

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

		t.genHttpRoute(logger, tg, srv, &k8sSrv)
	}

	return nil
}

func (t *Translator) genHttpRoute(logger zerolog.Logger, tg *api.TaskGroup, srv *api.Service, k8sSrv *corev1.Service) {
	// main point for this is to translate ingress rules to Gateway API resource
	// e.g: "ingress-/v1/devices"

	if len(srv.Tags) == 0 {
		return
	}

	var pathPrefixes []string
	for _, tag := range srv.Tags {
		if strings.HasPrefix(tag, "ingress-/") {
			if strings.HasSuffix(tag, "/hc") {
				// these are the health check endpoints.. ignore them
				logger.Debug().Str("tag", tag).Msg("ignoring route gen for health check endpoints")
				continue
			}

			pathPrefix := tag[8:]
			logger.Debug().Str("pathprefix", pathPrefix).Msg("gen httproute for service")
			pathPrefixes = append(pathPrefixes, pathPrefix)
		}
	}

	if len(pathPrefixes) == 0 {
		return
	}

	httpRoute := gwv1.HTTPRoute{}
	httpRoute.APIVersion = "gateway.networking.k8s.io/v1beta1"
	httpRoute.Kind = "HTTPRoute"
	httpRoute.Name = srv.Name
	httpRoute.Spec.ParentRefs = []gwv1.ParentReference{
		{
			Namespace: (*gwv1.Namespace)(ptr.To("{{ gateway_namespace }}")),
			Name:      "{{ gateway_name }}",
		},
	}
	matchers := make([]gwv1.HTTPRouteMatch, 0, len(pathPrefixes))
	for _, prefix := range pathPrefixes {
		matchers = append(matchers, gwv1.HTTPRouteMatch{
			Path: &gwv1.HTTPPathMatch{
				Type:  ptr.To(gwv1.PathMatchPathPrefix),
				Value: ptr.To(prefix),
			},
		})
	}
	httpRoute.Spec.Rules = append(httpRoute.Spec.Rules, gwv1.HTTPRouteRule{
		Matches: matchers,
		BackendRefs: []gwv1.HTTPBackendRef{
			{
				BackendRef: gwv1.BackendRef{
					BackendObjectReference: gwv1.BackendObjectReference{
						Name:      gwv1.ObjectName(k8sSrv.Name),
						Port:      (*gwv1.PortNumber)(&k8sSrv.Spec.Ports[0].Port),
						Namespace: (*gwv1.Namespace)(ptr.To("{{ namespace }}")),
					},
				},
			},
		},
	})

	t.K8sHTTPRoute = append(t.K8sHTTPRoute, &httpRoute)

	t.Notices = append(t.Notices, NoticeItem{
		Importance: NoticeImportant,
		Msg:        "review all HTTPRoute resources to fix the gateway info, and service namespace",
	})
}

func (t *Translator) logUnspportedPortUsage(logger zerolog.Logger, tg *api.TaskGroup, resvPort []api.Port, dynPort []api.Port) {
	for _, port := range resvPort {
		t.Notices = append(t.Notices, NoticeItem{
			Importance: NoticeImportant,
			Msg:        fmt.Sprintf("group %s has static port (%s %d) which is not translated.", *tg.Name, port.Label, port.To),
		})
		// logger.Warn().Str("port_label", port.Label).Msg("host port not supported")
	}
	for _, port := range dynPort {
		if strings.ToLower(port.Label) == "exposed" {
			// this is a special keyword used in our job files.
			// exposed port is for health checking against mesh jobs
			continue
		}
		t.Notices = append(t.Notices, NoticeItem{
			Importance: NoticeImportant,
			Msg:        fmt.Sprintf("group %s has dynamic port (%s %d) which is not translated.", *tg.Name, port.Label, port.To),
		})
		// logger.Warn().Str("port_label", port.Label).Msg("host port not supported")
	}
}

var nomadVarRe = regexp.MustCompile(`\$\{.*\}`)

func (t *Translator) genEnv(logger zerolog.Logger, envs map[string]string) []corev1.EnvVar {
	downardApiMap := map[string]string{}

	res := make([]corev1.EnvVar, 0, len(envs))

	for k, v := range envs {
		orik := k
		k := k
		v := v
		if strings.Contains(k, ":") {
			k = strings.ReplaceAll(k, ":", "__")
			logger.Debug().Str("old", orik).Str("new", k).Msg("env var with : not allowed, renaming")
		}

		env := corev1.EnvVar{
			Name:  k,
			Value: v,
		}

		matched := nomadVarRe.FindStringSubmatch(v)
		if len(matched) > 0 {
			// this uses some of the nomad runtime variables: https://developer.hashicorp.com/nomad/docs/runtime/interpolation
			// change it to K8s downward api: https://kubernetes.io/docs/concepts/workloads/pods/downward-api/
			// and composes env var: https://kubernetes.io/docs/tasks/inject-data-application/define-interdependent-environment-variables/

			for _, nomadVWithDollar := range matched {
				nomadV := nomadVWithDollar[2 : len(nomadVWithDollar)-1]

				envVarName, ok := downardApiMap[nomadV]
				if !ok {
					dav, mapped := nomadVarToDownardApiVar(logger, nomadV)
					downardApiMap[nomadV] = dav.Name
					envVarName = dav.Name // assign the generated envvar name, could be empty if mapping failed.

					if mapped {
						res = append(res, dav)
					}
				}

				if envVarName == "" {
					t.Notices = append(t.Notices, NoticeItem{
						Importance: NoticeImportant,
						Msg:        fmt.Sprintf("env \"%s\" uses %s which is not supported in downward api", orik, nomadVWithDollar),
					})

					// there is no point of further substitute if some of the variable cannot be channged to downward api
					goto afterSubstitute
				}

				env.Value = strings.ReplaceAll(env.Value, nomadVWithDollar, "$("+envVarName+")")
			}
		}

	afterSubstitute:
		res = append(res, env)
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

func (t *Translator) setTemplates(logger zerolog.Logger, c *corev1.Container, podSpec *corev1.PodSpec, task *api.Task) (cmReloaderTagVal string, secretReloaderTagVal string, err error) {
	var cmReloaderTags []string
	var secretReloaderTags []string

	mountedPaths := map[string]*corev1.Volume{}

	for _, tpl := range task.Templates {
		logger = logger.With().Str("dest", *tpl.DestPath).Logger()
		changeModeIsRestart := tpl.ChangeMode == nil || *tpl.ChangeMode == "restart"
		changeModeIsNoop := tpl.ChangeMode != nil && *tpl.ChangeMode == "noop"
		isEnv := tpl.Envvars != nil && *tpl.Envvars
		// baseDest := path.Base(*tpl.DestPath)

		if tpl.EmbeddedTmpl == nil {
			logger.Warn().Str("source", *tpl.SourcePath).Msg("only embedded data is supported, tpl from remote source ignored")
			t.Notices = append(t.Notices, NoticeItem{
				Importance: NoticeBroken,
				Msg:        fmt.Sprintf("template %s of task %s does not have embeded template, remote source is not suppoted, thus ignored", *tpl.DestPath, task.Name),
			})
			continue
		}

		if !changeModeIsRestart && !changeModeIsNoop {
			logger.Warn().Str("mode", *tpl.ChangeMode).Msg("only restart/noop change modes are supported. will not handle the change mode for this, need to FIX accordingly")
			t.Notices = append(t.Notices, NoticeItem{
				Importance: NoticeImportant,
				Msg:        fmt.Sprintf("template %s of task %s has change mode of %s, only restart/noop change modes are supported. will not handle the change mode for this, need to FIX accordingly", *tpl.DestPath, task.Name, *tpl.ChangeMode),
			})
		}

		tplContent, isSecret, err := t.evalTemplate(logger, c, tpl)
		if err != nil {
			t.Notices = append(t.Notices, NoticeItem{
				Importance: NoticeBroken,
				Msg:        fmt.Sprintf("template %s of task %s is not parsable, plz review the translation manually", *tpl.DestPath, task.Name),
			})
			continue
		}

		if isEnv {
			logger.Debug().Msg("processing env template")
			// handling it like default env
			// envFrom:
			// - configMapRef:
			//     name: device-state-api-config-default
			// - configMapRef:
			//     name: device-state-api-config

			// https://www.baeldung.com/linux/kubernetes-pod-environment-variables#1-order-of-precedence

			cmName := ""
			unique := false
			cmIdx := 0
			for !unique {
				if !isSecret {
					cmName = t.getDefaultConfigMapName(*tpl.DestPath, cmIdx)
					cm, err := formatEnvvarConfigMap(logger, cmName, tplContent)
					if err != nil {
						return "", "", err
					}

					if cmExisting, ok := t.K8sConfigMaps[cmName]; ok && !reflect.DeepEqual(cm, cmExisting) {
						logger.Debug().Str("name", cmName).Msg("configmap name clashed, try next...")
						cmIdx++
					} else {
						unique = true
						t.K8sConfigMaps[cmName] = cm
					}
				} else {
					// dont use the default name for secret, the secret is not supposed to be
					// overlay'd
					cmName = t.getConfigMapName(*tpl.DestPath, cmIdx)
					cm, err := formatEnvvarSecret(logger, cmName, tplContent)
					if err != nil {
						return "", "", err
					}

					if cmExisting, ok := t.K8sSecret[cmName]; ok && !reflect.DeepEqual(cm, cmExisting) {
						logger.Debug().Str("name", cmName).Msg("configmap name clashed, try next...")
						cmIdx++
					} else {
						unique = true
						t.K8sSecret[cmName] = cm
						if shouldOpenAIGen {
							vSS := t.translateSecretUsingOpenAI(logger, isEnv, tpl, cmName)
							t.K8sCRs = append(t.K8sCRs, vSS)
						}
					}
				}
			}

			if !isSecret {
				c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{
					ConfigMapRef: &corev1.ConfigMapEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: cmName,
						},
					},
				})
				c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{
					ConfigMapRef: &corev1.ConfigMapEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: t.getConfigMapName(*tpl.DestPath, cmIdx),
						},
						Optional: ptr.To(true),
					},
				})

				// for configMap only reload the optional one.
				cmReloaderTags = append(cmReloaderTags, t.getConfigMapName(*tpl.DestPath, cmIdx))
			} else {
				c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{
					SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: t.getConfigMapName(*tpl.DestPath, cmIdx),
						},
					},
				})

				secretReloaderTags = append(secretReloaderTags, t.getConfigMapName(*tpl.DestPath, cmIdx))
			}
		} else {
			logger.Debug().Msg("processing volume template")
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

			cmName := ""
			unique := false
			cmIdx := 0
			for !unique {
				if !isSecret {
					cmName = t.getDefaultConfigMapName(*tpl.DestPath, cmIdx)
					cm, err := formatVolumeConfigMap(logger, cmName, tplContent, *tpl.DestPath)
					if err != nil {
						return "", "", err
					}

					if cmExisting, ok := t.K8sConfigMaps[cmName]; ok && !reflect.DeepEqual(cm, cmExisting) {
						logger.Debug().Str("name", cmName).Msg("configmap name clashed, try next...")
						cmIdx++
					} else {
						unique = true
						t.K8sConfigMaps[cmName] = cm
					}
				} else {
					// dont use the default name for secret, the secret is not supposed to be
					// overlay'd
					cmName = t.getConfigMapName(*tpl.DestPath, cmIdx)
					cm, err := formatVolumeSecret(logger, cmName, tplContent, *tpl.DestPath)
					if err != nil {
						return "", "", err
					}

					if cmExisting, ok := t.K8sSecret[cmName]; ok && !reflect.DeepEqual(cm, cmExisting) {
						logger.Debug().Str("name", cmName).Msg("configmap name clashed, try next...")
						cmIdx++
					} else {
						unique = true
						t.K8sSecret[cmName] = cm
						if shouldOpenAIGen {
							vSS := t.translateSecretUsingOpenAI(logger, isEnv, tpl, cmName)
							vSS.Spec.Destination.Transformation.Templates[filepath.Base(*tpl.DestPath)] = vSS.Spec.Destination.Transformation.Templates["content"]
							t.K8sCRs = append(t.K8sCRs, vSS)
						}
					}
				}
			}

			mountPath := getMountPath(*tpl.DestPath)
			var volRef *corev1.Volume
			if vol, ok := mountedPaths[mountPath]; ok {
				volRef = vol
			} else {
				volName := task.Name + strings.ReplaceAll(mountPath, "/", "-")

				volRef = &corev1.Volume{
					Name: volName,
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{},
					},
				}

				mountedPaths[mountPath] = volRef

				c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
					Name:      volName,
					MountPath: mountPath,
				})
			}

			if !isSecret {
				volRef.Projected.Sources = append(volRef.Projected.Sources, getVolumeProjectionRef(false, cmName, false))
				volRef.Projected.Sources = append(volRef.Projected.Sources, getVolumeProjectionRef(false, t.getConfigMapName(*tpl.DestPath, cmIdx), true))

				cmReloaderTags = append(cmReloaderTags, t.getConfigMapName(*tpl.DestPath, cmIdx))
			} else {
				volRef.Projected.Sources = append(volRef.Projected.Sources, getVolumeProjectionRef(true, t.getConfigMapName(*tpl.DestPath, cmIdx), false))

				secretReloaderTags = append(secretReloaderTags, t.getConfigMapName(*tpl.DestPath, cmIdx))
			}
		}
	}

	for _, vol := range mountedPaths {
		podSpec.Volumes = append(podSpec.Volumes, *vol)
	}

	return strings.Join(cmReloaderTags, ","), strings.Join(secretReloaderTags, ","), nil
}

func (t *Translator) translateSecretUsingOpenAI(logger zerolog.Logger, isEnv bool, tpl *api.Template, cmName string) *vsov1.VaultStaticSecret {
	logger.Debug().Msg("start translating using openai")
	oaiTranslateResp, err := CachedTranslateSecretUsingOpenAI(logger, isEnv, *tpl.EmbeddedTmpl)
	if err != nil {
		panic(err)
	}
	logger.Info().Float32("confidence", oaiTranslateResp.Confidence).Msg("got translation result")
	if oaiTranslateResp.Confidence < 7 {
		logger.Debug().Msgf("translation explanation: %s", oaiTranslateResp.Explanation)
		t.Notices = append(t.Notices, NoticeItem{
			Importance: NoticeImportant,
			Msg:        fmt.Sprintf("template %s has a low confidence score (%f) in translation, plz review the translation manually", *tpl.DestPath, oaiTranslateResp.Confidence),
		})
	}
	vSS := oaiTranslateResp.Result
	vSS.Name = cmName
	vSS.Spec.Destination.Name = cmName
	vSS.Spec.RefreshAfter = "1m"
	// vSS.Spec.VaultAuthRef = t.GetNamePrefix()
	vSS.Spec.VaultAuthRef = "default"
	vSS.Annotations = make(map[string]string)
	vSS.Annotations["confidence"] = fmt.Sprintf("%.1f", oaiTranslateResp.Confidence)
	vSS.Spec.Destination.Transformation.Excludes = []string{".*"}
	vSS.Spec.Destination.Transformation.Templates["__secret_translate_explanation"] = vsov1.Template{
		Text: oaiTranslateResp.Explanation,
	}
	return &vSS
}

func (t *Translator) getCRFileName(cr client.Object) string {
	prefix := t.GetNamePrefix()
	name := cr.GetName()
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}

	return fmt.Sprintf("%s-%s.yml", name, cr.GetObjectKind().GroupVersionKind().Kind)
}

func (t *Translator) getServiceFileName(name string) string {
	prefix := t.GetNamePrefix()
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}
	return name + "-service.yml"
}

func (t *Translator) getRouteFileName(name string) string {
	prefix := t.GetNamePrefix()
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}
	return name + "-route.yml"
}

func (t *Translator) getConfigMapFileName(name string) string {
	prefix := t.GetNamePrefix()
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}
	return name + "-config-map.yml"
}

func (t *Translator) getSecretFileName(name string) string {
	prefix := t.GetNamePrefix()
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}
	return name + "-secret.yml"
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

func (t *Translator) evalTemplate(logger zerolog.Logger, c *corev1.Container, nmdTpl *api.Template) (content string, isSecret bool, err error) {
	logger = logger.With().Str("dest", *nmdTpl.DestPath).Logger()

	// baseDest := path.Base(*nmdTpl.DestPath)

	tpl := template.New(*nmdTpl.DestPath)
	tpl.Funcs(t.stubTplFuncs(logger, nmdTpl, &isSecret))
	tpl, err = tpl.Parse(*nmdTpl.EmbeddedTmpl)
	if err != nil {
		logger.Error().Err(err).Msg("failed to parse template")
		return "", false, err
	}

	buf := bytes.Buffer{}
	err = tpl.Execute(&buf, nil)

	return buf.String(), isSecret, err
}

func (t *Translator) stubTplFuncs(logger zerolog.Logger, tpl *api.Template, isSecret *bool) template.FuncMap {
	return template.FuncMap{
		"keyOrDefault": func(s, def string) (string, error) {
			return def, nil
		},
		"secret": func(s ...string) (interface{}, error) {
			if !shouldOpenAIGen {
				logger.Warn().Msg("secret is not supporetd")
				t.Notices = append(t.Notices, NoticeItem{
					Importance: NoticeBroken,
					Msg:        "plz review ALL secrete type resources, translation is incomplete.",
					// Msg:        fmt.Sprintf("template %s of task %s is a secrete type, plz review the translation manually", *tpl.DestPath, task.Name),
				})
			} else {
				t.Notices = append(t.Notices, NoticeItem{
					Importance: NoticeImportant,
					Msg:        "plz review ALL secrete type resources, translation is done to use VaultStaticSecret template, translation might not be accurate.",
				})
			}

			*isSecret = true

			return map[string]interface{}{
				"Data": map[string]interface{}{},
			}, nil
		},
	}
}

// getConfigMapName gets the resource name for both configmap and secret
func (t *Translator) getConfigMapName(name string, idx int) string {
	basename := t.GetNamePrefix() + "-" + strings.ReplaceAll(name, "/", "-")
	basename = strings.TrimSuffix(basename, filepath.Ext(basename))
	if idx != 0 {
		return basename + strconv.Itoa(idx)
	}
	return sanitizeResourceName(strings.TrimSuffix(basename, filepath.Ext(basename)))
}

func (t *Translator) getDefaultConfigMapName(name string, idx int) string {
	return t.getConfigMapName(name, idx) + "-default"
}

var validNameRe = regexp.MustCompile(`[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*`)

func sanitizeResourceName(in string) string {
	// https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#dns-subdomain-names

	res := strings.ToLower(strings.ReplaceAll(in, "_", "-"))
	if !validNameRe.MatchString(res) {
		panic(fmt.Errorf("failed to sanitize name: (%s -> %s): the processed name does not fits validation regex: %s", in, res, validNameRe.String()))
	}
	if len(res) > 253 {
		panic(fmt.Errorf("resource name %s is too long (limit 253)", res))
	}

	return strings.ToLower(strings.ReplaceAll(in, "_", "-"))
}

func formatVolumeConfigMap(logger zerolog.Logger, name, content, destPath string) (*corev1.ConfigMap, error) {
	res := corev1.ConfigMap{}
	res.Name = name
	res.APIVersion = "v1"
	res.Kind = "ConfigMap"

	res.Data = map[string]string{
		filepath.Base(destPath): content,
	}

	return &res, nil
}

func formatVolumeSecret(logger zerolog.Logger, name, content, destPath string) (*corev1.Secret, error) {
	res := corev1.Secret{}
	res.Name = name
	res.APIVersion = "v1"
	res.Kind = "Secret"

	res.StringData = map[string]string{
		filepath.Base(destPath): content,
	}

	return &res, nil
}

func tplContentToEnvvar(logger zerolog.Logger, content string) (map[string]string, error) {
	res := map[string]string{}

	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		idx := strings.Index(l, "=")
		if idx == -1 {
			logger.Error().Str("line", l).Msg("malformated")
			return nil, errors.New("malformated line")
		}
		res[l[:idx]] = l[idx+1:]
	}
	return res, nil
}

func formatEnvvarConfigMap(logger zerolog.Logger, name, content string) (*corev1.ConfigMap, error) {
	var err error

	res := corev1.ConfigMap{}
	res.Name = name
	res.APIVersion = "v1"
	res.Kind = "ConfigMap"

	res.Data, err = tplContentToEnvvar(logger, content)
	return &res, err
}

func formatEnvvarSecret(logger zerolog.Logger, name, content string) (*corev1.Secret, error) {
	var err error

	res := corev1.Secret{}
	res.Name = name
	res.APIVersion = "v1"
	res.Kind = "Secret"

	res.StringData, err = tplContentToEnvvar(logger, content)
	return &res, err
}

func genPodSelector(tg *api.TaskGroup) map[string]string {
	return map[string]string{
		"app": *tg.Name,
	}
}

func getMountPath(path string) string {
	return filepath.Dir("/" + path)
}

func getVolumeProjectionRef(isSecret bool, name string, optional bool) corev1.VolumeProjection {
	if isSecret {
		return corev1.VolumeProjection{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: name,
				},
				Optional: &optional,
			},
		}
	}

	return corev1.VolumeProjection{
		ConfigMap: &corev1.ConfigMapProjection{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: name,
			},
			Optional: &optional,
		},
	}
}

func nomadVarToDownardApiVar(logger zerolog.Logger, v string) (corev1.EnvVar, bool) {
	switch v {
	case "attr.unique.network.ip-address":
		return corev1.EnvVar{
			Name: nomadVarToEnvvarName(v),
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.hostIP",
				},
			},
		}, true
	case "attr.unique.hostname":
		return corev1.EnvVar{
			Name: nomadVarToEnvvarName(v),
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "spec.nodeName",
				},
			},
		}, true
	default:
		logger.Warn().Str("var", v).Msg("failed to find corresponding downward api")
		return corev1.EnvVar{}, false
	}
}

func nomadVarToEnvvarName(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, ".", "_"), "-", "_")
}
