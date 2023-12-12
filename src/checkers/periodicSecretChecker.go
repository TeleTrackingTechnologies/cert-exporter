package checkers

import (
	"context"
	"errors"
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

// PeriodicSecretChecker is an object designed to check for files on disk at a regular interval
type PeriodicSecretChecker struct {
	period                  time.Duration
	labelSelectors          []string
	kubeconfigPath          string
	annotationSelectors     []string
	namespaces              []string
	exporter                *exporters.SecretExporter
	includeSecretsDataGlobs []string
	excludeSecretsDataGlobs []string
	includeSecretsTypes     []string
}

// NewSecretChecker is a factory method that returns a new PeriodicSecretChecker
func NewSecretChecker(period time.Duration, labelSelectors, includeSecretsDataGlobs, excludeSecretsDataGlobs, annotationSelectors, namespaces []string, kubeconfigPath string, e *exporters.SecretExporter, includeSecretsTypes []string) *PeriodicSecretChecker {
	return &PeriodicSecretChecker{
		period:                  period,
		labelSelectors:          labelSelectors,
		annotationSelectors:     annotationSelectors,
		namespaces:              namespaces,
		kubeconfigPath:          kubeconfigPath,
		exporter:                e,
		includeSecretsDataGlobs: includeSecretsDataGlobs,
		excludeSecretsDataGlobs: excludeSecretsDataGlobs,
		includeSecretsTypes:     includeSecretsTypes,
	}
}

func getPasswordFromSecret(client kubernetes.Interface, namespace, secretName, passwordKey string) (string, error) {
	secret, err := client.CoreV1().Secrets(namespace).Get(context.TODO(), secretName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	password, ok := secret.Data[passwordKey]
	if !ok {
		return "", errors.New("password not found in secret")
	}

	return string(password), nil
}

// StartChecking starts the periodic file check.  Most likely you want to run this as an independent go routine.
func (p *PeriodicSecretChecker) StartChecking() {
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
		glog.Infof("Scan secrets in %v", strings.Join(p.namespaces, ", "))
	}
	for {
		glog.Info("Begin periodic check")

		p.exporter.ResetMetrics()

		var secrets []corev1.Secret
		for _, ns := range p.namespaces {
			if len(p.labelSelectors) > 0 {
				for _, labelSelector := range p.labelSelectors {
					var s *corev1.SecretList
					s, err = client.CoreV1().Secrets(ns).List(context.TODO(), metav1.ListOptions{
						LabelSelector: labelSelector,
					})
					if err != nil {
						glog.Errorf("Error requesting secrets %v", err)
						metrics.ErrorTotal.Inc()
						continue
					}
					secrets = append(secrets, s.Items...)
				}
			} else {
				var s *corev1.SecretList
				s, err = client.CoreV1().Secrets(ns).List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					glog.Errorf("Error requesting secrets %v", err)
					metrics.ErrorTotal.Inc()
					continue
				}
				secrets = append(secrets, s.Items...)
			}
		}

		for _, secret := range secrets {
			include, exclude := false, false
			// If you want only a certain type of cert
			if len(p.includeSecretsTypes) > 0 {
				exclude = false
				for _, t := range p.includeSecretsTypes {
					if string(secret.Type) == t {
						include = true
					}
					if include {
						continue
					}
				}
				if !include {
					glog.Infof("Ignoring secret %s in %s because %s is not included in your secret-include-types %v", secret.GetName(), secret.GetNamespace(), secret.Type, p.includeSecretsTypes)
					continue
				}
			}

			glog.Infof("Reviewing secret %v in %v", secret.GetName(), secret.GetNamespace())

			if len(p.annotationSelectors) > 0 {
				matches := false
				annotations := secret.GetAnnotations()
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
			glog.Infof("Annotations matched. Parsing Secret.")

			for name, bytes := range secret.Data {
				include, exclude = false, false

				for _, glob := range p.includeSecretsDataGlobs {
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

				for _, glob := range p.excludeSecretsDataGlobs {
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
					glog.Infof("Publishing %v/%v metrics %v", secret.Name, secret.Namespace, name)

					// Try to get password from same secret assuming "password" as key - JITBundleSecret
					password, err := getPasswordFromSecret(client, secret.Namespace, secret.Name, "password")
					if err != nil {
						glog.Infof("Password not present within secret %v", secret.Name)
					}

					// Try to get password from another secret with name secret-name-password and "key.password" as key - Generic JIT
					passwordKey := strings.TrimSuffix(name, path.Ext(name)) + ".password"
					if password == "" {
						password, err = getPasswordFromSecret(client, secret.Namespace, secret.Name+"-password", passwordKey)
						if err != nil {
							glog.Infof("Password not present in possible expected secret for secret %v", secret.Name)
						}
					}

					if password == "" {
						password, err = getPasswordFromSecret(client, secret.Namespace, secret.Name+"-password", name+".password")
						if err != nil {
							glog.Infof("Password not present in possible expected secret for secret %v", secret.Name)
						}
					}

					err = p.exporter.ExportMetrics(bytes, name, secret.Name, secret.Namespace, password, secret.GetLabels())
					if err != nil {
						glog.Errorf("Error exporting secret %v", err)
						metrics.ErrorTotal.Inc()
					}
				} else {
					glog.Infof("Ignoring %v. Does not match %v or matches %v.", name, p.includeSecretsDataGlobs, p.excludeSecretsDataGlobs)
				}
			}
		}

		<-periodChannel
	}
}
