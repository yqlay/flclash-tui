//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const cliCommandProxyTimeout = time.Second

var (
	cliCommandLookPath = exec.LookPath
	cliCommandExec     = syscall.Exec
)

func wrappedCommand(args []string) error {
	if len(args) == 0 {
		return errors.New(
			"no command specified; usage: flc COMMAND [ARG...]\n" +
				"未指定要执行的命令；用法：flc 命令 [参数...]",
		)
	}

	proxyURL, err := activeCLIProxyURL()
	if err != nil {
		return err
	}
	executable, err := cliCommandLookPath(args[0])
	if err != nil {
		return fmt.Errorf(
			"command not found or cannot be executed: %q (%v)\n"+
				"找不到命令或命令无法执行：%q（%v）",
			args[0],
			err,
			args[0],
			err,
		)
	}
	args = cliWrappedCommandArguments(executable, args)
	environment := cliProxyEnvironment(os.Environ(), proxyURL)
	if err := cliCommandExec(executable, args, environment); err != nil {
		return fmt.Errorf(
			"cannot start command %q: %v\n无法启动命令 %q：%v",
			args[0],
			err,
			args[0],
			err,
		)
	}
	return nil
}

func cliWrappedCommandArguments(executable string, args []string) []string {
	result := append([]string(nil), args...)
	if filepath.Base(executable) != "wget" {
		return result
	}
	for _, argument := range result[1:] {
		if argument == "--no-config" {
			return result
		}
	}
	return append(
		[]string{result[0], "--no-config"},
		result[1:]...,
	)
}

func activeCLIProxyURL() (string, error) {
	paths, err := resolvePaths("", "")
	if err != nil {
		return "", fmt.Errorf(
			"cannot locate the FlClash data directory: %v\n"+
				"无法确定 FlClash 数据目录：%v",
			err,
			err,
		)
	}
	return activeCLIProxyURLForPaths(paths)
}

func activeCLIProxyURLForPaths(paths cliPaths) (string, error) {
	client := newTUIServiceClient(paths.homeDir)
	status, statusErr := client.status()
	if statusErr == nil && status.Version != "" && status.ProtocolVersion != 0 &&
		(status.Version != cliVersion ||
			status.ProtocolVersion != tuiServiceProtocolVersion) {
		client, status, statusErr = ensureTUIService(
			paths,
			defaultCLITestURL,
			false,
			false,
		)
		if statusErr != nil {
			return "", fmt.Errorf(
				"upgrade the FlClash Backend before running flc: %w\n"+
					"运行 flc 前升级 FlClash 后端失败：%w",
				statusErr,
				statusErr,
			)
		}
	}
	if statusErr != nil {
		if _, legacyStatus, found := findLegacyTUIService(paths); found {
			status = legacyStatus
			statusErr = nil
		}
	}
	if statusErr != nil {
		return "", fmt.Errorf(
			"FlClash backend is not running; run `flclash start` first. Details: %v\n"+
				"FlClash 后端未运行；请先执行 `flclash start`。详情：%v",
			statusErr,
			statusErr,
		)
	}
	if !status.Running {
		return "", errors.New(
			"FlClash Core is stopped; run `flclash start` first.\n" +
				"FlClash Core 已停止；请先执行 `flclash start`。",
		)
	}
	if status.Mode == tuiSilentMode {
		privateStatus, err := client.flcProxy()
		if err != nil {
			return "", fmt.Errorf(
				"private FLC listener is unavailable: %v\nFLC 私有代理入口不可用：%v",
				err,
				err,
			)
		}
		proxyURL, err := url.Parse(privateStatus.FLCProxyURL)
		if err != nil || proxyURL.Host == "" || proxyURL.User == nil {
			return "", errors.New(
				"FlClash returned invalid private FLC credentials\n" +
					"FlClash 返回了无效的 FLC 私有认证信息",
			)
		}
		connection, err := net.DialTimeout("tcp", proxyURL.Host, cliCommandProxyTimeout)
		if err != nil {
			return "", fmt.Errorf(
				"private FLC listener is not accepting connections: %v\n"+
					"FLC 私有代理入口无法连接：%v",
				err,
				err,
			)
		}
		_ = connection.Close()
		return privateStatus.FLCProxyURL, nil
	}
	if strings.TrimSpace(status.CoreSocket) == "" {
		return "", errors.New(
			"FlClash reported no Core controller socket; restart the backend and try again.\n" +
				"FlClash 未返回 Core 控制套接字；请重启后端再试。",
		)
	}

	options := controllerOptions{unixSocket: status.CoreSocket}
	controller := controllerClient{
		options: options,
		client: controllerHTTPClientForOptions(
			options,
			cliCommandProxyTimeout,
		),
	}
	data, err := controller.request(http.MethodGet, "/configs", nil)
	if err != nil {
		return "", fmt.Errorf(
			"cannot read the active Proxy port (Mihomo mixed-port) from FlClash Core: %v\n"+
				"无法从 FlClash Core 读取当前代理端口（Mihomo mixed-port）：%v",
			err,
			err,
		)
	}
	var config tuiConfigResponse
	if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf(
			"FlClash Core returned invalid configuration data: %v\n"+
				"FlClash Core 返回了无效的配置数据：%v",
			err,
			err,
		)
	}
	if config.MixedPort <= 0 || config.MixedPort > 65535 {
		return "", fmt.Errorf(
			"FlClash has no usable Proxy port (Mihomo mixed-port, current value: %d).\n"+
				"FlClash 没有可用的代理端口（Mihomo mixed-port，当前值：%d）。",
			config.MixedPort,
			config.MixedPort,
		)
	}

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(config.MixedPort))
	connection, err := net.DialTimeout("tcp", address, cliCommandProxyTimeout)
	if err != nil {
		return "", fmt.Errorf(
			"FlClash proxy port %d is not accepting connections: %v\n"+
				"FlClash 代理端口 %d 无法连接：%v",
			config.MixedPort,
			err,
			config.MixedPort,
			err,
		)
	}
	_ = connection.Close()
	return "http://" + address, nil
}

func cliProxyEnvironment(environment []string, proxyURL string) []string {
	proxyKeys := map[string]bool{
		"HTTP_PROXY":  true,
		"HTTPS_PROXY": true,
		"ALL_PROXY":   true,
		"http_proxy":  true,
		"https_proxy": true,
		"all_proxy":   true,
	}
	result := make([]string, 0, len(environment)+len(proxyKeys))
	for _, item := range environment {
		key, _, found := strings.Cut(item, "=")
		if found && proxyKeys[key] {
			continue
		}
		result = append(result, item)
	}
	for _, key := range []string{
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"ALL_PROXY",
		"http_proxy",
		"https_proxy",
		"all_proxy",
	} {
		result = append(result, key+"="+proxyURL)
	}
	return result
}
