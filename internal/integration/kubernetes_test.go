package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type kubernetesRoundTripFunc func(*http.Request) (*http.Response, error)

func (f kubernetesRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func kubernetesJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestKubernetesPodsFollowsPagination(t *testing.T) {
	requests := 0
	client := &KubernetesClient{
		baseURL: "https://kubernetes.example.internal",
		token:   "secret-token",
		http: &http.Client{Transport: kubernetesRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			if request.URL.Query().Get("limit") != "500" || request.URL.Query().Get("labelSelector") != "component=singleuser-server" {
				t.Fatalf("pagination query lost fixed parameters: %s", request.URL.RawQuery)
			}
			if request.Header.Get("Authorization") != "Bearer secret-token" {
				t.Fatalf("missing Kubernetes bearer token: %q", request.Header.Get("Authorization"))
			}
			switch requests {
			case 1:
				if request.URL.Query().Get("continue") != "" {
					t.Fatalf("first page unexpectedly has continue token: %s", request.URL.RawQuery)
				}
				return kubernetesJSONResponse(`{"metadata":{"continue":"next token/+="},"items":[{"metadata":{"name":"pod-a","namespace":"team","labels":{"safe":"yes"}},"spec":{"nodeName":"node-a"},"status":{"phase":"Running"}}]}`), nil
			case 2:
				if request.URL.Query().Get("continue") != "next token/+=" {
					t.Fatalf("continue token was not preserved: %s", request.URL.RawQuery)
				}
				return kubernetesJSONResponse(`{"metadata":{},"items":[{"metadata":{"name":"pod-b","namespace":"team"},"spec":{"nodeName":"node-b"},"status":{"phase":"Pending"}}]}`), nil
			default:
				t.Fatalf("unexpected request %d", requests)
				return nil, nil
			}
		})},
	}
	pods, err := client.Pods(context.Background(), "team", "component=singleuser-server")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(pods) != 2 || pods[0].Name != "pod-a" || pods[1].Name != "pod-b" || pods[1].Phase != "Pending" {
		t.Fatalf("unexpected paginated Pod result: requests=%d pods=%#v", requests, pods)
	}
}

func TestKubernetesPodsRejectsRepeatedContinueToken(t *testing.T) {
	requests := 0
	client := &KubernetesClient{
		baseURL: "https://kubernetes.example.internal",
		http: &http.Client{Transport: kubernetesRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return kubernetesJSONResponse(`{"metadata":{"continue":"cycle"},"items":[]}`), nil
		})},
	}
	if _, err := client.Pods(context.Background(), "", ""); err == nil || !strings.Contains(err.Error(), "반복 continue token") {
		t.Fatalf("repeated continuation was not rejected: requests=%d err=%v", requests, err)
	}
	if requests != 2 {
		t.Fatalf("repeated token should stop on second page, requests=%d", requests)
	}
}

func TestKubernetesPodsRejectsUnboundedPagination(t *testing.T) {
	requests := 0
	client := &KubernetesClient{
		baseURL: "https://kubernetes.example.internal",
		http: &http.Client{Transport: kubernetesRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return kubernetesJSONResponse(fmt.Sprintf(`{"metadata":{"continue":"page-%d"},"items":[]}`, requests)), nil
		})},
	}
	if _, err := client.Pods(context.Background(), "", ""); err == nil || !strings.Contains(err.Error(), "최대 페이지 수") {
		t.Fatalf("unbounded pagination was not rejected: requests=%d err=%v", requests, err)
	}
	if requests != maxKubernetesPodPages {
		t.Fatalf("page ceiling mismatch: requests=%d want=%d", requests, maxKubernetesPodPages)
	}
}

func TestKubernetesPodsRejectsOversizedResponse(t *testing.T) {
	client := &KubernetesClient{
		baseURL: "https://kubernetes.example.internal",
		http: &http.Client{Transport: kubernetesRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return kubernetesJSONResponse(strings.Repeat("x", maxResponseBytes+1)), nil
		})},
	}
	if _, err := client.Pods(context.Background(), "", ""); err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("oversized Kubernetes response was not rejected: %v", err)
	}
}

func TestDecodeKubernetesPodRejectsMissingName(t *testing.T) {
	if _, ok := decodeKubernetesPod([]byte(`{"metadata":{"namespace":"team"}}`)); ok {
		t.Fatal("Pod without metadata.name was accepted")
	}
}
