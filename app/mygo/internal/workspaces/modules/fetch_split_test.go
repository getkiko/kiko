package modules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

const lanModURL = "http://nas.local/demo.sgmodule"

// splitFetchers answers the main address from a LAN host and refuses every
// declared download from a local address, as a public-only server does.
func splitFetchers(text string) (main, declared Fetcher) {
	main = func(_ context.Context, url string) ([]byte, error) {
		if url != lanModURL {
			return nil, errors.New("unexpected main fetch " + url)
		}
		return []byte(text), nil
	}
	declared = func(_ context.Context, url string) ([]byte, error) {
		if strings.Contains(url, "192.168.") || strings.Contains(url, "nas.local") {
			return nil, errors.New("不允许访问内网地址")
		}
		if url == scriptURL {
			return []byte(scriptBody), nil
		}
		return nil, errors.New("HTTP 404")
	}
	return main, declared
}

func TestLANMainAddressInstalls(t *testing.T) {
	f := newFixture(t, fixtureDoc(), 1000, 800)
	main, declared := splitFetchers(moduleText(baseHosts))
	f.w.SetFetchers(main, declared)
	f.fetchInstall(lanModURL)
	testkit.UntilText(t, f.tt, "将解密 2 个主机名")
	f.wantNot("不允许访问内网地址")
}

func TestLANScriptAddressFailsAndIsNamed(t *testing.T) {
	lanScript := "http://192.168.1.5/price.js"
	text := strings.Replace(moduleText(baseHosts), scriptURL, lanScript, 1)
	f := newFixture(t, fixtureDoc(), 1000, 800)
	main, declared := splitFetchers(text)
	f.w.SetFetchers(main, declared)
	f.fetchInstall(lanModURL)
	testkit.UntilText(t, f.tt, "1 个下载失败")
	f.want(lanScript + "：不允许访问内网地址")
	f.wantNot(lanModURL + "：")
}

// fetchServer answers /info with the capabilities and /fetch with a module
// text, recording the body of each fetch request.
func fetchServer(t *testing.T, capabilities map[string]bool) (*api.Client, *[]map[string]any) {
	t.Helper()
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/info":
			_ = json.NewEncoder(w).Encode(api.ServerInfo{Capabilities: capabilities})
		case "/api/v1/fetch":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			requests = append(requests, body)
			_ = json.NewEncoder(w).Encode(api.FetchResult{Status: 200, Body: "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return api.New(srv.URL), &requests
}

func TestAPIFetcherLimitsRedirectsOnNewServers(t *testing.T) {
	client, requests := fetchServer(t, map[string]bool{"fetchPublic": true, "fetchPublicRedirects": true})
	body, err := APIFetcher(client)(context.Background(), lanModURL)
	if err != nil || string(body) != "ok" {
		t.Fatalf("body %q err %v", body, err)
	}
	if len(*requests) != 1 || (*requests)[0]["publicRedirects"] != true || (*requests)[0]["publicOnly"] != nil || (*requests)[0]["url"] != lanModURL {
		t.Errorf("requests %v", *requests)
	}
}

func TestAPIFetcherOnOldServerStillDownloads(t *testing.T) {
	client, requests := fetchServer(t, map[string]bool{"fetchPublic": true})
	body, err := APIFetcher(client)(context.Background(), lanModURL)
	if err != nil || string(body) != "ok" {
		t.Fatalf("body %q err %v", body, err)
	}
	if len(*requests) != 1 || (*requests)[0]["publicRedirects"] != nil || (*requests)[0]["publicOnly"] != nil {
		t.Errorf("requests %v", *requests)
	}
}

func TestDuplicateModuleIDShowsOneCard(t *testing.T) {
	first, dup := installed(t, moduleText(baseHosts)), installed(t, moduleText(baseHosts))
	first.Name, dup.Name = "第一个模块", "重复的模块"
	f := newFixture(t, fixtureDoc(first, dup), 1280, 700)
	testkit.UntilText(t, f.tt, "第一个模块")
	f.wantNot("重复的模块")
	if n := len(f.modules()); n != 2 {
		t.Fatalf("modules = %d", n)
	}
}
