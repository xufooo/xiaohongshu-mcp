package headless_browser

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/cdp"
)

type websocketConnectResult struct {
	websocket *cdp.WebSocket
	err       error
}

func startCancelableWebSocketConnect(ctx context.Context, wsURL string) (<-chan websocketConnectResult, <-chan struct{}) {
	result := make(chan websocketConnectResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ws, err := connectCancelableWebSocket(ctx, wsURL, nil)
		result <- websocketConnectResult{websocket: ws, err: err}
	}()
	return result, done
}

func closeAcceptedConnection(t *testing.T, accepted <-chan net.Conn) {
	t.Helper()
	var conn net.Conn
	select {
	case conn = <-accepted:
	case <-time.After(time.Second):
	}
	if conn != nil {
		closed := make(chan struct{})
		go func() {
			_ = conn.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Errorf("accepted socket close exceeded watchdog")
		}
	}
}

func joinConnect(t *testing.T, done <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Errorf("%s connect goroutine 未在 watchdog 内退出", description)
	}
}

func joinTestServer(t *testing.T, done <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Errorf("%s server goroutine 未在 watchdog 内退出", description)
	}
}

func startWebSocketClose(ws *cdp.WebSocket) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- ws.Close()
	}()
	return done
}

func waitWebSocketClose(done <-chan error) (error, bool) {
	select {
	case err := <-done:
		return err, true
	case <-time.After(time.Second):
		return errors.New("websocket close exceeded watchdog"), false
	}
}

func setReadWatchdog(conn net.Conn) error {
	return conn.SetReadDeadline(time.Now().Add(time.Second))
}

func setWriteWatchdog(conn net.Conn) error {
	return conn.SetWriteDeadline(time.Now().Add(time.Second))
}

func readFullWithWatchdog(conn net.Conn, payload []byte) (int, error) {
	if err := setReadWatchdog(conn); err != nil {
		return 0, err
	}
	return io.ReadFull(conn, payload)
}

func readOneWithWatchdog(conn net.Conn) (int, error) {
	if err := setReadWatchdog(conn); err != nil {
		return 0, err
	}
	return conn.Read(make([]byte, 1))
}

func readPeerCloseWithWatchdog(conn net.Conn) error {
	read, err := readOneWithWatchdog(conn)
	if read != 0 {
		return errors.New("unexpected data received while waiting for peer close")
	}
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("peer connection remained open after read")
		}
		return err
	}
	return nil
}

func requirePeerCloseObserved(t *testing.T, observed <-chan error, description string) {
	t.Helper()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatalf("%s: %v", description, err)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s exceeded independent watchdog", description)
	}
}

func writeWithWatchdog(conn net.Conn, payload []byte) (int, error) {
	if err := setWriteWatchdog(conn); err != nil {
		return 0, err
	}
	return conn.Write(payload)
}

func readStringWithWatchdog(conn net.Conn, reader *bufio.Reader, delimiter byte) (string, error) {
	if err := setReadWatchdog(conn); err != nil {
		return "", err
	}
	return reader.ReadString(delimiter)
}

func readRequestWithWatchdog(conn net.Conn) (*http.Request, error) {
	if err := setReadWatchdog(conn); err != nil {
		return nil, err
	}
	return http.ReadRequest(bufio.NewReader(conn))
}

func writeStringWithWatchdog(conn net.Conn, value string) (int, error) {
	if err := setWriteWatchdog(conn); err != nil {
		return 0, err
	}
	return io.WriteString(conn, value)
}

func readWebSocketTextFrame(conn net.Conn) ([]byte, error) {
	var header [2]byte
	if _, err := readFullWithWatchdog(conn, header[:]); err != nil {
		return nil, err
	}
	if header[0]&0x80 == 0 || header[0]&0x0f != 1 {
		return nil, errors.New("expected one complete websocket text frame")
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := readFullWithWatchdog(conn, extended[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := readFullWithWatchdog(conn, extended[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	if length > 1<<20 {
		return nil, errors.New("websocket test frame exceeded size limit")
	}
	var mask [4]byte
	masked := header[1]&0x80 != 0
	if masked {
		if _, err := readFullWithWatchdog(conn, mask[:]); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, int(length))
	if _, err := readFullWithWatchdog(conn, payload); err != nil {
		return nil, err
	}
	if masked {
		for index := range payload {
			payload[index] ^= mask[index&3]
		}
	}
	return payload, nil
}

func writeWebSocketTextFrame(conn net.Conn, payload []byte) error {
	frame := []byte{0x81}
	switch length := len(payload); {
	case length < 126:
		frame = append(frame, byte(length))
	case length <= 65535:
		frame = append(frame, 126, 0, 0)
		binary.BigEndian.PutUint16(frame[len(frame)-2:], uint16(length))
	default:
		frame = append(frame, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(frame[len(frame)-8:], uint64(length))
	}
	frame = append(frame, payload...)
	for len(frame) > 0 {
		written, err := writeWithWatchdog(conn, frame)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func TestCancelableWebSocketHandshakeClosesDialedConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	requestReceived := make(chan struct{})
	serverDone := make(chan struct{})
	peerCloseObserved := make(chan error, 1)
	accepted := make(chan net.Conn, 1)
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- conn
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := readStringWithWatchdog(conn, reader, '\n'); err != nil {
			return
		}
		for {
			line, err := readStringWithWatchdog(conn, reader, '\n')
			if err != nil {
				return
			}
			if line == "\r\n" {
				close(requestReceived)
				break
			}
		}
		peerCloseObserved <- readPeerCloseWithWatchdog(conn)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	connectResults, connectDone := startCancelableWebSocketConnect(ctx, "ws://"+listener.Addr().String()+"/devtools/browser/test")
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		closeAcceptedConnection(t, accepted)
		joinConnect(t, connectDone, "cancelled handshake")
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Errorf("test server goroutine 未在 watchdog 内退出")
		}
	})
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("handshake request did not reach the test server")
	}
	cancel()
	select {
	case result := <-connectResults:
		if result.err == nil || result.websocket != nil {
			t.Fatalf("cancelled handshake must return an error without a websocket: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not unblock websocket handshake")
	}
	joinConnect(t, connectDone, "cancelled handshake")
	requirePeerCloseObserved(t, peerCloseObserved, "cancelled handshake peer-close observation")
	joinTestServer(t, serverDone, "cancelled handshake")
}

func TestCancelableWebSocketHandshakeDetachesAfterSuccess(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct{})
	peerCloseObserved := make(chan error, 1)
	serverReady := make(chan error, 1)
	serverCallReadDone := make(chan error, 1)
	serverCallWriteDone := make(chan error, 1)
	accepted := make(chan net.Conn, 1)
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			serverReady <- err
			return
		}
		accepted <- conn
		defer conn.Close()
		request, err := readRequestWithWatchdog(conn)
		if err != nil {
			serverReady <- err
			return
		}
		key := request.Header.Get("Sec-WebSocket-Key")
		hash := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		accept := base64.StdEncoding.EncodeToString(hash[:])
		_, err = writeStringWithWatchdog(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+accept+"\r\n\r\n")
		serverReady <- err
		if err != nil {
			return
		}
		payload, err := readWebSocketTextFrame(conn)
		if err != nil {
			serverCallReadDone <- err
			serverCallWriteDone <- err
			return
		}
		var call struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err = json.Unmarshal(payload, &call); err == nil && (call.ID == 0 || call.Method != "Browser.getVersion") {
			err = errors.New("unexpected CDP request over returned websocket")
		}
		serverCallReadDone <- err
		if err != nil {
			serverCallWriteDone <- err
			return
		}
		var response []byte
		response, err = json.Marshal(struct {
			ID     int64             `json:"id"`
			Result map[string]string `json:"result"`
		}{ID: call.ID, Result: map[string]string{"product": "transport-test"}})
		if err == nil {
			err = writeWebSocketTextFrame(conn, response)
		}
		serverCallWriteDone <- err
		if err != nil {
			return
		}
		peerCloseObserved <- readPeerCloseWithWatchdog(conn)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	connectResults, connectDone := startCancelableWebSocketConnect(ctx, "ws://"+listener.Addr().String()+"/devtools/browser/test")
	var ws *cdp.WebSocket
	var wsCloseDone <-chan error
	var callDone <-chan struct{}
	var cancelCall context.CancelFunc
	t.Cleanup(func() {
		cancel()
		if cancelCall != nil {
			cancelCall()
		}
		_ = listener.Close()
		closeAcceptedConnection(t, accepted)
		if ws != nil {
			if wsCloseDone == nil {
				wsCloseDone = startWebSocketClose(ws)
			}
			if closeErr, closed := waitWebSocketClose(wsCloseDone); !closed || (closeErr != nil && !errors.Is(closeErr, net.ErrClosed)) {
				t.Errorf("cleanup close returned websocket: %v", closeErr)
			}
			ws = nil
		}
		joinConnect(t, connectDone, "successful production websocket connect")
		if callDone != nil {
			select {
			case <-callDone:
			case <-time.After(time.Second):
				t.Errorf("CDP message call did not join within watchdog")
			}
		}
		joinTestServer(t, serverDone, "successful websocket")
	})
	select {
	case err := <-serverReady:
		if err != nil {
			t.Fatalf("test server handshake response failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("test server handshake response 未在 watchdog 内完成")
	}
	select {
	case result := <-connectResults:
		ws, err = result.websocket, result.err
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("production websocket Connect 未在独立结果 watchdog 内返回")
	}
	if err != nil || ws == nil {
		t.Fatalf("production websocket Connect failed: websocket=%v err=%v", ws, err)
	}
	joinConnect(t, connectDone, "successful production websocket connect")
	cancel()
	client := cdp.New().Start(ws)
	callCtx, stopCall := context.WithTimeout(context.Background(), 3*time.Second)
	cancelCall = stopCall
	defer stopCall()
	type callResult struct {
		payload []byte
		err     error
	}
	resultCh := make(chan callResult, 1)
	callFinished := make(chan struct{})
	callDone = callFinished
	go func() {
		payload, err := client.Call(callCtx, "", "Browser.getVersion", struct{}{})
		resultCh <- callResult{payload: payload, err: err}
		close(callFinished)
	}()
	select {
	case err := <-serverCallReadDone:
		if err != nil {
			t.Fatalf("test server could not read the successful CDP request: %v", err)
		}
	case <-callCtx.Done():
		t.Fatalf("successful CDP request send exceeded its context: %v", callCtx.Err())
	case <-time.After(time.Second):
		t.Fatal("test server CDP request read exceeded independent watchdog")
	}
	select {
	case err := <-serverCallWriteDone:
		if err != nil {
			t.Fatalf("test server could not write the successful CDP response: %v", err)
		}
	case <-callCtx.Done():
		t.Fatalf("successful CDP response send exceeded its context: %v", callCtx.Err())
	case <-time.After(time.Second):
		t.Fatal("test server CDP response write exceeded independent watchdog")
	}
	select {
	case result := <-resultCh:
		if result.err != nil || !strings.Contains(string(result.payload), "transport-test") {
			t.Fatalf("startup context cancellation must leave returned websocket usable for CDP send/read: payload=%s err=%v", result.payload, result.err)
		}
	case <-callCtx.Done():
		t.Fatalf("CDP response read exceeded its context: %v", callCtx.Err())
	case <-time.After(time.Second):
		t.Fatal("CDP response read exceeded independent watchdog")
	}
	stopCall()
	select {
	case <-callFinished:
	case <-time.After(time.Second):
		t.Fatal("stopped CDP message call did not join within its watchdog")
	}
	wsCloseDone = startWebSocketClose(ws)
	closeErr, closed := waitWebSocketClose(wsCloseDone)
	if !closed {
		t.Fatalf("explicit websocket close exceeded watchdog: %v", closeErr)
	}
	wsCloseDone = nil
	ws = nil
	if closeErr != nil {
		t.Fatalf("close returned production websocket: %v", closeErr)
	}
	requirePeerCloseObserved(t, peerCloseObserved, "successful websocket peer-close observation")
	joinTestServer(t, serverDone, "successful websocket close")
}

func TestCancelableWebSocketHandshakeCancellationClosesNon101BodyRead(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	responseSent := make(chan struct{})
	serverDone := make(chan struct{})
	peerCloseObserved := make(chan error, 1)
	accepted := make(chan net.Conn, 1)
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- conn
		defer conn.Close()
		if _, err := readRequestWithWatchdog(conn); err != nil {
			return
		}
		if _, err := writeStringWithWatchdog(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 100000\r\n\r\npartial"); err != nil {
			return
		}
		close(responseSent)
		peerCloseObserved <- readPeerCloseWithWatchdog(conn)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	connectResults, connectDone := startCancelableWebSocketConnect(ctx, "ws://"+listener.Addr().String()+"/devtools/browser/test")
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		closeAcceptedConnection(t, accepted)
		joinConnect(t, connectDone, "non-101 cancellation")
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Errorf("non-101 test server goroutine 未在 watchdog 内退出")
		}
	})
	select {
	case <-responseSent:
	case <-time.After(time.Second):
		t.Fatal("non-101 响应未到达测试服务端")
	}
	cancel()
	select {
	case result := <-connectResults:
		if result.err == nil || result.websocket != nil {
			t.Fatal("non-101 websocket 握手不应成功")
		}
	case <-time.After(time.Second):
		t.Fatal("取消未能中断 non-101 响应体读取")
	}
	joinConnect(t, connectDone, "non-101 cancellation")
	requirePeerCloseObserved(t, peerCloseObserved, "non-101 cancellation peer-close observation")
	joinTestServer(t, serverDone, "non-101 cancellation")
}

func TestFailStartupClosesBrowserOwnedTransport(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverReady := make(chan error, 1)
	serverDone := make(chan struct{})
	peerCloseObserved := make(chan error, 1)
	accepted := make(chan net.Conn, 1)
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			serverReady <- err
			return
		}
		accepted <- conn
		defer conn.Close()
		request, err := readRequestWithWatchdog(conn)
		if err != nil {
			serverReady <- err
			return
		}
		key := request.Header.Get("Sec-WebSocket-Key")
		hash := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		accept := base64.StdEncoding.EncodeToString(hash[:])
		_, err = writeStringWithWatchdog(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+accept+"\r\n\r\n")
		serverReady <- err
		if err == nil {
			peerCloseObserved <- readPeerCloseWithWatchdog(conn)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	connectResults, connectDone := startCancelableWebSocketConnect(ctx, "ws://"+listener.Addr().String()+"/devtools/browser/test")
	var ws *cdp.WebSocket
	var wsCloseDone <-chan error
	var failStartupDone <-chan struct{}
	var failStartupStarted bool
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		closeAcceptedConnection(t, accepted)
		if ws != nil && !failStartupStarted {
			if wsCloseDone == nil {
				wsCloseDone = startWebSocketClose(ws)
			}
			if closeErr, closed := waitWebSocketClose(wsCloseDone); !closed || (closeErr != nil && !errors.Is(closeErr, net.ErrClosed)) {
				t.Errorf("cleanup close failStartup websocket: %v", closeErr)
			}
		}
		joinConnect(t, connectDone, "failStartup")
		if failStartupDone != nil {
			select {
			case <-failStartupDone:
			case <-time.After(time.Second):
				t.Errorf("failStartup goroutine 未在 watchdog 内退出")
			}
		}
		joinTestServer(t, serverDone, "failStartup")
	})
	select {
	case result := <-connectResults:
		ws, err = result.websocket, result.err
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("failStartup websocket handshake 未在独立结果 watchdog 内返回")
	}
	joinConnect(t, connectDone, "failStartup")
	if err != nil {
		t.Fatalf("websocket handshake failed: %v", err)
	}
	select {
	case err := <-serverReady:
		if err != nil {
			t.Fatalf("test server handshake failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("test server handshake response 未在 watchdog 内完成")
	}
	startupErr := errors.New("injected startup failure")
	type failStartupResult struct {
		browser *Browser
		err     error
	}
	resultCh := make(chan failStartupResult, 1)
	failStartupDoneCh := make(chan struct{})
	failStartupDone = failStartupDoneCh
	failStartupStarted = true
	go func() {
		failed, err := (&Browser{transport: ws}).failStartup(startupErr)
		resultCh <- failStartupResult{browser: failed, err: err}
		close(failStartupDoneCh)
	}()
	var failed *Browser
	select {
	case result := <-resultCh:
		failed, err = result.browser, result.err
	case <-time.After(time.Second):
		t.Fatal("failStartup websocket close exceeded independent watchdog")
	}
	select {
	case <-failStartupDoneCh:
	case <-time.After(time.Second):
		t.Fatal("failStartup goroutine join exceeded independent watchdog")
	}
	ws = nil
	if !errors.Is(err, startupErr) || failed == nil || !failed.transportClosed {
		t.Fatalf("startup failure 应保留原错误并关闭 Browser-owned transport: browser=%v err=%v", failed, err)
	}
	requirePeerCloseObserved(t, peerCloseObserved, "failStartup peer-close observation")
	joinTestServer(t, serverDone, "failStartup transport close")
}

func TestCancelableWebSocketRejectsUnknownScheme(t *testing.T) {
	if _, err := connectCancelableWebSocket(context.Background(), "http://127.0.0.1:9222", nil); err == nil {
		t.Fatal("non-websocket URL should be rejected")
	}
}
