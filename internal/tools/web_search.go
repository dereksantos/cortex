package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

const (
	searchEndpoint   = "https://html.duckduckgo.com/html/"
	defaultSearchMax = 5
	maximumSearchMax = 10
)

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type webSearchResult struct {
	Title   string
	URL     string
	Snippet string
}

func webSearch(ctx context.Context, tc ToolCall) (string, error) {
	var args webSearchArgs
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return "invalid web_search arguments: " + err.Error(), nil
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return "query is required", nil
	}
	if args.MaxResults == 0 {
		args.MaxResults = defaultSearchMax
	}
	if args.MaxResults < 1 || args.MaxResults > maximumSearchMax {
		return fmt.Sprintf("max_results must be between 1 and %d", maximumSearchMax), nil
	}

	printToolAction(fmt.Sprintf("web_search(%s)", args.Query))

	form := url.Values{"q": []string{args.Query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build search request: %w", err)
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := fetchHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("web search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("web search: HTTP %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read search response: %w", err)
	}
	if len(body) > fetchMaxBodyBytes {
		return "", fmt.Errorf("search response exceeds %d bytes", fetchMaxBodyBytes)
	}
	results, err := parseSearchResults(body, args.MaxResults)
	if err != nil {
		return "", fmt.Errorf("parse search response: %w", err)
	}
	if len(results) == 0 {
		return "(no search results)", nil
	}
	return formatSearchResults(results), nil
}

func parseSearchResults(body []byte, maxResults int) ([]webSearchResult, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	var results []webSearchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= maxResults {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" && hasHTMLClass(n, "result") {
			if result, ok := parseSearchResult(n); ok {
				results = append(results, result)
			}
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return results, nil
}

func parseSearchResult(root *html.Node) (webSearchResult, bool) {
	var result webSearchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "a" && hasHTMLClass(n, "result__a") && result.URL == "" {
				result.Title = nodeText(n)
				result.URL = searchResultURL(attrValue(n, "href"))
			}
			if hasHTMLClass(n, "result__snippet") && result.Snippet == "" {
				result.Snippet = nodeText(n)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if result.Title == "" || result.URL == "" {
		return webSearchResult{}, false
	}
	u, err := validatePublicURL(result.URL)
	if err != nil {
		return webSearchResult{}, false
	}
	result.URL = u.String()
	return result, true
}

func searchResultURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if target := u.Query().Get("uddg"); target != "" {
		return target
	}
	return raw
}

func formatSearchResults(results []webSearchResult) string {
	var out strings.Builder
	for i, result := range results {
		if i > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(strconv.Itoa(i + 1))
		out.WriteString(". ")
		out.WriteString(result.Title)
		out.WriteString("\n   ")
		out.WriteString(result.URL)
		if result.Snippet != "" {
			out.WriteString("\n   ")
			out.WriteString(result.Snippet)
		}
	}
	return out.String()
}

func hasHTMLClass(n *html.Node, class string) bool {
	for _, value := range strings.Fields(attrValue(n, "class")) {
		if value == class {
			return true
		}
	}
	return false
}

func attrValue(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			if text := strings.Join(strings.Fields(n.Data), " "); text != "" {
				parts = append(parts, text)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(parts, " ")
}
