package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "yonder")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") }))
	defer up.Close()
	upPort := up.Listener.Addr().(*net.TCPAddr).Port

	free, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	listenPort := free.Addr().(*net.TCPAddr).Port
	free.Close()

	cmd := exec.Command(bin, fmt.Sprint(upPort), "--listen", fmt.Sprint(listenPort))
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	urlRe := regexp.MustCompile(fmt.Sprintf(`http://[0-9.]+:%d`, listenPort))
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if u := urlRe.FindString(sc.Text()); u != "" {
				select {
				case found <- u:
				default:
				}
			}
		}
	}()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	var url string
	select {
	case url = <-found:
	case <-exited:
		if strings.Contains(stderr.String(), "no network connection found") {
			t.Skip("no LAN address on this machine")
		}
		t.Fatalf("yonder exited early: %s", stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("no URL printed within 10s")
	}

	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello" {
		t.Errorf("body = %q, want hello", body)
	}

	if runtime.GOOS == "windows" {
		return // no os.Interrupt delivery to child processes
	}
	cmd.Process.Signal(os.Interrupt)
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("exit after interrupt: %v, want code 0", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("yonder did not exit within 3s of interrupt")
	}
}
