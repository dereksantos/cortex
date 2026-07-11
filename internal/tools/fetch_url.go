package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	fetchTimeout      = 20 * time.Second
	fetchMaxRedirects = 5
	fetchMaxBodyBytes = 1 << 20 // Bound download and parsing work to 1 MiB.
	fetchMaxTextBytes = MaxToolOutput
	fetchUserAgent    = "Cortex/loop (+https://github.com/dereksantos/cortex)"
)

var fetchHTTPClient = newSafeHTTPClient()

type fetchURLArgs struct {
	URL string `json:"url"`
}

func fetchURL(ctx context.Context, tc ToolCall) (string, error) {
	var args fetchURLArgs
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return "invalid fetch_url arguments: " + err.Error(), nil
	}
	if strings.TrimSpace(args.URL) == "" {
		return "url is required", nil
	}

	u, err := validatePublicURL(args.URL)
	if err != nil {
		return "fetch_url refused: " + err.Error(), nil
	}

	printToolAction(fmt.Sprintf("fetch_url(%s)", u.String()))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build fetch request: %w", err)
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", "text/html, text/plain, application/json;q=0.9, application/xhtml+xml;q=0.8")

	resp, err := fetchHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", u.Redacted(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("fetch %s: HTTP %s", resp.Request.URL.Redacted(), resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", resp.Request.URL.Redacted(), err)
	}
	if len(body) > fetchMaxBodyBytes {
		return "", fmt.Errorf("fetch %s: response exceeds %d bytes", resp.Request.URL.Redacted(), fetchMaxBodyBytes)
	}

	mediaType := responseMediaType(resp.Header.Get("Content-Type"), body)
	var title, text string
	switch mediaType {
	case "text/html", "application/xhtml+xml":
		title, text, err = extractHTMLText(body)
	case "text/plain", "application/json":
		text = strings.TrimSpace(string(body))
	default:
		return "", fmt.Errorf("fetch %s: unsupported content type %q", resp.Request.URL.Redacted(), mediaType)
	}
	if err != nil {
		return "", fmt.Errorf("extract %s: %w", resp.Request.URL.Redacted(), err)
	}
	if text == "" {
		text = "(no readable text)"
	}

	text, truncated := truncateUTF8(text, fetchMaxTextBytes)
	var out strings.Builder
	fmt.Fprintf(&out, "URL: %s\n", resp.Request.URL.String())
	if title != "" {
		fmt.Fprintf(&out, "Title: %s\n", title)
	}
	fmt.Fprintf(&out, "Content-Type: %s\n\n%s", mediaType, text)
	if truncated {
		out.WriteString("\n\n[truncated: use a more specific URL or another source]")
	}
	return out.String(), nil
}

func newSafeHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // A proxy could resolve a public hostname onto a private network, bypassing the dial guard.
	transport.DialContext = publicDialContext
	transport.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{
		Transport: transport,
		Timeout:   fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= fetchMaxRedirects {
				return errors.New("too many redirects")
			}
			_, err := validatePublicURL(req.URL.String())
			return err
		},
	}
}

func validatePublicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("only http and https URLs are allowed")
	}
	if u.Hostname() == "" {
		return nil, errors.New("URL must include a host")
	}
	if u.User != nil {
		return nil, errors.New("URLs containing credentials are not allowed")
	}
	if strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), "localhost") {
		return nil, errors.New("local addresses are not allowed")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !isPublicIP(ip) {
		return nil, errors.New("private or non-public addresses are not allowed")
	}
	return u, nil
}

func publicDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if !isPublicIP(net.IP(ip.AsSlice())) {
			return nil, fmt.Errorf("host %q resolves to a private or non-public address", host)
		}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func isPublicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() &&
		!ip.IsPrivate() &&
		!ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast()
}

func responseMediaType(header string, body []byte) string {
	if header != "" {
		if mediaType, _, err := mime.ParseMediaType(header); err == nil {
			return strings.ToLower(mediaType)
		}
	}
	return strings.ToLower(strings.SplitN(http.DetectContentType(body), ";", 2)[0])
}

func extractHTMLText(body []byte) (string, string, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}

	var titleParts, textParts []string
	var walk func(*html.Node, bool, bool)
	walk = func(n *html.Node, hidden, inTitle bool) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "svg", "template":
				hidden = true
			case "title":
				inTitle = true
			}
		}
		if n.Type == html.TextNode && !hidden {
			value := strings.Join(strings.Fields(n.Data), " ")
			if value != "" {
				if inTitle {
					titleParts = append(titleParts, value)
				} else {
					textParts = append(textParts, value)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, hidden, inTitle)
		}
	}
	walk(doc, false, false)
	return strings.Join(titleParts, " "), strings.Join(textParts, "\n"), nil
}

func truncateUTF8(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]), true
}
