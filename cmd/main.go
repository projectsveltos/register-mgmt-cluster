package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

var (
	setupLog                = ctrl.Log.WithName("setup")
	labels                  string
	sveltosClusterNamespace string
	sveltosClusterName      string
	serviceAccountToken     bool
)

const (
	//nolint: gosec // this is just postfix of the secret name
	sveltosKubeconfigSecretNamePostfix = "-sveltos-kubeconfig"
	projectsveltos                     = "projectsveltos"
	tokenExpirationInSeconds           = 7200
	kubeconfigKey                      = "kubeconfig"
)

func main() {
	klog.InitFlags(nil)

	initFlags(pflag.CommandLine)
	pflag.CommandLine.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	ctrl.SetLogger(klog.Background())

	scheme, err := initScheme()
	if err != nil {
		os.Exit(1)
	}

	restConfig := ctrl.GetConfigOrDie()

	var c client.Client
	c, err = client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		werr := fmt.Errorf("failed to connect: %w", err)
		log.Fatal(werr)
	}

	caData, err := getCaData(setupLog)
	if err != nil {
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	err = registerManagementCluster(ctx, restConfig, c, caData, setupLog)
	if err != nil {
		os.Exit(1)
	}
}

func initScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := rbacv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

func initFlags(fs *pflag.FlagSet) {
	fs.StringVar(&labels, "labels", "",
		"This option allows you to specify labels for the SveltosCluster resource being created."+
			"The format for labels is <key1=value1,key2=value2>, where each key-value pair is separated by a comma (,) and "+
			"the key and value are separated by an equal sign (=). You can define multiple labels by adding more key-value pairs "+
			"separated by commas.")

	fs.StringVar(&sveltosClusterNamespace, "namespace", "mgmt",
		"This option allows you to specify the namespace where the SveltosCluster instance representing the management cluster will be created")

	fs.StringVar(&sveltosClusterName, "name", "mgmt",
		"This option allows you to specify the name of the SveltosCluster instance representing the management cluster")

	fs.BoolVar(&serviceAccountToken, "service-account-token", false,
		"This option instructs Sveltos to create a Secret of type kubernetes.io/service-account-token instead of generating a token associated to ServiceAccount")
}

func registerManagementCluster(ctx context.Context, restConfig *rest.Config, c client.Client,
	caData []byte, logger logr.Logger) error {

	kubeconfig, err := generateKubeconfigForServiceAccount(ctx, restConfig, c, projectsveltos,
		projectsveltos, tokenExpirationInSeconds, caData, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get kubeconfig: %v", err))
		return err
	}

	var sveltosClusterLabels map[string]string
	sveltosClusterLabels, err = stringToMap(labels)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse labels: %v", err))
		return err
	}

	err = onboardManagementCluster(ctx, c, sveltosClusterNamespace, sveltosClusterName, kubeconfig,
		sveltosClusterLabels, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to register cluster: %v", err))
		return err
	}

	return nil
}

func stringToMap(data string) (map[string]string, error) {
	if data == "" {
		return nil, nil
	}

	const keyValueLength = 2
	result := make(map[string]string)
	for _, pair := range strings.Split(data, ",") {
		kv := strings.Split(pair, "=")
		if len(kv) != keyValueLength {
			return nil, fmt.Errorf("invalid key-value pair format: %s", pair)
		}
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])
		result[key] = value
	}
	return result, nil
}

func generateKubeconfigForServiceAccount(ctx context.Context, restConfig *rest.Config, c client.Client,
	namespace, serviceAccountName string, expirationSeconds int, caData []byte, logger logr.Logger) (string, error) {

	if err := createNamespace(ctx, c, namespace, logger); err != nil {
		return "", err
	}
	if err := createServiceAccount(ctx, c, namespace, serviceAccountName, logger); err != nil {
		return "", err
	}
	if err := createClusterRole(ctx, c, projectsveltos, logger); err != nil {
		return "", err
	}
	if err := createClusterRoleBinding(ctx, c, projectsveltos, projectsveltos, namespace, serviceAccountName, logger); err != nil {
		return "", err
	}

	var token string
	if serviceAccountToken {
		if err := createSecret(ctx, c, namespace, serviceAccountName, logger); err != nil {
			return "", err
		}
		var err error
		token, err = getToken(ctx, c, namespace, serviceAccountName)
		if err != nil {
			return "", err
		}
	} else {
		tokenRequest, err := getServiceAccountTokenRequest(ctx, restConfig, namespace, serviceAccountName, expirationSeconds, logger)
		if err != nil {
			return "", err
		}
		token = tokenRequest.Token
	}

	logger.V(logs.LogInfo).Info("Get Kubeconfig from TokenRequest")
	data := getKubeconfigFromToken(restConfig, namespace, serviceAccountName, token, caData)

	return data, nil
}

func createNamespace(ctx context.Context, c client.Client, name string, logger logr.Logger) error {
	logger.V(logs.LogInfo).Info(fmt.Sprintf("Create namespace %s", name))
	currentNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	err := c.Create(ctx, currentNs)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("Failed to create Namespace %s: %v",
			name, err))
		return err
	}

	return nil
}

func createServiceAccount(ctx context.Context, c client.Client, namespace, name string,
	logger logr.Logger) error {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Create serviceAccount %s/%s", namespace, name))
	currentSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}

	err := c.Create(ctx, currentSA)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("Failed to create ServiceAccount %s/%s: %v",
			namespace, name, err))
		return err
	}

	return nil
}

func createSecret(ctx context.Context, c client.Client, namespace, saName string,
	logger logr.Logger) error {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Create Secret %s/%s", namespace, saName))
	currentSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      saName,
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: saName,
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}

	err := c.Create(ctx, currentSecret)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("Failed to create Secret %s/%s: %v",
			namespace, saName, err))
		return err
	}

	return nil
}

func getToken(ctx context.Context, c client.Client, namespace, secretName string) (string, error) {
	retries := 0
	const maxRetries = 5
	for {
		secret := &corev1.Secret{}
		err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName},
			secret)
		if err != nil {
			if retries < maxRetries {
				time.Sleep(time.Second)
				continue
			}
			return "", err
		}

		if secret.Data == nil {
			time.Sleep(time.Second)
			continue
		}

		v, ok := secret.Data["token"]
		if !ok {
			time.Sleep(time.Second)
			continue
		}

		return string(v), nil
	}
}

func createClusterRole(ctx context.Context, c client.Client, clusterRoleName string, logger logr.Logger) error {
	logger.V(logs.LogInfo).Info(fmt.Sprintf("Create ClusterRole %s", clusterRoleName))
	// Extends permission in addon-controller-role-extra
	clusterrole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"*"},
				APIGroups: []string{"*"},
				Resources: []string{"*"},
			},
			{
				Verbs:           []string{"*"},
				NonResourceURLs: []string{"*"},
			},
		},
	}

	err := c.Create(ctx, clusterrole)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("Failed to create ClusterRole %s: %v",
			clusterRoleName, err))
		return err
	}

	return nil
}

func createClusterRoleBinding(ctx context.Context, c client.Client,
	clusterRoleName, clusterRoleBindingName, serviceAccountNamespace, serviceAccountName string, logger logr.Logger) error {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Create ClusterRoleBinding %s", clusterRoleBindingName))
	clusterrolebinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleBindingName,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     clusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Namespace: serviceAccountNamespace,
				Name:      serviceAccountName,
				Kind:      "ServiceAccount",
				APIGroup:  corev1.SchemeGroupVersion.Group,
			},
		},
	}
	err := c.Create(ctx, clusterrolebinding)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("Failed to create clusterrolebinding %s: %v",
			clusterRoleBindingName, err))
		return err
	}

	return nil
}

// getServiceAccountTokenRequest returns token for a serviceaccount
func getServiceAccountTokenRequest(ctx context.Context, restConfig *rest.Config, serviceAccountNamespace, serviceAccountName string,
	expirationSeconds int, logger logr.Logger) (*authenticationv1.TokenRequestStatus, error) {

	expiration := int64(expirationSeconds)

	treq := &authenticationv1.TokenRequest{}

	if expirationSeconds != 0 {
		treq.Spec = authenticationv1.TokenRequestSpec{
			ExpirationSeconds: &expiration,
		}
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	logger.V(logs.LogInfo).Info(
		fmt.Sprintf("Create Token for ServiceAccount %s/%s", serviceAccountNamespace, serviceAccountName))
	var tokenRequest *authenticationv1.TokenRequest
	tokenRequest, err = clientset.CoreV1().ServiceAccounts(serviceAccountNamespace).
		CreateToken(ctx, serviceAccountName, treq, metav1.CreateOptions{})
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("Failed to create token for ServiceAccount %s/%s: %v",
				serviceAccountNamespace, serviceAccountName, err))
		return nil, err
	}

	return &tokenRequest.Status, nil
}

// getKubeconfigFromToken returns Kubeconfig to access management cluster from token.
func getKubeconfigFromToken(restConfig *rest.Config, namespace, serviceAccountName, token string, caData []byte) string {
	template := `apiVersion: v1
kind: Config
clusters:
- name: local
  cluster:
    server: %s
    certificate-authority-data: "%s"
users:
- name: %s
  user:
    token: %s
contexts:
- name: sveltos-context
  context:
    cluster: local
    namespace: %s
    user: %s
current-context: sveltos-context`

	data := fmt.Sprintf(template, restConfig.Host,
		base64.StdEncoding.EncodeToString(caData), serviceAccountName, token, namespace, serviceAccountName)

	return data
}

func onboardManagementCluster(ctx context.Context, c client.Client, clusterNamespace, clusterName, kubeconfigData string,
	labels map[string]string, logger logr.Logger) error {

	err := createNamespace(ctx, c, clusterNamespace, logger)
	if err != nil {
		return err
	}

	err = patchSveltosCluster(ctx, c, clusterNamespace, clusterName, labels, logger)
	if err != nil {
		return err
	}

	secretName := clusterName + sveltosKubeconfigSecretNamePostfix
	return patchSecret(ctx, c, clusterNamespace, secretName, kubeconfigData, logger)
}

func patchSveltosCluster(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	labels map[string]string, logger logr.Logger) error {

	// Token duration is fixed at one hour.  Increasing this value would cause issues
	// because Sveltos relies on this duration to determine when to refresh the token.
	// If this value is larger than the actual token expiration (set by previous released
	// images), Sveltos might attempt to use an expired token, leading to authentication failures.
	// This value must match the duration of the renewed tokens provided by the shipped version.
	const renewalInterval = 1 * 3600 * time.Second // every 1 hour
	currentSveltosCluster := &libsveltosv1beta1.SveltosCluster{}
	err := c.Get(ctx, types.NamespacedName{Namespace: clusterNamespace, Name: clusterName},
		currentSveltosCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("Creating SveltosCluster %s/%s", clusterNamespace, clusterName))
			currentSveltosCluster.Namespace = clusterNamespace
			currentSveltosCluster.Name = clusterName
			currentSveltosCluster.Labels = labels
			currentSveltosCluster.Spec = libsveltosv1beta1.SveltosClusterSpec{
				TokenRequestRenewalOption: &libsveltosv1beta1.TokenRequestRenewalOption{
					RenewTokenRequestInterval: metav1.Duration{Duration: renewalInterval},
				},
			}
			return c.Create(ctx, currentSveltosCluster)
		}
		return err
	}

	logger.V(logs.LogInfo).Info("Updating SveltosCluster")
	if currentSveltosCluster.Labels == nil {
		currentSveltosCluster.Labels = map[string]string{}
	}
	for k := range labels {
		currentSveltosCluster.Labels[k] = labels[k]
	}

	currentSveltosCluster.Spec = libsveltosv1beta1.SveltosClusterSpec{
		TokenRequestRenewalOption: &libsveltosv1beta1.TokenRequestRenewalOption{
			RenewTokenRequestInterval: metav1.Duration{Duration: renewalInterval},
		},
	}
	return c.Update(ctx, currentSveltosCluster)
}

func patchSecret(ctx context.Context, c client.Client, clusterNamespace, secretName, kubeconfigData string,
	logger logr.Logger) error {

	currentSecret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Namespace: clusterNamespace, Name: secretName}, currentSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("Creating Secret %s/%s", clusterNamespace, secretName))
			currentSecret.Namespace = clusterNamespace
			currentSecret.Name = secretName
			currentSecret.Data = map[string][]byte{kubeconfigKey: []byte(kubeconfigData)}
			return c.Create(ctx, currentSecret)
		}
		return err
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Updating Secret %s/%s", clusterNamespace, secretName))
	currentSecret.Data = map[string][]byte{
		kubeconfigKey: []byte(kubeconfigData),
	}

	return c.Update(ctx, currentSecret)
}

func getCaData(logger logr.Logger) ([]byte, error) {
	filename := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	content, err := os.ReadFile(filename)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get file %s: %v", filename, err))
		return nil, err
	}

	return content, nil
}
