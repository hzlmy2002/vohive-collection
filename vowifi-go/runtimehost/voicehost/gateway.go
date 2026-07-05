package voicehost

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
)

const DefaultSimulateCallHoldSeconds = 15
const MaxSimulateCallHoldSeconds = 120

type Gateway struct {
	mu            sync.RWMutex
	clientAdapter any
	notifier      any
}

func NewGateway() *Gateway { return &Gateway{} }

func (g *Gateway) Start(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (g *Gateway) Stop() error { return nil }

func (g *Gateway) GetAgent(deviceID string) any { return nil }

func (g *Gateway) DeviceStatus(deviceID string) any { return nil }

func (g *Gateway) SetClientAdapter(adapter any) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.clientAdapter = adapter
	g.mu.Unlock()
}

func (g *Gateway) SetNotifier(notifier any) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.notifier = notifier
	g.mu.Unlock()
}

func (g *Gateway) HandleClientInvite(deviceID string, req *sip.Request, tx sip.ServerTransaction) {
	respond(req, tx, 480, "Temporarily Unavailable")
}

func (g *Gateway) HandleClientCancel(deviceID string, req *sip.Request, tx sip.ServerTransaction) {
	respond(req, tx, 200, "OK")
}

func (g *Gateway) HandleClientPrack(deviceID string, req *sip.Request, tx sip.ServerTransaction) {
	respond(req, tx, 200, "OK")
}

func (g *Gateway) HandleClientAck(deviceID string, req *sip.Request, tx sip.ServerTransaction) {
	respond(req, tx, 200, "OK")
}

func (g *Gateway) HandleClientBye(deviceID string, req *sip.Request, tx sip.ServerTransaction) {
	respond(req, tx, 200, "OK")
}

func (g *Gateway) SimulateCall(ctx context.Context, deviceID string, req SimulateCallRequest) (SimulateCallResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	hold := req.HoldSeconds
	if hold <= 0 {
		hold = DefaultSimulateCallHoldSeconds
	}
	if hold > MaxSimulateCallHoldSeconds {
		hold = MaxSimulateCallHoldSeconds
	}
	start := time.Now()
	if req.OnConnected != nil {
		req.OnConnected()
	}
	timer := time.NewTimer(time.Duration(hold) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return SimulateCallResult{Success: false, DurationMs: time.Since(start).Milliseconds(), Reason: ctx.Err().Error()}, ctx.Err()
	case <-timer.C:
		return SimulateCallResult{Success: true, DurationMs: time.Since(start).Milliseconds()}, nil
	}
}

type SimulateCallRequest struct {
	Callee      string
	HoldSeconds int
	OnConnected func()
}

type SimulateCallResult struct {
	Success    bool   `json:"success"`
	DurationMs int64  `json:"duration_ms"`
	Reason     string `json:"reason,omitempty"`
}

type SDPInfo struct {
	ConnectionIP string
	MediaPort    int
}

func ParseSDP(body []byte) (SDPInfo, error) {
	var info SDPInfo
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "c=") {
			fields := strings.Fields(strings.TrimPrefix(line, "c="))
			if len(fields) >= 3 {
				info.ConnectionIP = fields[2]
			}
			continue
		}
		if strings.HasPrefix(line, "m=audio") {
			fields := strings.Fields(strings.TrimPrefix(line, "m="))
			if len(fields) >= 2 {
				port, err := strconv.Atoi(fields[1])
				if err == nil {
					info.MediaPort = port
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return SDPInfo{}, err
	}
	if info.ConnectionIP == "" || info.MediaPort == 0 {
		return info, errors.New("sdp missing audio connection")
	}
	return info, nil
}

func respond(req *sip.Request, tx sip.ServerTransaction, code int, reason string) {
	if req == nil || tx == nil {
		return
	}
	_ = tx.Respond(sip.NewResponseFromRequest(req, code, reason, nil))
}
