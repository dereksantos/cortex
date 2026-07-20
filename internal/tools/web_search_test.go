package tools

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func searchCall(query string, maxResults int) ToolCall {
	args := `{"query":` + quoteJSON(query)
	if maxResults != 0 {
		args += `,"max_results":` + jsonInt(maxResults)
	}
	args += `}`
	return ToolCall{Function: FunctionCall{Name: FunctionWebSearch, Arguments: args}}
}

func jsonInt(n int) string {
	return strconv.Itoa(n)
}

func TestWebSearchReturnsRankedResults(t *testing.T) {
	oldClient := fetchHTTPClient
	t.Cleanup(func() { fetchHTTPClient = oldClient })
	fetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.String() != searchEndpoint {
			t.Fatalf("request = %s %s", req.Method, req.URL)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if got := values.Get("q"); got != "go context cancellation" {
			t.Fatalf("query = %q", got)
		}
		page := `<html><body>
<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fblog%2Fcontext">Go Concurrency Patterns: Context</a><a class="result__snippet">Cancellation and deadlines.</a></div>
<div class="result"><a class="result__a" href="https://pkg.go.dev/context">context package</a><div class="result__snippet">Package context documentation.</div></div>
</body></html>`
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(page)), Request: req}, nil
	})}

	got, err := webSearch(context.Background(), searchCall("go context cancellation", 0), headlessDeps{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1. Go Concurrency Patterns: Context", "https://go.dev/blog/context", "Cancellation and deadlines.", "2. context package", "https://pkg.go.dev/context"} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q:\n%s", want, got)
		}
	}
}

func TestWebSearchValidatesArguments(t *testing.T) {
	for _, tc := range []ToolCall{searchCall(" ", 0), searchCall("query", 11)} {
		got, err := webSearch(context.Background(), tc, headlessDeps{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "required") && !strings.Contains(got, "between") {
			t.Fatalf("result = %q, want validation observation", got)
		}
	}
}

func TestWebSearchHonorsResultLimitAndSkipsUnsafeLinks(t *testing.T) {
	page := `<div class="result"><a class="result__a" href="http://127.0.0.1/private">Private</a></div>
<div class="result"><a class="result__a" href="https://one.example/">One</a></div>
<div class="result"><a class="result__a" href="https://two.example/">Two</a></div>`
	results, err := parseSearchResults([]byte(page), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "One" {
		t.Fatalf("results = %#v", results)
	}
}

func TestWebSearchIsRegisteredForCoderOnly(t *testing.T) {
	if !toolListContains(All, FunctionWebSearch) {
		t.Fatal("web_search missing from coder tool set")
	}
	if toolListContains(Study.Tools, FunctionWebSearch) {
		t.Fatal("web_search must not be available to the study subagent")
	}
}
