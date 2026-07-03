# vowifi-go Go Rewrite Plan

This document is the English companion to `VOWIFI_GO_REWRITE_PLAN.md`. Its goal is to define a concrete plan for rebuilding `github.com/iniwex5/vowifi-go` from scratch in Go while preserving the Go API surface expected by the current VoHive repository.

The implementation should remain a Go module. `/opt/SimAdmin` can be used as an architectural and protocol-stage reference because its VoWiFi runtime follows the same broad design: SIM identity, carrier profile matching, SIM AKA, ePDG, IKEv2/EAP-AKA, Child SA, userspace ESP/TUN, IMS registration, SMS over IMS, diagnostics, and restore logic.

## 1. Current Situation

VoHive itself is mostly intact. The missing component is the sibling module:

```text
/opt/vohive-collection/vowifi-go
module github.com/iniwex5/vowifi-go
```

The current VoHive `go.work` already references that sibling path:

```text
use (
    .
    ../quectel-qmi-go
    ../vowifi-go
)
```

Therefore the preferred recovery path is to recreate `/opt/vohive-collection/vowifi-go` as a Go module, rather than changing VoHive imports first.

VoHive currently owns:

- Device discovery and lifecycle management.
- QMI, MBIM, and AT modem backends.
- SIM and eSIM management.
- APDU arbitration.
- AKA provider adaptation.
- VoWiFi enable, disable, restart, and recovery orchestration.
- Web/API surface and runtime state projection.
- SMS history persistence and notification dispatch.
- E911 websheet broker integration.
- Country-based upstream proxy configuration.
- eSIM switch restore scheduling.

The missing `vowifi-go` module must own:

- Carrier VoWiFi profiles and default 3GPP domain generation.
- ePDG and IMS policy resolution.
- SIM AKA and EAP-AKA integration.
- IKEv2/IPsec/ESP userspace dataplane.
- IMS SIP registration.
- SMS over IMS.
- USSD over IMS.
- E911 entitlement and websheet provider logic.
- A stable `runtimehost` API consumed by VoHive.

## 2. How to Use SimAdmin as Reference

SimAdmin is a Rust project, so it should not be mechanically translated file-by-file. The useful part is its boundary design and runtime stage model. The Go rewrite should reproduce the same stage architecture in Go-native packages.

Recommended stage mapping:

| SimAdmin stage | Go rewrite module | VoHive state mapping |
| --- | --- | --- |
| `identity` | `runtimehost/identity` | prerequisite for `SIMReady` |
| `profiles` | `runtimehost/carrier` | `PreparedSession.EffectiveCarrier` |
| `aka` / `qmi_uim` | `engine/sim` plus runtime SIM adapter | `SIMReady`, `AccessReady` |
| `epdg` / `transport` | `internal/epdg` | prerequisite for `TunnelReady` |
| `ike_*` / `eap_aka` | `internal/ike`, `internal/eapaka` | `TunnelReady` |
| `dataplane` / `tun_gateway` | `internal/ipsec`, `internal/tun` | `TunnelReady` |
| `ims` | `internal/ims` | `IMSReady` |
| `sms` | `runtimehost/messaging` plus `internal/ims/sms` | `SMSReady` |
| `restore` / `stability` | runtime reconnect and retry logic | `LastReason`, `LastErrorClass` |
| `diagnostics` / `flow` | `runtimehost.Instance.Obs()` | Web diagnostics |

Design principles:

1. `runtimehost.Start` should orchestrate stages, not contain every protocol detail.
2. Every stage should produce structured state.
3. `runtimehost.State` should stay stable and small.
4. Detailed diagnostics should go into `Instance.Obs()`.
5. Public Go API compatibility must be restored before full protocol support.
6. Sensitive material must never be logged or serialized: IMSI, RAND, AUTN, RES, CK, IK, AUTS, K_aut, MSK, IKE keys, ESP keys.

## 3. Recommended Module Layout

Create this module:

```text
vowifi-go/
  go.mod
  engine/
    sim/
      aka.go
    swu/
      constants.go
  runtimehost/
    instance.go
    start.go
    state.go
    modem.go
    sim_adapter.go
    trace.go
    logger.go
    carrier/
      carrier.go
      overrides.go
      presets.go
    e911/
      e911.go
      http.go
      att.go
    eventhost/
      events.go
    identity/
      profile.go
      prepare.go
      isim.go
      normalize.go
    messaging/
      delivery.go
      sms.go
      ussd.go
      context.go
    voicehost/
      gateway.go
      sdp.go
  internal/
    epdg/
      resolve.go
      plan.go
    eapaka/
      packet.go
      kdf.go
      response.go
    ike/
      codec.go
      payloads.go
      state.go
      keys.go
      retransmit.go
    ipsec/
      esp.go
      child_sa.go
      replay.go
    tun/
      gateway.go
      route.go
    ims/
      sip.go
      register.go
      auth.go
      sms.go
      ussd.go
    obs/
      redaction.go
      flow.go
```

`runtimehost/*` is the compatibility surface. `internal/*` is implementation detail and can evolve without changing VoHive.

## 4. Required Go Compatibility Surface

The following packages, types, functions, fields, and methods are required by current VoHive code. The first implementation milestone must provide all of them.

### 4.1 `engine/sim`

```go
package sim

import "errors"

var ErrSyncFailure = errors.New("aka sync failure")

type AKAResult struct {
    RES  []byte
    CK   []byte
    IK   []byte
    AUTS []byte
}

type AKAProvider interface {
    CalculateAKA(rand16, autn16 []byte) (AKAResult, error)
}

type ISIMAKAProvider interface {
    CalculateISIMAKA(rand16, autn16 []byte) (AKAResult, error)
}
```

Requirements:

- `ErrSyncFailure` must work with `errors.Is`.
- If `AKAResult.AUTS` is non-empty and the error is `ErrSyncFailure`, EAP-AKA must build a sync-failure response.
- No AKA secrets should be printed in errors, logs, JSON, or `fmt.Stringer` output.

### 4.2 `engine/swu`

```go
package swu

const DataplaneModeUserspace = "userspace"
```

VoHive currently only needs this constant.

### 4.3 `runtimehost`

Core state and request types:

```go
package runtimehost

import (
    "context"
    "errors"
    "time"

    swusim "github.com/iniwex5/vowifi-go/engine/sim"
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
```

Modem and SIM adaptation:

```go
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

func NewReaderSIMAdapter(provider swusim.AKAProvider) SIMAdapter
func NewModemAccessAdapter(modem Modem) any
```

Runtime instance:

```go
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

func (f ObserverFunc) OnRuntimeEvent(ctx context.Context, ev Event)

type Instance struct {
    // Zero value must be safe.
}

func Start(ctx context.Context, req StartRequest) (*Instance, error)
func (i *Instance) Stop(ctx context.Context) error
func (i *Instance) State() State
func (i *Instance) Status() string
func (i *Instance) Obs() map[string]interface{}
func (i *Instance) Service() IMSService
func (i *Instance) AddObserver(observer Observer)
```

Trace and logging:

```go
func NewTraceID() string
func WithTraceID(ctx context.Context, traceID string) context.Context
func TraceIDFromContext(ctx context.Context) string
func SetLogger(logger any)
```

Runtime requirements:

- `Instance{}` zero value must not panic.
- `Stop` must be idempotent.
- `Start` must check `ctx.Err()` and `req.ShouldRun()` between every expensive stage.
- `BeforeStart` must run before real network dialing.
- Observers must receive state updates.
- `Obs()` must be safe to call concurrently and must not leak secrets.

### 4.4 `runtimehost/identity`

```go
package identity

type Profile struct {
    IMSI string
    MCC  string
    MNC  string
    IMEI string
    SMSC string
}

type Identity struct {
    IMPI   string
    IMPU   []string
    Domain string
}

type PrepareStartInput struct {
    DeviceID            string
    Profile             Profile
    RuntimeEPDGOverride string
    Access              any
}

type IMSIdentity struct {
    RequestedSource  string
    ActualSource     string
    AKAAppPreference string
    Applied          bool
}

type PreparedSession struct {
    Profile            Profile
    EffectiveCarrier   carrier.Config
    EPDGSource         string
    EPDGAddr           string
    IdentityIMEISource string
    IMSIdentity        IMSIdentity
}

const (
    IMSIdentitySourceISIM      = "isim"
    IMSIdentitySourceUSIM      = "usim"
    IMSIdentitySourceGenerated = "generated"
    AKAAppPreferenceAuto       = "auto"
    AKAAppPreferenceISIM       = "isim"
    AKAAppPreferenceISIMStrict = "isim_strict"
    AKAAppPreferenceUSIM       = "usim"
)

func NormalizeProfile(p Profile) Profile
func PrepareStart(input PrepareStartInput) (PreparedSession, error)
func ReadISIMIdentity(access any) (Identity, error)
```

`PrepareStart` algorithm:

1. Normalize IMSI, MCC, MNC, IMEI, and SMSC.
2. If MCC/MNC are missing, derive them from IMSI when possible.
3. Resolve carrier config with `carrier.ResolveEffectiveCarrierConfig`.
4. Choose ePDG:
   - If `RuntimeEPDGOverride` is non-empty, use it and set `EPDGSource="redirect"`.
   - Otherwise use carrier profile ePDG and set `EPDGSource="carrier"`.
   - Fall back to standard 3GPP ePDG domain.
5. Try ISIM identity via `ReadISIMIdentity`.
6. If ISIM is available, set `ActualSource="isim"` and `AKAAppPreference="isim_strict"`.
7. Otherwise generate IMS identity from IMSI and set `ActualSource="generated"` and `AKAAppPreference="usim"`.

Generated identity:

```text
IMPI: {IMSI}@ims.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
IMPU: sip:{IMSI}@ims.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
Domain: ims.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
```

IKE permanent NAI:

```text
0{IMSI}@nai.epc.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
```

### 4.5 `runtimehost/carrier`

```go
package carrier

type LoadResult struct {
    Path    string
    Missing bool
    Count   int
}

type EffectiveCarrierConfigInput struct {
    MCC string
    MNC string
}

type Config struct {
    MCC      string
    MNC      string
    PresetID string
    EPDG     EPDGConfig
    IMS      IMSConfig
    SMS      SMSConfig
    E911     E911Config
    IKE      IKEConfig
}

type EPDGConfig struct {
    Host      string
    Port      int
    IPStack   string
    APN       string
    DNSServer string
}

type IMSConfig struct {
    Domain         string
    Realm          string
    Registrar      string
    PCSCF          string
    Transport      string
    LocalPort      int
    UserAgent      string
    IdentitySource string
}

type SMSConfig struct {
    ReceiverTransport string
}

type E911Config struct {
    Enabled            bool
    Provider           string
    EntitlementURL     string
    WebsheetHostPolicy string
}

type IKEConfig struct {
    IKEProposals []string
    ESPProposals []string
    IncludeEPDGIDr bool
}

func LoadCarrierOverrides(path string) (LoadResult, error)
func ClearCarrierOverrides()
func ResolveEffectiveCarrierConfig(input EffectiveCarrierConfigInput) Config
func IsVoWiFiBlockedMCC(mcc string) bool
func NewVoWiFiBlockedMCCError(mcc string) error
func IsVoWiFiPolicyBlockedError(err error) bool
```

Initial built-in profiles:

- Generic 3GPP fallback for any MCC/MNC.
- AT&T US `310/410`, used for the first E911 implementation.
- T-Mobile US `310/260`.
- EE UK `234/33`.
- Vodafone NL `204/04`.

Standard fallback domains:

```text
epdg.epc.mnc{MNC3}.mcc{MCC}.pub.3gppnetwork.org
ims.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
```

MNC should be left-padded to three digits for domain generation. PLMN matching must still preserve the real two-digit or three-digit MNC semantics.

### 4.6 `runtimehost/messaging`

```go
package messaging

import (
    "context"
    "errors"
    "time"
)

var ErrDeliveryNotFound = errors.New("delivery not found")

type SendOptions struct {
    Encoding string
}

type SendOutcome struct {
    MessageID     string
    PartsTotal    int
    DeliveryState string
}

type USSDResult struct {
    SessionID string `json:"session_id,omitempty"`
    Text      string `json:"text,omitempty"`
    Done      bool   `json:"done"`
    Raw       string `json:"raw,omitempty"`
}

type DeliveryPartMatch struct {
    MessageID string
    PartNo    int
    State     string
}

type DeliveryStatus struct {
    MessageID  string
    IMSI       string
    DeviceID   string
    Peer       string
    Content    string
    PartsTotal int
    Acks       int
    State      string
    LastError  string
    CreatedAt  time.Time
    UpdatedAt  time.Time
    Parts      []DeliveryPartStatus
}

type DeliveryPartStatus struct {
    PartNo      int
    CallID      string
    InReplyTo   string
    RPMR        int
    State       string
    SIPCode     int
    RPCause     int
    RPCauseText string
    ErrorText   string
    SentAt      time.Time
    ReportAt    time.Time
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type DeliveryStore interface {
    CreateSMSDelivery(messageID, imsi, deviceID, peer, content string, partsTotal int, at time.Time) error
    UpsertSMSDeliveryPart(messageID string, partNo int, callID string, rpMR int, state string, sentAt time.Time) error
    MarkSMSDeliveryPartReport(inReplyTo, callID, deviceID string, rpMR int, state string, sipCode int, rpCause int, errText string, at time.Time) (DeliveryPartMatch, error)
    RecomputeSMSDelivery(messageID string, at time.Time) error
    UpdateSMSDeliveryState(messageID, state, lastError string, acks int, at time.Time) error
    GetSMSDeliveryStatus(messageID string) (*DeliveryStatus, error)
}

func RPCauseText(cause int) string
func WithSuppressSendTGSuccess(ctx context.Context) context.Context
func SuppressSendTGSuccess(ctx context.Context) bool
```

### 4.7 `runtimehost/eventhost`

```go
package eventhost

import (
    "context"
    "time"
)

type Event interface{}

type Dispatcher interface {
    Dispatch(context.Context, Event)
}

type DispatcherFunc func(context.Context, Event)

func (f DispatcherFunc) Dispatch(ctx context.Context, e Event)

type SMSReceived struct {
    DevID   string
    Sender  string
    Content string
    Time    time.Time
}

type SMSSent struct {
    DevID     string
    TargetURI string
    Content   string
    Time      time.Time
}

type LocalNumberLearned struct {
    DevID  string
    IMSI   string
    Number string
}

type LogNotify struct {
    Message string
}
```

### 4.8 `runtimehost/e911`

```go
package e911

import (
    "context"
    "errors"

    swusim "github.com/iniwex5/vowifi-go/engine/sim"
    "github.com/iniwex5/vowifi-go/runtimehost/carrier"
)

var (
    ErrUnsupportedProvider     = errors.New("unsupported e911 provider")
    ErrChallengeNotImplemented = errors.New("e911 challenge not implemented")
    ErrWebsheetUnavailable     = errors.New("e911 websheet unavailable")
)

type Identity struct {
    IMSI        string
    IMEI        string
    MCC         string
    MNC         string
    SIPUsername string
    DisplayName string
}

type HeaderPair struct {
    Key   string
    Value string
}

type HTTPRequest struct {
    Method  string
    URL     string
    Headers []HeaderPair
    Body    []byte
}

type HTTPResponse struct {
    StatusCode int
    Body       []byte
}

type HTTPClient interface {
    Do(*HTTPRequest) (*HTTPResponse, error)
}

type TraceSink interface {
    Request(*HTTPRequest)
    Response(*HTTPRequest, *HTTPResponse)
    Error(*HTTPRequest, error)
}

type Request struct {
    Carrier     carrier.Config
    Identity    Identity
    AKAProvider swusim.AKAProvider
    Client      HTTPClient
    Trace       TraceSink
}

type WebsheetRequest struct {
    URL         string
    UserData    string
    ContentType string
    Title       string
}

func NewDefaultHTTPClient() HTTPClient
func StartEmergencyAddressUpdate(ctx context.Context, req Request) (WebsheetRequest, error)
```

Initial E911 scope:

- Support only `att` / `att_e911` providers.
- If provider is empty, return `ErrUnsupportedProvider`.
- If entitlement succeeds but no websheet URL exists, return `ErrWebsheetUnavailable`.
- If the flow requires an unsupported cellular challenge, return `ErrChallengeNotImplemented`.
- All requests and responses must pass through `TraceSink`.

### 4.9 `runtimehost/voicehost`

```go
package voicehost

import "context"

const DefaultSimulateCallHoldSeconds = 15
const MaxSimulateCallHoldSeconds = 120

type Gateway struct {}

func NewGateway() *Gateway
func (g *Gateway) Start(ctx context.Context) error
func (g *Gateway) GetAgent(deviceID string) any
func (g *Gateway) DeviceStatus(deviceID string) any
func (g *Gateway) SimulateCall(ctx context.Context, deviceID string, req SimulateCallRequest) (SimulateCallResult, error)

type SimulateCallRequest struct {
    Callee      string
    HoldSeconds int
    OnConnected func()
}

type SimulateCallResult struct {
    Success    bool  `json:"success"`
    DurationMs int64 `json:"duration_ms"`
}

type SDPInfo struct {
    ConnectionIP string
    MediaPort    int
}

func ParseSDP(body []byte) (SDPInfo, error)
```

`ParseSDP` must parse common SDP:

```text
c=IN IP4 192.0.2.10
m=audio 4000 RTP/AVP 0 8
```

## 5. Runtime Implementation Details

### 5.1 `runtimehost.Start`

`Start` should be a thin orchestrator:

1. Validate `DeviceID`, `SIM`, and `Prepared`.
2. Create an `Instance` with context and cancellation.
3. Store a redacted copy of `StartRequest`.
4. Initialize `service`, `state`, and `obs`.
5. Publish `PhaseStarting`.
6. Run `req.BeforeStart(ctx, SessionConfig{DataplaneMode: req.Dataplane.Mode})`.
7. Check `req.ShouldRun()`.
8. Run SIM and access gate.
9. Resolve ePDG.
10. Establish IKEv2/EAP-AKA.
11. Establish Child SA, ESP, and TUN.
12. Register IMS.
13. Bind SMS capability.
14. Return `Instance`.

Example state update:

```go
inst.setState(State{
    DeviceID:      req.DeviceID,
    Phase:         PhaseSIMReady,
    DataplaneMode: req.Dataplane.Mode,
    SIMReady:      true,
    AccessReady:   true,
    NetworkMode:   req.NetworkMode,
    UpdatedAt:     time.Now(),
})
```

Error mapping:

| Stage | `LastErrorClass` | `LastReason` |
| --- | --- | --- |
| profile / identity | `identity` | `identity_prepare_failed` |
| SIM AKA | `aka` | `sim_auth_failed` |
| ePDG DNS / UDP | `epdg` | `epdg_dns_failed` or `epdg_unreachable` |
| IKE | `ike` | `ike_auth_failed` |
| ESP / TUN | `dataplane` | `esp_or_tun_failed` |
| IMS | `ims` | `ims_register_failed` |
| SMS | `sms` | `sms_binding_failed` |
| proxy | `proxy` | returned by VoHive `BeforeStart` |

### 5.2 Carrier Profiles

Each profile should cover:

- PLMN: MCC, MNC, MNC length.
- ePDG host, port, IP stack, APN.
- IKE proposals.
- ESP proposals.
- IMS domain, realm, transport, and local port.
- IMS REGISTER header policy.
- SMS transport.
- E911 provider and entitlement URL.

Suggested Go structure:

```go
type Profile struct {
    ID string
    MCC string
    MNC string
    MNCLen int
    CountryISO2 string
    Brand string
    EPDG EPDGConfig
    IKE IKEConfig
    IMS IMSConfig
    SMS SMSConfig
    E911 E911Config
}
```

Fallback generation:

```text
epdg.epc.mnc{MNC3}.mcc{MCC}.pub.3gppnetwork.org
ims.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
```

### 5.3 ISIM and USIM Identity

`identity.ReadISIMIdentity(access any)` strategy:

1. If `access` exposes `GetISIMIdentity() (Identity, error)`, call it.
2. Otherwise, if APDU access is available:
   - Resolve ISIM AID.
   - Read EF_IMPI, EF_IMPU, and EF_DOMAIN.
   - Parse BER/TLV.
3. On failure, return error and let `PrepareStart` fall back to generated identity.

`PrepareStart` should never fail only because ISIM is unavailable, as long as IMSI/MCC/MNC are available.

### 5.4 AKA and EAP-AKA

The runtime receives AKA through VoHive:

```go
SIMAdapter.CalculateAKA(rand, autn)
```

The runtime should not know whether the underlying path is AT, QMI, MBIM Auth, or APDU. It only consumes:

- `RES`
- `CK`
- `IK`
- `AUTS`

EAP-AKA must support:

1. Identity request:
   - Parse AT_PERMANENT_ID_REQ, AT_FULLAUTH_ID_REQ, AT_ANY_ID_REQ.
   - Return permanent NAI.
2. Challenge:
   - Extract RAND, AUTN, AT_MAC, AT_RESULT_IND.
   - Call `SIM.CalculateAKA`.
   - On success, build AT_RES and AT_MAC.
   - On sync failure, build AT_AUTS response.
3. Notification:
   - Support post-challenge notification.
4. Key derivation:
   - Generate EAP-AKA key material for IKE_AUTH MSK usage.

Security:

- Do not serialize RAND, AUTN, RES, CK, IK, AUTS, MSK, or K_aut.
- Diagnostics should only record presence flags and byte lengths.

### 5.5 ePDG Resolution and Transport

ePDG stage:

1. Take host from `PreparedSession.EPDGAddr`.
2. If `ProxyConfig.Enabled`:
   - First version may return `proxy_not_implemented`.
   - Later version should implement SOCKS5 UDP ASSOCIATE.
3. DNS:
   - Try system resolver.
   - Fall back to nameservers from `/etc/resolv.conf` or public DNS.
4. UDP path:
   - Start with UDP 500.
   - Switch to UDP 4500 after NAT-T.
   - Keep NAT keepalive timer.

Failure states:

- DNS failure: `LastErrorClass="epdg"`, `LastReason="epdg_dns_failed"`.
- UDP unreachable: `LastErrorClass="epdg"`, `LastReason="epdg_unreachable"`.
- SOCKS5 UDP failure: `LastErrorClass="proxy"`, `LastReason="socks5_udp_associate_failed"`.

### 5.6 IKEv2 State Machine

Implement the minimal standard VoWiFi path:

```text
IKE_SA_INIT:
  SAi1 + KEi + Ni + NAT_DETECTION_SOURCE_IP + NAT_DETECTION_DESTINATION_IP

IKE_AUTH:
  IDi + CP + SAi2 + TSi + TSr + EAP-Only notify
  <- EAP Identity or EAP AKA Challenge
  -> EAP AKA Response
  <- EAP Success + AUTH + SAr2 + TSi + TSr + CP reply
```

Recommended package files:

- `codec.go`: IKE header and payload encode/decode.
- `payloads.go`: SA, KE, Nonce, ID, Notify, CP, TS, EAP, Encrypted.
- `keys.go`: DH, SKEYSEED, SK_d, SK_ai, SK_ar, SK_ei, SK_er, SK_pi, SK_pr.
- `state.go`: state machine.
- `retransmit.go`: retransmission policy.

Initial proposal set:

```text
IKE: aes128-sha256-prfsha256-modp2048
IKE: aes256-sha256-prfsha256-modp2048
IKE: aes128-sha1-prfsha1-modp1024
ESP: aes128-sha256
ESP: aes128-sha1
```

Expand this later by carrier profile.

### 5.7 Child SA, ESP, and TUN

Userspace dataplane flow:

1. Extract Child SA proposal, SPI values, TS values, assigned address, DNS, and P-CSCF from IKE_AUTH response.
2. Derive inbound and outbound ESP keys.
3. Create TUN:
   - Default name can be `vohive-vowifi0` or device-derived.
   - Configure inner address.
   - Configure MTU.
4. ESP loop:
   - TUN read -> ESP protect -> UDP 4500 send.
   - UDP 4500 receive -> ESP unprotect -> TUN write.
5. Anti-replay window.
6. NAT keepalive.

For minimum SMS recovery, the first implementation can focus on carrying IMS TCP/UDP traffic through the tunnel, then generalize the dataplane.

### 5.8 IMS Registration

Minimal IMS REGISTER flow:

1. Resolve P-CSCF:
   - IKE CP result first.
   - Carrier profile second.
   - DNS fallback third.
2. Create SIP transport:
   - TCP first.
   - UDP and TLS later.
3. Initial REGISTER:
   - From, To, Contact, Call-ID, CSeq, Expires.
   - P-Preferred-Identity.
   - P-Access-Network-Info: IEEE-802.11.
   - Supported: path, sec-agree, gruu.
   - Security-Client.
4. Handle 401/407:
   - Parse Digest AKA challenge.
   - Generate response from AKA material.
   - Send Security-Verify.
5. Handle 200 OK:
   - Extract Expires.
   - Save Service-Route.
   - Set `IMSReady=true`.
6. Refresh registration before expiry.
7. Mark runtime degraded and reconnect on transport failure.

Use a carrier-configurable register variant list, because real IMS cores differ in header strictness:

```go
type RegisterVariant struct {
    Label string
    IncludeRoute bool
    IncludeSecurityClient bool
    InitialAuthorization string
    IdentityFormat string
    PANIFormat string
    UserAgent string
}
```

Try variants until REGISTER 200 or a terminal rejection.

### 5.9 SMS over IMS

`Instance.Service().SendSMSWithOptions` should:

1. Verify IMS registration and SMS capability.
2. Split SMS according to encoding.
3. Build SIP MESSAGE or RP-DATA payload.
4. Persist delivery:
   - `CreateSMSDelivery`
   - `UpsertSMSDeliveryPart`
   - `MarkSMSDeliveryPartReport`
   - `RecomputeSMSDelivery`
5. On success, dispatch `eventhost.SMSSent`.
6. On failure, return partial `SendOutcome` and error.

Inbound SMS:

- Receive SIP MESSAGE or NOTIFY.
- Parse RP-DATA.
- Dispatch `eventhost.SMSReceived`.
- Binary or OTA payloads can be logged with `LogNotify` or ignored by VoHive's suppression policy.

### 5.10 USSD over IMS

Provide stable methods first:

```go
SendUSSD(ctx, command) (*messaging.USSDResult, error)
ContinueUSSD(ctx, sessionID, input) (*messaging.USSDResult, error)
CancelUSSD(ctx, sessionID) error
```

True USSD over IMS can be implemented later. In the first version:

- Return a clear `ussd_not_supported_over_ims` error if not supported.
- Do not panic.
- Do not block VoHive's CS-domain fallback logic.

### 5.11 E911

Initial target: AT&T-style entitlement and websheet.

Flow:

1. Require `carrier.Config.E911.Provider="att"` or equivalent.
2. Build entitlement request from `e911.Identity`.
3. Use provided `HTTPClient`.
4. Send trace events through `TraceSink`.
5. Parse websheet URL, user data, content type, and title.
6. Return `WebsheetRequest`.

Stable error mapping:

- Unsupported provider -> `ErrUnsupportedProvider`.
- Unsupported cellular challenge -> `ErrChallengeNotImplemented`.
- No websheet URL -> `ErrWebsheetUnavailable`.

### 5.12 Voicehost

Short-term goal is compatibility:

- `NewGateway`
- `Start`
- `DeviceStatus`
- `GetAgent`
- `SimulateCall`
- `ParseSDP`

The first version can be a state container plus SDP parser. Real VoWiFi voice agent and SIP dialog integration can be added later.

## 6. Minimal Recovery Milestones

### Milestone A: Compile Compatibility

Deliverables:

- Create `vowifi-go` module.
- Add all public packages.
- Implement all required types and zero-safe methods.
- `Start` may return a structured not-implemented error or dry-run instance.

Validation:

```bash
cd /opt/vohive-collection/vohive
GOCACHE=$PWD/.gocache GOMODCACHE=$PWD/.gomodcache go test ./internal/vowifihost ./internal/sim ./internal/device
```

Note: full `go test ./...` also requires generated `internal/web/dist`.

### Milestone B: Dry-run Runtime

Deliverables:

- `runtimehost.Start` returns an `Instance`.
- State progresses from SIM-ready to SMS-ready without touching network.
- `Obs()` exposes stage diagnostics.
- `SendSMSWithOptions` returns a clear dry-run or not-connected outcome.

Purpose:

- Web/API does not crash.
- VoHive lifecycle, state stream, restart, and eSIM switch recovery can be tested.

### Milestone C: Real SIM AKA

Deliverables:

- EAP-AKA parser and response builder.
- `SIMAdapter.CalculateAKA` integrated.
- AUTS sync failure support.
- ISIM/USIM preference respected.

Validation:

- Unit vectors for EAP-AKA.
- Real SIM challenge through at least one backend path.

### Milestone D: Real ePDG and IKE

Deliverables:

- ePDG DNS and UDP path.
- IKE_SA_INIT.
- IKE_AUTH with EAP-AKA.
- Child SA key derivation.

Validation:

- Reach EAP success and Child SA establishment.
- `TunnelReady=true`.

### Milestone E: ESP/TUN and IMS

Deliverables:

- Userspace ESP.
- TUN gateway.
- IMS REGISTER 200 OK.
- `IMSReady=true`.

### Milestone F: SMS

Deliverables:

- Real `SendSMSWithOptions`.
- Delivery store integration.
- Inbound SMS dispatch.
- `SMSReady=true`.

## 7. Testing Strategy

### Unit Tests

Required:

- `identity.NormalizeProfile`
- Standard ePDG/IMS domain generation.
- Carrier profile matching.
- EAP-AKA parse/build.
- AKA sync failure and AUTS.
- IKE payload encode/decode.
- Proposal parser.
- SIP REGISTER builder/parser.
- SDP parser.
- `Instance{}` zero-value safety.
- Idempotent `Stop`.
- Observer event delivery.

### Live Integration Tests

Use build tags:

```go
//go:build vowifi_live
```

Live tests:

- Real SIM AKA.
- ePDG DNS and UDP reachability.
- IKE SA establishment.
- IMS REGISTER.
- SMS send.

### VoHive Integration Tests

Recommended test targets:

```bash
go test ./internal/sim
go test ./internal/vowifihost
go test ./internal/device -run VoWiFi
go test ./internal/api -run VoWiFi
```

## 8. Main Risks

### Carrier Differences

Use carrier profiles and register variants. Do not hard-code carrier behavior into the protocol state machine.

### APDU and AKA Concurrency

VoHive already owns APDU arbitration. The runtime should avoid long-lived logical-channel ownership. Each challenge should use a short lease, short timeout, and immediate cleanup.

### eSIM Switch Recovery

`Start` must respect `ShouldRun`. `Stop` must cancel IKE, ESP, TUN, IMS, and SMS goroutines quickly. Every goroutine must be rooted in the instance context.

### Sensitive Logs

`Obs()` should expose profile ID, stage, proposal names, status, and error classes only. It must not expose raw IMSI or secret material.

### Upstream Proxy

VoHive already checks SOCKS5 UDP ASSOCIATE in `BeforeStart`. The first runtime version may reject enabled proxy with `proxy_not_implemented`; later versions should implement SOCKS5 UDP relay.

## 9. First Week Task Plan

### Day 1

- Create `/opt/vohive-collection/vowifi-go/go.mod`.
- Create all public packages.
- Add compatibility types.
- Add zero-safe `Instance`.
- Make `go test ./internal/vowifihost` compile.

### Day 2

- Implement carrier fallback.
- Implement identity normalization.
- Implement `PrepareStart`.
- Implement interface-based `ReadISIMIdentity` fallback.
- Run VoHive identity-related VoWiFi tests.

### Day 3

- Implement `Instance` state machine.
- Implement observers.
- Implement dry-run `Start`.
- Implement `messaging` and `eventhost`.
- Make Web/API runtime DTOs show state.

### Day 4 and Day 5

- Implement EAP-AKA packet parser and builder.
- Connect `SIMAdapter.CalculateAKA`.
- Add AUTS handling.
- Add test vectors.

### Day 6 and Day 7

- Implement IKE codec and state skeleton.
- Implement IKE_SA_INIT encode/decode.
- Implement proposal negotiation.
- Implement ePDG DNS and UDP transport plan.

## 10. Suggested Commit Sequence

First batch:

```text
vowifi-go: scaffold public runtimehost API
vowifi-go: add carrier and identity prepare pipeline
vowifi-go: add zero-safe runtime instance and observer state
vowifi-go: add messaging and eventhost compatibility models
vohive: document vowifi-go rewrite plan
```

Second batch:

```text
vowifi-go: implement EAP-AKA parser and response builder
vowifi-go: connect SIMAdapter AKA into runtime auth stage
vowifi-go: add ePDG resolver and UDP transport plan
```

Third batch:

```text
vowifi-go: implement IKE_SA_INIT and IKE_AUTH state machine
vowifi-go: implement userspace ESP and TUN gateway
vowifi-go: implement IMS REGISTER and SMS over IMS
```

## 11. Acceptance Criteria

Minimum recovery:

- VoHive compiles.
- VoWiFi enable/disable/reconnect APIs no longer fail due to missing module.
- Runtime state can show startup failure reasons.
- Disable, reconnect, and eSIM-switch recovery do not leak goroutines.
- APDU operations do not get stuck.

Functional recovery:

- At least one backend path can perform SIM AKA.
- ePDG IKE/ESP tunnel can be established.
- IMS REGISTER succeeds.
- SMS over IMS can send and receive.
- E911 websheet works for supported carriers.

Full recovery:

- Multiple carrier profiles.
- SOCKS5 UDP upstream proxy support.
- USSD over IMS.
- Inbound SMS delivery reports.
- Voice gateway/agent.
- Automatic reconnect, keepalive, DPD, rekey, and re-register.

