package check

import (
	"bytes"
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	defaultTestImage = "registry.access.redhat.com/ubi9/ubi:latest"
	podTimeout       = 120 * time.Second
	execTimeout      = 10 * time.Second
)

var TestPodImage string

func DeployTestPod(ctx context.Context, clientset kubernetes.Interface,
	name, namespace string, hostNetwork bool, nodeName string,
) (*corev1.Pod, error) {
	image := TestPodImage
	if image == "" {
		image = defaultTestImage
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": "dr-check-net-test"},
		},
		Spec: corev1.PodSpec{
			HostNetwork:   hostNetwork,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "net-test",
				Image:   image,
				Command: []string{"sleep", "3600"},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					RunAsNonRoot:             ptr.To(true),
					SeccompProfile: &corev1.SeccompProfile{
						Type: corev1.SeccompProfileTypeRuntimeDefault,
					},
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
				},
			}},
		},
	}

	if nodeName != "" {
		pod.Spec.NodeName = nodeName
	}

	created, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating test pod %s: %w", name, err)
	}

	return created, nil
}

func WaitForPodReady(ctx context.Context, clientset kubernetes.Interface, name, namespace string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, podTimeout, true,
		func(ctx context.Context) (bool, error) {
			pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}

			return pod.Status.Phase == corev1.PodRunning, nil
		})
}

func ExecInPod(ctx context.Context, restConfig *rest.Config, clientset kubernetes.Interface,
	podName, namespace string, command []string,
) (string, string, error) {
	return ExecInPodContainer(ctx, restConfig, clientset, podName, namespace, "net-test", command)
}

func ExecInPodContainer(ctx context.Context, restConfig *rest.Config, clientset kubernetes.Interface,
	podName, namespace, containerName string, command []string,
) (string, string, error) {
	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(restConfig, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("creating executor: %w", err)
	}

	var stdout, stderr bytes.Buffer

	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	err = exec.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	return stdout.String(), stderr.String(), err
}

func DeleteTestPod(ctx context.Context, clientset kubernetes.Interface, name, namespace string) error {
	return clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}
