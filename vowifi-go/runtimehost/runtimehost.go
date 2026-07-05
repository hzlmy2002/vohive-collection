package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	swusim "github.com/iniwex5/vowifi-go/engine/sim"
	"github.com/iniwex5/vowifi-go/internal/vowifi/runtimecore"
	"github.com/iniwex5/vowifi-go/runtimehost/eventhost"
	"github.com/iniwex5/vowifi-go/runtimehost/identity"
	"github.com/iniwex5/vowifi-go/runtimehost/messaging"
	"github.com/iniwex5/vowifi-go/runtimehost/voicehost"
)

var ErrAPDUBusy = errors.New("apdu busy")

type StartMode string

const StartModeMain StartMode = "main"

type Phase string

const (
	PhaseStarting Phase = "starting"
	PhaseSIMReady Phase = "sim_ready"
	PhaseTunnel   Phase = "tunnel_ready"
	PhaseIMSReady Phase = "ims_ready"
	PhaseSMSReady Phase = "sms_ready"
	PhaseFailed   Phase = "failed"
	PhaseStopped  Phase = "stopped"
)

type State struct {
	DeviceID       string
	Phase          Phase
	DataplaneMode  string
	SIMReady       bool
	AccessReady    bool
	TunnelReady    bool
	IMSReady       bool
	SMSReady       bool
	RegStatus      int
	RegStatusText  string
	NetworkMode    string
	LastErrorClass string
	LastError      string
	LastReason     string
	UpdatedAt      time.Time
}

type ProxyConfig struct {
	ID       string
	Addr     string
	Username string
	Password string
	Enabled  bool
}

type DataplanePolicy struct {
	Mode string
}

type SessionConfig struct {
	DataplaneMode string
}

type StartRequest struct {
	Mode          StartMode
	DeviceID      string
	TraceID       string
	Profile       identity.Profile
	Prepared      *identity.PreparedSession
	NetworkMode   string
	VoiceGateway  *voicehost.Gateway
	SIM           SIMAdapter
	Access        any
	Dataplane     DataplanePolicy
	Proxy         *ProxyConfig
	DeliveryStore messaging.DeliveryStore
	Dispatch      eventhost.Dispatcher
	BeforeStart   func(context.Context, SessionConfig) error
	ShouldRun     func() bool
}

type Modem interface {
	DeviceID() string
	IsHealthy() bool
	IsSimInserted() bool
	QuerySIMInserted() (bool, error)
	GetRegStatus() (int, string)
	GetNetworkMode() string
	ExecuteATSilent(cmd string, timeout time.Duration) (string, error)
	OpenLogicalChannel(aid string) (int, error)
	CloseLogicalChannel(channel int) error
	TransmitAPDU(channel int, hexAPDU string) (string, error)
	Stop()
}

type LogicalChannelAIDResolver interface {
	ResolveLogicalChannelAID(app string, fallbackAID string) (aid string, source string, err error)
}

type ISIMIdentityReader interface {
	GetISIMIdentity() (identity.Identity, error)
}

type SIMAdapter interface {
	GetIMSI() (string, error)
	CalculateAKA(rand, autn []byte) (swusim.AKAResult, error)
	Close() error
}

type readerSIMAdapter struct {
	provider swusim.AKAProvider
}

func NewReaderSIMAdapter(provider swusim.AKAProvider) SIMAdapter {
	return readerSIMAdapter{provider: provider}
}

func (a readerSIMAdapter) GetIMSI() (string, error) {
	if provider, ok := a.provider.(interface{ GetIMSI() (string, error) }); ok {
		return provider.GetIMSI()
	}
	return "", errors.New("imsi unavailable")
}

func (a readerSIMAdapter) CalculateAKA(rand, autn []byte) (swusim.AKAResult, error) {
	if a.provider == nil {
		return swusim.AKAResult{}, errors.New("aka provider unavailable")
	}
	return a.provider.CalculateAKA(rand, autn)
}

func (a readerSIMAdapter) Close() error {
	if closer, ok := a.provider.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

type modemAccessAdapter struct {
	modem Modem
}

func NewModemAccessAdapter(modem Modem) any {
	if modem == nil {
		return nil
	}
	return modemAccessAdapter{modem: modem}
}

func (a modemAccessAdapter) RuntimeModem() any { return a.modem }
func (a modemAccessAdapter) DeviceID() string  { return a.modem.DeviceID() }
func (a modemAccessAdapter) ExecuteATSilent(cmd string, timeout time.Duration) (string, error) {
	return a.modem.ExecuteATSilent(cmd, timeout)
}
func (a modemAccessAdapter) OpenLogicalChannel(aid string) (int, error) {
	return a.modem.OpenLogicalChannel(aid)
}
func (a modemAccessAdapter) CloseLogicalChannel(channel int) error {
	return a.modem.CloseLogicalChannel(channel)
}
func (a modemAccessAdapter) TransmitAPDU(channel int, hexAPDU string) (string, error) {
	return a.modem.TransmitAPDU(channel, hexAPDU)
}
func (a modemAccessAdapter) ResolveLogicalChannelAID(app string, fallbackAID string) (string, string, error) {
	if resolver, ok := a.modem.(LogicalChannelAIDResolver); ok {
		return resolver.ResolveLogicalChannelAID(app, fallbackAID)
	}
	return fallbackAID, "fallback", nil
}

type IMSService interface {
	SendSMSWithOptions(ctx context.Context, to, text string, opts messaging.SendOptions) (messaging.SendOutcome, error)
	SendUSSD(ctx context.Context, command string) (*messaging.USSDResult, error)
	ContinueUSSD(ctx context.Context, sessionID, input string) (*messaging.USSDResult, error)
	CancelUSSD(ctx context.Context, sessionID string) error
}

type Event struct {
	State State
}

type Observer interface {
	OnRuntimeEvent(context.Context, Event)
}

type ObserverFunc func(context.Context, Event)

func (f ObserverFunc) OnRuntimeEvent(ctx context.Context, ev Event) {
	if f != nil {
		f(ctx, ev)
	}
}

type Instance struct {
	mu        sync.RWMutex
	cancel    context.CancelFunc
	state     State
	obs       map[string]interface{}
	service   IMSService
	delivery  messaging.DeliveryStore
	observers []Observer
	stopped   bool
	notifier  func(string)
	smsNotify func(string, string, string, time.Time)
	tunnel    runtimecore.Session
}

func Start(ctx context.Context, req StartRequest) (*Instance, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		return nil, errors.New("device_id is empty")
	}
	if req.Mode == "" {
		req.Mode = StartModeMain
	}
	if req.ShouldRun != nil && !req.ShouldRun() {
		return nil, errors.New("runtime start canceled")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	prepared := req.Prepared
	if prepared == nil {
		p, err := identity.PrepareStart(identity.PrepareStartInput{
			DeviceID: strings.TrimSpace(req.DeviceID),
			Profile:  req.Profile,
			Access:   req.Access,
		})
		if err != nil {
			return nil, fmt.Errorf("identity prepare failed: %w", err)
		}
		prepared = &p
	}

	runCtx, cancel := context.WithCancel(ctx)
	inst := &Instance{
		cancel:   cancel,
		service:  notReadyIMSService{},
		delivery: req.DeliveryStore,
		obs: map[string]interface{}{
			"device_id":      deviceID,
			"trace_id":       strings.TrimSpace(req.TraceID),
			"dry_run":        true,
			"swu_enabled":    swuRuntimeEnabled(),
			"prepared_epdg":  strings.TrimSpace(prepared.EPDGAddr),
			"epdg_source":    strings.TrimSpace(prepared.EPDGSource),
			"carrier_preset": strings.TrimSpace(prepared.EffectiveCarrier.PresetID),
		},
	}

	inst.setState(runCtx, State{
		DeviceID:      deviceID,
		Phase:         PhaseStarting,
		DataplaneMode: strings.TrimSpace(req.Dataplane.Mode),
		NetworkMode:   strings.TrimSpace(req.NetworkMode),
		LastReason:    "starting",
		UpdatedAt:     time.Now(),
	})

	if req.BeforeStart != nil {
		if err := req.BeforeStart(runCtx, SessionConfig{DataplaneMode: strings.TrimSpace(req.Dataplane.Mode)}); err != nil {
			inst.fail(runCtx, "proxy", "before_start_failed", err)
			cancel()
			return inst, err
		}
	}
	if req.ShouldRun != nil && !req.ShouldRun() {
		err := errors.New("runtime start canceled")
		inst.fail(runCtx, "lifecycle", "should_run_false", err)
		cancel()
		return inst, err
	}
	if err := runCtx.Err(); err != nil {
		inst.fail(context.Background(), "lifecycle", "context_canceled", err)
		cancel()
		return inst, err
	}

	regStatus, regText := 0, ""
	if modem, ok := req.Access.(interface{ RuntimeModem() any }); ok {
		if m, ok := modem.RuntimeModem().(Modem); ok && m != nil {
			regStatus, regText = m.GetRegStatus()
			if req.NetworkMode == "" {
				req.NetworkMode = m.GetNetworkMode()
			}
		}
	}

	inst.setState(runCtx, State{
		DeviceID:      deviceID,
		Phase:         PhaseSIMReady,
		DataplaneMode: strings.TrimSpace(req.Dataplane.Mode),
		SIMReady:      req.SIM != nil,
		AccessReady:   req.Access != nil,
		RegStatus:     regStatus,
		RegStatusText: strings.TrimSpace(regText),
		NetworkMode:   strings.TrimSpace(req.NetworkMode),
		LastReason:    "dry_run_runtime_started",
		UpdatedAt:     time.Now(),
	})

	if !swuRuntimeEnabled() {
		return inst, nil
	}

	inst.setState(runCtx, State{
		DeviceID:      deviceID,
		Phase:         PhaseStarting,
		DataplaneMode: strings.TrimSpace(req.Dataplane.Mode),
		SIMReady:      req.SIM != nil,
		AccessReady:   req.Access != nil,
		RegStatus:     regStatus,
		RegStatusText: strings.TrimSpace(regText),
		NetworkMode:   strings.TrimSpace(req.NetworkMode),
		LastReason:    "epdg_connecting",
		UpdatedAt:     time.Now(),
	})

	tunnel, err := runtimecore.StartAndWaitEPDG(runCtx, runtimecore.StartInput{
		DeviceID:      deviceID,
		Profile:       profileOrPrepared(req.Profile, *prepared),
		Prepared:      *prepared,
		SIM:           req.SIM,
		DataplaneMode: strings.TrimSpace(req.Dataplane.Mode),
		Proxy:         runtimecoreProxy(req.Proxy),
		EnableDriver:  true,
		Logger:        logger,
	})
	if err != nil {
		inst.fail(runCtx, "epdg", "epdg_tunnel_failed", err)
		cancel()
		return inst, err
	}
	snap := tunnel.Snapshot()
	inst.mu.Lock()
	inst.tunnel = tunnel
	if inst.obs == nil {
		inst.obs = make(map[string]interface{})
	}
	inst.obs["dry_run"] = false
	inst.obs["tunnel"] = tunnelObs(snap)
	inst.mu.Unlock()
	inst.setState(runCtx, State{
		DeviceID:      deviceID,
		Phase:         PhaseTunnel,
		DataplaneMode: strings.TrimSpace(req.Dataplane.Mode),
		SIMReady:      true,
		AccessReady:   true,
		TunnelReady:   snap.Established,
		RegStatus:     regStatus,
		RegStatusText: strings.TrimSpace(regText),
		NetworkMode:   strings.TrimSpace(req.NetworkMode),
		LastReason:    "epdg_tunnel_ready",
		UpdatedAt:     time.Now(),
	})
	return inst, nil
}

func (i *Instance) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	i.mu.Lock()
	if i.stopped {
		i.mu.Unlock()
		return nil
	}
	i.stopped = true
	cancel := i.cancel
	st := i.state
	tunnel := i.tunnel
	st.Phase = PhaseStopped
	st.LastReason = "stopped"
	st.UpdatedAt = time.Now()
	i.state = st
	observers := append([]Observer(nil), i.observers...)
	i.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if tunnel != nil {
		tunnel.Shutdown()
		_ = tunnel.WaitDoneContext(ctx)
	}
	ev := Event{State: st}
	for _, observer := range observers {
		if observer != nil {
			observer.OnRuntimeEvent(ctx, ev)
		}
	}
	return nil
}

func (i *Instance) State() State {
	if i == nil {
		return State{}
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.state
}

func (i *Instance) Status() string {
	st := i.State()
	if st.Phase == "" {
		return ""
	}
	return string(st.Phase)
}

func (i *Instance) Obs() map[string]interface{} {
	if i == nil {
		return map[string]interface{}{}
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make(map[string]interface{}, len(i.obs)+2)
	for k, v := range i.obs {
		out[k] = v
	}
	out["state"] = i.state
	out["status"] = string(i.state.Phase)
	return out
}

func (i *Instance) Service() IMSService {
	if i == nil {
		return notReadyIMSService{}
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.service == nil {
		return notReadyIMSService{}
	}
	return i.service
}

func (i *Instance) SendSMSWithOptions(ctx context.Context, to, text string, opts messaging.SendOptions) (messaging.SendOutcome, error) {
	if i == nil {
		return messaging.SendOutcome{}, errors.New("vowifi instance unavailable")
	}
	return i.Service().SendSMSWithOptions(ctx, to, text, opts)
}

func (i *Instance) GetSMSDeliveryStatus(messageID string) (*messaging.DeliveryStatus, error) {
	if i == nil {
		return nil, errors.New("vowifi instance unavailable")
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil, errors.New("message_id is empty")
	}

	i.mu.RLock()
	delivery := i.delivery
	service := i.service
	i.mu.RUnlock()

	if delivery != nil {
		return delivery.GetSMSDeliveryStatus(messageID)
	}
	if svc, ok := service.(interface {
		GetSMSDeliveryStatus(string) (*messaging.DeliveryStatus, error)
	}); ok && svc != nil {
		return svc.GetSMSDeliveryStatus(messageID)
	}
	return nil, messaging.ErrDeliveryNotFound
}

func (i *Instance) AddObserver(observer Observer) {
	if i == nil || observer == nil {
		return
	}
	i.mu.Lock()
	i.observers = append(i.observers, observer)
	st := i.state
	i.mu.Unlock()
	if st.UpdatedAt.IsZero() && st.Phase == "" && st.DeviceID == "" {
		return
	}
	observer.OnRuntimeEvent(context.Background(), Event{State: st})
}

func (i *Instance) SetNotifier(fn func(string)) {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.notifier = fn
	i.mu.Unlock()
}

func (i *Instance) SetSMSNotifier(fn func(deviceID, sender, content string, ts time.Time)) {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.smsNotify = fn
	i.mu.Unlock()
}

func (i *Instance) TriggerMOBIKE(oldIP, newIP string) error {
	if i == nil {
		return errors.New("vowifi instance unavailable")
	}
	i.mu.Lock()
	tunnel := i.tunnel
	if i.obs == nil {
		i.obs = make(map[string]interface{})
	}
	i.obs["mobike_old_ip"] = strings.TrimSpace(oldIP)
	i.obs["mobike_new_ip"] = strings.TrimSpace(newIP)
	i.obs["mobike_requested_at"] = time.Now()
	i.mu.Unlock()
	if tunnel != nil {
		return tunnel.TriggerMOBIKE(oldIP, newIP)
	}
	return nil
}

func (i *Instance) setState(ctx context.Context, st State) {
	i.mu.Lock()
	i.state = st
	observers := append([]Observer(nil), i.observers...)
	i.mu.Unlock()

	ev := Event{State: st}
	for _, observer := range observers {
		if observer != nil {
			observer.OnRuntimeEvent(ctx, ev)
		}
	}
}

func (i *Instance) fail(ctx context.Context, class, reason string, err error) {
	st := i.State()
	st.Phase = PhaseFailed
	st.LastErrorClass = class
	st.LastReason = reason
	if err != nil {
		st.LastError = err.Error()
	}
	st.UpdatedAt = time.Now()
	i.setState(ctx, st)
}

type notReadyIMSService struct{}

func (notReadyIMSService) SendSMSWithOptions(context.Context, string, string, messaging.SendOptions) (messaging.SendOutcome, error) {
	return messaging.SendOutcome{}, errors.New("ims service not ready")
}

func (notReadyIMSService) SendUSSD(context.Context, string) (*messaging.USSDResult, error) {
	return nil, errors.New("ims service not ready")
}

func (notReadyIMSService) ContinueUSSD(context.Context, string, string) (*messaging.USSDResult, error) {
	return nil, errors.New("ims service not ready")
}

func (notReadyIMSService) CancelUSSD(context.Context, string) error {
	return errors.New("ims service not ready")
}

func swuRuntimeEnabled() bool {
	for _, key := range []string{"VOWIFI_GO_ENABLE_SWU", "VOHIVE_VOWIFI_ENABLE_SWU"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func runtimecoreProxy(proxy *ProxyConfig) *runtimecore.ProxyConfig {
	if proxy == nil {
		return nil
	}
	return &runtimecore.ProxyConfig{
		Addr:     strings.TrimSpace(proxy.Addr),
		Username: strings.TrimSpace(proxy.Username),
		Password: proxy.Password,
		Enabled:  proxy.Enabled,
	}
}

func profileOrPrepared(profile identity.Profile, prepared identity.PreparedSession) identity.Profile {
	profile = identity.NormalizeProfile(profile)
	if strings.TrimSpace(profile.IMSI) != "" {
		return profile
	}
	return prepared.Profile
}

func tunnelObs(snap runtimecore.TunnelSnapshot) map[string]interface{} {
	return map[string]interface{}{
		"established":                snap.Established,
		"tun_name":                   snap.TUNName,
		"last_error":                 snap.LastError,
		"ike_profile":                snap.IKEProfile,
		"ike_encr":                   snap.IKEEncr,
		"ike_integ":                  snap.IKEInteg,
		"ike_prf":                    snap.IKEPRF,
		"ike_dh":                     snap.IKEDH,
		"ipv4":                       ipToString(snap.IPv4),
		"ipv6":                       ipToString(snap.IPv6),
		"ipv6_prefix":                snap.IPv6Prefix,
		"dns_v4":                     ipsToStrings(snap.DNSv4),
		"dns_v6":                     ipsToStrings(snap.DNSv6),
		"pcscf_v4":                   ipsToStrings(snap.PCSCFv4),
		"pcscf_v6":                   ipsToStrings(snap.PCSCFv6),
		"offered_ike_profiles":       append([]string(nil), snap.OfferedIKEProfiles...),
		"effective_cipher_policy":    snap.EffectiveCipherPolicy,
		"negotiation_fallback_count": snap.NegotiationFallbackCount,
	}
}

func ipsToStrings(ips []net.IP) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		out = append(out, ip.String())
	}
	return out
}

func ipToString(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}
