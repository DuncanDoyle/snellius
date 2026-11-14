//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const uiNamespace = "snellius"

// forward port-forwards target (for example "pod/x") port 8080 to a free local
// port, waits until /api/v1/meta answers, and returns the base URL.
func forward(t *testing.T, target string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", harnessKubeconfig(t), "-n", uiNamespace,
		"port-forward", target, fmt.Sprintf("%d:8080", port))
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })

	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	eventually(t, func() error {
		resp, err := http.Get(u + "/api/v1/meta")
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	})
	return u
}

// meta returns authMode from base's /api/v1/meta.
func meta(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u + "/api/v1/meta")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m struct {
		AuthMode string `json:"authMode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m.AuthMode
}

func uiPod(t *testing.T) string {
	t.Helper()
	out, err := kubectlRun(t, "-n", uiNamespace, "get", "pod", "-l", "app.kubernetes.io/name=snellius",
		"-o", "jsonpath={.items[0].metadata.name}")
	if err != nil || len(out) == 0 {
		t.Fatalf("UI pod: %v %s", err, out)
	}
	return string(out)
}

// jsonpath reads one field of a UI-namespace object.
func jsonpath(t *testing.T, kind, name, path string) string {
	t.Helper()
	out, err := kubectlRun(t, "-n", uiNamespace, "get", kind, name, "-o", "jsonpath="+path)
	if err != nil || len(out) == 0 {
		t.Fatalf("get %s/%s %s: %v %s", kind, name, path, err, out)
	}
	return string(out)
}

func TestLocalModeReachableThroughForwards(t *testing.T) {
	pod := uiPod(t)
	for name, target := range map[string]string{
		"service": "svc/ui-snellius",
		"pod":     "pod/" + pod,
	} {
		t.Run(name, func(t *testing.T) {
			if got := meta(t, forward(t, target)); got != "local" {
				t.Fatalf("authMode = %q, want local", got)
			}
		})
	}
}

func TestLocalModeRejectsForeignHostAndOrigin(t *testing.T) {
	u := forward(t, "svc/ui-snellius")
	for name, tc := range map[string]struct {
		header, value string
		want          int
	}{
		"foreign host":   {"Host", "evil.example", http.StatusMisdirectedRequest},
		"foreign origin": {"Origin", "http://evil.example", http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, u+"/api/v1/meta", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.header == "Host" {
				req.Host = tc.value // net/http ignores Header["Host"]
			} else {
				req.Header.Set(tc.header, tc.value)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// probe is curl's "<url>=<http code>" output, 000 when the connection failed.
const probeScript = `for u in "$@"; do
  c=$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$u"); echo "$u=$c"
done`

// TestLocalModeUnreachableInCluster checks from a throwaway pod that the main
// listener is not served on the pod IP or ClusterIP and the probe port serves only probes.
func TestLocalModeUnreachableInCluster(t *testing.T) {
	podIP := jsonpath(t, "pod", uiPod(t), "{.status.podIP}")
	svcIP := jsonpath(t, "svc", "ui-snellius", "{.spec.clusterIP}")
	urls := []string{
		"http://" + podIP + ":8080/api/v1/meta",
		"http://" + svcIP + ":80/api/v1/meta",
		"http://" + podIP + ":8081/healthz",
		"http://" + podIP + ":8081/readyz",
		"http://" + podIP + ":8081/metrics",
		"http://" + podIP + ":8081/api/v1/meta",
	}
	want := []string{"000", "000", "200", "200", "200", "404"}

	var got map[string]string
	eventually(t, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		args := append([]string{"--kubeconfig", harnessKubeconfig(t), "-n", uiNamespace,
			"run", "probe-modes", "--rm", "-i", "--restart=Never", "--quiet",
			"--image=curlimages/curl:8.10.1", "--command", "--", "sh", "-c", probeScript, "sh"}, urls...)
		out, err := exec.CommandContext(ctx, "kubectl", args...).Output()
		if err != nil {
			return fmt.Errorf("probe pod: %w", err)
		}
		got = map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if u, code, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				got[u] = code
			}
		}
		return nil
	})
	for i, u := range urls {
		if got[u] != want[i] {
			t.Errorf("GET %s = %q, want %q", u, got[u], want[i])
		}
	}
}
