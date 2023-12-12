package checkers

import (
	"context"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/joe-elliott/cert-exporter/src/exporters"
	"github.com/joe-elliott/cert-exporter/src/metrics"
)

// PeriodicConfigMapChecker is an object designed to check for files on disk at a regular interval
type PeriodicConfigMapChecker struct {
	period                     time.Duration
	labelSelectors             []string
	kubeconfigPath             string
	annotationSelectors        []string
	namespaces                 []string
	exporter                   *exporters.ConfigMapExporter
	includeConfigMapsDataGlobs []string
	excludeConfigMapsDataGlobs []string
}

// NewConfigMapChecker is a factory method that returns a new PeriodicConfigMapChecker
func NewConfigMapChecker(period time.Duration, labelSelectors, includeConfigMapsDataGlobs, excludeConfigMapsDataGlobs, annotationSelectors, namespaces []string, kubeconfigPath string, e *exporters.ConfigMapExporter) *PeriodicConfigMapChecker {
	return &PeriodicConfigMapChecker{
		period:                     period,
		labelSelectors:             labelSelectors,
		annotationSelectors:        annotationSelectors,
		namespaces:                 namespaces,
		kubeconfigPath:             kubeconfigPath,
		exporter:                   e,
		includeConfigMapsDataGlobs: includeConfigMapsDataGlobs,
		excludeConfigMapsDataGlobs: excludeConfigMapsDataGlobs,
	}
}

// StartChecking starts the periodic file check.  Most likely you want to run this as an independent go routine.
func (p *PeriodicConfigMapChecker) StartChecking() {
	config, err := clientcmd.BuildConfigFromFlags("", p.kubeconfigPath)
	if err != nil {
		glog.Fatalf("Error building kubeconfig: %s", err.Error())
	}

	// creates the clientset
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Fatalf("kubernetes.NewForConfig failed: %v", err)
	}

	periodChannel := time.Tick(p.period)

	if strings.Join(p.namespaces, ", ") != "" {
		glog.Infof("Scan configMaps in %v", strings.Join(p.namespaces, ", "))
	}
	for {
		glog.Info("Begin periodic check")

		p.exporter.ResetMetrics()

		var configMaps []corev1.ConfigMap
		for _, ns := range p.namespaces {
			if len(p.labelSelectors) > 0 {
				for _, labelSelector := range p.labelSelectors {
					var c *corev1.ConfigMapList
					c, err = client.CoreV1().ConfigMaps(ns).List(context.TODO(), metav1.ListOptions{
						LabelSelector: labelSelector,
					})
					if err != nil {
						glog.Errorf("Error requesting configMaps %v", err)
						metrics.ErrorTotal.Inc()
						continue
					}
					configMaps = append(configMaps, c.Items...)
				}
			} else {
				var c *corev1.ConfigMapList
				c, err = client.CoreV1().ConfigMaps(ns).List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					glog.Errorf("Error requesting configMaps %v", err)
					metrics.ErrorTotal.Inc()
					continue
				}
				configMaps = append(configMaps, c.Items...)
			}
		}

		for _, configMap := range configMaps {
			include, exclude := false, false
			glog.Infof("Reviewing configMap %v in %v", configMap.GetName(), configMap.GetNamespace())

			if len(p.annotationSelectors) > 0 {
				matches := false
				annotations := configMap.GetAnnotations()
				for _, selector := range p.annotationSelectors {
					_, ok := annotations[selector]
					if ok {
						matches = true
						break
					}
				}

				if !matches {
					continue
				}
			}
			glog.Infof("Annotations matched. Parsing configMap.")

			combinedMap := make(map[string][]byte)
			for key, value := range configMap.Data {
				combinedMap[key] = []byte(value)
			}

			for key, value := range configMap.BinaryData {
				combinedMap[key] = value
			}

			for name, data := range combinedMap {
				include, exclude = false, false

				for _, glob := range p.includeConfigMapsDataGlobs {
					include, err = filepath.Match(glob, name)
					if err != nil {
						glog.Errorf("Error matching %v to %v: %v", glob, name, err)
						metrics.ErrorTotal.Inc()
						continue
					}

					if include {
						break
					}
				}

				for _, glob := range p.excludeConfigMapsDataGlobs {
					exclude, err = filepath.Match(glob, name)
					if err != nil {
						glog.Errorf("Error matching %v to %v: %v", glob, name, err)
						metrics.ErrorTotal.Inc()
						continue
					}

					if exclude {
						break
					}
				}

				if include && !exclude {
					glog.Infof("Publishing %v/%v metrics %v", configMap.Name, configMap.Namespace, name)

					// Try to get password from a secret with name secret-name-password and "key.password" as key

					passwordKey := strings.TrimSuffix(name, path.Ext(name)) + ".password"
					password, err := getPasswordFromSecret(client, configMap.Namespace, configMap.Name+"-password", passwordKey)
					if err != nil {
						glog.Infof("Password not present in possible expected secret")
					}

					if password == "" {
						password, err = getPasswordFromSecret(client, configMap.Namespace, configMap.Name+"-password", name+".password")
						if err != nil {
							glog.Infof("Password not present in possible expected secret")
						}
					}

					err = p.exporter.ExportMetrics(data, name, configMap.Name, configMap.Namespace, password)
					if err != nil {
						glog.Errorf("Error exporting configMap %v", err)
						metrics.ErrorTotal.Inc()
					}
				} else {
					glog.Infof("Ignoring %v. Does not match %v or matches %v.", name, p.includeConfigMapsDataGlobs, p.excludeConfigMapsDataGlobs)
				}
			}
		}

		<-periodChannel
	}
}
