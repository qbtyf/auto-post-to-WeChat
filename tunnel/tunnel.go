// Package tunnel 实现「exe 内置自动隧道」（决策点 3 · 方案 A）：
//
// 程序启动后自动用 SSH 连上 config.ECSHost 指定的服务器；之后所有微信公众号
// API 请求通过 http.Transport 的 DialContext 直接在 SSH 通道上拨号出去
// （该服务器出口 IP 固定，需提前加入公众号后台 IP 白名单）。
//
// 【2026-10-05 重大重构】原实现是 SSH + 本地 SOCKS5 动态转发（对应 Python 版
// paramiko 的 socks 方案），实测踩坑：SSH 转发通道在「先读后写」时序下会立即
// 收到假 EOF（SOCKS 协议恰好是客户端等回复才发数据，必踩）；而「先写后读」
// 完全正常（对照实验：先写后读收到 HTTP 200，先读立即 EOF）。
// HTTPS 请求天然是"先写"（TLS ClientHello 先发），因此直接用 DialContext
// 走 SSH 通道，跳过 SOCKS5 层——代码更少，且完全匹配稳定模式。
package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// Debugf 调试日志钩子：默认静默；诊断程序把它设为 fmt.Printf 即可看到全过程。
// （正式 exe 不设置，零输出）
var Debugf = func(format string, args ...any) {}

// Tunnel 一条正在运行的 SSH 隧道。
type Tunnel struct {
	sshConn *ssh.Client
}

// Close 关闭隧道（程序退出或上传完成时调用）。
func (t *Tunnel) Close() {
	if t.sshConn != nil {
		_ = t.sshConn.Close()
	}
}

// privateKeyCandidates 常见 SSH 私钥文件名（按顺序探测，先密钥后密码）。
var privateKeyCandidates = []string{
	"id_ed25519", // 甲/乙电脑登记过的 ed25519 密钥
	"id_rsa",
}

// authMethods 组装 SSH 认证方式：
//  1. 优先用本机 %USERPROFILE%\.ssh\ 下的私钥（免密，最稳）
//  2. 私钥不可用时退回密码认证（密码由编译期嵌入，见 config 包）
func authMethods(password string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	home, err := os.UserHomeDir()
	if err == nil {
		for _, name := range privateKeyCandidates {
			keyPath := filepath.Join(home, ".ssh", name)
			data, err := os.ReadFile(keyPath)
			if err != nil {
				continue // 该文件不存在，试下一个
			}
			signer, err := ssh.ParsePrivateKey(data)
			if err != nil {
				continue // 解析失败（如带口令的私钥），试下一个
			}
			methods = append(methods, ssh.PublicKeys(signer))
			break // 拿到第一把可用私钥就够
		}
	}

	if password != "" {
		methods = append(methods, ssh.Password(password))
		methods = append(methods, ssh.KeyboardInteractive(
			func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					answers[i] = password
				}
				return answers, nil
			}))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("没有可用的 SSH 认证方式（本机无私钥文件，也未嵌入密码）")
	}
	return methods, nil
}

// Start 建立 SSH 隧道（仅建立 SSH 连接；请求拨号由 NewHTTPClient 按需进行）。
// 参数：ECS 地址/端口/用户名/密码（可为空）。
func Start(host, port, user, password string) (*Tunnel, error) {
	methods, err := authMethods(password)
	if err != nil {
		return nil, err
	}

	sshConfig := &ssh.ClientConfig{
		User:    user,
		Auth:    methods,
		Timeout: 10 * time.Second,
		// 说明：家庭内网自用工具，不做严格主机指纹校验（Python 版 paramiko
		// 原实现同样是 AutoAddPolicy 等价行为）。如需收紧可换成固定指纹。
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	addr := net.JoinHostPort(host, port)
	sshConn, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接 ECS(%s) 失败: %w", addr, err)
	}
	Debugf("[隧道] SSH 连接成功 %s", addr)

	return &Tunnel{sshConn: sshConn}, nil
}

// NewHTTPClient 返回一个走 SSH 隧道的 HTTP 客户端：
// 每次 HTTP 请求通过 DialContext 在 SSH 通道上拨号到目标地址
// （DNS 也在 ECS 端解析），出站 IP = ECS 公网 IP，即通过公众号 IP 白名单。
func (t *Tunnel) NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				Debugf("[隧道] SSH 拨号 %s %s ...", network, addr)
				type result struct {
					conn net.Conn
					err  error
				}
				ch := make(chan result, 1)
				go func() {
					conn, err := t.sshConn.Dial(network, addr)
					ch <- result{conn, err}
				}()
				select {
				case <-ctx.Done(): // 请求被取消/超时
					go func() {
						if r := <-ch; r.conn != nil {
							_ = r.conn.Close()
						}
					}()
					return nil, ctx.Err()
				case r := <-ch:
					if r.err != nil {
						Debugf("[隧道] SSH 拨号失败: %v", r.err)
						return nil, r.err
					}
					Debugf("[隧道] SSH 拨号成功 %s", addr)
					return r.conn, nil
				}
			},
		},
	}
}
