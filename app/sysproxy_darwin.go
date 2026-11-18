//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func networkServices() ([]string, error) {
	return networkServicesContext(context.Background())
}

func networkServicesContext(ctx context.Context) ([]string, error) {
	out, err := networksetupOutput(ctx, "-listallnetworkservices")
	if err != nil {
		return nil, err
	}
	var services []string
	for i, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if i == 0 || line == "" || strings.HasPrefix(line, "*") {
			continue
		}
		services = append(services, line)
	}
	return services, nil
}

func networksetup(args ...string) error {
	return networksetupContext(context.Background(), args...)
}

func networksetupContext(ctx context.Context, args ...string) error {
	if out, err := networksetupOutput(ctx, args...); err != nil {
		return fmt.Errorf("networksetup %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func networksetupOutput(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := hiddenCommandContext(ctx, "networksetup", args...)
	prepareProcess(cmd)
	cmd.Cancel = func() error { return forceProcess(cmd) }
	cmd.WaitDelay = 200 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return out, err
}

func setSystemProxy(a sysProxyApply, _ string) error {
	services, err := networkServices()
	if err != nil {
		return err
	}
	port := strconv.Itoa(a.Port)
	var errs []error
	for _, svc := range services {
		var commands [][]string
		if a.PAC != "" {
			commands = [][]string{
				{"-setautoproxyurl", svc, a.PAC}, {"-setautoproxystate", svc, "on"},
				{"-setwebproxystate", svc, "off"}, {"-setsecurewebproxystate", svc, "off"}, {"-setsocksfirewallproxystate", svc, "off"},
			}
		} else {
			bypass := a.Bypass
			if len(bypass) == 0 {
				bypass = []string{"Empty"}
			}
			commands = [][]string{
				{"-setwebproxy", svc, a.Host, port}, {"-setsecurewebproxy", svc, a.Host, port},
				{"-setsocksfirewallproxy", svc, a.Host, port}, {"-setautoproxystate", svc, "off"},
				append([]string{"-setproxybypassdomains", svc}, bypass...),
			}
		}
		for _, args := range commands {
			if err = networksetup(args...); err != nil {
				errs = append(errs, err)
				break
			}
		}
	}
	if len(errs) > 0 && len(errs) == len(services) {
		return errors.Join(errs...)
	}
	return nil
}

func clearSystemProxy(_ string) error {
	services, err := networkServices()
	if err != nil {
		return err
	}
	var errs []error
	for _, svc := range services {
		for _, flag := range []string{"-setwebproxystate", "-setsecurewebproxystate", "-setsocksfirewallproxystate", "-setautoproxystate"} {
			if err = networksetup(flag, svc, "off"); err != nil {
				errs = append(errs, err)
				break
			}
		}
	}
	if len(errs) > 0 && len(errs) == len(services) {
		return errors.Join(errs...)
	}
	return nil
}

func recoverSystemProxy(_ string) error { return nil }

func systemProxyMatches(a sysProxyApply) bool {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return true
	}
	values := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), " : "); ok {
			values[key] = value
		}
	}
	if a.PAC != "" {
		return values["ProxyAutoConfigEnable"] == "1" && values["ProxyAutoConfigURLString"] == a.PAC
	}
	port := strconv.Itoa(a.Port)
	for _, prefix := range []string{"HTTP", "HTTPS", "SOCKS"} {
		if values[prefix+"Enable"] != "1" || values[prefix+"Proxy"] != a.Host || values[prefix+"Port"] != port {
			return false
		}
	}
	return values["ProxyAutoConfigEnable"] != "1"
}
