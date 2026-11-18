package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sagernet/sing-box/common/srs"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
)

const (
	// ruleSetUpdateTimeout covers the core's own 60 s download limit.
	ruleSetUpdateTimeout = 75 * time.Second
	// ruleSetSourceLimit bounds a rule set read from the core or a download.
	ruleSetSourceLimit = 32 << 20
)

// errRuleSetTooLarge is a rule set source over ruleSetSourceLimit.
var errRuleSetTooLarge = fmt.Errorf("rule-set exceeds %d MiB", ruleSetSourceLimit>>20)

// ruleSetSourceResponse is the rule set source content the client views,
// in sing-box source format, with where it came from.
type ruleSetSourceResponse struct {
	Version int    `json:"version"`
	Rules   any    `json:"rules"`
	From    string `json:"from"`
}

// controllerRequest sends a request to the running core's Clash API.
func (s *server) controllerRequest(ctx context.Context, method, path string) (*http.Response, error) {
	address, secret, ok := s.runningController()
	if !ok {
		return nil, errCoreUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	return http.DefaultClient.Do(request)
}

var errCoreUnavailable = errors.New("core runtime unavailable")

// coreMessage is the error text of a Clash API reply.
func coreMessage(body []byte, status string) string {
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Message != "" {
		return payload.Message
	}
	if text := strings.TrimSpace(string(body)); text != "" {
		return text
	}
	return status
}

// updateRuleSet asks the core to download a remote rule set, or read a
// local one again, now.
func (s *server) updateRuleSet(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Tag string `json:"tag"`
	}
	if !decode(w, r, &q) {
		return
	}
	if strings.TrimSpace(q.Tag) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tag required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ruleSetUpdateTimeout)
	defer cancel()
	response, err := s.controllerRequest(ctx, http.MethodPut, "/rulesets/"+url.PathEscape(q.Tag))
	if errors.Is(err, errCoreUnavailable) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if response.StatusCode != http.StatusOK {
		writeJSON(w, response.StatusCode, map[string]string{"error": coreMessage(body, response.Status)})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// ruleSetSource returns the rules of a rule set before its overrides: from
// the running core when it holds the rule set, otherwise downloaded from
// url.
func (s *server) ruleSetSource(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Tag string `json:"tag"`
		URL string `json:"url"`
	}
	if !decode(w, r, &q) {
		return
	}
	if strings.TrimSpace(q.Tag) == "" && strings.TrimSpace(q.URL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tag or url required"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	coreStatus, coreError := http.StatusNotFound, "rule-set not found"
	// The core's content is the rule set's while it loads it from the url
	// asked for: after the link changed, before the configuration is
	// applied again, the new link's content shows.
	if applied, ok := s.appliedRuleSetURL(q.Tag); strings.TrimSpace(q.Tag) != "" && (!ok || q.URL == "" || applied == q.URL) {
		result, status, err := s.coreRuleSetSource(r.Context(), q.Tag)
		if err == nil {
			writeJSON(w, http.StatusOK, result)
			return
		}
		coreStatus, coreError = status, err.Error()
	}
	if strings.TrimSpace(q.URL) == "" {
		writeJSON(w, coreStatus, map[string]string{"error": coreError})
		return
	}
	result, status, err := downloadRuleSetSource(r.Context(), q.URL)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// appliedRuleSetURL is the url of the rule set tag in the configuration
// the core runs (runtime/config.json), "" for an inline or local one; ok
// is false when that configuration cannot be read or has no rule set tag.
func (s *server) appliedRuleSetURL(tag string) (address string, ok bool) {
	data, err := os.ReadFile(filepath.Join(s.dataDir, "runtime", "config.json"))
	if err != nil {
		return "", false
	}
	var config struct {
		Route struct {
			RuleSet []struct {
				Type string `json:"type"`
				Tag  string `json:"tag"`
				URL  string `json:"url"`
			} `json:"rule_set"`
		} `json:"route"`
	}
	if json.Unmarshal(data, &config) != nil {
		return "", false
	}
	for _, r := range config.Route.RuleSet {
		if r.Tag == tag {
			if r.Type != "remote" {
				return "", true
			}
			return r.URL, true
		}
	}
	return "", false
}

// coreRuleSetSource reads the source content of tag from the running core,
// with the status to report when it fails.
func (s *server) coreRuleSetSource(ctx context.Context, tag string) (ruleSetSourceResponse, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, err := s.controllerRequest(ctx, http.MethodGet, "/rulesets/"+url.PathEscape(tag)+"/source")
	if errors.Is(err, errCoreUnavailable) {
		return ruleSetSourceResponse{}, http.StatusServiceUnavailable, err
	}
	if err != nil {
		return ruleSetSourceResponse{}, http.StatusBadGateway, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, ruleSetSourceLimit+1))
	if err != nil {
		return ruleSetSourceResponse{}, http.StatusBadGateway, err
	}
	if len(body) > ruleSetSourceLimit {
		return ruleSetSourceResponse{}, http.StatusBadGateway, errRuleSetTooLarge
	}
	if response.StatusCode != http.StatusOK {
		status := http.StatusBadGateway
		if response.StatusCode == http.StatusNotFound {
			status = http.StatusNotFound
		}
		return ruleSetSourceResponse{}, status, errors.New(coreMessage(body, response.Status))
	}
	var content struct {
		Version int               `json:"version"`
		Rules   []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(body, &content); err != nil {
		return ruleSetSourceResponse{}, http.StatusBadGateway, err
	}
	if content.Rules == nil {
		content.Rules = []json.RawMessage{}
	}
	return ruleSetSourceResponse{Version: content.Version, Rules: content.Rules, From: "core"}, http.StatusOK, nil
}

// downloadRuleSetSource downloads a rule set as /fetch does and decodes it,
// binary or source.
func downloadRuleSetSource(ctx context.Context, address string) (ruleSetSourceResponse, int, error) {
	if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
		return ruleSetSourceResponse{}, http.StatusBadRequest, errors.New("http/https URL required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return ruleSetSourceResponse{}, http.StatusBadRequest, err
	}
	request.Header.Set("User-Agent", fetchUserAgent)
	response, err := newFetchClient().Do(request)
	if err != nil {
		return ruleSetSourceResponse{}, http.StatusBadGateway, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ruleSetSourceResponse{}, http.StatusBadGateway, errors.New("download rule-set: " + response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, ruleSetSourceLimit+1))
	if err != nil {
		return ruleSetSourceResponse{}, http.StatusBadGateway, err
	}
	if len(body) > ruleSetSourceLimit {
		return ruleSetSourceResponse{}, http.StatusBadGateway, errRuleSetTooLarge
	}
	content, err := decodeRuleSetSource(body)
	if err != nil {
		return ruleSetSourceResponse{}, http.StatusUnprocessableEntity, err
	}
	rules := content.Options.Rules
	if rules == nil {
		rules = []option.HeadlessRule{}
	}
	return ruleSetSourceResponse{Version: int(content.Version), Rules: rules, From: "download"}, http.StatusOK, nil
}

// decodeRuleSetSource decodes a binary (srs) or source (JSON) rule set,
// keeping the domain and address lists of binary ones.
func decodeRuleSetSource(content []byte) (option.PlainRuleSetCompat, error) {
	if bytes.HasPrefix(content, srs.MagicBytes[:]) {
		ruleSet, err := srs.Read(bytes.NewReader(content), true)
		if err != nil {
			return ruleSet, fmt.Errorf("decode binary rule-set: %w", err)
		}
		return ruleSet, nil
	}
	ruleSet, err := sjson.UnmarshalExtended[option.PlainRuleSetCompat](content)
	if err != nil {
		return ruleSet, fmt.Errorf("decode source rule-set: %w", err)
	}
	return ruleSet, nil
}
