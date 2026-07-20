package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func fetchCall(rawURL string) ToolCall {
	return ToolCall{Function: FunctionCall{Name: FunctionFetchURL, Arguments: `{"url":` + quoteJSON(rawURL) + `}`}}
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func TestFetchURLExtractsReadableHTML(t *testing.T) {
	oldClient := fetchHTTPClient
	t.Cleanup(func() { fetchHTTPClient = oldClient })

	fetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("User-Agent"); got != fetchUserAgent {
			t.Fatalf("User-Agent = %q, want %q", got, fetchUserAgent)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(`<html><head><title>Example Page</title><style>hidden css</style></head><body><main>Hello <b>world</b>.</main><script>hidden js</script></body></html>`)),
			Request:    req,
		}, nil
	})}

	got, err := fetchURL(context.Background(), fetchCall("https://example.com/page"), headlessDeps{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"URL: https://example.com/page", "Title: Example Page", "Hello", "world"} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"hidden css", "hidden js"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("result contains %q:\n%s", unwanted, got)
		}
	}
}

func TestFetchURLRefusesUnsafeURLsBeforeRequest(t *testing.T) {
	oldClient := fetchHTTPClient
	t.Cleanup(func() { fetchHTTPClient = oldClient })
	fetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected request to %s", req.URL)
		return nil, nil
	})}

	for _, rawURL := range []string{
		"file:///etc/passwd",
		"http://localhost/admin",
		"http://127.0.0.1/admin",
		"http://10.0.0.2/admin",
		"http://169.254.169.254/latest/meta-data",
		"https://user:password@example.com/",
	} {
		t.Run(rawURL, func(t *testing.T) {
			got, err := fetchURL(context.Background(), fetchCall(rawURL), headlessDeps{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, "refused") {
				t.Fatalf("result = %q, want refusal", got)
			}
		})
	}
}

func TestFetchURLRejectsUnsupportedContent(t *testing.T) {
	oldClient := fetchHTTPClient
	t.Cleanup(func() { fetchHTTPClient = oldClient })
	fetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader("not really an image")),
			Request:    req,
		}, nil
	})}

	_, err := fetchURL(context.Background(), fetchCall("https://example.com/image.png"), headlessDeps{})
	if err == nil || !strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("error = %v, want unsupported content type", err)
	}
}

func TestFetchURLIsRegisteredForCoderOnly(t *testing.T) {
	if !toolListContains(All, FunctionFetchURL) {
		t.Fatal("fetch_url missing from coder tool set")
	}
	if toolListContains(Study.Tools, FunctionFetchURL) {
		t.Fatal("fetch_url must not be available to the study subagent")
	}
}

func toolListContains(list []Tool, name string) bool {
	for _, tool := range list {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
