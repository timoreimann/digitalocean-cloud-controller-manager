// +build integration

package e2e

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	numWantNodes          = 2
	doLabel               = "beta.kubernetes.io/instance-type"
	kopsEnvVarClusterName = "KOPS_CLUSTER_NAME"
	kopsEnvVarStateStore  = "KOPS_STATE_STORE"
)

// TestE2E verifies that the node and service controller work as intended for
// all supported Kubernetes versions; that is, we expect nodes to become ready
// and requests to be routed through a DO-provisioned load balancer.
// The test creates various components and makes sure they get deleted prior to
// (to clean up any previous left-overs) and after testing.
func TestE2E(t *testing.T) {
	var missingEnvs []string
	for _, env := range []string{kopsEnvVarClusterName, kopsEnvVarStateStore} {
		if _, ok := os.LookupEnv(env); !ok {
			missingEnvs = append(missingEnvs, env)
		}
	}
	if len(missingEnvs) > 0 {
		t.Fatalf("missing required environment variable(s): %s", missingEnvs)
	}

	s3Cl, err := createS3Client()
	if err != nil {
		t.Fatalf("failed to create S3 client: %s", err)
	}

	tests := []struct {
		desc    string
		kubeVer string
	}{
		{
			desc:    "latest release",
			kubeVer: "1.12.0",
		},
		{
			desc:    "previous release",
			kubeVer: "1.11.2",
		},
		{
			desc:    "previous previous release",
			kubeVer: "1.10.6",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.desc, func(t *testing.T) {
			l := log.New(os.Stdout, fmt.Sprintf("[%s] ", t.Name()), 0)
			dnsName := toDNSName(t.Name())

			wd, err := os.Getwd()
			if err != nil {
				t.Fatalf("failed to get working directory: %s", err)
			}
			kubeConfFile := path.Join(wd, "kubeconfig-e2e."+dnsName)

			// Delete old kubeconfig
			if err := os.Remove(kubeConfFile); err != nil && !os.IsNotExist(err) {
				t.Fatalf("failed to delete kubeconfig %q: %s", kubeConfFile, err)
			}

			// Create space.
			storeName := toS3Name(fmt.Sprintf("%s-%s", os.Getenv(kopsEnvVarClusterName), dnsName))
			if err := s3Cl.deleteSpace(storeName); err != nil {
				t.Fatalf("failed to delete space %q (pre-test): %s", storeName, err)
			}
			if err := s3Cl.ensureSpace(storeName); err != nil {
				t.Fatalf("failed to ensure space %q: %s", storeName, err)
			}
			defer func() {
				if err := s3Cl.deleteSpace(storeName); err != nil {
					t.Fatalf("failed to delete space %q (post-test): %s", storeName, err)
				}
			}()

			// Create cluster.
			extraEnvs := []string{
				fmt.Sprintf("%s=do://%s", kopsEnvVarStateStore, storeName),
				"KUBECONFIG=" + kubeConfFile,
			}
			if err := runScript(extraEnvs, "destroy_cluster.sh"); err != nil {
				t.Fatalf("failed to destroy cluster (pre-test): %s", err)
			}
			if err := runScript(extraEnvs, "setup_cluster.sh", tt.kubeVer, strconv.Itoa(numWantNodes)); err != nil {
				t.Fatalf("failed to set up cluster: %s", err)
			}
			defer func() {
				if err := runScript(extraEnvs, "destroy_cluster.sh"); err != nil {
					t.Errorf("failed to destroy cluster (post-test): %s", err)
				}
			}()

			cs, err := kubeClient(kubeConfFile)
			if err != nil {
				t.Fatalf("failed to create Kubernetes client: %s", err)
			}

			// Check that nodes become ready.
			l.Println("Polling for node readiness")
			var (
				gotNodes      []corev1.Node
				numReadyNodes int
			)
			start := time.Now()
			if err := wait.Poll(5*time.Second, 6*time.Minute, func() (bool, error) {
				nl, err := cs.Core().Nodes().List(metav1.ListOptions{LabelSelector: "kubernetes.io/role=node"})
				if err != nil {
					return false, err
				}

				gotNodes = nl.Items
				numReadyNodes = 0
				for _, node := range gotNodes {
					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
							if _, ok := node.Labels[doLabel]; ok {
								numReadyNodes++
							}
						}
					}
				}

				l.Printf("Found %d/%d ready node(s)", numReadyNodes, numWantNodes)
				return numReadyNodes == numWantNodes, nil
			}); err != nil {
				t.Fatalf("got %d ready node(s), want %d: %s\nnnodes: %v", numReadyNodes, numWantNodes, err, spew.Sdump(gotNodes))
			}
			l.Printf("Took %v for nodes to become ready\n", time.Since(start))

			// Check that load balancer is working.

			// Install example pod to load-balance to.
			appName := "app"
			pod := corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: appName,
					Labels: map[string]string{
						"app": appName,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						corev1.Container{
							Name:  "nginx",
							Image: "nginx",
							Ports: []corev1.ContainerPort{
								corev1.ContainerPort{
									ContainerPort: 80,
								},
							},
						},
					},
				},
			}

			if _, err := cs.CoreV1().Pods(corev1.NamespaceDefault).Create(&pod); err != nil {
				t.Fatalf("failed to create example pod: %s", err)
			}

			// Wait for example pod to become ready.
			l.Println("Polling for pod readiness")
			start = time.Now()
			var appPod *corev1.Pod
			if err := wait.Poll(1*time.Second, 1*time.Minute, func() (bool, error) {
				pod, err := cs.CoreV1().Pods(corev1.NamespaceDefault).Get(appName, metav1.GetOptions{})
				if err != nil {
					if kerrors.IsNotFound(err) {
						return false, nil
					}
					return false, err
				}
				appPod = pod
				for _, cond := range appPod.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						return true, nil
					}
				}
				return false, nil
			}); err != nil {
				t.Fatalf("failed to observe ready example pod %q in time: %s\npod: %v", appName, err, appPod)
			}
			l.Printf("Took %v for pod to become ready\n", time.Since(start))

			// Create service object.
			svcName := "svc"
			svc := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: svcName,
				},
				Spec: corev1.ServiceSpec{
					Selector: map[string]string{
						"app": appName,
					},
					Type: corev1.ServiceTypeLoadBalancer,
					Ports: []corev1.ServicePort{
						corev1.ServicePort{
							Port: 80,
						},
					},
				},
			}

			if _, err := cs.CoreV1().Services(corev1.NamespaceDefault).Create(&svc); err != nil {
				t.Fatalf("failed to create service: %s", err)
			}
			// External LBs don't seem to get deleted when the kops cluster is
			// removed, at least not on DO. Hence, we'll do it explicitly.
			defer func() {
				if err := cs.CoreV1().Services(corev1.NamespaceDefault).Delete(svcName, &metav1.DeleteOptions{}); err != nil {
					t.Fatalf("failed to delete service: %s", err)
				}
			}()

			// Wait for service IP address to be assigned.
			l.Println("Polling for service load balancer IP address assignment")
			start = time.Now()
			var lbAddr string
			if err := wait.Poll(5*time.Second, 10*time.Minute, func() (bool, error) {
				svc, err := cs.CoreV1().Services(corev1.NamespaceDefault).Get(svcName, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				for _, ing := range svc.Status.LoadBalancer.Ingress {
					lbAddr = ing.IP
					return true, nil
				}

				return false, nil
			}); err != nil {
				t.Fatalf("failed to observe load balancer IP address assignment: %s", err)
			}
			l.Printf("Took %v for load balancer to get its IP address assigned\n", time.Since(start))

			// Send request to the pod over the LB.
			cl := &http.Client{
				Timeout: 5 * time.Second,
			}
			u := fmt.Sprintf("http://%s:80", lbAddr)

			var attempts, lastStatusCode int
			if err := wait.Poll(1*time.Second, 3*time.Minute, func() (bool, error) {
				l.Printf("Sending request to %s", u)
				attempts++
				resp, err := cl.Get(u)
				if err != nil {
					return false, nil
				}
				defer resp.Body.Close()

				lastStatusCode = resp.StatusCode
				if resp.StatusCode != http.StatusOK {
					return false, nil
				}

				return true, nil
			}); err != nil {
				t.Fatalf("failed to send request over LB to example application: %s (last status code: %d / attempts: %d)", err, lastStatusCode, attempts)
			}
			l.Printf("Needed %d attempt(s) to successfully deliver sample request\n", attempts)
		})
	}
}
