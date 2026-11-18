package profiles

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// MaxConfigImportBytes bounds a configuration downloaded for import.
const MaxConfigImportBytes = 5 * 1024 * 1024

var headerName = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

// blockedHeaders are managed by the network layer and cannot be set.
var blockedHeaders = map[string]bool{
	"connection": true, "content-length": true, "expect": true, "host": true, "keep-alive": true,
	"proxy-authorization": true, "proxy-connection": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
}

// NormalizeConfigURL checks an import URL: HTTP or HTTPS, without
// credentials, which belong in the custom headers.
func NormalizeConfigURL(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "") {
		return "", errors.New("请输入完整的 HTTP 或 HTTPS URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("URL 仅支持 HTTP 或 HTTPS")
	}
	// An opaque URL ("https:example.com") has no host to request.
	if u.Host == "" {
		return "", errors.New("请输入完整的 HTTP 或 HTTPS URL")
	}
	if u.User != nil {
		return "", errors.New("请将认证信息填写在自定义 Header 中")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

// ParseCustomHeaders reads "Name: value" lines, skipping blank ones.
func ParseCustomHeaders(value string) (map[string]string, error) {
	headers := map[string]string{}
	total := 0
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for index, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		separator := strings.Index(line, ":")
		if separator < 1 {
			return nil, fmt.Errorf("第 %d 行 Header 格式应为“名称: 值”", index+1)
		}
		name := strings.TrimSpace(line[:separator])
		headerValue := strings.TrimSpace(line[separator+1:])
		lower := strings.ToLower(name)
		if !headerName.MatchString(name) {
			return nil, fmt.Errorf("第 %d 行 Header 名称无效", index+1)
		}
		if blockedHeaders[lower] {
			return nil, fmt.Errorf("第 %d 行 Header 由网络层管理，无法自定义", index+1)
		}
		if strings.ContainsAny(headerValue, "\r\n\x00") {
			return nil, fmt.Errorf("第 %d 行 Header 值无效", index+1)
		}
		for existing := range headers {
			if strings.ToLower(existing) == lower {
				return nil, fmt.Errorf("Header「%s」重复", name)
			}
		}
		total += len(name) + len(headerValue)
		if len(headers) >= 50 || total > 32*1024 {
			return nil, errors.New("自定义 Header 数量或总长度超出限制")
		}
		headers[name] = headerValue
	}
	return headers, nil
}

// URLResponse is a downloaded configuration's status and body.
type URLResponse struct {
	Status int
	Body   string
}

// URLRequester downloads a URL with headers; the console asks
// kiko-server (POST /fetch).
type URLRequester func(ctx context.Context, url string, headers map[string]string) (URLResponse, error)

// FetchConfigText downloads a configuration for import: a successful,
// non-empty response within MaxConfigImportBytes.
func FetchConfigText(ctx context.Context, rawURL string, headers map[string]string, requester URLRequester) (string, error) {
	normalized, err := NormalizeConfigURL(rawURL)
	if err != nil {
		return "", err
	}
	result, err := requester(ctx, normalized, headers)
	if err != nil {
		return "", err
	}
	if result.Status < 200 || result.Status >= 300 {
		detail := strings.TrimSpace(result.Body)
		if r := []rune(detail); len(r) > 300 {
			detail = string(r[:300])
		}
		if detail == "" {
			detail = fmt.Sprintf("下载失败（HTTP %d）", result.Status)
		}
		return "", errors.New(detail)
	}
	if len(result.Body) == 0 {
		return "", errors.New("下载内容为空")
	}
	if len(result.Body) > MaxConfigImportBytes {
		return "", errors.New("配置文件超过 5 MiB 限制")
	}
	return result.Body, nil
}
