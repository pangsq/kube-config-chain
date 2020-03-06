package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	yaml "gopkg.in/yaml.v2"
	appsV1beta1 "k8s.io/api/apps/v1beta1"
	v1 "k8s.io/api/core/v1"
	v1beta1 "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var namespace string
var kubeconfig string
var format string
var simplify bool
var configName string

func main() {
	flag.StringVar(&namespace, "n", "default", "指定命名空间")
	flag.StringVar(&kubeconfig, "k", "/etc/kubernetes/kubelet.conf", "kube配置文件")
	flag.StringVar(&format, "f", "yaml", "展示格式(支持yaml、json)")
	flag.StringVar(&configName, "c", ".*", "挑选配置项(支持正则匹配)")
	flag.BoolVar(&simplify, "s", false, "精简输出")

	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}
	configChain, err := readConfigChains(config, namespace)
	if err != nil {
		panic(err.Error())
	}
	if configName != "" && configName != "*" && configName != ".*" {
		re, err := regexp.Compile(configName)
		if err != nil {
			panic(err.Error())
		}
		for name, _ := range configChain {
			if !re.MatchString(name) {
				delete(configChain, name)
			}
		}
	}
	var output string
	var configChainForDisplay interface{}
	if simplify {
		configChainForDisplay = formatConfigChains(&configChain, false)
	} else {
		configChainForDisplay = configChain
	}
	switch format {
	case "json":
		outputBytes, err := json.MarshalIndent(configChainForDisplay, "", " ")
		if err != nil {
			panic(err.Error())
		}
		output = string(outputBytes)
	case "yaml":
		outputBytes, err := yaml.Marshal(configChainForDisplay)
		if err != nil {
			panic(err.Error())
		}
		output = string(outputBytes)
	default:
		panic("no such format: " + format)
	}
	fmt.Println(output)
}

func readConfigChains(config *rest.Config, namespace string) (map[string]*ConfigChain, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("Can not create clientset: " + err.Error())
	}
	configChains := make(map[string]*ConfigChain)
	configmaps, err := clientset.Core().ConfigMaps(namespace).List(metav1.ListOptions{})
	for _, configmap := range configmaps.Items {
		keyUsed := make(map[string]*KeyUsed)
		for keyName := range configmap.Data {
			keyUsed[keyName] = &KeyUsed{}
		}
		name := configmapKey(configmap.Name)
		configChains[name] = &ConfigChain{
			Name:         configmap.Name,
			Type:         "configmap",
			Namespace:    configmap.Namespace,
			UsedAsVolumn: []UsedBy{},
			KeyUsed:      keyUsed,
		}
	}
	secrets, err := clientset.Core().Secrets(namespace).List(metav1.ListOptions{})
	for _, secret := range secrets.Items {
		keyUsed := make(map[string]*KeyUsed)
		for keyName := range secret.Data {
			keyUsed[keyName] = &KeyUsed{}
		}
		name := secretKey(secret.Name)
		configChains[name] = &ConfigChain{
			Name:         secret.Name,
			Type:         "secret",
			Namespace:    secret.Namespace,
			UsedAsVolumn: []UsedBy{},
			KeyUsed:      keyUsed,
		}
	}
	deployments, err := clientset.ExtensionsV1beta1().Deployments(namespace).List(metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Can not list deployments: " + err.Error())
	}
	for _, deployment := range deployments.Items {
		usedBy := NewUsedByDeployment(&deployment)
		caculateConfigChain(&configChains, &deployment.Spec.Template.Spec, usedBy)
	}
	daemonsets, err := clientset.ExtensionsV1beta1().DaemonSets(namespace).List(metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Can not list daemonsets: " + err.Error())
	}
	for _, daemonset := range daemonsets.Items {
		usedBy := NewUsedByDaemonSet(&daemonset)
		caculateConfigChain(&configChains, &daemonset.Spec.Template.Spec, usedBy)
	}
	statefulsets, err := clientset.AppsV1beta1().StatefulSets(namespace).List(metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Can not list statefulsets: " + err.Error())
	}
	for _, statefulset := range statefulsets.Items {
		usedBy := NewUsedByStatefuleSet(&statefulset)
		caculateConfigChain(&configChains, &statefulset.Spec.Template.Spec, usedBy)
	}
	rcs, err := clientset.CoreV1().ReplicationControllers(namespace).List(metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Can not list rcs: " + err.Error())
	}
	for _, rc := range rcs.Items {
		usedBy := NewUsedByRC(&rc)
		caculateConfigChain(&configChains, &rc.Spec.Template.Spec, usedBy)
	}
	return configChains, nil
}

func caculateConfigChain(configChains *map[string]*ConfigChain, podSpec *v1.PodSpec, usedBy UsedBy) {
	for _, volume := range podSpec.Volumes {
		configmapSource := volume.VolumeSource.ConfigMap
		if configmapSource != nil {
			name := configmapKey(configmapSource.Name)
			if len(configmapSource.Items) == 0 {
				appendUsedAsVolumn(configChains, name, usedBy)
			} else {
				for _, item := range configmapSource.Items {
					appendKeyUsedAsVolumn(configChains, name, item.Key, usedBy)
				}
			}
		}
		secretSource := volume.VolumeSource.Secret
		if secretSource != nil {
			name := secretKey(secretSource.SecretName)
			if len(secretSource.Items) == 0 {
				appendUsedAsVolumn(configChains, name, usedBy)
			} else {
				for _, item := range secretSource.Items {
					appendKeyUsedAsVolumn(configChains, name, item.Key, usedBy)
				}
			}
		}
	}

	for _, container := range podSpec.Containers {
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			configMapKeyRef := env.ValueFrom.ConfigMapKeyRef
			if configMapKeyRef != nil {
				name := configmapKey(configMapKeyRef.Name)
				keyName := configMapKeyRef.Key
				appendKeyUsedAsEnv(configChains, name, keyName, usedBy)
			}
			secretKeyRef := env.ValueFrom.SecretKeyRef
			if secretKeyRef != nil {
				name := secretKey(secretKeyRef.Name)
				keyName := secretKeyRef.Key
				appendKeyUsedAsEnv(configChains, name, keyName, usedBy)
			}
		}
	}
}

func configmapKey(name string) string {
	return fmt.Sprintf("configmap/%s", name)
}

func secretKey(name string) string {
	return fmt.Sprintf("secret/%s", name)
}

func appendUsedAsVolumn(configChains *map[string]*ConfigChain, name string, usedBy UsedBy) {
	getOrUnexpected(configChains, name).UsedAsVolumn = append(getOrUnexpected(configChains, name).UsedAsVolumn, usedBy)
}

func appendKeyUsedAsEnv(configChains *map[string]*ConfigChain, name string, keyName string, usedBy UsedBy) {
	if keyUsed, ok := getOrUnexpected(configChains, name).KeyUsed[keyName]; ok {
		(*keyUsed).AsEnv = append((*keyUsed).AsEnv, usedBy)
	} else {
		panic(fmt.Sprintf("%s is not in %s", keyName, name))
	}
}

func appendKeyUsedAsVolumn(configChains *map[string]*ConfigChain, name string, keyName string, usedBy UsedBy) {
	if keyUsed, ok := getOrUnexpected(configChains, name).KeyUsed[keyName]; ok {
		(*keyUsed).AsVolumn = append((*keyUsed).AsVolumn, usedBy)
	} else {
		// panic(fmt.Sprintf("%s is not in %s", keyName, name))
		fmt.Fprintln(os.Stderr, fmt.Sprintf("%s is not in %s", keyName, name))
	}
}

func getOrUnexpected(configChains *map[string]*ConfigChain, name string) *ConfigChain {
	if configChain, ok := (*configChains)[name]; ok {
		return configChain
	} else {
		configChain := &ConfigChain{
			Name:         name,
			Type:         "unexpected/" + strings.Split(name, "/")[0],
			UsedAsVolumn: []UsedBy{},
			KeyUsed:      map[string]*KeyUsed{},
		}
		(*configChains)[name] = configChain
		return configChain
	}
}

type ConfigChain struct {
	Name         string
	Type         string
	Namespace    string
	UsedAsVolumn []UsedBy            `json:"usedAsVolumn" yaml:"usedAsVolumn"`
	KeyUsed      map[string]*KeyUsed `json:"keyUsed" yaml:"keyUsed"`
}

type KeyUsed struct {
	AsEnv    []UsedBy `json:"asEnv" yaml:"asEnv"`
	AsVolumn []UsedBy `json:"asVolumn" yaml:"asVolumn"`
}

type UsedBy string

func NewUsedByDeployment(deployment *v1beta1.Deployment) UsedBy {
	return UsedBy("deployment/" + deployment.Name)
}

func NewUsedByRC(rc *v1.ReplicationController) UsedBy {
	return UsedBy("rc/" + rc.Name)
}

func NewUsedByStatefuleSet(statefulset *appsV1beta1.StatefulSet) UsedBy {
	return UsedBy("statefulset/" + statefulset.Name)
}

func NewUsedByDaemonSet(daemonset *v1beta1.DaemonSet) UsedBy {
	return UsedBy("daemonset/" + daemonset.Name)
}

func formatConfigChains(configChains *map[string]*ConfigChain, withNamespace bool) map[string]interface{} {
	configChainsForDisplay := make(map[string]interface{})
	for name, configChain := range *configChains {
		configChainForDisplay := make(map[string]interface{})
		if withNamespace {
			configChainForDisplay["namespace"] = configChain.Namespace
		}
		if len(configChain.UsedAsVolumn) != 0 {
			configChainForDisplay["usedAsVolumn"] = configChain.UsedAsVolumn
		}
		keyUseds := make(map[string]interface{})
		for keyName, keyUsed := range configChain.KeyUsed {
			keyUsedItem := make(map[string]interface{})
			if len(keyUsed.AsEnv) != 0 {
				keyUsedItem["asEnv"] = keyUsed.AsEnv
			}
			if len(keyUsed.AsVolumn) != 0 {
				keyUsedItem["asVolume"] = keyUsed.AsVolumn
			}
			if len(keyUsedItem) != 0 {
				keyUseds[keyName] = keyUsedItem
			}
		}
		if len(keyUseds) != 0 {
			configChainForDisplay["keyUsed"] = keyUseds
		}
		configChainsForDisplay[name] = configChainForDisplay
	}
	return configChainsForDisplay
}
