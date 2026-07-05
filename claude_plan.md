# vowifi-go Reconstruction Plan

## Context

The `github.com/iniwex5/vowifi-go` v1.1.2 module was deleted from GitHub. VoHive depends on it for all VoWiFi functionality. The binary `vohive_v1.5.2_linux_amd64` (built by GitHub Actions) contains the compiled code. We have:

1. **IDA Pro binary** — reveals the real package structure and all function names (2,200+ functions)
2. **SimAdmin Rust impl** (`/opt/SimAdmin/backend/src/vowifi/`) — 28K lines covering IKEv2, EAP-AKA, IMS SIP, SMS, carrier profiles
3. **swu-go** (`/opt/vohive-collection/swu-go/`) — existing Go IKEv2/IPsec engine (full source)
4. **Existing rewrite plan** (`VOWIFI_GO_REWRITE_PLAN.md`) — detailed API spec and milestone roadmap
5. **VoHive consumer code** — shows exact call sites and interface contracts

## Real Package Structure (from IDA binary)

The actual module structure differs from what the plan assumed:

```
github.com/iniwex5/vowifi-go/
├── engine/
│   ├── crypto/         # DH, AES-CBC/GCM, 3DES, HMAC-SHA1/256/512, PRF
│   ├── sim/            # AKAProvider, ISIMAKAProvider, AKAResult, ErrSyncFailure
│   └── swu/            # DataplaneModeUserspace constant
├── internal/vowifi/
│   ├── common/         # TraceID, Plmn3, IsIPv6, HostHasIP, RandomHex, ToStrings
│   ├── policy/         # CarrierPlan, EffectiveCarrierConfig, presets, overrides, IMS register templates
│   ├── runtimecore/    # Runtime.Start, RunLoop, RunSession, PrepareSessionStart, BuildSWUConfig, StartAndWaitEPDG, AKA adapters
│   ├── imscore/        # Service (register, dialogs, SMS fragments, AKA digest, PANI, delivery)
│   ├── imsheaders/     # SIP Contact/P-Preferred-Identity/Route/SecAgree headers
│   ├── sipkit/         # SIP request/response building, URI parsing, transport, Via routing
│   ├── imsendpoint/    # ClientInviteResult, Event types
│   ├── smsdelivery/    # SendOutcome, DeliveryPartStatus, DeliveryPartMatch
│   ├── entitlement/
│   │   ├── ts43/       # TS.43 entitlement protocol (auth, challenge, subscriber ID, NAI)
│   │   └── providers/
│   │       └── att/    # AT&T E911 address update (EAP-AKA + PhoneServicesAction)
│   └── voice/
│       └── media/      # RTP relay, comfort noise, PCAP capture, RTCP keepalive, DSCP
├── runtimehost/        # Instance, Start, ObserverFunc, adapters (SIM, modem, delivery, event)
│   ├── identity/       # PrepareStart, ReadISIMIdentity, Profile, PreparedSession
│   ├── carrier/        # ResolveEffectiveCarrierConfig, LoadCarrierOverrides
│   ├── messaging/      # ServiceStatus, SendOutcome, USSDResult, DeliveryPartStatus
│   ├── voicehost/      # Gateway (SimulateCall, HandleClient*, ParseSDP, StartPCAP)
│   ├── eventhost/      # SMSReceived, SMSSent, LocalNumberLearned, LogNotify, Generic
│   └── e911/           # StartEmergencyAddressUpdate, NewDefaultHTTPClient, startATT, entitlementHTTPClientAdapter
```

## Key Differences from Existing Plan

1. **No `internal/swuadapter` package** — the binary uses `internal/vowifi/runtimecore.BuildSWUConfig` directly
2. **`internal/vowifi/policy`** is the real carrier layer — runtimehost/carrier is a thin public facade
3. **Voice media is fully implemented** — RTP relay, comfort noise generator, PCAP, not just a stub gateway
4. **IMS is in `internal/vowifi/imscore`** with separate `sipkit` and `imsheaders` packages
5. **`engine/crypto`** is a full IKE-grade crypto package (DH, CBC, GCM, HMAC, PRF) — likely shared with swu-go or duplicated

---

## Entitlement Architecture (from binary analysis)

### E911 Dispatch Flow

`runtimehost/e911.StartEmergencyAddressUpdate` is the entry point. It dispatches based on the `provider` field in the carrier preset's `e911` config:

```
StartEmergencyAddressUpdate(provider, entitlement_url, websheet_host_policy, ...)
├─ provider == "att_entitlement"
│   └─► startATT (via function pointer table at 0x3205e18)
│       ├─ TS.43 EAP-AKA authentication (BuildAuthAction, BuildChallengePayload)
│       ├─ Parse PhoneServicesAction response
│       └─ Optional websheet if status requires address update
│
└─ provider == "T-Mobile_entitlement" (or any non-ATT provider)
    └─► vohive/internal/e911.(*Coordinator).StartWebsheet (NOT in vowifi-go)
        ├─ Opens entitlement_url directly in WebView (no pre-auth)
        ├─ Injects JS bridge (websheet injection script at 0x2282a40)
        └─ Waits for JS callback signaling completion
```

### Provider-Specific Details

**AT&T (`att_entitlement`)**:
- Uses `ts43/` package for EAP-AKA SIM authentication
- Server: `https://sentitlement2.mobile.att.net/`
- Functions: `BuildAuthAction`, `BuildSubscriberID`, `BuildPermanentNAIIdentity`, `BuildChallengePayload`, `buildSignedEAPResponse`, `ParseResponse`
- NAI format: `0%s@nai.epc.mnc%s.mcc%s.3gppnetwork.org`
- EAP-AKA challenge payload: `{"challenge":"%s"}`
- After auth: parses `PhoneServicesAction` to determine if websheet needed
- Websheet JS detects completion by watching XHR responses to `/sfservice/v1/address/e911/` for `status === "validated"`

**T-Mobile (`T-Mobile_entitlement`)**:
- Does NOT use TS.43 at all — no EAP-AKA, no SIM authentication
- Server: `https://eas3.msg.t-mobile.com/` (this IS the websheet URL, not a TS.43 endpoint)
- Opens websheet directly via `Coordinator.StartWebsheet` (vohive package, not vowifi-go)
- JS bridge detects completion via carrier-standard callbacks:
  - `VoWiFiWebServiceFlow.entitlementChanged()` → success
  - `VoWiFiWebServiceFlow.dismissFlow()` → cancel
  - `WiFiCallingWebViewController.phoneServicesAccountStatusChanged(true)` → success
  - `NsdsWebSheetController.entitlementChanged()` → success

### TS.43 Package (`internal/vowifi/entitlement/ts43/`)

Functions (AT&T-only, called through interface dispatch):
- `BuildAuthAction` — constructs the initial TS.43 auth request
- `BuildSubscriberID` — builds subscriber identity for TS.43
- `BuildPermanentNAIIdentity` — formats NAI from IMSI
- `BuildChallengePayload` — creates EAP-AKA challenge response (largest function, 0x1080 bytes)
- `buildSignedEAPResponse` — signs the EAP response with derived keys
- `deriveKAut` — derives K_aut key from AKA vectors
- `ParseResponse` — parses TS.43 server response (entitlement status, names, auth-type)
- `DoJSONGzipRequest` — HTTP helper for gzipped JSON TS.43 requests
- `DecodeGzipBodyIfPresent` — decompresses response body

Key JSON tags for TS.43 types:
- `json:"entitlement-status"`, `json:"entitlement-name"`, `json:"entitlement-names"`
- `json:"auth-type"`, `json:"subscriber-id"`, `json:"phone-number"`
- `json:"connectivity-auth-type"`, `json:"app_id"`, `json:"action"`

### E911 Package (`runtimehost/e911/`)

Functions:
- `StartEmergencyAddressUpdate` — top-level dispatcher
- `startATT` — AT&T-specific flow (EAP-AKA + PhoneServices)
- `entitlementHTTPClientAdapter.Do` — wraps HTTP client for entitlement requests
- `entitlementBackedHTTPClient.Do` — HTTP client backed by entitlement session
- `entitlementTraceAdapter.Request` / `.Response` / `.Error` — request tracing

### Websheet JS Bridge (at 0x2282a40, embedded in vohive binary)

Template variables: `{{CALLBACK_URL}}`, `{{ABSOLUTE_PATH_PROXY_PREFIX}}`, `{{WEBSHEET_TOKEN}}`

Installs shim controllers for carrier websheet callback detection:
- `window.VoWiFiWebServiceFlow` — `entitlementChanged()`, `dismissFlow()`
- `window.WiFiCallingWebViewController` — `cancelButtonClicked()`, `phoneServicesAccountStatusChanged()`
- `window.NsdsWebSheetController` — `entitlementChanged()`, `dismissFlow()`, `cancelButtonClicked()`
- ODSA support: `ODSAServiceFlow` with activation code, SMDP callbacks

Communication channels: BroadcastChannel("vohive-websheet"), localStorage, postMessage, fetch to callbackURL

### uTLS Transport (`runtimehost/e911/` or `runtimehost/entitlement/`)

Functions for TLS fingerprint evasion:
- `dialUTLSWithNextProtos` — dials with uTLS and negotiates ALPN
- `fallbackRoundTripper.RoundTrip` — H2→H1 fallback
- `readRequestBody` / `cloneRequestWithBody` — request body handling for retries
- `isHTTP2SawHTTP1HeaderError` — detects H2/H1 mismatch

---

## IMS Register Template Architecture

### No Separate Template Files

IMS register templates are NOT stored as separate embedded files. They are:
1. Defined **inline** within carrier preset YAMLs (most carriers)
2. Referenced by **ID only** for cross-carrier sharing (e.g. `O2_de_26207_alias` → `O2_de_26203`)
3. Filled by `DefaultIMSRegisterTemplate` when preset has no body

### Template Resolution Chain

```
imscore.resolveActiveIMSRegisterTemplate()
  ├─ Preset has full inline template? → use directly
  ├─ Preset references ID from another preset? → look it up
  ├─ Preset has ID-only, no body? → DefaultIMSRegisterTemplate() fills defaults
  └─ No template at all? → DefaultIMSRegisterTemplate() provides full fallback
      └─ calls EffectiveCarrierConfigFromCarrierPlan() to derive from carrier config
```

### Key Functions

- `policy.DefaultIMSRegisterTemplate` — builds default template from carrier plan (992 bytes, 57 basic blocks)
- `policy.NormalizeIMSRegisterTemplate` — normalizes/merges template fields
- `policy.NormalizeIMSRegisterPolicy` — normalizes register policy section
- `policy.applyIMSRegisterTemplateOverride` — applies runtime overrides from VoHive
- `policy.isZeroIMSRegisterTemplate` — checks if template is empty/default
- `policy.IMSRegisterTemplateSecAgreeMode` — resolves sec-agree mode
- `policy.IMSRegisterTemplateInitialSecAgreeEnabled` — checks if initial sec-agree is on
- `imscore.resolveActiveIMSRegisterTemplate` — final runtime resolver
- `runtimehost/carrier.imsRegisterTemplateFromInternal` — converts internal→public type

### Template Fields (from binary YAML analysis)

```yaml
ims_register_template:
  id: <string>                          # Template identifier for cross-referencing
  supported_header: <string>            # e.g. "path,sec-agree,gruu"
  allow_header: <string>                # e.g. "INVITE,BYE,CANCEL,ACK,NOTIFY,UPDATE,PRACK,INFO,MESSAGE,OPTIONS"
  include_pani_authenticated: <bool>    # Include P-Access-Network-Info with "authenticated" flag
  strict_security_server_offer: <bool>  # Strict matching of Security-Server offer
  enable_initial_reject_fallback: <bool># Retry on initial rejection
  use_plain_digest_placeholder: <bool>  # Use plain digest instead of AKA
  sec_agree_mode: <string>              # "auto" or explicit mode
  icsi_ref: <string>                    # IMS Communication Service Identifier
  security_client_mechanisms:           # IPsec security mechanisms
    - alg: <string>                     # e.g. "hmac-sha-1-96"
      ealg: <string>                    # e.g. "aes-cbc", "null"
      prot: <string>                    # e.g. "esp"
      mode: <string>                    # e.g. "trans"
  contact_param_order:                  # Order of Contact header parameters
    - access_type
    - sip_instance
    - audio
    - smsip
    - icsi_ref
    - mid_call
    - srvcc_alerting
    - ps2cs_srvcc_orig_pre_alerting
  register_policy:                      # Registration retry policy
    id: <string>
    temporary_status_codes: [<int>...]
    forbidden_status_codes: [<int>...]
    initial_reject_fallback_status_codes: [<int>...]
    temporary_retry_seconds: <int>
```

---

## Carrier Presets (16 total, confirmed complete from binary)

All embedded in `internal/vowifi/policy/presets/`. File names confirmed from binary strings.

### File Naming (current → correct)

Some files need renaming to match binary-confirmed names:
- `T-Mobile_240.yaml` → `tmobile_310240.yaml`
- `T-Mobile_260.yaml` → `tmobile_310260.yaml`
- `CTEUK_23433.yaml` → `cteuk_23433.yaml`
- `LycaMobile_310410.yaml` → `att_310410.yaml`
- `O2_de_26203.yaml` → `o2_de_26203.yaml`
- `O2_de_26207_alias.yaml` → `o2_de_26207_alias.yaml`

### Carrier Config Fields (from binary YAML analysis)

```yaml
# Top-level carrier plan fields:
mcc: <string>
mnc: <string>
id: <string>                        # Carrier identifier
custom_epdg: <string>               # Custom ePDG hostname (overrides 3GPP default)
apn: <string>                       # APN name (e.g. "ims")
ip_stack: <string>                  # "ipv4" or "ipv6" or "dual"
dpd_interval_seconds: <int>         # Dead Peer Detection interval
nat_keepalive_seconds: <int>        # NAT keepalive interval
device_model: <string>              # Device model string (e.g. "iphone15,4", "rmx3366")
device_identity_enabled: <bool>     # Whether device identity is sent
aka_challenge_mode: <string>        # "checkcode" | "omit" | (default)
ims_identity_source: <string>       # "isim" or (default from USIM)
ims_domain: <string>                # Override IMS domain
ims_realm: <string>                 # Override IMS realm
reauth_interval_seconds: <int>      # Re-authentication interval
ike_proposals:                      # IKE SA proposals
  - <string>                        # e.g. "aes256-sha256-prfsha1-modp2048"
esp_proposals:                      # ESP/Child SA proposals
  - <string>                        # e.g. "aes256-sha256"
e911:                               # E911 entitlement config (optional)
  enabled: <bool>
  provider: <string>                # "att_entitlement" | "T-Mobile_entitlement"
  entitlement_url: <string>
  websheet_host_policy: <string>    # "public_https"
ims_register_template:              # IMS REGISTER template (inline or ID-only reference)
  id: <string>
  ...                               # (see Template Fields above)
```

### Per-Carrier Summary

| Preset | MCC/MNC | ePDG | IKE | ESP | IMS Template | E911 | Special |
|--------|---------|------|-----|-----|------|------|---------|
| att_310280 | 310/280 | epdg.epc.att.net | aes128-sha256-modp2048 | aes128-sha256 | att_310280 (inline, full) | att_entitlement | ims_identity_source: isim |
| att_310410 | 310/410 | epdg.epc.att.net | aes128-sha256-modp2048 | aes128-sha256 | att_310280 (reuses) | att_entitlement | ims_identity_source: isim |
| tmobile_310240 | 310/240 | (default) | (default) | aes128-sha256, aes128-sha1 | (none) | T-Mobile_entitlement | — |
| tmobile_310260 | 310/260 | (default) | (default) | aes128-sha256, aes128-sha1 | (none) | T-Mobile_entitlement | — |
| cteuk_23433 | 234/33 | (default) | (default) | (default) | CTEUK_23433 (inline) | — | device_model: rmx3366 |
| giffgaff_23410 | 234/10 | (default) | aes256-sha512-prfsha512-modp2048 | aes256-sha512 | giffgaff (inline, full) | — | device_model: rmx3366, reauth_interval: 180s |
| three_uk_234020 | 234/020 | (default) | aes256-sha256-modp2048 | aes128-sha256 | three_uk_234020 (inline, full) | — | device_model: rmx3366 |
| o2_de_26203 | 262/03 | epdg.epc.mnc003.mcc262... | aes256-sha256-prfsha1-modp2048 | aes256-sha256 | O2_de_26203_ios (inline) | — | device_model: iphone15,4, nat_keepalive: 20s |
| o2_de_26207_alias | 262/07 | epdg.epc.mnc007.mcc262... | aes256-sha256-prfsha1-modp2048 | aes256-sha256 | O2_de_26203 (cross-ref) | — | device_model: iphone15,4, nat_keepalive: 20s |
| vodafone_nl_20404 | 204/04 | epdg.epc.mnc004.mcc204... | aes256-sha256-prfsha512-modp2048 | aes256-sha256 | vodafone_nl_20404_ios (inline) | — | apn: ims |
| sunrise_22802 | 228/002 | (default) | aes128-sha256-modp2048 | aes128-sha256 | (none) | — | aka_challenge_mode: checkcode, ip_stack: ipv4 |
| csl_454000 | 454/000 | (default) | aes256-sha256-modp2048 | aes256-sha256, aes128-sha256 | csl_454000 (inline) | — | aka_challenge_mode: omit, ip_stack: ipv4 |
| three_hk_454003 | 454/003 | wlan.three.com.hk | aes256-sha256-modp2048 | aes256-sha256, aes128-sha256 | three_hk_454003 (inline) | — | aka_challenge_mode: checkcode, ip_stack: ipv4 |
| spark_nz_53005 | 530/05 | epdg.epc.mnc005.mcc530...spark.co.nz | aes256-sha256-prfsha256-modp2048 | aes256-sha256 | spark_nz_53005_ios (ID-only) | — | device_model: iphone15,4, ims_domain/realm set |
| 2degrees_nz_53024 | 530/24 | epdg.ims.2degrees.net.nz | aes256-sha512-prfsha512-modp1024 | aes256-sha512 | 2degrees_nz_53024_ios (ID-only) | — | ip_stack: ipv4, dpd: 300s |
| one_nz_53001 | 530/01 | (default) | (default) | (default) | (none) | — | device_identity_enabled: false |

---

## Implementation Approach

### Phase 1: Module Scaffold + Public API (Milestone A)

Create all public packages with exact type signatures from `VOWIFI_GO_REWRITE_PLAN.md` Section 4. This gets VoHive to compile.

**Files to create:**
- `vowifi-go/go.mod` — module `github.com/iniwex5/vowifi-go`, require swu-go with local replace
- `vowifi-go/engine/sim/` — AKAProvider, AKAResult, ErrSyncFailure
- `vowifi-go/engine/swu/` — DataplaneModeUserspace constant
- `vowifi-go/runtimehost/` — Instance, Start, State, StartRequest, Modem, SIMAdapter, etc.
- `vowifi-go/runtimehost/identity/` — Profile, PreparedSession, PrepareStart
- `vowifi-go/runtimehost/carrier/` — Config, LoadCarrierOverrides, ResolveEffectiveCarrierConfig
- `vowifi-go/runtimehost/messaging/` — SendOutcome, DeliveryStore, USSDResult, etc.
- `vowifi-go/runtimehost/eventhost/` — SMSReceived, SMSSent, Dispatcher, etc.
- `vowifi-go/runtimehost/voicehost/` — Gateway, SimulateCallRequest/Result, ParseSDP
- `vowifi-go/runtimehost/e911/` — StartEmergencyAddressUpdate, HTTPClient, etc.

### Phase 2: Internal Common + Policy (carrier resolution)

- `internal/vowifi/common/` — utilities (TraceID, Plmn3, etc.)
- `internal/vowifi/policy/` — carrier presets (embedded YAML), override system, IMS register template normalization
- `internal/vowifi/policy/presets/` — 16 embedded YAML files (rename to binary-confirmed names)

Key policy functions to implement:
- `DefaultIMSRegisterTemplate()` — builds default from carrier plan
- `NormalizeIMSRegisterTemplate()` — normalizes/merges fields
- `EffectiveCarrierConfigFromCarrierPlan()` — resolves full effective config
- `applyIMSRegisterTemplateOverride()` — runtime override application
- `isZeroIMSRegisterTemplate()` — empty check
- `IMSRegisterTemplateSecAgreeMode()` — sec-agree mode resolution

Reference: SimAdmin `profiles.rs` for 7 built-in carriers (EE UK, Vodafone NL, T-Mobile US, Tello, AT&T, O2 DE, Spark NZ) plus 3GPP dynamic fallback.

### Phase 3: Runtime Core + SWU Integration

- `internal/vowifi/runtimecore/` — Runtime.Start, RunLoop, RunSession, BuildSWUConfig, StartAndWaitEPDG
- Wire `runtimehost.SIMAdapter` → swu-go `sim.SIMProvider` via adapter
- Map carrier config → `swu.Config`
- Tunnel lifecycle: connect, snapshot polling, state emission, stop

### Phase 4: IMS Registration

- `internal/vowifi/sipkit/` — SIP message building, URI parsing, transport handling
- `internal/vowifi/imsheaders/` — Contact, P-Preferred-Identity, Route, SecurityClient/Verify, PANI
- `internal/vowifi/imscore/` — Service (REGISTER state machine, digest AKA challenge, IMS registration refresh)
- `internal/vowifi/imscore/` — `resolveActiveIMSRegisterTemplate()` for runtime template resolution

Reference: SimAdmin `ims.rs` for register flow (401→SecurityAgreement→AuthRegister→200), digest AKAv1-MD5.

### Phase 5: SMS over IMS

- `internal/vowifi/imscore/` — SMS fragment handling, MO/MT, reassembly, delivery tracking
- `internal/vowifi/smsdelivery/` — delivery state types

Reference: SimAdmin `sms.rs` for RP-DATA encoding, GSM-7/UCS2, segmentation, SIP MESSAGE transport.

### Phase 6: Voice + E911 + Entitlement

- `internal/vowifi/voice/media/` — RTP relay, comfort noise, PCAP
- `internal/vowifi/entitlement/ts43/` — TS.43 protocol (EAP-AKA auth, challenge/response, NAI)
- `internal/vowifi/entitlement/providers/att/` — AT&T E911 (PhoneServicesAction, address validation)
- `runtimehost/e911/` — dispatch logic, uTLS transport, HTTP adapters
- Note: T-Mobile E911 is handled entirely by vohive's `Coordinator.StartWebsheet`, NOT by vowifi-go

## Verification

1. **Compile check:** `cd /opt/vohive-collection/vohive && go build ./...`
2. **Unit tests:** `cd /opt/vohive-collection/vowifi-go && go test ./...`
3. **Integration:** VoHive test packages `go test ./internal/vowifihost ./internal/sim ./internal/device`

## Critical Reuse

- **swu-go** (`/opt/vohive-collection/swu-go/`) — entire SWu/IKEv2/ESP/TUN layer
- **SimAdmin** (`/opt/SimAdmin/backend/src/vowifi/`) — carrier profiles, IMS register logic, SMS encoding, EAP-AKA flow
- **VOWIFI_GO_REWRITE_PLAN.md** — exact public API signatures (already verified against binary)
- **IDA binary** — function names confirm package boundaries and method sets

---

## Progress Update - 2026-07-05

### Completed This Stage

- Confirmed Node/npm are available locally:
  - `node v22.16.0`
  - `npm 10.9.2`
- Installed VoHive frontend dependencies in `vohive/web` with a workspace-local npm cache:
  - `npm_config_cache=/opt/vohive-collection/.npm-cache npm ci`
- Built frontend production assets:
  - `npm_config_cache=/opt/vohive-collection/.npm-cache npm run build`
- Copied generated frontend assets into the backend embed location expected by `internal/web/fs.go`:
  - source: `vohive/web/dist`
  - embed target: `vohive/internal/web/dist`
- Added/confirmed local module wiring so VoHive resolves the restored local implementation:
  - `vohive/go.mod`: `replace github.com/iniwex5/vowifi-go => ../vowifi-go`

### vowifi-go Compatibility Work Completed

- Filled the remaining public API gaps surfaced by full `cmd/vohive` compilation:
  - `runtimehost.Instance.SendSMSWithOptions(...)`
  - `runtimehost.Instance.GetSMSDeliveryStatus(...)`
  - `voicehost.Gateway.Stop()`
  - `voicehost.SimulateCallResult.Reason`
- Stored `messaging.DeliveryStore` on `runtimehost.Instance` so VoHive's delivery status API can resolve SMS delivery records through the runtime instance.
- Preserved the conservative runtime behavior:
  - dry-run startup remains the default
  - real SWu/ePDG startup remains opt-in via `VOWIFI_GO_ENABLE_SWU=1` or `VOHIVE_VOWIFI_ENABLE_SWU=1`

### Verification Completed

- `vowifi-go` package test/compile sweep passed:
  - `GOCACHE=/opt/vohive-collection/.gocache GOMODCACHE=/opt/vohive-collection/.gomodcache GOPATH=/opt/vohive-collection/.gopath go test ./...`
- Focused VoHive tests passed:
  - `go test ./internal/vowifihost ./internal/sim`
- Full Linux `cmd/vohive` compile check passed after frontend embed assets were generated:
  - `GOWORK=/opt/vohive-collection/vohive/go.work GOCACHE=/opt/vohive-collection/.gocache GOMODCACHE=/opt/vohive-collection/.gomodcache GOPATH=/opt/vohive-collection/.gopath GOOS=linux CGO_ENABLED=0 go test -tags "with_utls nomsgpack" -c ./cmd/vohive -o /opt/vohive-collection/.gocache/vohive.test`

### Generated Local Artifacts

- Ignored/generated assets now present:
  - `vohive/web/node_modules/`
  - `vohive/web/dist/`
  - `vohive/internal/web/dist/`
  - `/opt/vohive-collection/.npm-cache/`
  - `/opt/vohive-collection/.gocache/`
  - `/opt/vohive-collection/.gomodcache/`
  - `/opt/vohive-collection/.gopath/`

### Current Remaining Implementation Gaps

- The restored `vowifi-go` module is now sufficient for VoHive to compile, but major runtime behavior is still scaffolded or partial:
  - IMS REGISTER state machine is not implemented yet.
  - SMS over IMS is not implemented yet beyond API compatibility and delivery-store wiring.
  - Voice gateway behavior is still a compatibility stub.
  - AT&T E911 entitlement remains `ErrChallengeNotImplemented`.
  - Carrier policy is still a minimal fallback set rather than the full carrier preset/template system from the rewrite plan.
- Next recommended stage:
  - Implement the internal IMS registration core (`sipkit`, `imsheaders`, `imscore`) and wire it behind the existing `runtimehost.IMSService` surface after SWu tunnel readiness.
