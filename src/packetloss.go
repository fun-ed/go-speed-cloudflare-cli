package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

const (
	defaultPacketLossCount          = 1000
	defaultPacketLossBatchSize      = 10
	defaultPacketLossBatchDelay     = 10 * time.Millisecond
	defaultPacketLossResponseWait   = 3 * time.Second
	defaultPacketLossConnectTimeout = 5 * time.Second
	maxPacketLossCredentialBytes    = 64 * 1024
	maxPacketLossMessageBuffer      = 1024
	maxPacketLossCount              = 1_000_000
	packetLossCleanupTimeout        = 2 * time.Second
	defaultTURNServer               = "turn.speed.cloudflare.com:50000"
)

type packetLossConfig struct {
	CredentialsURL string
	TURNServer     string
	Count          int
	BatchSize      int
	BatchDelay     time.Duration
	ResponseWait   time.Duration
	ConnectTimeout time.Duration
	Timeout        time.Duration
}

type packetLossResult struct {
	Sent      int
	Received  int
	Lost      []int
	Ratio     float64
	Available bool
	Err       error
}

type packetLossCredentials struct {
	Username   string
	Credential string
	Server     string
}

type packetLossMessage struct {
	Text       string
	ReceivedAt time.Time
}

type packetLossSession interface {
	Messages() <-chan packetLossMessage
	Errors() <-chan error
	SendText(string) error
	Close() error
}

type packetLossSessionFactory func(sessionCtx, setupCtx context.Context, cfg packetLossConfig, credentials packetLossCredentials) (packetLossSession, error)

var (
	errPacketLossConfig             = errors.New("packet loss configuration is invalid")
	errPacketLossCredentialsRequest = errors.New("packet loss credentials request failed")
	errPacketLossCredentialsStatus  = errors.New("packet loss credentials endpoint returned an unsuccessful status")
	errPacketLossCredentialsSize    = errors.New("packet loss credentials response is too large")
	errPacketLossCredentialsJSON    = errors.New("packet loss credentials response is invalid")
	errPacketLossPeerSetup          = errors.New("packet loss WebRTC setup failed")
	errPacketLossCleanup            = errors.New("packet loss WebRTC cleanup failed")
	errPacketLossTransport          = errors.New("packet loss WebRTC transport failed")
	errPacketLossSend               = errors.New("packet loss message send failed")
	errPacketLossNoMessages         = errors.New("packet loss measurement sent no messages")
)

func defaultPacketLossConfig(baseURL string, timeout time.Duration) packetLossConfig {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = cloudflareBaseURL
	}
	if timeout <= 0 {
		timeout = defaultMeasurementTimeout
	}

	return packetLossConfig{
		CredentialsURL: strings.TrimRight(baseURL, "/") + "/turn-creds",
		TURNServer:     defaultTURNServer,
		Count:          defaultPacketLossCount,
		BatchSize:      defaultPacketLossBatchSize,
		BatchDelay:     defaultPacketLossBatchDelay,
		ResponseWait:   defaultPacketLossResponseWait,
		ConnectTimeout: defaultPacketLossConnectTimeout,
		Timeout:        timeout,
	}
}

func normalizePacketLossConfig(cfg packetLossConfig) packetLossConfig {
	defaults := defaultPacketLossConfig(cloudflareBaseURL, defaultMeasurementTimeout)
	if cfg.CredentialsURL == "" {
		cfg.CredentialsURL = defaults.CredentialsURL
	}
	if cfg.TURNServer == "" {
		cfg.TURNServer = defaults.TURNServer
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaults.BatchSize
	}
	if cfg.BatchDelay <= 0 {
		cfg.BatchDelay = defaults.BatchDelay
	}
	if cfg.ResponseWait <= 0 {
		cfg.ResponseWait = defaults.ResponseWait
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = defaults.ConnectTimeout
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaults.Timeout
	}

	return cfg
}

func measurePacketLoss(ctx context.Context, client *http.Client, cfg packetLossConfig, onSent func(int)) packetLossResult {
	return measurePacketLossWithSessionFactory(ctx, client, cfg, onSent, newPionPacketLossSession)
}

func measurePacketLossWithSessionFactory(
	ctx context.Context,
	client *http.Client,
	cfg packetLossConfig,
	onSent func(int),
	openSession packetLossSessionFactory,
) (result packetLossResult) {
	cfg = normalizePacketLossConfig(cfg)
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.Count <= 0 || cfg.Count > maxPacketLossCount || openSession == nil {
		return packetLossResult{Err: errPacketLossConfig}
	}

	outerCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	credentials, err := fetchPacketLossCredentials(outerCtx, client, cfg)
	if err != nil {
		return packetLossResult{Err: err}
	}

	setupCtx, cancelSetup := context.WithTimeout(outerCtx, cfg.ConnectTimeout)
	session, setupErr := openSession(outerCtx, setupCtx, cfg, credentials)
	setupContextErr := setupCtx.Err()
	cancelSetup()
	if session != nil {
		defer func() {
			if closeErr := session.Close(); closeErr != nil {
				result.Available = false
				result.Err = errPacketLossCleanup
			}
		}()
	}
	if setupErr != nil {
		if setupContextErr != nil {
			return packetLossResult{Err: fmt.Errorf("%w: %w", errPacketLossPeerSetup, setupContextErr)}
		}
		return packetLossResult{Err: errPacketLossPeerSetup}
	}
	if session == nil {
		return packetLossResult{Err: errPacketLossPeerSetup}
	}

	return runPacketLossSession(outerCtx, cfg, session, onSent)
}

func fetchPacketLossCredentials(ctx context.Context, client *http.Client, cfg packetLossConfig) (credentials packetLossCredentials, err error) {
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	endpoint, err := url.Parse(cfg.CredentialsURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return packetLossCredentials{}, errPacketLossConfig
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return packetLossCredentials{}, errPacketLossConfig
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain;q=0.9")
	req.Header.Set("Origin", endpoint.Scheme+"://"+endpoint.Host)

	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return packetLossCredentials{}, fmt.Errorf("%w: %w", errPacketLossCredentialsRequest, ctxErr)
		}
		return packetLossCredentials{}, errPacketLossCredentialsRequest
	}
	defer func() {
		if resp.Body.Close() != nil {
			credentials = packetLossCredentials{}
			err = errors.Join(err, errPacketLossCredentialsRequest)
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return packetLossCredentials{}, errPacketLossCredentialsStatus
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPacketLossCredentialBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return packetLossCredentials{}, fmt.Errorf("%w: %w", errPacketLossCredentialsRequest, ctxErr)
		}
		return packetLossCredentials{}, errPacketLossCredentialsRequest
	}
	if len(body) > maxPacketLossCredentialBytes {
		return packetLossCredentials{}, errPacketLossCredentialsSize
	}

	credentials, err = decodePacketLossCredentials(body, cfg.TURNServer)
	if err != nil {
		return packetLossCredentials{}, err
	}

	return credentials, nil
}

func decodePacketLossCredentials(body []byte, fallbackServer string) (packetLossCredentials, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return packetLossCredentials{}, errPacketLossCredentialsJSON
	}

	username, present, ok := packetLossJSONField(fields, "username")
	if !ok || !present || username == "" {
		return packetLossCredentials{}, errPacketLossCredentialsJSON
	}
	credential, present, ok := packetLossJSONField(fields, "credential")
	if !ok || !present || credential == "" {
		return packetLossCredentials{}, errPacketLossCredentialsJSON
	}
	apiServer, present, ok := packetLossJSONField(fields, "server")
	if !ok {
		return packetLossCredentials{}, errPacketLossCredentialsJSON
	}
	apiError, presentError, ok := packetLossJSONField(fields, "error")
	if !ok || (presentError && apiError != "") {
		return packetLossCredentials{}, errPacketLossCredentialsJSON
	}

	server := fallbackServer
	if present && apiServer != "" {
		server = apiServer
	}
	if (!present || apiServer == "") && fields["urls"] != nil {
		var found bool
		server, found = packetLossUDPServer(fields["urls"])
		if !found {
			return packetLossCredentials{}, errPacketLossCredentialsJSON
		}
	}
	if !validTURNAuthority(server) {
		return packetLossCredentials{}, errPacketLossCredentialsJSON
	}

	return packetLossCredentials{Username: username, Credential: credential, Server: server}, nil
}

func packetLossUDPServer(raw json.RawMessage) (string, bool) {
	var urls []string
	if err := json.Unmarshal(raw, &urls); err != nil {
		var single string
		if err := json.Unmarshal(raw, &single); err != nil {
			return "", false
		}
		urls = []string{single}
	}
	for _, address := range urls {
		endpoint, err := url.Parse(address)
		if err != nil || !strings.EqualFold(endpoint.Scheme, "turn") || endpoint.Fragment != "" {
			continue
		}
		query, err := url.ParseQuery(endpoint.RawQuery)
		if err != nil {
			continue
		}
		transport := query.Get("transport")
		if transport != "" && !strings.EqualFold(transport, "udp") {
			continue
		}
		authority := endpoint.Opaque
		if validTURNAuthority(authority) {
			return authority, true
		}
	}
	return "", false
}

func packetLossJSONField(fields map[string]json.RawMessage, name string) (string, bool, bool) {
	raw, present := fields[name]
	if !present {
		return "", false, true
	}
	raw = bytes.TrimSpace(raw)
	if string(raw) == "null" {
		return "", true, true
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, false
	}
	return value, true, true
}

func validTURNAuthority(authority string) bool {
	host, portText, err := net.SplitHostPort(authority)
	if err != nil || host == "" || strings.TrimSpace(authority) != authority {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func runPacketLossSession(ctx context.Context, cfg packetLossConfig, session packetLossSession, onSent func(int)) packetLossResult {
	cfg = normalizePacketLossConfig(cfg)
	if ctx == nil {
		ctx = context.Background()
	}
	if session == nil || cfg.Count <= 0 || cfg.Count > maxPacketLossCount {
		return packetLossResult{Err: errPacketLossConfig}
	}

	sentIDs := make(map[int]struct{}, cfg.Count)
	sentAt := make(map[int]time.Time, cfg.Count)
	receivedIDs := make(map[int]struct{}, cfg.Count)
	messages := session.Messages()
	transportErrors := session.Errors()

	for start := 1; start <= cfg.Count; start += cfg.BatchSize {
		end := start + cfg.BatchSize - 1
		if end > cfg.Count {
			end = cfg.Count
		}
		for id := start; id <= end; id++ {
			if err := ctx.Err(); err != nil {
				return packetLossSnapshot(sentIDs, receivedIDs, false, err)
			}
			select {
			case <-transportErrors:
				return packetLossSnapshot(sentIDs, receivedIDs, false, errPacketLossTransport)
			default:
			}

			// Register before SendText so an immediate receive cannot race the sent-ID set.
			sentIDs[id] = struct{}{}
			sentAt[id] = time.Now()
			if err := session.SendText(strconv.Itoa(id)); err != nil {
				delete(sentIDs, id)
				delete(sentAt, id)
				return packetLossSnapshot(sentIDs, receivedIDs, false, errPacketLossSend)
			}
			if onSent != nil {
				onSent(len(sentIDs))
			}
		}
		if err := waitPacketLossBatch(ctx, cfg.BatchDelay, transportErrors); err != nil {
			return packetLossSnapshot(sentIDs, receivedIDs, false, err)
		}
	}

	if len(sentIDs) == 0 {
		return packetLossSnapshot(sentIDs, receivedIDs, false, errPacketLossNoMessages)
	}
	if err := ctx.Err(); err != nil {
		return packetLossSnapshot(sentIDs, receivedIDs, false, err)
	}
	if len(receivedIDs) == len(sentIDs) {
		return packetLossSnapshot(sentIDs, receivedIDs, true, nil)
	}

	debounce := time.NewTimer(cfg.ResponseWait)
	defer debounce.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return packetLossSnapshot(sentIDs, receivedIDs, false, err)
		}
		select {
		case message, ok := <-messages:
			if !ok {
				return packetLossSnapshot(sentIDs, receivedIDs, false, errPacketLossTransport)
			}
			id, valid := parsePacketLossMessage(message.Text)
			if !valid {
				continue
			}
			registeredAt, wasSent := sentAt[id]
			if !wasSent || message.ReceivedAt.Before(registeredAt) {
				continue
			}
			if _, duplicate := receivedIDs[id]; duplicate {
				continue
			}
			receivedIDs[id] = struct{}{}
			if len(receivedIDs) == len(sentIDs) {
				if err := ctx.Err(); err != nil {
					return packetLossSnapshot(sentIDs, receivedIDs, false, err)
				}
				return packetLossSnapshot(sentIDs, receivedIDs, true, nil)
			}
			resetPacketLossTimer(debounce, cfg.ResponseWait)
		case <-debounce.C:
			if err := ctx.Err(); err != nil {
				return packetLossSnapshot(sentIDs, receivedIDs, false, err)
			}
			return packetLossSnapshot(sentIDs, receivedIDs, true, nil)
		case <-transportErrors:
			return packetLossSnapshot(sentIDs, receivedIDs, false, errPacketLossTransport)
		case <-ctx.Done():
			return packetLossSnapshot(sentIDs, receivedIDs, false, ctx.Err())
		}
	}
}

func waitPacketLossBatch(ctx context.Context, delay time.Duration, transportErrors <-chan error) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-transportErrors:
		return errPacketLossTransport
	}
}

func resetPacketLossTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}

func parsePacketLossMessage(message string) (int, bool) {
	id, err := strconv.Atoi(message)
	if err != nil || id < 1 || strconv.Itoa(id) != message {
		return 0, false
	}
	return id, true
}

func packetLossSnapshot(sentIDs, receivedIDs map[int]struct{}, available bool, err error) packetLossResult {
	result := packetLossResult{
		Sent:      len(sentIDs),
		Received:  len(receivedIDs),
		Available: available,
		Err:       err,
		Lost:      make([]int, 0, len(sentIDs)-len(receivedIDs)),
	}
	for id := range sentIDs {
		if _, received := receivedIDs[id]; !received {
			result.Lost = append(result.Lost, id)
		}
	}
	sort.Ints(result.Lost)
	if result.Sent > 0 {
		result.Ratio = float64(len(result.Lost)) / float64(result.Sent)
	}
	return result
}

type pionPacketLossSession struct {
	ctx              context.Context
	cancel           context.CancelFunc
	sender           *webrtc.PeerConnection
	receiver         *webrtc.PeerConnection
	senderChannel    *webrtc.DataChannel
	messages         chan packetLossMessage
	errors           chan error
	relayUDPVerified bool
	closeOnce        sync.Once
	closeErr         error
}

func newPionPacketLossSession(
	sessionCtx, setupCtx context.Context,
	cfg packetLossConfig,
	credentials packetLossCredentials,
) (opened packetLossSession, resultErr error) {
	ctx, cancel := context.WithCancel(sessionCtx)
	bufferSize := cfg.Count
	if bufferSize < 1 {
		bufferSize = 1
	}
	if bufferSize > maxPacketLossMessageBuffer {
		bufferSize = maxPacketLossMessageBuffer
	}
	session := &pionPacketLossSession{
		ctx:      ctx,
		cancel:   cancel,
		messages: make(chan packetLossMessage, bufferSize),
		errors:   make(chan error, 1),
	}
	initialized := false
	defer func() {
		if !initialized {
			if closeErr := session.Close(); closeErr != nil {
				resultErr = errPacketLossCleanup
			}
		}
	}()

	if !validTURNAuthority(credentials.Server) || credentials.Username == "" || credentials.Credential == "" {
		return nil, errPacketLossPeerSetup
	}
	turnURL := "turn:" + credentials.Server + "?transport=udp"
	settings := webrtc.SettingEngine{}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	// Relay-only peers do not need host-candidate multicast discovery.
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))
	configuration := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{
			URLs:           []string{turnURL},
			Username:       credentials.Username,
			Credential:     credentials.Credential,
			CredentialType: webrtc.ICECredentialTypePassword,
		}},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	}

	var err error
	session.sender, err = api.NewPeerConnection(configuration)
	if err != nil {
		return nil, errPacketLossPeerSetup
	}
	session.receiver, err = api.NewPeerConnection(configuration)
	if err != nil {
		return nil, errPacketLossPeerSetup
	}

	readyEvents := make(chan error, 2)
	session.sender.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed {
			session.reportError()
		}
	})
	session.receiver.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed {
			session.reportError()
		}
	})
	session.receiver.OnDataChannel(func(channel *webrtc.DataChannel) {
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			if !message.IsString {
				return
			}
			messageEvent := packetLossMessage{Text: string(message.Data), ReceivedAt: time.Now()}
			select {
			case session.messages <- messageEvent:
			case <-session.ctx.Done():
			}
		})
		channel.OnError(func(error) { session.reportError() })
		channel.OnClose(func() { session.reportError() })
		channel.OnOpen(func() {
			readyEvents <- verifyPacketLossSelectedPath(channel)
		})
	})

	ordered := false
	maxRetransmits := uint16(0)
	session.senderChannel, err = session.sender.CreateDataChannel("channel", &webrtc.DataChannelInit{
		Ordered:        &ordered,
		MaxRetransmits: &maxRetransmits,
	})
	if err != nil {
		return nil, errPacketLossPeerSetup
	}
	session.senderChannel.OnError(func(error) { session.reportError() })
	session.senderChannel.OnClose(func() { session.reportError() })
	session.senderChannel.OnOpen(func() {
		readyEvents <- verifyPacketLossSelectedPath(session.senderChannel)
	})

	offer, err := session.sender.CreateOffer(nil)
	if err != nil {
		return nil, errPacketLossPeerSetup
	}
	if err = setPacketLossLocalDescription(setupCtx, session.sender, offer); err != nil {
		return nil, errPacketLossPeerSetup
	}
	offerDescription := session.sender.LocalDescription()
	if offerDescription == nil || !packetLossSDPHasOnlyRelayUDP(offerDescription.SDP) {
		return nil, errPacketLossPeerSetup
	}
	if err = session.receiver.SetRemoteDescription(*offerDescription); err != nil {
		return nil, errPacketLossPeerSetup
	}
	answer, err := session.receiver.CreateAnswer(nil)
	if err != nil {
		return nil, errPacketLossPeerSetup
	}
	if err = setPacketLossLocalDescription(setupCtx, session.receiver, answer); err != nil {
		return nil, errPacketLossPeerSetup
	}
	answerDescription := session.receiver.LocalDescription()
	if answerDescription == nil || !packetLossSDPHasOnlyRelayUDP(answerDescription.SDP) {
		return nil, errPacketLossPeerSetup
	}
	if err = session.sender.SetRemoteDescription(*answerDescription); err != nil {
		return nil, errPacketLossPeerSetup
	}

	for range 2 {
		select {
		case err = <-readyEvents:
			if err != nil {
				return nil, errPacketLossPeerSetup
			}
		case <-session.errors:
			return nil, errPacketLossPeerSetup
		case <-setupCtx.Done():
			return nil, errPacketLossPeerSetup
		case <-session.ctx.Done():
			return nil, errPacketLossPeerSetup
		}
	}
	if err := setupCtx.Err(); err != nil {
		return nil, errPacketLossPeerSetup
	}
	session.relayUDPVerified = true

	initialized = true
	go func() {
		<-session.ctx.Done()
		_ = session.Close()
	}()
	return session, nil
}

func setPacketLossLocalDescription(ctx context.Context, peer *webrtc.PeerConnection, description webrtc.SessionDescription) error {
	gathered := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(description); err != nil {
		return err
	}
	select {
	case <-gathered:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func packetLossSDPHasOnlyRelayUDP(sdp string) bool {
	found := false
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "a="))
		if len(fields) < 8 || !strings.EqualFold(fields[2], "udp") || !strings.EqualFold(fields[6], "typ") || !strings.EqualFold(fields[7], "relay") {
			return false
		}
		found = true
	}
	return found
}

func verifyPacketLossSelectedPath(channel *webrtc.DataChannel) error {
	if channel == nil || channel.Transport() == nil || channel.Transport().Transport() == nil {
		return errPacketLossPeerSetup
	}
	iceTransport := channel.Transport().Transport().ICETransport()
	if iceTransport == nil {
		return errPacketLossPeerSetup
	}
	pair, err := iceTransport.GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil {
		return errPacketLossPeerSetup
	}
	if pair.Local.Typ != webrtc.ICECandidateTypeRelay || pair.Remote.Typ != webrtc.ICECandidateTypeRelay || pair.Local.Protocol != webrtc.ICEProtocolUDP || pair.Remote.Protocol != webrtc.ICEProtocolUDP {
		return errPacketLossPeerSetup
	}
	return nil
}

func (session *pionPacketLossSession) Messages() <-chan packetLossMessage { return session.messages }
func (session *pionPacketLossSession) Errors() <-chan error               { return session.errors }

func (session *pionPacketLossSession) SendText(text string) error {
	if session.senderChannel == nil {
		return errPacketLossSend
	}
	if err := session.senderChannel.SendText(text); err != nil {
		return errPacketLossSend
	}
	return nil
}

func (session *pionPacketLossSession) reportError() {
	select {
	case session.errors <- errPacketLossTransport:
	default:
	}
}

func (session *pionPacketLossSession) Close() error {
	session.closeOnce.Do(func() {
		session.cancel()
		peers := []*webrtc.PeerConnection{session.sender, session.receiver}
		cleanupDeadline := time.Now().Add(packetLossCleanupTimeout)
		results := make(chan error, len(peers))
		count := 0
		for _, peer := range peers {
			if peer == nil {
				continue
			}
			count++
			go func(peer *webrtc.PeerConnection) {
				results <- peer.GracefulClose()
			}(peer)
		}
		if err := waitPacketLossPeerCloses(results, count, cleanupDeadline); err != nil {
			session.closeErr = err
		}
		for _, peer := range peers {
			if peer != nil && peer.ConnectionState() != webrtc.PeerConnectionStateClosed {
				session.closeErr = fmt.Errorf("%w: a peer remains open", errPacketLossCleanup)
			}
		}
	})
	return session.closeErr
}

func waitPacketLossPeerCloses(results <-chan error, count int, deadline time.Time) error {
	if count == 0 {
		return nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("%w: shutdown timed out", errPacketLossCleanup)
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	var failure error
	for count > 0 {
		select {
		case err := <-results:
			if err != nil {
				failure = fmt.Errorf("%w: peer returned a close error", errPacketLossCleanup)
			}
			count--
		case <-timer.C:
			return fmt.Errorf("%w: shutdown timed out", errPacketLossCleanup)
		}
	}
	return failure
}
