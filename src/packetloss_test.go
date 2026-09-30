package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
)

type fakePacketLossSession struct {
	messages  chan packetLossMessage
	errors    chan error
	attempted []string
	sent      []string
	failOn    string
	sendError error
	onSend    func(string)
	closeErr  error
	closeCall atomic.Int32
}

func newFakePacketLossSession(capacity int) *fakePacketLossSession {
	return &fakePacketLossSession{
		messages: make(chan packetLossMessage, capacity),
		errors:   make(chan error, 1),
	}
}

func (session *fakePacketLossSession) Messages() <-chan packetLossMessage { return session.messages }
func (session *fakePacketLossSession) Errors() <-chan error               { return session.errors }

func (session *fakePacketLossSession) deliver(text string) {
	session.messages <- packetLossMessage{Text: text, ReceivedAt: time.Now()}
}

func (session *fakePacketLossSession) SendText(text string) error {
	session.attempted = append(session.attempted, text)
	if text == session.failOn {
		if session.sendError != nil {
			return session.sendError
		}
		return errors.New("private transport send failure")
	}
	session.sent = append(session.sent, text)
	if session.onSend != nil {
		session.onSend(text)
	}
	return nil
}

func (session *fakePacketLossSession) Close() error {
	session.closeCall.Add(1)
	return session.closeErr
}

func testPacketLossConfig(count, batchSize int) packetLossConfig {
	cfg := defaultPacketLossConfig("http://example.test", time.Second)
	cfg.Count = count
	cfg.BatchSize = batchSize
	cfg.BatchDelay = 3 * time.Millisecond
	cfg.ResponseWait = 15 * time.Millisecond
	cfg.ConnectTimeout = 50 * time.Millisecond
	return cfg
}

func TestDefaultPacketLossConfig(t *testing.T) {
	cfg := defaultPacketLossConfig("https://speed.test/", 7*time.Second)
	if cfg.CredentialsURL != "https://speed.test/turn-creds" || cfg.TURNServer != defaultTURNServer {
		t.Fatalf("unexpected credential defaults: %+v", cfg)
	}
	if cfg.Count != 1000 || cfg.BatchSize != 10 || cfg.BatchDelay != 10*time.Millisecond || cfg.ResponseWait != 3*time.Second || cfg.ConnectTimeout != 5*time.Second || cfg.Timeout != 7*time.Second {
		t.Fatalf("unexpected packet loss defaults: %+v", cfg)
	}
	if got := defaultPacketLossConfig("", 0); got.CredentialsURL != cloudflareBaseURL+"/turn-creds" || got.Timeout != defaultMeasurementTimeout {
		t.Fatalf("empty inputs did not use production defaults: %+v", got)
	}
}

func TestPacketLossSendsCanonicalBatchesAndReportsOwnerProgress(t *testing.T) {
	cfg := testPacketLossConfig(23, 10)
	cfg.BatchDelay = 7 * time.Millisecond
	session := newFakePacketLossSession(32)
	progress := make([]int, 0, cfg.Count)
	batchBoundary := make(map[string]time.Time)
	session.onSend = func(message string) {
		if message == "10" || message == "11" || message == "20" || message == "21" {
			batchBoundary[message] = time.Now()
		}
		session.deliver(message)
	}

	result := runPacketLossSession(context.Background(), cfg, session, func(sent int) {
		progress = append(progress, sent)
	})
	if !result.Available || result.Sent != 23 || result.Received != 23 || result.Ratio != 0 || result.Err != nil {
		t.Fatalf("unexpected complete result: %+v", result)
	}
	if len(session.sent) != cfg.Count || len(progress) != cfg.Count {
		t.Fatalf("sent/progress counts = %d/%d, expected %d", len(session.sent), len(progress), cfg.Count)
	}
	for i, message := range session.sent {
		if message != strconv.Itoa(i+1) || progress[i] != i+1 {
			t.Fatalf("message/progress[%d] = %q/%d", i, message, progress[i])
		}
	}
	for _, pair := range [][2]string{{"10", "11"}, {"20", "21"}} {
		if elapsed := batchBoundary[pair[1]].Sub(batchBoundary[pair[0]]); elapsed < cfg.BatchDelay {
			t.Errorf("batch %s->%s delay = %v, expected at least %v", pair[0], pair[1], elapsed, cfg.BatchDelay)
		}
	}
}

func TestPacketLossCountsOnlyUniqueCanonicalSentIDs(t *testing.T) {
	cfg := testPacketLossConfig(5, 5)
	session := newFakePacketLossSession(16)
	session.onSend = func(message string) {
		switch message {
		case "3":
			for _, reply := range []string{"3", "3", "01", "0", "6", "-1", "not-an-id"} {
				session.deliver(reply)
			}
		case "5":
			session.deliver("5")
		}
	}

	result := runPacketLossSession(context.Background(), cfg, session, nil)
	if !result.Available || result.Sent != 5 || result.Received != 2 || result.Ratio != 0.6 {
		t.Fatalf("unexpected deduplicated result: %+v", result)
	}
	if !reflect.DeepEqual(result.Lost, []int{1, 2, 4}) {
		t.Fatalf("lost IDs = %v, expected [1 2 4]", result.Lost)
	}
}

func TestPacketLossRejectsPreRegistrationAndFutureBatchResponses(t *testing.T) {
	cfg := testPacketLossConfig(2, 1)
	cfg.ResponseWait = 5 * time.Millisecond
	session := newFakePacketLossSession(4)
	session.deliver("1") // This arrived before the owner registered ID 1.
	session.onSend = func(message string) {
		if message == "1" {
			session.deliver("2") // ID 2 arrives between batches, before its registration.
		}
	}

	result := runPacketLossSession(context.Background(), cfg, session, nil)
	if !result.Available || result.Sent != 2 || result.Received != 0 || result.Ratio != 1 || !reflect.DeepEqual(result.Lost, []int{1, 2}) {
		t.Fatalf("pre-registration/future responses were incorrectly counted: %+v", result)
	}
}

func TestPacketLossOutOfOrderArrivalsAndMeasured100PercentLoss(t *testing.T) {
	t.Run("out of order", func(t *testing.T) {
		cfg := testPacketLossConfig(4, 4)
		session := newFakePacketLossSession(8)
		session.onSend = func(message string) {
			if message == "4" {
				for _, reply := range []string{"4", "2", "1", "3"} {
					session.deliver(reply)
				}
			}
		}
		result := runPacketLossSession(context.Background(), cfg, session, nil)
		if !result.Available || result.Received != 4 || result.Ratio != 0 || len(result.Lost) != 0 {
			t.Fatalf("unexpected out-of-order result: %+v", result)
		}
	})

	t.Run("all lost is a measured result", func(t *testing.T) {
		cfg := testPacketLossConfig(4, 4)
		cfg.ResponseWait = 5 * time.Millisecond
		result := runPacketLossSession(context.Background(), cfg, newFakePacketLossSession(1), nil)
		if !result.Available || result.Sent != 4 || result.Received != 0 || result.Ratio != 1 || !reflect.DeepEqual(result.Lost, []int{1, 2, 3, 4}) || result.Err != nil {
			t.Fatalf("100%% loss must remain an available measurement: %+v", result)
		}
	})
}

func TestPacketLossDebouncesOnlyNewValidFirstArrivals(t *testing.T) {
	cfg := testPacketLossConfig(3, 3)
	cfg.ResponseWait = 80 * time.Millisecond
	session := newFakePacketLossSession(8)
	started := time.Now()
	go func() {
		time.Sleep(20 * time.Millisecond)
		session.deliver("1")
		time.Sleep(50 * time.Millisecond)
		session.deliver("2")
		time.Sleep(70 * time.Millisecond)
		session.deliver("2") // A duplicate must not extend the window.
	}()

	result := runPacketLossSession(context.Background(), cfg, session, nil)
	elapsed := time.Since(started)
	if !result.Available || result.Received != 2 || !reflect.DeepEqual(result.Lost, []int{3}) {
		t.Fatalf("unexpected debounced result: %+v", result)
	}
	if elapsed < 135*time.Millisecond || elapsed > 190*time.Millisecond {
		t.Fatalf("response window elapsed %v; expected extension for ID 2 but not its duplicate", elapsed)
	}
}

func TestPacketLossSendFailureIsUnavailableAndSanitized(t *testing.T) {
	cfg := testPacketLossConfig(5, 2)
	session := newFakePacketLossSession(4)
	session.failOn = "3"
	session.sendError = errors.New("SEND_SECRET_TOKEN must not escape")
	var progress []int
	result := runPacketLossSession(context.Background(), cfg, session, func(sent int) { progress = append(progress, sent) })
	if result.Available || result.Sent != 2 || result.Received != 0 || !reflect.DeepEqual(result.Lost, []int{1, 2}) || !errors.Is(result.Err, errPacketLossSend) {
		t.Fatalf("send failure should be unavailable with the successful partial snapshot: %+v", result)
	}
	if strings.Contains(result.Err.Error(), "SEND_SECRET_TOKEN") || !reflect.DeepEqual(progress, []int{1, 2}) {
		t.Fatalf("send error leaked or progress was wrong: err=%v progress=%v", result.Err, progress)
	}
}

func TestPacketLossTransportErrorAndZeroSentUnavailable(t *testing.T) {
	t.Run("transport failure", func(t *testing.T) {
		cfg := testPacketLossConfig(1, 1)
		session := newFakePacketLossSession(2)
		session.onSend = func(string) { session.errors <- errors.New("TRANSPORT_SECRET") }
		result := runPacketLossSession(context.Background(), cfg, session, nil)
		if result.Available || result.Sent != 1 || !errors.Is(result.Err, errPacketLossTransport) || strings.Contains(result.Err.Error(), "TRANSPORT_SECRET") {
			t.Fatalf("transport failure was not safely unavailable: %+v", result)
		}
	})

	t.Run("zero successful sends", func(t *testing.T) {
		cfg := testPacketLossConfig(1, 1)
		session := newFakePacketLossSession(1)
		session.failOn = "1"
		result := runPacketLossSession(context.Background(), cfg, session, nil)
		if result.Available || result.Sent != 0 || result.Ratio != 0 || len(result.Lost) != 0 || !errors.Is(result.Err, errPacketLossSend) {
			t.Fatalf("zero successful sends must be unavailable: %+v", result)
		}
	})
}

func TestPacketLossCredentialDefaultsAndValidation(t *testing.T) {
	fallback, err := decodePacketLossCredentials([]byte(`{"username":"user","credential":"pass"}`), "fallback.example:3478")
	if err != nil || fallback.Server != "fallback.example:3478" || fallback.Username != "user" || fallback.Credential != "pass" {
		t.Fatalf("fallback credentials = %+v, err=%v", fallback, err)
	}
	withOverride, err := decodePacketLossCredentials([]byte(`{"username":"user","credential":"pass","server":"turn.example:50000","extra":true}`), "fallback.example:3478")
	if err != nil || withOverride.Server != "turn.example:50000" {
		t.Fatalf("server override credentials = %+v, err=%v", withOverride, err)
	}

	for _, authority := range []string{"turn:example:3478", "example/path:3478", "example.test:0", "example.test:70000", "user@example.test:3478", "bad host:3478"} {
		if validTURNAuthority(authority) {
			t.Errorf("accepted unsafe TURN authority %q", authority)
		}
	}
	for _, authority := range []string{"turn.example:50000", "127.0.0.1:3478", "[::1]:3478"} {
		if !validTURNAuthority(authority) {
			t.Errorf("rejected valid TURN authority %q", authority)
		}
	}
}

func TestPacketLossCredentialFetchHeadersAndFlatSchema(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/turn-creds" {
			t.Errorf("unexpected credential request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("User-Agent") != userAgent || r.Header.Get("Accept") != "application/json, text/plain;q=0.9" || r.Header.Get("Origin") != "http://"+r.Host {
			t.Errorf("unexpected credential headers: %#v", r.Header)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("credential request unexpectedly included auth/cookie headers")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"username":"temporary-user","credential":"temporary-pass","server":"relay.test:50000","extra":true}`))
	}))
	defer server.Close()

	cfg := testPacketLossConfig(1, 1)
	cfg.CredentialsURL = server.URL + "/turn-creds"
	cfg.TURNServer = "fallback.test:3478"
	got, err := fetchPacketLossCredentials(context.Background(), server.Client(), cfg)
	if err != nil || got.Username != "temporary-user" || got.Credential != "temporary-pass" || got.Server != "relay.test:50000" || requests.Load() != 1 {
		t.Fatalf("credential fetch = %+v, err=%v, requests=%d", got, err, requests.Load())
	}

	for name, body := range map[string]string{
		"missing username":      `{"credential":"SECRET_CREDENTIAL"}`,
		"missing credential":    `{"username":"SECRET_USERNAME"}`,
		"empty username":        `{"username":"","credential":"p"}`,
		"empty credential":      `{"username":"u","credential":""}`,
		"non-string username":   `{"username":3,"credential":"p"}`,
		"non-string credential": `{"username":"u","credential":[]}`,
		"nested credential":     `{"username":"u","credential":{"secret":"TOKEN"}}`,
		"API error field":       `{"username":"SECRET_USERNAME","credential":"SECRET_CREDENTIAL","error":"TOKENIZED API ERROR"}`,
		"invalid server":        `{"username":"u","credential":"p","server":"turn:relay.test:3478"}`,
		"malformed JSON":        `{"username":"SECRET_USERNAME"`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodePacketLossCredentials([]byte(body), "fallback.test:3478")
			if err == nil || got.Username != "" || got.Credential != "" {
				t.Fatalf("invalid credential body accepted: got=%+v err=%v", got, err)
			}
			for _, secret := range []string{"SECRET_USERNAME", "SECRET_CREDENTIAL", "TOKENIZED API ERROR", "TOKEN"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("credential error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestPacketLossCurrentCredentialURLsAndUnknownFields(t *testing.T) {
	for _, tc := range []struct {
		name, body, server string
		valid              bool
	}{
		{name: "unknown nested fields", body: `{"username":"u","credential":"p","extra":{"nested":[1,2]}}`, server: "fallback.test:3478", valid: true},
		{name: "public endpoint shape", body: `{"username":"u","credential":"p","urls":["stun:relay.test:3478","turn:relay.test:3478?transport=tcp","turn:relay.test:3478?transport=udp","turns:relay.test:5349?transport=tcp"]}`, server: "relay.test:3478", valid: true},
		{name: "single URL", body: `{"username":"u","credential":"p","urls":"turn:relay.test:50000?transport=udp"}`, server: "relay.test:50000", valid: true},
		{name: "explicit server wins", body: `{"username":"u","credential":"p","server":"explicit.test:3478","urls":["turn:other.test:50000?transport=udp"]}`, server: "explicit.test:3478", valid: true},
		{name: "only TCP", body: `{"username":"u","credential":"p","urls":["turn:relay.test:3478?transport=tcp"]}`},
		{name: "only TLS", body: `{"username":"u","credential":"p","urls":["turns:relay.test:5349?transport=tcp"]}`},
		{name: "only STUN", body: `{"username":"u","credential":"p","urls":["stun:relay.test:3478"]}`},
		{name: "URI user information", body: `{"username":"u","credential":"p","urls":["turn:private-token@relay.test:3478?transport=udp"]}`},
		{name: "nested Realtime wrapper", body: `{"iceServers":[{"username":"u","credential":"p","urls":["turn:relay.test:3478?transport=udp"]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodePacketLossCredentials([]byte(tc.body), "fallback.test:3478")
			if tc.valid {
				if err != nil || got.Server != tc.server {
					t.Fatal("valid credentials schema or UDP server selection rejected")
				}
			} else if err == nil || got != (packetLossCredentials{}) {
				t.Fatal("credentials without a safe UDP relay were accepted")
			}
		})
	}
}

func TestPacketLossCredentialHTTPFailuresDoNotLeakResponseBody(t *testing.T) {
	secretBody := `{"username":"API_SECRET_USER","credential":"API_SECRET_PASS","error":"BODY_SECRET_TOKEN"}`
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "non-success status", status: http.StatusServiceUnavailable, body: secretBody, want: errPacketLossCredentialsStatus},
		{name: "oversized response", status: http.StatusOK, body: strings.Repeat("x", maxPacketLossCredentialBytes+1), want: errPacketLossCredentialsSize},
		{name: "invalid response", status: http.StatusOK, body: secretBody, want: errPacketLossCredentialsJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			cfg := testPacketLossConfig(1, 1)
			cfg.CredentialsURL = server.URL + "/turn-creds"
			_, err := fetchPacketLossCredentials(context.Background(), server.Client(), cfg)
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v, expected %v", err, test.want)
			}
			for _, secret := range []string{"API_SECRET_USER", "API_SECRET_PASS", "BODY_SECRET_TOKEN"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaked response data %q: %v", secret, err)
				}
			}
		})
	}
}

func TestPacketLossCredentialRequestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	cfg := testPacketLossConfig(1, 1)
	cfg.CredentialsURL = server.URL + "/turn-creds"
	_, err := fetchPacketLossCredentials(ctx, server.Client(), cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("credential request error = %v, expected context deadline", err)
	}
}

func TestPacketLossCleanupErrorsAreCheckedAndSanitized(t *testing.T) {
	server := packetLossCredentialServer(t, "")
	defer server.Close()
	cfg := testPacketLossConfig(1, 1)
	cfg.CredentialsURL = server.URL + "/turn-creds"
	session := newFakePacketLossSession(1)
	session.closeErr = errors.New("CLEANUP_SECRET")
	session.onSend = func(message string) { session.deliver(message) }

	result := measurePacketLossWithSessionFactory(context.Background(), server.Client(), cfg, nil,
		func(context.Context, context.Context, packetLossConfig, packetLossCredentials) (packetLossSession, error) {
			return session, nil
		})
	if result.Available || !errors.Is(result.Err, errPacketLossCleanup) || strings.Contains(result.Err.Error(), "CLEANUP_SECRET") || session.closeCall.Load() != 1 {
		t.Fatalf("cleanup error was not safely propagated: result=%+v close-calls=%d", result, session.closeCall.Load())
	}
}

func TestPacketLossSetupFailuresAreUnavailableAndSanitized(t *testing.T) {
	server := packetLossCredentialServer(t, "")
	defer server.Close()
	cfg := testPacketLossConfig(2, 2)
	cfg.CredentialsURL = server.URL + "/turn-creds"

	t.Run("setup failure", func(t *testing.T) {
		session := newFakePacketLossSession(2)
		result := measurePacketLossWithSessionFactory(context.Background(), server.Client(), cfg, nil,
			func(context.Context, context.Context, packetLossConfig, packetLossCredentials) (packetLossSession, error) {
				return session, errors.New("TURN_PASSWORD_SECRET setup detail")
			})
		if result.Available || !errors.Is(result.Err, errPacketLossPeerSetup) || strings.Contains(result.Err.Error(), "TURN_PASSWORD_SECRET") || session.closeCall.Load() != 1 {
			t.Fatalf("setup error/cleanup result = %+v, closes=%d", result, session.closeCall.Load())
		}
	})

	t.Run("setup timeout", func(t *testing.T) {
		timeoutCfg := cfg
		timeoutCfg.ConnectTimeout = 20 * time.Millisecond
		timeoutCfg.Timeout = time.Second
		result := measurePacketLossWithSessionFactory(context.Background(), server.Client(), timeoutCfg, nil,
			func(_ context.Context, setupCtx context.Context, _ packetLossConfig, _ packetLossCredentials) (packetLossSession, error) {
				<-setupCtx.Done()
				return nil, setupCtx.Err()
			})
		if result.Available || !errors.Is(result.Err, context.DeadlineExceeded) {
			t.Fatalf("setup timeout result = %+v", result)
		}
	})

	t.Run("outer cancellation while setting up", func(t *testing.T) {
		outerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		outerCfg := cfg
		outerCfg.ConnectTimeout = time.Second
		time.AfterFunc(15*time.Millisecond, cancel)
		result := measurePacketLossWithSessionFactory(outerCtx, server.Client(), outerCfg, nil,
			func(sessionCtx, _ context.Context, _ packetLossConfig, _ packetLossCredentials) (packetLossSession, error) {
				<-sessionCtx.Done()
				return nil, sessionCtx.Err()
			})
		if result.Available || !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("outer setup cancellation result = %+v", result)
		}
	})
}

func TestPacketLossOuterCancellationAfterSetupClosesSession(t *testing.T) {
	server := packetLossCredentialServer(t, "")
	defer server.Close()
	cfg := testPacketLossConfig(3, 3)
	cfg.CredentialsURL = server.URL + "/turn-creds"
	cfg.ResponseWait = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	session := newFakePacketLossSession(3)
	time.AfterFunc(20*time.Millisecond, cancel)
	result := measurePacketLossWithSessionFactory(ctx, server.Client(), cfg, nil,
		func(context.Context, context.Context, packetLossConfig, packetLossCredentials) (packetLossSession, error) {
			return session, nil
		})
	cancel()
	if result.Available || !errors.Is(result.Err, context.Canceled) || session.closeCall.Load() != 1 {
		t.Fatalf("canceled measurement result=%+v, close calls=%d", result, session.closeCall.Load())
	}
}

func TestPacketLossSessionFactoryNotCalledForCredentialFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("CREDENTIAL_SECRET"))
	}))
	defer server.Close()
	cfg := testPacketLossConfig(1, 1)
	cfg.CredentialsURL = server.URL + "/turn-creds"
	called := false
	result := measurePacketLossWithSessionFactory(context.Background(), server.Client(), cfg, nil,
		func(context.Context, context.Context, packetLossConfig, packetLossCredentials) (packetLossSession, error) {
			called = true
			return nil, nil
		})
	if result.Available || called || !errors.Is(result.Err, errPacketLossCredentialsStatus) || strings.Contains(result.Err.Error(), "CREDENTIAL_SECRET") {
		t.Fatalf("credential failure result=%+v factory-called=%t", result, called)
	}
}

func packetLossCredentialServer(t *testing.T, authority string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]string{"username": "fixture-user", "credential": "fixture-password"}
		if authority != "" {
			response["server"] = authority
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode local credentials: %v", err)
		}
	}))
}

func TestPacketLossLocalTURNIntegration(t *testing.T) {
	const (
		realm    = "packetloss.local"
		username = "integration-user"
		password = "integration-password"
	)
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start localhost TURN listener: %v", err)
	}
	authKeys := map[string][]byte{username: turn.GenerateAuthKey(username, realm, password)}
	turnServer, err := turn.NewServer(turn.ServerConfig{
		Realm: realm,
		AuthHandler: func(attributes *turn.RequestAttributes) (string, []byte, bool) {
			if key, ok := authKeys[attributes.Username]; ok {
				return attributes.Username, key, true
			}
			return "", nil, false
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: listener,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP("127.0.0.1"),
				Address:      "127.0.0.1",
			},
		}},
	})
	if err != nil {
		_ = listener.Close()
		t.Fatalf("start localhost TURN server: %v", err)
	}
	defer func() {
		if closeErr := turnServer.Close(); closeErr != nil {
			t.Errorf("close localhost TURN server: %v", closeErr)
		}
	}()

	credentialsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("credential request missing shared User-Agent")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"username":   username,
			"credential": password,
			"urls": []string{
				"turn:" + listener.LocalAddr().String() + "?transport=tcp",
				"turn:" + listener.LocalAddr().String() + "?transport=udp",
			},
		})
	}))
	defer credentialsServer.Close()

	cfg := defaultPacketLossConfig(credentialsServer.URL, 20*time.Second)
	cfg.Count = 6
	cfg.BatchSize = 3
	cfg.BatchDelay = time.Millisecond
	cfg.ResponseWait = 100 * time.Millisecond
	cfg.ConnectTimeout = 10 * time.Second
	client := &http.Client{Timeout: cfg.Timeout}
	var actual *pionPacketLossSession
	result := measurePacketLossWithSessionFactory(context.Background(), client, cfg, nil,
		func(sessionCtx, setupCtx context.Context, cfg packetLossConfig, credentials packetLossCredentials) (packetLossSession, error) {
			opened, openErr := newPionPacketLossSession(sessionCtx, setupCtx, cfg, credentials)
			if openErr == nil {
				actual, _ = opened.(*pionPacketLossSession)
			}
			return opened, openErr
		})
	if !result.Available || result.Sent != cfg.Count || result.Received != cfg.Count || result.Ratio != 0 || len(result.Lost) != 0 {
		if actual != nil {
			t.Logf("cleanup outcome: %v; peer states: %s/%s", actual.closeErr, actual.sender.ConnectionState(), actual.receiver.ConnectionState())
		}
		t.Fatalf("local TURN WebRTC measurement result: %+v", result)
	}
	if actual == nil || !actual.relayUDPVerified {
		t.Fatal("local measurement did not verify selected UDP relay pairs on both peers")
	}
	if actual.senderChannel.Ordered() {
		t.Fatal("sender data channel unexpectedly requires ordered delivery")
	}
	if retransmits := actual.senderChannel.MaxRetransmits(); retransmits == nil || *retransmits != 0 {
		t.Fatalf("sender data channel MaxRetransmits = %v, expected pointer to zero", retransmits)
	}
	if actual.sender.ConnectionState() != webrtc.PeerConnectionStateClosed || actual.receiver.ConnectionState() != webrtc.PeerConnectionStateClosed {
		t.Fatalf("measurement did not close peer connections: sender=%s receiver=%s", actual.sender.ConnectionState(), actual.receiver.ConnectionState())
	}
	deadline := time.Now().Add(2 * time.Second)
	for turnServer.AllocationCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if allocations := turnServer.AllocationCount(); allocations != 0 {
		t.Fatalf("TURN allocations remain after session shutdown: %d", allocations)
	}
}

func TestPacketLossCandidateFilteringRequiresOnlyUDPRelays(t *testing.T) {
	valid := "v=0\r\na=candidate:1 1 udp 100 127.0.0.1 5000 typ relay raddr 127.0.0.1 rport 6000\r\n"
	if !packetLossSDPHasOnlyRelayUDP(valid) {
		t.Fatal("valid UDP relay candidate was rejected")
	}
	for _, sdp := range []string{
		"v=0\r\na=candidate:1 1 tcp 100 127.0.0.1 5000 typ relay tcptype passive\r\n",
		"v=0\r\na=candidate:1 1 udp 100 127.0.0.1 5000 typ host\r\n",
		"v=0\r\n",
	} {
		if packetLossSDPHasOnlyRelayUDP(sdp) {
			t.Errorf("accepted non-relay or empty candidate SDP %q", sdp)
		}
	}
}

func TestPacketLossConfigFailureDoesNotStartCredentialRequest(t *testing.T) {
	cfg := testPacketLossConfig(0, 1)
	called := atomic.Bool{}
	result := measurePacketLossWithSessionFactory(context.Background(), nil, cfg, nil,
		func(context.Context, context.Context, packetLossConfig, packetLossCredentials) (packetLossSession, error) {
			called.Store(true)
			return nil, nil
		})
	if result.Available || !errors.Is(result.Err, errPacketLossConfig) || called.Load() {
		t.Fatalf("invalid config result=%+v factory-called=%t", result, called.Load())
	}
}

func TestPacketLossContextErrorIsNotHiddenByOuterTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := runPacketLossSession(ctx, testPacketLossConfig(1, 1), newFakePacketLossSession(1), nil)
	if result.Available || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("pre-canceled measurement result = %+v", result)
	}
}

func TestPacketLossSetupErrorTextDoesNotIncludeURLQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	cfg := testPacketLossConfig(1, 1)
	cfg.CredentialsURL = server.URL + "/turn-creds?token=URL_QUERY_SECRET"
	_, err := fetchPacketLossCredentials(context.Background(), server.Client(), cfg)
	if err == nil || strings.Contains(err.Error(), "URL_QUERY_SECRET") {
		t.Fatalf("credential error exposed URL query token: %v", err)
	}
}

func TestPacketLossResultRatioIsAZeroToOneFraction(t *testing.T) {
	result := packetLossSnapshot(map[int]struct{}{1: {}, 2: {}, 3: {}}, map[int]struct{}{1: {}}, true, nil)
	if result.Ratio != 2.0/3.0 || result.Ratio < 0 || result.Ratio > 1 {
		t.Fatalf("unexpected packet loss ratio: %.8f", result.Ratio)
	}
}
