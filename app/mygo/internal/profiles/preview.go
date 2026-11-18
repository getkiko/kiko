package profiles

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// secretFields are keys whose values are credentials, masked wherever they
// are.
var secretFields = map[string]bool{
	"auth": true, "auth_key": true, "auth_str": true, "authorization": true, "proxy-authorization": true, "cookie": true,
	"client_id": true, "client_key": true, "client_secret": true, "credential": true, "credentials": true, "key": true,
	"password": true, "passwd": true, "passphrase": true, "pin": true, "private_key": true, "private_key_path": true,
	"pre_shared_key": true, "psk": true, "public_key": true, "peer_public_key": true, "secret": true, "secret_key": true,
	"secret_path": true, "short_id": true, "token": true, "user": true, "username": true, "uuid": true,
}

var serverPortPattern = regexp.MustCompile(`(?i)^server_ports?$`)

func isUpstreamPath(path []string) bool {
	if len(path) == 0 {
		return false
	}
	switch path[0] {
	case "outbounds", "endpoints", "services":
		return true
	}
	return len(path) > 1 && path[0] == "dns" && path[1] == "servers"
}

func isEndpointPeerPath(path []string) bool {
	return len(path) > 0 && path[0] == "endpoints" && slices.Contains(path, "peers")
}

func isSecretField(field string, path []string) bool {
	return secretFields[strings.ToLower(field)] || (field == "name" && slices.Contains(path, "users"))
}

func isServerAddressField(field string, path []string) bool {
	if isUpstreamPath(path) {
		switch field {
		case "server", "server_name", "remote", "exit_node", "control_url", "relay_server_static_endpoints":
			return true
		}
	}
	// The Host headers and hosts of a V2Ray transport name the CDN domain
	// fronting the server.
	if isUpstreamPath(path) && strings.EqualFold(field, "host") && slices.Contains(path, "transport") {
		return true
	}
	return isEndpointPeerPath(path) && field == "address"
}

func isServerPortField(field string, path []string) bool {
	return (isUpstreamPath(path) && serverPortPattern.MatchString(field)) ||
		(len(path) > 0 && path[0] == "endpoints" && field == "remote_port") ||
		(isEndpointPeerPath(path) && field == "port")
}

// RedactConfigForCopy masks upstream addresses, server ports and
// credentials of a sing-box configuration while keeping its routing data,
// for sharing it. Each distinct masked text gets one alias, "<masked 1>" in Chinese,
// never one that already appears in the configuration.
func RedactConfigForCopy(config string) (string, error) {
	parsed, err := decodeOrdered([]byte(config))
	if err != nil {
		return "", fmt.Errorf("配置不是有效 JSON：%w", err)
	}
	aliases := map[string]string{}
	next := 1
	aliasFor := func(value string) string {
		alias, ok := aliases[value]
		if !ok {
			for {
				alias = fmt.Sprintf("<已屏蔽 %d>", next)
				next++
				if !strings.Contains(config, alias) {
					break
				}
			}
			aliases[value] = alias
		}
		return alias
	}
	var maskSensitive func(any) any
	maskSensitive = func(value any) any {
		switch x := value.(type) {
		case []any:
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = maskSensitive(item)
			}
			return out
		case *object:
			out := &object{keys: x.keys, values: map[string]any{}}
			for _, key := range x.keys {
				out.values[key] = maskSensitive(x.values[key])
			}
			return out
		case json.Number:
			return json.Number("0")
		case string:
			if x != "" {
				return aliasFor(x)
			}
		}
		return value
	}
	var maskPort func(any) any
	maskPort = func(value any) any {
		switch x := value.(type) {
		case []any:
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = maskPort(item)
			}
			return out
		case json.Number, string:
			return json.Number("0")
		}
		return value
	}
	var redact func(value any, field string, path []string) any
	redact = func(value any, field string, path []string) any {
		if isSecretField(field, path) || isServerAddressField(field, path) {
			return maskSensitive(value)
		}
		if isServerPortField(field, path) {
			return maskPort(value)
		}
		switch x := value.(type) {
		case []any:
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = redact(item, field, path)
			}
			return out
		case *object:
			out := &object{keys: x.keys, values: map[string]any{}}
			for _, key := range x.keys {
				out.values[key] = redact(x.values[key], key, append(slices.Clip(path), key))
			}
			return out
		}
		return value
	}
	return encodeOrdered(redact(parsed, "", nil)) + "\n", nil
}

// FindConfigMatches returns the byte offsets in text of each match of
// query, case-insensitive and literal, not overlapping.
func FindConfigMatches(text, query string) []int {
	if query == "" {
		return nil
	}
	var matches []int
	n := len(query)
	for i := 0; i+n <= len(text); {
		if strings.EqualFold(text[i:i+n], query) {
			matches = append(matches, i)
			i += n
			continue
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	return matches
}
