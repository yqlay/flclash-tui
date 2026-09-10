//go:build linux && !cgo && cli

package protocol

import "time"

type TuiConnection struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Process     string `json:"process,omitempty"`
	ProcessPath string `json:"process_path,omitempty"`
	UID         uint32 `json:"uid,omitempty"`
	SourceIP    string `json:"source_ip,omitempty"`
	InboundName string `json:"inbound_name,omitempty"`
	InboundUser string `json:"inbound_user,omitempty"`
	Network     string `json:"network,omitempty"`
	Chain       string `json:"chain,omitempty"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
}

type TuiRequest struct {
	TuiConnection
	FirstSeen time.Time
	LastSeen  time.Time
	Active    bool
}

type TuiSettings struct {
	Mode          string
	MixedPort     int
	AllowLAN      bool
	IPv6          bool
	UnifiedDelay  bool
	TCPConcurrent bool
	LogLevel      string
	TunEnabled    bool
	TunScope      string
	SystemProxy   bool
}

type TuiSpeedResult struct {
	Bytes          int64   `json:"bytes"`
	DurationMillis int64   `json:"duration_millis"`
	BytesPerSecond float64 `json:"bytes_per_second"`
	Complete       bool    `json:"complete"`
	Testing        bool    `json:"-"`
	Error          string  `json:"-"`
}

type TuiServiceRequest struct {
	ProtocolVersion  int          `json:"protocol_version,omitempty"`
	RequestID        string       `json:"request_id,omitempty"`
	ExpectedRevision *uint64      `json:"expected_revision,omitempty"`
	Action           string       `json:"action"`
	ConfigPath       string       `json:"config_path,omitempty"`
	ProxyGroup       string       `json:"proxy_group,omitempty"`
	ProxyName        string       `json:"proxy_name,omitempty"`
	Mode             string       `json:"mode,omitempty"`
	MixedPort        int          `json:"mixed_port,omitempty"`
	TunScope         string       `json:"tun_scope,omitempty"`
	TestURL          string       `json:"test_url,omitempty"`
	Settings         *TuiSettings `json:"settings,omitempty"`
	Enabled          *bool        `json:"enabled,omitempty"`
	ExpectedSHA256   string       `json:"expected_sha256,omitempty"`
	ProfileData      []byte       `json:"profile_data,omitempty"`
	CreateOnly       bool         `json:"create_only,omitempty"`
	SubscriptionURL  *string      `json:"subscription_url,omitempty"`
	NewName          string       `json:"new_name,omitempty"`
	ConnectionID     string       `json:"connection_id,omitempty"`
	AfterRevision    uint64       `json:"after_revision,omitempty"`
	WatchTimeoutMS   int          `json:"watch_timeout_ms,omitempty"`
	LogLimit         int          `json:"log_limit,omitempty"`
}

type TuiServiceStatus struct {
	ProtocolVersion     int             `json:"protocol_version,omitempty"`
	RequestID           string          `json:"request_id,omitempty"`
	Revision            uint64          `json:"revision,omitempty"`
	OK                  bool            `json:"ok"`
	ErrorCode           string          `json:"error_code,omitempty"`
	Error               string          `json:"error,omitempty"`
	PID                 int             `json:"pid"`
	Version             string          `json:"version"`
	HomeDir             string          `json:"home_dir"`
	ConfigPath          string          `json:"config_path"`
	CoreSocket          string          `json:"core_socket"`
	Running             bool            `json:"running"`
	ShuttingDown        bool            `json:"shutting_down,omitempty"`
	SystemProxy         bool            `json:"system_proxy"`
	Mode                string          `json:"mode,omitempty"`
	ProxyPort           int             `json:"proxy_port,omitempty"`
	ConfiguredProxyPort int             `json:"configured_proxy_port,omitempty"`
	ActiveProxyPort     int             `json:"active_proxy_port,omitempty"`
	TunScope            string          `json:"tun_scope,omitempty"`
	TunState            string          `json:"tun_state,omitempty"`
	TunOwnerUID         uint32          `json:"tun_owner_uid,omitempty"`
	TunOwnerPID         int             `json:"tun_owner_pid,omitempty"`
	FLCEnabled          bool            `json:"flc_enabled,omitempty"`
	FLCOutbound         string          `json:"flc_outbound,omitempty"`
	FLCProxyURL         string          `json:"flc_proxy_url,omitempty"`
	ResultPath          string          `json:"result_path,omitempty"`
	History             []TuiRequest    `json:"history,omitempty"`
	Connections         []TuiConnection `json:"connections,omitempty"`
	Logs                []string        `json:"logs,omitempty"`
	FrontendCount       int             `json:"frontend_count"`
	Delay               int             `json:"delay,omitempty"`
	DelayJitter         int             `json:"delay_jitter,omitempty"`
	DelayMin            int             `json:"delay_min,omitempty"`
	DelayMax            int             `json:"delay_max,omitempty"`
	DelaySamples        int             `json:"delay_samples,omitempty"`
	Speed               *TuiSpeedResult `json:"speed,omitempty"`
}

type TuiServiceError struct {
	Code     string
	Revision uint64
	Message  string
}

func (e *TuiServiceError) Error() string {
	return e.Message
}
