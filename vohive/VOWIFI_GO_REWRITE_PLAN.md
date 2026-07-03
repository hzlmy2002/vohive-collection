# vowifi-go Go 重写计划

本文档目标：从零重写 `github.com/iniwex5/vowifi-go`，恢复当前 VoHive 对 VoWiFi 的编译兼容与运行能力。实现语言仍为 Go。`/opt/SimAdmin` 的 Rust VoWiFi 实现可作为架构和协议阶段参考，但最终产物应是一个独立 Go module，并保持 VoHive 现有 Go 层接口兼容。

## 1. 当前结论

当前 VoHive 主仓库没有缺少宿主层代码。缺失的是 sibling module：

```text
/opt/vohive-collection/vowifi-go
module github.com/iniwex5/vowifi-go
```

VoHive 的 `go.work` 已经引用了这个目录：

```text
use (
    .
    ../quectel-qmi-go
    ../vowifi-go
)
```

所以恢复路径应该优先在 `/opt/vohive-collection/vowifi-go` 新建 Go module，而不是先改 VoHive import。

当前 VoHive 负责：

- 设备发现、生命周期、QMI/MBIM/AT backend。
- SIM/eSIM 管理、APDU arbitration、AKA provider 适配。
- VoWiFi 启停编排、策略、Web/API、短信历史落库、通知、E911 websheet broker。
- 前置代理配置和 eSIM 切卡后的 VoWiFi 恢复调度。

缺失的 `vowifi-go` 应负责：

- 运营商 VoWiFi profile、ePDG/IMS 域名和策略。
- SIM AKA/EAP-AKA 与 IKEv2 的衔接。
- ePDG IKEv2/IPsec/ESP 用户态数据面。
- IMS SIP REGISTER、SMS over IMS、USSD over IMS。
- E911 entitlement/websheet provider。
- 对 VoHive 暴露稳定 `runtimehost` API。

## 2. 参考 SimAdmin 的方式

SimAdmin 的 `backend/src/vowifi/` 模块划分可以直接借鉴为 Go 的阶段模型：

| SimAdmin 阶段 | Go 重写模块建议 | VoHive 状态映射 |
| --- | --- | --- |
| `identity` | `runtimehost/identity` | `SIMReady` 前置 |
| `profiles` | `runtimehost/carrier` | `PreparedSession.EffectiveCarrier` |
| `aka` / `qmi_uim` | `engine/sim` + runtime SIM adapter | `SIMReady`, `AccessReady` |
| `epdg` / `transport` | `internal/epdg` | `TunnelReady` 前置 |
| `ike_*` / `eap_aka` | `internal/ike`, `internal/eapaka` | `TunnelReady` |
| `dataplane` / `tun_gateway` | `internal/ipsec`, `internal/tun` | `TunnelReady` |
| `ims` | `internal/ims` | `IMSReady` |
| `sms` | `runtimehost/messaging` + `internal/ims/sms` | `SMSReady` |
| `restore` / `stability` | `runtimehost` reconnect/retry | `LastReason`, `LastErrorClass` |
| `diagnostics` / `flow` | `runtimehost.Instance.Obs()` | Web 状态展示 |

关键设计原则：

1. 不把所有逻辑塞进 `runtimehost.Start`。`Start` 只编排阶段和管理实例生命周期。
2. 每个阶段都产生结构化状态，最终汇总为 `runtimehost.State` 和 `Instance.Obs()`。
3. 真实协议可以逐步补齐，但 public API、类型字段和状态字段要第一天稳定。
4. 敏感材料永不进入日志/Obs：IMSI、RAND、AUTN、RES、CK、IK、K_aut、MSK、ESP key 等只允许本地内存使用。

## 3. 目录结构建议

在 `/opt/vohive-collection/vowifi-go` 新建：

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

`runtimehost/*` 是 VoHive 依赖的稳定 API。`internal/*` 是真实协议实现，后续可以重构，不影响 VoHive。

## 4. Go 层接口兼容清单

下面是当前 VoHive 源码实际依赖的兼容面。第一阶段必须完整实现这些包路径、类型、字段、函数和方法。

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

要求：

- `ErrSyncFailure` 必须可被 `errors.Is` 判断。
- `AKAResult.AUTS` 非空且返回 `ErrSyncFailure` 时，EAP-AKA 层必须发 sync failure response。
- 不在 `String()` 或日志中输出 AKA secret。

### 4.2 `engine/swu`

```go
package swu

const DataplaneModeUserspace = "userspace"
```

当前 VoHive 只使用这个常量。

### 4.3 `runtimehost`

核心类型：

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
    PhaseStarting  Phase = "starting"
    PhaseSIMReady  Phase = "sim_ready"
    PhaseTunnel    Phase = "tunnel_ready"
    PhaseIMSReady  Phase = "ims_ready"
    PhaseSMSReady  Phase = "sms_ready"
    PhaseFailed    Phase = "failed"
    PhaseStopped   Phase = "stopped"
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

Modem/SIM 适配：

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

Instance/API：

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
    // 零值必须安全。
}

func Start(ctx context.Context, req StartRequest) (*Instance, error)
func (i *Instance) Stop(ctx context.Context) error
func (i *Instance) State() State
func (i *Instance) Status() string
func (i *Instance) Obs() map[string]interface{}
func (i *Instance) Service() IMSService
func (i *Instance) AddObserver(observer Observer)
```

Trace/log：

```go
func NewTraceID() string
func WithTraceID(ctx context.Context, traceID string) context.Context
func TraceIDFromContext(ctx context.Context) string
func SetLogger(logger any)
```

实现要求：

- `Instance{}` 零值方法不 panic。
- `AddObserver` 在状态更新时收到 `Event{State: ...}`。
- `Stop` 必须幂等。
- `Start` 必须在每个耗时阶段检查 `req.ShouldRun()` 和 `ctx.Err()`。
- `BeforeStart` 应在真实网络连接前调用，用于 VoHive 的前置代理自检和状态刷新。

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
    IMSIdentitySourceISIM       = "isim"
    IMSIdentitySourceUSIM       = "usim"
    IMSIdentitySourceGenerated  = "generated"
    AKAAppPreferenceAuto        = "auto"
    AKAAppPreferenceISIM        = "isim"
    AKAAppPreferenceISIMStrict  = "isim_strict"
    AKAAppPreferenceUSIM        = "usim"
)

func NormalizeProfile(p Profile) Profile
func PrepareStart(input PrepareStartInput) (PreparedSession, error)
func ReadISIMIdentity(access any) (Identity, error)
```

`PrepareStart` 逻辑：

1. 规范化 IMSI/MCC/MNC/IMEI/SMSC。
2. 如果 MCC/MNC 缺失，尝试从 IMSI 推导。
3. 调 `carrier.ResolveEffectiveCarrierConfig`。
4. 计算 ePDG：
   - 若 `RuntimeEPDGOverride` 非空，`EPDGAddr=override`, `EPDGSource="redirect"`。
   - 否则使用 carrier profile 的 ePDG host，`EPDGSource="carrier"`。
   - 最后 fallback 到标准 3GPP 域名。
5. 尝试通过 `ReadISIMIdentity` 读取 IMPI/IMPU。
6. 如果 ISIM identity 可用，`IMSIdentity.ActualSource="isim"`，`AKAAppPreference="isim_strict"`。
7. 否则生成 `IMSI` based identity，`ActualSource="generated"`，`AKAAppPreference="usim"`。

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
    Domain        string
    Realm         string
    Registrar     string
    PCSCF         string
    Transport     string
    LocalPort     int
    UserAgent     string
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

第一版内置 profile：

- 通用 3GPP fallback：任何 MCC/MNC 都生成标准 ePDG/IMS 域名。
- AT&T US：`310/410`，用于 E911 初版。
- T-Mobile US：`310/260`。
- EE UK：`234/33`，可按 SimAdmin profile 结构建模。
- Vodafone NL：`204/04`。

注意 VoHive 现在显式对部分 MCC 做策略阻断，例如中国大陆 MCC 可能被认为不适合启动 VoWiFi。保持 `NewVoWiFiBlockedMCCError` 可被 `IsVoWiFiPolicyBlockedError` 识别。

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
    MessageID      string
    PartsTotal     int
    DeliveryState  string
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
    ErrUnsupportedProvider      = errors.New("unsupported e911 provider")
    ErrChallengeNotImplemented  = errors.New("e911 challenge not implemented")
    ErrWebsheetUnavailable      = errors.New("e911 websheet unavailable")
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

第一版实现：

- 只支持 provider `att` / `att_e911`。
- 如果 provider 为空：返回 `ErrUnsupportedProvider`。
- 如果 entitlement 成功但没有 websheet URL：返回 `ErrWebsheetUnavailable`。
- 如果遇到需要蜂窝侧不可复现 challenge：返回 `ErrChallengeNotImplemented`。

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

`ParseSDP` 当前被 `internal/cscall` 使用，必须能解析常见：

```text
c=IN IP4 192.0.2.10
m=audio 4000 RTP/AVP 0 8
```

## 5. 真实实现细节

### 5.1 `runtimehost.Start` 编排

`Start` 应做这些步骤：

1. 校验 `DeviceID`、`SIM`、`Prepared`。
2. 初始化 `Instance`：
   - 持有 `context.Context` 和 cancel。
   - 保存 `StartRequest` 精简副本。
   - 初始化 `service`、`state`、`obs`。
3. 发布 `PhaseStarting`。
4. 调 `req.BeforeStart(ctx, SessionConfig{DataplaneMode: req.Dataplane.Mode})`。
5. 检查 `req.ShouldRun()`。
6. SIM/Access gate：
   - `req.SIM.CalculateAKA` 不应在这里立刻消耗真实 challenge。
   - 但可以做 SIM adapter presence check。
7. ePDG resolve。
8. IKEv2/EAP-AKA 建隧道。
9. 建立 Child SA/ESP/TUN。
10. IMS REGISTER。
11. SMS capability bind。
12. 返回 `Instance`。

状态更新：

```go
inst.setState(State{
    DeviceID: req.DeviceID,
    Phase: PhaseSIMReady,
    DataplaneMode: req.Dataplane.Mode,
    SIMReady: true,
    AccessReady: true,
    NetworkMode: req.NetworkMode,
    UpdatedAt: time.Now(),
})
```

失败映射：

| 阶段 | LastErrorClass | LastReason |
| --- | --- | --- |
| profile/identity | `identity` | `identity_prepare_failed` |
| SIM AKA | `aka` | `sim_auth_failed` |
| ePDG DNS/UDP | `epdg` | `epdg_dns_failed` / `epdg_unreachable` |
| IKE | `ike` | `ike_auth_failed` |
| ESP/TUN | `dataplane` | `esp_or_tun_failed` |
| IMS | `ims` | `ims_register_failed` |
| SMS | `sms` | `sms_binding_failed` |
| proxy | `proxy` | 由 VoHive `BeforeStart` 返回 |

### 5.2 运营商 Profile

Profile 需要覆盖：

- PLMN：MCC/MNC/MNC 长度。
- ePDG host/port/ip stack/APN。
- IKE proposal set。
- ESP proposal set。
- IMS domain/realm/transport/local port。
- IMS REGISTER header policy。
- SMS receiver transport。
- E911 provider/URL。

Go 结构建议：

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

Fallback 规则：

```text
epdg.epc.mnc{MNC3}.mcc{MCC}.pub.3gppnetwork.org
ims.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
```

其中 MNC 需要按 3 位左补零生成域名，但 PLMN 匹配仍保留真实 2/3 位 MNC。

### 5.3 ISIM/USIM 身份

`identity.ReadISIMIdentity(access any)` 的实际策略：

1. 如果 `access` 暴露 `GetISIMIdentity() (Identity,error)`，直接调用。
2. 否则如果能 APDU：
   - 解析 ISIM AID。
   - 读取 EF_IMPI、EF_IMPU、EF_DOMAIN。
   - 解析 TLV/BER。
3. 失败时返回 error，由 `PrepareStart` fallback 到 generated identity。

生成身份：

```text
IMPI: {IMSI}@ims.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
IMPU: sip:{IMSI}@ims.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
Domain: ims.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
```

IKE identity 通常使用 permanent NAI：

```text
0{IMSI}@nai.epc.mnc{MNC3}.mcc{MCC}.3gppnetwork.org
```

### 5.4 AKA 与 EAP-AKA

SIM AKA 入口由 VoHive 注入：

```go
SIMAdapter.CalculateAKA(rand, autn)
```

runtime 内部不关心 AT/QMI/MBIM 细节，只关心返回：

- `RES`
- `CK`
- `IK`
- `AUTS`

EAP-AKA 实现要支持：

1. Identity request：
   - 解析 AT_PERMANENT_ID_REQ / AT_FULLAUTH_ID_REQ / AT_ANY_ID_REQ。
   - 响应 permanent NAI。
2. Challenge：
   - 提取 RAND/AUTN/AT_MAC/AT_RESULT_IND。
   - 调 `SIM.CalculateAKA`。
   - 成功：用 RES/CK/IK 生成 AT_RES + AT_MAC。
   - 同步失败：用 AUTS 生成 sync failure response。
3. Notification：
   - 支持 challenge 后 notification。
4. Key derivation：
   - 生成 EAP-AKA key material，供 IKE_AUTH 使用 MSK。

安全要求：

- 不序列化 RAND/AUTN/RES/CK/IK/AUTS。
- 调试状态只记录长度、是否存在、错误分类。

### 5.5 ePDG 解析和传输

ePDG 阶段：

1. 从 `PreparedSession.EPDGAddr` 取 host。
2. 如果 `ProxyConfig.Enabled`：
   - 第一版可以只支持直连，遇到 proxy 返回明确错误。
   - 后续实现 SOCKS5 UDP ASSOCIATE。
3. DNS 解析：
   - 先系统 resolver。
   - 失败后可 fallback 到 `/etc/resolv.conf` nameserver 或公共 DNS。
4. UDP 路径：
   - IKE 初始用 UDP 500。
   - NAT-T 后用 UDP 4500。
   - 保留 NAT keepalive。

状态：

- DNS 成功但 UDP 不通：`LastErrorClass="epdg"`, `LastReason="epdg_unreachable"`。
- proxy 不支持 UDP：`LastErrorClass="proxy"`, `LastReason="socks5_udp_associate_failed"`。

### 5.6 IKEv2 状态机

建议先实现最小标准路径：

```text
IKE_SA_INIT:
  SAi1 + KEi + Ni + NAT_DETECTION_SOURCE_IP + NAT_DETECTION_DESTINATION_IP

IKE_AUTH:
  IDi + CERTREQ? + CP + SAi2 + TSi + TSr + EAP-Only notify
  <- EAP Identity / EAP AKA Challenge
  -> EAP AKA Response
  <- EAP Success + AUTH + SAr2 + TSi + TSr + CP reply
```

模块拆分：

- `codec.go`：IKE header/payload encode/decode。
- `payloads.go`：SA/KE/Nonce/ID/Notify/CP/TS/EAP/Encrypted。
- `keys.go`：DH、SKEYSEED、SK_d/SK_ai/SK_ar/SK_ei/SK_er/SK_pi/SK_pr。
- `state.go`：状态机。
- `retransmit.go`：重传计时。

Proposal 初版：

```text
IKE: aes128-sha256-prfsha256-modp2048
IKE: aes256-sha256-prfsha256-modp2048
IKE: aes128-sha1-prfsha1-modp1024
ESP: aes128-sha256
ESP: aes128-sha1
```

后续按 carrier profile 扩展。

### 5.7 Child SA / ESP / TUN

目标是 userspace dataplane：

1. 从 IKE_AUTH response 提取 Child SA proposal、SPI、TS、CP assigned address、DNS、P-CSCF。
2. 派生 inbound/outbound ESP keys。
3. 创建 TUN：
   - 默认名可 `vohive-vowifi0` 或按 device id 派生。
   - 配置 inner address。
   - 配置 MTU。
4. 用户态 ESP：
   - TUN read -> ESP protect -> UDP 4500 send。
   - UDP 4500 recv -> ESP unprotect -> TUN write。
5. Anti-replay window。
6. NAT keepalive。

如果第一版只恢复 SMS over IMS，也仍然需要能把 IMS TCP/UDP 流量送进 tunnel。可先内置一个 minimal userspace TCP/UDP gateway，后续再通用化。

### 5.8 IMS 注册

IMS 阶段最小流程：

1. 解析 P-CSCF：
   - 优先 IKE CP 返回。
   - 其次 carrier profile。
   - 再次 DNS fallback。
2. 建 SIP transport：
   - 初版 TCP。
   - 后续 UDP/TLS。
3. 初始 REGISTER：
   - From/To/Contact/Call-ID/CSeq/Expires。
   - P-Preferred-Identity。
   - P-Access-Network-Info: IEEE-802.11。
   - Supported: path, sec-agree, gruu。
   - Security-Client。
4. 处理 401/407：
   - Digest AKA challenge。
   - 使用 AKA material 生成 response。
   - Security-Verify。
5. 处理 200 OK：
   - 提取 Expires。
   - 保存 Service-Route。
   - 标记 `IMSReady=true`。
6. 注册保活：
   - 到期前刷新 REGISTER。
   - 网络失败触发 runtime degraded/reconnect。

实现上可以先提供多 header variant 尝试机制，参考 SimAdmin 的思想：不同运营商对 Security-Client、Route、PANI、User-Agent 很敏感。Go 里可以做：

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

按 carrier profile 定义候选列表，直到 REGISTER 200 或遇到不可重试拒绝。

### 5.9 SMS over IMS

`Instance.Service().SendSMSWithOptions` 应：

1. 确认 IMS registered。
2. 按编码拆分 SMS：
   - UTF-8/GSM-7/UCS2 由 VoHive 已经传入 encoding，runtime 可以先尊重 options。
3. 生成 MESSAGE 或 SIP MESSAGE carrying RP-DATA。
4. 记录 delivery：
   - `CreateSMSDelivery`
   - `UpsertSMSDeliveryPart`
   - SIP response/report 回来后 `MarkSMSDeliveryPartReport`
   - `RecomputeSMSDelivery`
5. 成功 dispatch：
   - `eventhost.SMSSent`
6. 失败返回 `SendOutcome` + error。

入站 SMS：

- SIP MESSAGE/NOTIFY 接收后解析。
- dispatch `eventhost.SMSReceived`。
- 对二进制/OTA 包给 `LogNotify` 或静默过滤策略交给 VoHive。

### 5.10 USSD over IMS

先实现接口壳：

```go
SendUSSD(ctx, command) (*messaging.USSDResult, error)
ContinueUSSD(ctx, sessionID, input) (*messaging.USSDResult, error)
CancelUSSD(ctx, sessionID) error
```

真实实现可放 P6 后半段。不同运营商 USSD over IMS 支持差异大，第一版可：

- 如果 IMS service 未支持 USSD，返回明确 `ussd_not_supported_over_ims`。
- 不要 panic，也不要阻塞 VoHive 的 CS 回退逻辑。

### 5.11 E911

优先只支持 AT&T：

1. `carrier.Config.E911.Provider="att"`。
2. `StartEmergencyAddressUpdate` 构造 entitlement 请求。
3. 通过 `HTTPClient` 发送，所有请求/响应走 `TraceSink`。
4. 解析 websheet URL/userData/contentType。
5. 返回 `WebsheetRequest` 给 VoHive `websheet.Broker`。

错误映射必须稳定：

- provider 不支持：`ErrUnsupportedProvider`
- 需要暂未实现的蜂窝 challenge：`ErrChallengeNotImplemented`
- 没有 websheet：`ErrWebsheetUnavailable`

### 5.12 Voicehost

短期目标是兼容：

- `NewGateway`
- `Start`
- `DeviceStatus`
- `GetAgent`
- `SimulateCall`
- `ParseSDP`

第一版可以只做状态容器和 SDP parser；真实 VoWiFi voice agent 可以后续接 IMS dialog。

## 6. 最小恢复路径

如果目标是“用最小代价恢复功能”，推荐按下面顺序执行。

### Milestone A：可编译兼容

产出：

- 新建 `vowifi-go` module。
- 实现所有 public 包和类型。
- `Start` 返回明确错误或 dry-run instance。
- VoHive `go test` 能开始编译到业务测试。

验收：

```bash
cd /opt/vohive-collection/vohive
GOCACHE=$PWD/.gocache GOMODCACHE=$PWD/.gomodcache go test ./internal/vowifihost ./internal/sim ./internal/device
```

注意：VoHive 还需要先生成 `internal/web/dist` 才能全仓库 `go test ./...`。

### Milestone B：dry-run runtime

产出：

- `runtimehost.Start` 返回 `Instance`。
- `Instance.State()` 能从 SIMReady 走到 SMSReady，但不触网。
- `Obs()` 输出阶段诊断。
- `SendSMSWithOptions` 返回 `not connected` 或 dry-run outcome。

用途：

- Web/API 不崩。
- VoHive 的生命周期、切卡恢复、状态 stream 可以验证。

### Milestone C：真实 SIM AKA

产出：

- EAP-AKA challenge parser。
- `SIM.CalculateAKA` 接入。
- AUTS sync failure 处理。
- ISIM/USIM preference 生效。

验收：

- 用测试向量验证 EAP-AKA packet。
- 用真实卡验证 QMI/MBIM/AT backend 都能返回 AKA result 或明确错误。

### Milestone D：真实 ePDG + IKE

产出：

- ePDG DNS/UDP。
- IKE_SA_INIT。
- IKE_AUTH + EAP-AKA。
- Child SA keys。

验收：

- 能完成 IKE_AUTH 至 EAP success/Child SA。
- 状态变为 `TunnelReady=true`。

### Milestone E：ESP/TUN + IMS

产出：

- userspace ESP。
- TUN gateway。
- IMS REGISTER 200。
- `IMSReady=true`。

### Milestone F：SMS

产出：

- `SendSMSWithOptions` 真实发送。
- delivery store 全链路。
- 入站 SMS dispatch。
- `SMSReady=true`。

## 7. 测试策略

### 单元测试

必须覆盖：

- `identity.NormalizeProfile`
- standard ePDG/IMS domain generation
- carrier profile matching
- EAP-AKA parse/build
- AKA sync failure AUTS
- IKE payload encode/decode
- proposal parser
- SIP REGISTER builder/parser
- SDP parser
- `Instance{}` 零值安全
- `Stop` 幂等
- observer delivery

### 集成测试

建议加 build tag：

```text
//go:build vowifi_live
```

覆盖：

- 真实 SIM AKA。
- ePDG DNS/UDP。
- IKE SA 建立。
- IMS REGISTER。
- SMS send。

### 与 VoHive 联调

关键测试包：

```bash
go test ./internal/sim
go test ./internal/vowifihost
go test ./internal/device -run VoWiFi
go test ./internal/api -run VoWiFi
```

## 8. 风险和处理

1. **运营商差异**
   - 用 carrier profile + register variant 机制，不把策略写死在协议状态机里。

2. **APDU/AKA 并发**
   - VoHive 已有 APDU arbiter，runtime 不要私自长期占用 logical channel。
   - 每次 challenge 尽量短租借、短超时、结束即释放。

3. **eSIM 切卡恢复**
   - `Start` 必须支持 `ShouldRun`。
   - `Stop` 必须快速取消 IKE/ESP/TUN goroutine。
   - 所有 goroutine 都挂在 instance context 下。

4. **敏感日志**
   - `Obs()` 只暴露 profile id、阶段、proposal 名、状态，不暴露密钥和完整 IMSI。

5. **前置代理**
   - VoHive 已在 `BeforeStart` 做 SOCKS5 UDP ASSOCIATE 自检。
   - runtime 初版可以先直连；如果 `ProxyConfig.Enabled` 则返回 `proxy_not_implemented`。
   - 后续实现 SOCKS5 UDP relay。

## 9. 第一周具体任务拆分

### Day 1

- 新建 `/opt/vohive-collection/vowifi-go/go.mod`。
- 创建所有 public 包。
- 补齐类型和零值安全方法。
- `go test ./internal/vowifihost` 编译通过。

### Day 2

- 实现 carrier/identity/profile fallback。
- 实现 `PrepareStart`。
- 实现 `ReadISIMIdentity` 的接口调用路径和 fallback。
- 跑 VoHive `internal/device` VoWiFi identity 相关测试。

### Day 3

- 实现 `Instance` 状态机、observer、dry-run Start。
- 实现 messaging/eventhost。
- Web/API 状态 DTO 能显示 runtime state。

### Day 4-5

- 实现 EAP-AKA packet 和 SIM AKA adapter。
- 从 VoHive backend 真实调用 AKA。
- 加测试向量和错误映射。

### Day 6-7

- 开始 IKE codec/state machine。
- 先完成 IKE_SA_INIT encode/decode 和 proposal negotiation。
- 做 ePDG DNS/UDP path。

## 10. 推荐先提交的最小代码骨架

第一批 commit：

```text
vowifi-go: scaffold public runtimehost API
vowifi-go: add carrier and identity prepare pipeline
vowifi-go: add zero-safe runtime instance and observer state
vowifi-go: add messaging/eventhost compatibility models
vohive: document vowifi-go rewrite plan
```

第二批 commit：

```text
vowifi-go: implement EAP-AKA parser and response builder
vowifi-go: connect SIMAdapter AKA into runtime auth stage
vowifi-go: add ePDG resolver and UDP transport plan
```

第三批 commit：

```text
vowifi-go: implement IKE_SA_INIT and IKE_AUTH state machine
vowifi-go: implement userspace ESP and TUN gateway
vowifi-go: implement IMS REGISTER and SMS over IMS
```

## 11. 最终验收标准

最低可接受：

- VoHive 编译通过。
- VoWiFi 开关 API 不报缺包。
- Runtime state 能显示启动失败原因。
- 关闭/重连/切卡恢复不会泄露 goroutine 或卡住 APDU。

功能恢复：

- QMI/MBIM/AT 后端至少一种能完成 AKA。
- 能连 ePDG 并建立 IKE/ESP。
- IMS REGISTER 成功。
- SMS over IMS 可收发。
- E911 websheet 对支持运营商可用。

完整恢复：

- 多运营商 profile。
- SOCKS5 UDP 前置代理。
- USSD over IMS。
- 入站短信 delivery/report。
- 语音 gateway/agent。
- 自动重连、保活、DPD、rekey、re-register。

