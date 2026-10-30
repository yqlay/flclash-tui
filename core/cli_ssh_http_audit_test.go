//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestBugAuditHTTPExpectContinue(t *testing.T) {
	for _, action := range []string{"continue", "reject", "upstream"} {
		t.Run(action, func(t *testing.T) {
			const payload = "upload through SSH relay"
			upstream := listenCLITestTCP(t)
			defer upstream.Close()
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- func() error {
					connection, err := upstream.Accept()
					if err != nil {
						return err
					}
					defer connection.Close()
					_ = connection.SetDeadline(time.Now().Add(4 * time.Second))
					if _, err := readCLIInboundSOCKS5(connection); err != nil {
						return err
					}
					if action == "upstream" {
						return writeCLISOCKS5Reply(connection, 0x05)
					}
					if err := writeCLISOCKS5Reply(connection, 0); err != nil {
						return err
					}
					request, err := http.ReadRequest(bufio.NewReader(connection))
					if err != nil {
						return err
					}
					if action == "reject" {
						_, err = io.WriteString(connection, "HTTP/1.1 413 Content Too Large\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
						return err
					}
					if _, err := io.WriteString(connection, "HTTP/1.1 100 Continue\r\n\r\n"); err != nil {
						return err
					}
					body, err := io.ReadAll(request.Body)
					_ = request.Body.Close()
					if err != nil || string(body) != payload {
						return fmt.Errorf("upstream body %q: %v", body, err)
					}
					_, err = fmt.Fprintf(connection, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
					return err
				}()
			}()
			relay, port, done := startCLITestRelay(t, upstream.Addr().(*net.TCPAddr).Port)
			defer func() {
				relay.stop()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := fmt.Fprintf(connection, "POST http://example.test/upload HTTP/1.1\r\nHost: example.test\r\nExpect: 100-continue\r\nContent-Length: %d\r\n\r\n", len(payload)); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(connection)
			response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Fatalf("proxy did not forward the response before the upload body: %v", err)
			}
			if action == "continue" {
				if response.StatusCode != http.StatusContinue {
					t.Fatalf("interim response = %s", response.Status)
				}
				_ = response.Body.Close()
				if _, err := io.WriteString(connection, payload); err != nil {
					t.Fatal(err)
				}
				response, err = http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
				if err != nil {
					t.Fatal(err)
				}
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]int{"continue": http.StatusOK, "reject": http.StatusRequestEntityTooLarge, "upstream": http.StatusBadGateway}[action]
			if response.StatusCode != want || action == "continue" && string(body) != payload {
				t.Fatalf("response = %s %q", response.Status, body)
			}
			select {
			case err := <-serverDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("upstream did not finish")
			}
		})
	}
}
